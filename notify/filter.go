package notify

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// Filter is the contract for the notification_channels.filter JSONB column: the filtering criteria for a channel instance.
// All fields are optional, and omitting them means "no filtering"—which is also the fallback semantics for malformed configs; see ParseFilter.
type Filter struct {
	// MinSeverity is the minimum severity threshold (low/medium/high/critical); empty = no threshold.
	MinSeverity string `json:"min_severity"`
	// TaskIDs / AssetIDs as empty slices mean unrestricted; when non-empty, the event must intersect with them.
	TaskIDs  []int64 `json:"task_ids"`
	AssetIDs []int64 `json:"asset_ids"`
	// VulnClassInclude when empty accepts all; when non-empty, vulnclass must match at least one keyword.
	// VulnClassExclude excludes the event if any keyword matches (exclusion takes precedence over inclusion).
	// Matching uses case-insensitive substrings—safer than regex so a misconfigured pattern cannot silently disable a channel.
	VulnClassInclude []string `json:"vulnclass_include"`
	VulnClassExclude []string `json:"vulnclass_exclude"`
	// OnStatusChange controls whether this channel receives finding status-change events (meaningful only in realtime mode).
	OnStatusChange bool `json:"on_status_change"`
}

// ParseFilter parses channel filter configuration.
//
// **Never returns an error.** This is a deliberate design choice: malformed filter configurations always degrade
// to a zero-value Filter (= no filtering = match all), because for a vulnerability notification system, **sending an extra notification
// is far better than silently dropping a critical finding**. Turning a parse failure into "do not send" would give the user a channel
// that looks configured but actually pushes nothing—the worst possible failure mode.
func ParseFilter(raw []byte) Filter {
	var f Filter
	if len(raw) == 0 {
		return f
	}
	// On parse failure, f remains the zero value, i.e., no filtering.
	_ = json.Unmarshal(raw, &f)
	return f
}

// ValidMinSeverity reports whether s is a valid minimum severity threshold (empty string means no threshold).
func ValidMinSeverity(s string) bool {
	if s == "" {
		return true
	}
	_, ok := severityRank[s]
	return ok
}

// Validate checks fields with **restricted values** in the filter configuration, called when saving a channel.
//
// Why this must be blocked at write time: Match evaluates an unknown threshold as `rank >= 0`, which is always true—
// meaning a single typo in min_severity ("hgih") causes the filter to **silently degrade** into
// "push everything". While this aligns with the package's "prefer over-delivery to under-delivery" trade-off (nothing is missed),
// the consequence is that a user who thinks they configured severity filtering ends up flooding the group with every finding,
// with no indication of the typo. Such silent degradation should be caught at the entry point.
//
// Note that Validate is used only on the **write** path. The read path still follows ParseFilter's lenient semantics
// so pre-existing bad values in historical data do not prevent a channel from being loaded at all.
func (f Filter) Validate() error {
	if !ValidMinSeverity(f.MinSeverity) {
		return fmt.Errorf("invalid minimum severity %q: allowed values are low / medium / high / critical, or leave empty for no limit", f.MinSeverity)
	}
	return nil
}

// Match determines whether an event should be delivered to a channel with the given filter.
//
// **Never returns an error**, for the same reason as ParseFilter: any internal anomaly is treated as a match.
// Evaluation order: event kind → severity threshold → task/asset scope → vulnerability class keywords.
func Match(f Filter, s Snapshot) bool {
	// Status-change events are received only by channels that explicitly opt in. Disabled by default because most users
	// expect "notifications" to mean "new vulnerability discovered" rather than a running log of every status transition.
	if s.Kind == EventFindingStatusChanged && !f.OnStatusChange {
		return false
	}
	if !AtLeast(s.Severity, f.MinSeverity) {
		return false
	}
	if len(f.TaskIDs) > 0 && !slices.Contains(f.TaskIDs, s.TaskID) {
		return false
	}
	if len(f.AssetIDs) > 0 && !intersectsInt(f.AssetIDs, s.AssetIDs) {
		return false
	}
	// Exclusion takes precedence: matching any exclude keyword disqualifies the event, even if an include keyword also matches.
	if len(f.VulnClassExclude) > 0 && containsAnyFold(s.VulnClass, f.VulnClassExclude) {
		return false
	}
	if len(f.VulnClassInclude) > 0 && !containsAnyFold(s.VulnClass, f.VulnClassInclude) {
		return false
	}
	return true
}

func intersectsInt(a, b []int64) bool {
	// Linear scan is sufficient for small sets; both sides are on the order of "dozens of hand-selected items",
	// where building a map costs more than it saves.
	for _, v := range b {
		if slices.Contains(a, v) {
			return true
		}
	}
	return false
}

// containsAnyFold reports whether s contains any of the keywords (case-insensitive).
func containsAnyFold(s string, keywords []string) bool {
	lower := strings.ToLower(s)
	for _, kw := range keywords {
		kw = strings.ToLower(strings.TrimSpace(kw))
		if kw != "" && strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}
