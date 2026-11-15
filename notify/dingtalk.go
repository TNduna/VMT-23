package notify

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"time"
)

// dingTalkChannel implements the DingTalk custom group bot adapter.
//
// Platform characteristics (which dictate the implementation trade-offs here):
//   - Rate limit of 20 messages/minute per bot; excess messages are silently dropped (HTTP may still return 200),
//     so rate limiting must be enforced client-side; see DefaultRatePerMin.
//   - Security settings offer three choices: HMAC signature / custom keywords / IP allowlist. Signing is the only option
//     that does not depend on message content, so we support signing (as well as bare webhooks with none of the three enabled).
//   - Both success and failure return HTTP 200, distinguished by errcode in the JSON body—failing to check errcode
//     would record failed deliveries as successful.
type dingTalkChannel struct{}

func (dingTalkChannel) Kind() string { return KindDingTalk }

func (dingTalkChannel) DefaultRatePerMin() int { return 20 }

// DingTalk's Webhook URL contains the access_token query parameter, which is itself a credential, so the entire URL is masked.
func (dingTalkChannel) SecretKeys() []string { return []string{"webhook", "secret"} }

// The destination is the DingTalk Webhook URL itself; changing the URL requires re-confirming the signing secret for the new URL.
func (dingTalkChannel) DestinationKeys() []string { return []string{"webhook"} }

func (dingTalkChannel) Validate(cfg map[string]any) error {
	hook := cfgString(cfg, "webhook")
	if hook == "" {
		return errors.New("missing Webhook URL")
	}
	if err := validateHTTPURL(hook); err != nil {
		return fmt.Errorf("invalid Webhook URL: %w", err)
	}
	return nil
}

// Send delivers a single message. Uses ActionCard (with a button) when sending a single item with a detail URL; otherwise uses markdown.
func (c dingTalkChannel) Send(ctx context.Context, cfg map[string]any, m Message) (int, error) {
	hook := cfgString(cfg, "webhook")
	if err := c.Validate(cfg); err != nil {
		return 0, Permanent(err)
	}
	endpoint, err := dingTalkSignedURL(hook, cfgString(cfg, "secret"), time.Now())
	if err != nil {
		return 0, Permanent(err)
	}

	title := markdownTitle(m)
	// DingTalk markdown has no strict documented byte limit, but we still enforce an upper bound to guard against abnormally large evidence fields.
	text, kept := markdownBody(m, 20000)

	var payload any
	if !m.Batch && len(m.Items) == 1 && m.Items[0].DetailURL != "" {
		payload = map[string]any{
			"msgtype": "actionCard",
			"actionCard": map[string]any{
				"title":          title,
				"text":           text,
				"btnOrientation": "0",
				"singleTitle":    "View Details",
				"singleURL":      m.Items[0].DetailURL,
			},
		}
	} else {
		payload = map[string]any{
			"msgtype":  "markdown",
			"markdown": map[string]any{"title": title, "text": text},
		}
	}

	raw, err := doJSON(ctx, "POST", endpoint, nil, payload)
	if err != nil {
		return 0, err
	}
	// DingTalk embeds business errors inside HTTP 200 responses.
	var res struct {
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return 0, fmt.Errorf("failed to parse DingTalk response: %w (%s)", err, snippet(raw))
	}
	if res.ErrCode != 0 {
		// 301000 is signature verification failure, 310000 is keyword mismatch—both are configuration errors
		// that will not self-heal on retry.
		return 0, Permanent(fmt.Errorf("DingTalk returned error %d: %s", res.ErrCode, res.ErrMsg))
	}
	return kept, nil
}

// dingTalkSignedURL appends timestamp and sign query parameters to the webhook URL according to DingTalk's official signing specification.
//
// Rule: stringToSign = timestamp + "\n" + secret, HMAC-SHA256 with **secret as the key**,
// then base64-encode and URL-encode. timestamp is in milliseconds. When secret is empty, returns hook unchanged
// to support bots without signing enabled.
func dingTalkSignedURL(hook, secret string, now time.Time) (string, error) {
	if secret == "" {
		return hook, nil
	}
	ts := strconv.FormatInt(now.UnixMilli(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "\n" + secret))
	sign := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	u, err := url.Parse(hook)
	if err != nil {
		// Do not pass err through: url.Parse error strings contain the full URL (including access_token).
		return "", fmt.Errorf("failed to parse Webhook URL: %s", redactRequestTarget(hook))
	}
	q := u.Query()
	q.Set("timestamp", ts)
	q.Set("sign", sign)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// validateHTTPURL checks that the URL is parseable, uses a supported scheme, and checks literal IP targets against internal network rules.
//
// Two important details:
//
//  1. **Error messages must be redacted**. url.Parse returns *url.Error, whose Error() contains the
//     **complete original URL**, and URLs for several channels embed credentials directly (DingTalk access_token,
//     WeCom key, Telegram bot token, Feishu hook id). Returning `err` directly would leak credentials through
//     400 responses on the test endpoint, last_error persisted in the database, server logs, and the delivery history API.
//
//  2. **Literal IPs are checked immediately**, while domain names are checked at dial time (blockInternalDial is the final
//     enforcement point and also covers DNS rebinding). Checking literal IPs here gives immediate feedback when saving configuration
//     rather than waiting for the first delivery attempt to fail.
//
// Restricting the scheme is defensive: file:///gopher:// etc. could cause unexpected behavior in http.Client.
func validateHTTPURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("unparseable URL (%s)", redactRequestTarget(raw))
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("only http/https are supported, got %q", u.Scheme)
	}
	if u.Host == "" {
		return errors.New("missing hostname")
	}
	if ip := net.ParseIP(u.Hostname()); ip != nil && isBlockedDialIP(ip) && !allowLocalTargets() {
		return fmt.Errorf("delivery to loopback/link-local address %s is denied (set %s=1 if local service delivery is required)", ip, AllowLocalTargetsEnv)
	}
	return nil
}
