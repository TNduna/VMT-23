package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"text/template"
	"time"
)

// webhookChannel is the generic Webhook adapter: users configure a custom URL, HTTP method, headers, and JSON body template.
// Having this adapter avoids writing dedicated implementations for Slack / Mattermost / Discord / custom internal systems—
// all of those platforms can be covered by a configurable template.
type webhookChannel struct{}

func (webhookChannel) Kind() string { return KindWebhook }

// Generic Webhooks have no universal rate limit; returning 0 means unlimited by default, leaving rate limiting up to the operator.
func (webhookChannel) DefaultRatePerMin() int { return 0 }

// Mask both url and headers: destination URLs often carry tokens, and custom headers typically hold authentication credentials;
// both appear in API responses and must be masked.
// The trade-off is that editing a single header requires re-entering the entire headers map (a masked value means "keep original")—
// this is deliberate: re-entering headers is far preferable to echoing credentials back to the browser.
func (webhookChannel) SecretKeys() []string { return []string{"url", "headers"} }

// The destination is url. Changing url requires re-specifying headers—otherwise the original Authorization header
// would be sent as-is to the new URL, which is the primary mask-bypass exfiltration vector.
func (webhookChannel) DestinationKeys() []string { return []string{"url"} }

// webhookDefaultTemplate is the fallback request body when no custom template is provided: a straightforward JSON structure
// that covers the vast majority of custom receivers expecting a JSON payload.
const webhookDefaultTemplate = `{
  "title": {{json .Title}},
  "batch": {{.Batch}},
  "count": {{.Count}},
  "items": [
{{- range $i, $it := .Items}}
{{- if $i}},{{end}}
    {
      "finding_id": {{$it.FindingID}},
      "name": {{json $it.Name}},
      "vulnclass": {{json $it.VulnClass}},
      "severity": {{json $it.Severity}},
      "summary": {{json $it.Summary}},
      "assets": {{json $it.Assets}},
      "detail_url": {{json $it.DetailURL}}
    }
{{- end}}
  ]
}`

// webhookTemplateData is the context exposed to user templates.
type webhookTemplateData struct {
	Title   string
	Batch   bool
	Count   int
	Items   []webhookItem
	HomeURL string
	// SentAt is the delivery timestamp (RFC3339) for receiver logging.
	SentAt string
}

type webhookItem struct {
	FindingID     int64
	Name          string
	VulnClass     string
	Severity      string
	SeverityLabel string
	Summary       string
	Assets        []string
	DetailURL     string
	FromStatus    string
	ToStatus      string
	// StatusLabel is the human-readable status transition, e.g. "Pending → Fixed"; empty when not a status-change event.
	StatusLabel string
}

func (webhookChannel) Validate(cfg map[string]any) error {
	raw := cfgString(cfg, "url")
	if raw == "" {
		return errors.New("missing target URL")
	}
	if err := validateHTTPURL(raw); err != nil {
		return fmt.Errorf("invalid target URL: %w", err)
	}
	if m := strings.ToUpper(cfgString(cfg, "method")); m != "" && m != http.MethodGet && m != http.MethodPost && m != http.MethodPut && m != http.MethodPatch {
		return fmt.Errorf("unsupported method %s (allowed: GET/POST/PUT/PATCH)", m)
	}
	if tpl := cfgString(cfg, "body_template"); tpl != "" {
		if _, err := parseWebhookTemplate(tpl); err != nil {
			return fmt.Errorf("body template syntax error: %w", err)
		}
	}
	return nil
}

