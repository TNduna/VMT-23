package notify

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"
)

// Channel is an adapter for a notification channel. Implementations must be **stateless**: the same instance is reused
// concurrently across multiple channel configurations, and credentials are always passed via the cfg parameter.
type Channel interface {
	// Kind returns the channel type identifier, which must match its key in the registry.
	Kind() string
	// Validate is called when saving configuration to check required fields and formats. Returned errors are displayed
	// directly to the operator, so messages should state "which field is missing" rather than a vague "invalid config".
	Validate(cfg map[string]any) error
	// Send delivers a single message and returns the **number of items actually delivered** along with any error.
	//
	// Why return the item count: every platform has a message length limit, so a digest message that cannot fit the whole batch gets truncated.
	// If the caller unconditionally marked the entire batch as delivered, the truncated items would vanish—invisible in the message
	// yet marked as succeeded in delivery history, leaving no way to discover that findings were never sent. Returning kept allows
	// the caller to mark only the first kept items and leave the rest for the next batch.
	//
	// Returning an error indicates delivery failure, where *PermanentError means it should not be retried.
	// On failure, kept is meaningless and callers should ignore it.
	Send(ctx context.Context, cfg map[string]any, m Message) (int, error)
	// DefaultRatePerMin returns the platform's officially recommended per-minute delivery limit, used as the default rate limit
	// when creating a new channel instance. Returning 0 means no known limit.
	DefaultRatePerMin() int
	// SecretKeys returns the configuration keys that hold credentials for this channel. Their values are masked when echoed by the API,
	// and receiving a masked value on update preserves the original value in the database. Only the implementation itself knows which fields
	// count as credentials (for WeCom the entire Webhook URL is a credential, whereas for DingTalk only its secret is),
	// so this knowledge must be provided by the channel rather than guessed by upper layers.
	SecretKeys() []string
	// DestinationKeys returns the configuration keys that determine "where messages are sent" for this channel.
	//
	// Like SecretKeys, this is security-critical: destination addresses and credentials are two separate sets of fields;
	// if "changing only the address while keeping the original credentials" were allowed, anyone who can edit channel configurations
	// could exfiltrate real credentials from the database to a server they control, rendering configuration masking useless.
	// See PrepareConfigUpdate for details.
	DestinationKeys() []string
}

// registry is the channel registry. Intentionally defined as an explicit map literal rather than self-registering via init():
// this makes all available channels visible in one place and exposes omissions at compile time rather than relying on runtime side effects.
var registry = map[string]Channel{
	KindDingTalk: dingTalkChannel{},
	KindFeishu:   feishuChannel{},
	KindWeCom:    weComChannel{},
	KindWebhook:  webhookChannel{},
	KindTelegram: telegramChannel{},
	KindEmail:    emailChannel{},
}

// Get retrieves a channel implementation by kind.
func Get(kind string) (Channel, bool) {
	c, ok := registry[kind]
	return c, ok
}

// ValidKind reports whether kind is a supported channel type.
func ValidKind(kind string) bool {
	_, ok := registry[kind]
	return ok
}

// Kinds returns all supported channel types in lexicographical order (for stable UI dropdown display).
func Kinds() []string {
	out := make([]string, 0, len(registry))
	for k := range registry {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// PermanentError marks a delivery failure that should not be retried: invalid credentials, rejected destination, malformed payload, etc.
// Retrying only makes sense for transient faults (network jitter, rate limiting, upstream 5xx); repeatedly backing off and retrying permanent failures
// will never succeed and only buries the real error in retry logs.
type PermanentError struct{ Err error }

func (e *PermanentError) Error() string { return e.Err.Error() }
func (e *PermanentError) Unwrap() error { return e.Err }

// Permanent marks err as a permanent failure. Returns nil when err is nil,
// making it convenient to write `return Permanent(someCheck())`.
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return &PermanentError{Err: err}
}

// IsPermanent reports whether err's chain contains a permanent failure marker.
func IsPermanent(err error) bool {
	var pe *PermanentError
	return errors.As(err, &pe)
}

// ---- Configuration reader helpers ----
//
// Channel configurations come from the database's JSONB column and unmarshal via encoding/json into map[string]any,
// where numbers are always float64 and arrays are []any. The helpers below unify this conversion and tolerate
// type discrepancies caused by user input in the UI (such as entering a port number as a string).

// cfgString reads a string config item and always trims leading/trailing whitespace—copy-pasting from web forms easily introduces spaces.
func cfgString(cfg map[string]any, key string) string {
	v, ok := cfg[key]
	if !ok {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(s)
}

// cfgInt reads an integer config item, accepting both float64 (JSON default) and string sources.
func cfgInt(cfg map[string]any, key string) int {
	switch v := cfg[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return 0
		}
		return n
	default:
		return 0
	}
}

// cfgBool reads a boolean config item, also accepting string values "true"/"1"/"yes".
func cfgBool(cfg map[string]any, key string) bool {
	switch v := cfg[key].(type) {
	case bool:
		return v
	case string:
		s := strings.ToLower(strings.TrimSpace(v))
		return s == "true" || s == "1" || s == "yes"
	default:
		return false
	}
}

// cfgStrings reads a string slice config item, automatically trimming whitespace and discarding empty strings.
func cfgStrings(cfg map[string]any, key string) []string {
	raw, ok := cfg[key].([]any)
	if !ok {
		// Also accept a single string for convenience when submitting a form with a single value.
		if s := cfgString(cfg, key); s != "" {
			return []string{s}
		}
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		s, ok := v.(string)
		if !ok {
			continue
		}
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// cfgMap reads a string map config item (such as custom HTTP headers), trimming whitespace from keys and values and discarding empty keys.
func cfgMap(cfg map[string]any, key string) map[string]string {
	raw, ok := cfg[key].(map[string]any)
	if !ok {
		return nil
	}
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		s, ok := v.(string)
		if !ok {
			continue
		}
		out[k] = s
	}
	return out
}
