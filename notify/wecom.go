package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// weComMarkdownLimit is the hard byte limit (in bytes, not characters) for WeCom group bot markdown content.
// This is the tightest limit among all six channels and the primary reason TruncateBytes exists.
const weComMarkdownLimit = 4096

// weComChannel implements the WeCom (Enterprise WeChat) group bot adapter.
//
// Platform characteristics:
//   - Authenticates solely via the `key` query parameter in the URL and does not support signing—so the webhook URL itself is the entire credential.
//   - Markdown content is capped at 4096 **bytes**; exceeding it causes the entire message to be rejected (not truncated). Multi-byte UTF-8 characters
//     take 3 bytes each, leaving room for just over a thousand CJK characters, so client-side truncation is mandatory.
//   - Rate limit of 20 messages/minute, enforced client-side.
type weComChannel struct{}

func (weComChannel) Kind() string { return KindWeCom }

func (weComChannel) DefaultRatePerMin() int { return 20 }

// WeCom has only one credential field (the key embedded in the Webhook URL) and does not support signing—
// the URL itself is the sole credential, with no other fields needing masking.
func (weComChannel) SecretKeys() []string { return []string{"webhook"} }

// WeCom has only the Webhook field, which serves as both destination and credential, so there is no separate credential left behind when the URL changes.
func (weComChannel) DestinationKeys() []string { return []string{"webhook"} }

func (weComChannel) Validate(cfg map[string]any) error {
	hook := cfgString(cfg, "webhook")
	if hook == "" {
		return errors.New("missing Webhook URL")
	}
	if err := validateHTTPURL(hook); err != nil {
		return fmt.Errorf("invalid Webhook URL: %w", err)
	}
	return nil
}

func (c weComChannel) Send(ctx context.Context, cfg map[string]any, m Message) (int, error) {
	if err := c.Validate(cfg); err != nil {
		return 0, Permanent(err)
	}
	// A digest batch can easily exceed 4096 bytes (e.g., 50 items × 1 line each + intro).
	// Truncate client-side rather than relying on the platform to reject it: rejection loses the whole batch, whereas packing delivers the first N items.
	content, kept := markdownBody(m, weComMarkdownLimit)
	payload := map[string]any{
		"msgtype":  "markdown",
		"markdown": map[string]any{"content": content},
	}
	raw, err := doJSON(ctx, "POST", cfgString(cfg, "webhook"), nil, payload)
	if err != nil {
		return 0, err
	}
	var res struct {
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return 0, fmt.Errorf("failed to parse WeCom response: %w (%s)", err, snippet(raw))
	}
	if res.ErrCode != 0 {
		// 45009 means API call frequency exceeded the limit—the platform's rolling rate window will reset, so retrying after backoff works.
		// Reaching this branch means the client-side rate_per_min is configured too aggressively;
		// retry is only a safety net, and the proper fix is lowering the channel's rate limit.
		if res.ErrCode == 45009 {
			return 0, fmt.Errorf("WeCom rate limit %d: %s", res.ErrCode, res.ErrMsg)
		}
		// 93000 means invalid webhook key—a permanent failure that will not self-heal on retry.
		return 0, Permanent(fmt.Errorf("WeCom returned error %d: %s", res.ErrCode, res.ErrMsg))
	}
	return kept, nil
}