func (c webhookChannel) Send(ctx context.Context, cfg map[string]any, m Message) (int, error) {
	if err := c.Validate(cfg); err != nil {
		return 0, Permanent(err)
	}
	method := strings.ToUpper(cfgString(cfg, "method"))
	if method == "" {
		method = http.MethodPost
	}

	// GET requests do not carry a body: stuffing content into query parameters is beyond template scope and violates GET semantics,
	// so GET is only suitable for simple trigger-on-hit webhook receivers.
	var payload any
	if method != http.MethodGet {
		body, err := renderWebhookBody(cfgString(cfg, "body_template"), m)
		if err != nil {
			return 0, Permanent(err)
		}
		// The template renders a JSON string; wrap it in json.RawMessage so it is sent verbatim
		// without double-encoding the user's JSON structure as a quoted string.
		if !json.Valid([]byte(body)) {
			return 0, Permanent(errors.New("rendered body template is not valid JSON"))
		}
		payload = json.RawMessage(body)
	}

	headers := cfgMap(cfg, "headers")
	if ct := cfgString(cfg, "content_type"); ct != "" {
		// Allow overriding Content-Type, applied after headers so explicit configuration takes precedence.
		if headers == nil {
			headers = map[string]string{}
		}
		headers["Content-Type"] = ct
	}
	if _, err := doJSON(ctx, method, cfgString(cfg, "url"), headers, payload); err != nil {
		return 0, err
	}
	// Generic Webhooks do not truncate the body (the receiver is the user's own service, and size is governed by body_template),
	// so the entire batch counts as delivered.
	return len(m.Items), nil
}

// renderWebhookBody renders the request body using the user template (or the default template).
func renderWebhookBody(tpl string, m Message) (string, error) {
	if strings.TrimSpace(tpl) == "" {
		tpl = webhookDefaultTemplate
	}
	t, err := parseWebhookTemplate(tpl)
	if err != nil {
		return "", fmt.Errorf("body template syntax error: %w", err)
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, newWebhookTemplateData(m)); err != nil {
		return "", fmt.Errorf("failed to render body template: %w", err)
	}
	return buf.String(), nil
}

// parseWebhookTemplate parses a template string.
//
// missingkey=zero makes missing map keys render as zero values rather than erroring out—though our context is a struct,
// this keeps `range .Items` safe when empty.
func parseWebhookTemplate(tpl string) (*template.Template, error) {
	return template.New("body").Funcs(webhookTemplateFuncs).Option("missingkey=zero").Parse(tpl)
}

// webhookTemplateFuncs are the helper functions exposed to templates.
var webhookTemplateFuncs = template.FuncMap{
	// json serializes any value into JSON.
	//
	// This function is essential, not just a convenience: without it, users could only write `"{{.Title}}"` via raw interpolation,
	// and any quote or newline in a vulnerability title would produce invalid JSON—causing the receiver to reject the request
	// with a generic "JSON parse error" that gives no clue a quote in the title was to blame.
	"json": func(v any) (string, error) {
		raw, err := json.Marshal(v)
		if err != nil {
			return "", err
		}
		return string(raw), nil
	},
	// jsons is used to embed a JSON fragment inside another JSON string value (applying an extra layer of string escaping).
	"jsons": func(v any) (string, error) {
		raw, err := json.Marshal(v)
		if err != nil {
			return "", err
		}
		quoted, err := json.Marshal(string(raw))
		if err != nil {
			return "", err
		}
		// Strip outer quotes: the template author decides whether to wrap it in quotes.
		return string(quoted[1 : len(quoted)-1]), nil
	},
}

func newWebhookTemplateData(m Message) webhookTemplateData {
	d := webhookTemplateData{
		Title:   markdownTitle(m),
		Batch:   m.Batch,
		Count:   len(m.Items),
		HomeURL: m.HomeURL,
		SentAt:  time.Now().Format(time.RFC3339),
		Items:   make([]webhookItem, 0, len(m.Items)),
	}
	for _, it := range m.Items {
		wi := webhookItem{
			FindingID:     it.FindingID,
			Name:          it.Name,
			VulnClass:     it.VulnClass,
			Severity:      it.Severity,
			SeverityLabel: SeverityLabel(it.Severity),
			Summary:       it.Summary,
			Assets:        append([]string{}, it.Assets...),
			DetailURL:     it.DetailURL,
			FromStatus:    it.FromStatus,
			ToStatus:      it.ToStatus,
		}
		if it.IsStatusChange() {
			wi.StatusLabel = StatusLabel(it.FromStatus) + " → " + StatusLabel(it.ToStatus)
		}
		d.Items = append(d.Items, wi)
	}
	return d
}
