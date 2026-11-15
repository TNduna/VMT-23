package notify

import "testing"

func TestParseFilterMalformedFallsBackToMatchAll(t *testing.T) {
	// Malformed JSON, empty input, or mismatched field types must all degrade to a zero-value Filter
	// ("no filtering"). This invariant implements "prefer over-delivery to under-delivery":
	// if ParseFilter returned an error or partially failed, a single typo could silently drop all critical alerts.
	cases := []struct {
		name string
		raw  string
	}{
		{"empty input", ""},
		{"invalid JSON", `{not json`},
		{"truncated JSON", `{"min_severity":`},
		{"type mismatch", `{"min_severity": 123, "task_ids": "abc"}`},
		{"top-level array", `[1,2,3]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := ParseFilter([]byte(tc.raw))
			if f.MinSeverity != "" || len(f.TaskIDs) != 0 || len(f.AssetIDs) != 0 {
				t.Fatalf("malformed config should degrade to zero-value Filter, got %+v", f)
			}
			// Zero-value Filter must match any event.
			ev := Snapshot{Kind: EventFindingCreated, Severity: "low", VulnClass: "XSS"}
			if !Match(f, ev) {
				t.Fatal("zero-value Filter should match all events")
			}
		})
	}
}

func TestMatchSeverityThreshold(t *testing.T) {
	ev := func(sev string) Snapshot {
		return Snapshot{Kind: EventFindingCreated, Severity: sev}
	}
	cases := []struct {
		min    string
		sev    string
		expect bool
	}{
		{"", "low", true},
		{"", "critical", true},
		{"high", "critical", true},
		{"high", "high", true},
		{"high", "medium", false},
		{"high", "low", false},
		{"critical", "high", false},
		{"critical", "critical", true},
		// Unknown severities have rank 0 and should be blocked by any non-empty threshold (do not push when in doubt).
		{"low", "", false},
		{"low", "unknown", false},
		{"", "", true},
	}
	for _, tc := range cases {
		got := Match(Filter{MinSeverity: tc.min}, ev(tc.sev))
		if got != tc.expect {
			t.Errorf("min=%q sev=%q: expected %v, got %v", tc.min, tc.sev, tc.expect, got)
		}
	}
}

func TestMatchScopeRestrictions(t *testing.T) {
	ev := Snapshot{
		Kind:      EventFindingCreated,
		Severity:  "high",
		TaskID:    7,
		AssetIDs:  []int64{10, 20},
		VulnClass: "SQL Injection",
	}
	cases := []struct {
		name   string
		filter Filter
		expect bool
	}{
		{"empty scope = unrestricted", Filter{}, true},
		{"task match", Filter{TaskIDs: []int64{7}}, true},
		{"task mismatch", Filter{TaskIDs: []int64{8}}, false},
		{"multiple tasks with match", Filter{TaskIDs: []int64{8, 7}}, true},
		{"asset intersection", Filter{AssetIDs: []int64{20, 99}}, true},
		{"no asset intersection", Filter{AssetIDs: []int64{99}}, false},
		{"both task and asset match", Filter{TaskIDs: []int64{7}, AssetIDs: []int64{10}}, true},
		{"task matches but asset does not", Filter{TaskIDs: []int64{7}, AssetIDs: []int64{99}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Match(tc.filter, ev); got != tc.expect {
				t.Errorf("expected %v, got %v", tc.expect, got)
			}
		})
	}
}

func TestMatchVulnClassKeywords(t *testing.T) {
	ev := func(class string) Snapshot {
		return Snapshot{Kind: EventFindingCreated, Severity: "high", VulnClass: class}
	}
	cases := []struct {
		name   string
		filter Filter
		class  string
		expect bool
	}{
		{"empty include = accept all", Filter{}, "Any Class", true},
		{"include hit", Filter{VulnClassInclude: []string{"SQL"}}, "SQL Injection", true},
		{"include miss", Filter{VulnClassInclude: []string{"RCE"}}, "SQL Injection", false},
		{"include multiple keywords with hit", Filter{VulnClassInclude: []string{"RCE", "SQL"}}, "SQL Injection", true},
		{"case-insensitive match", Filter{VulnClassInclude: []string{"sql"}}, "SQL Injection", true},
		{"exclude hit disqualifies", Filter{VulnClassExclude: []string{"Info Leak"}}, "Info Leak", false},
		{"exclude miss passes", Filter{VulnClassExclude: []string{"Info Leak"}}, "SQL Injection", true},
		// Exclusion takes precedence over inclusion: when both match, the event is excluded.
		{"exclude takes precedence over include", Filter{
			VulnClassInclude: []string{"SQL"},
			VulnClassExclude: []string{"Injection"},
		}, "SQL Injection", false},
		// Whitespace-only keywords should be ignored so they do not match every string containing spaces.
		{"whitespace-only keywords ignored", Filter{VulnClassInclude: []string{"", "  "}}, "SQL Injection", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Match(tc.filter, ev(tc.class)); got != tc.expect {
				t.Errorf("expected %v, got %v", tc.expect, got)
			}
		})
	}
}

func TestMatchStatusChangeRequiresOptIn(t *testing.T) {
	ev := Snapshot{Kind: EventFindingStatusChanged, Severity: "critical", FromStatus: "pending", ToStatus: "fixed"}
	// Disabled by default: most users expect vulnerability notifications only for new findings, not every status change.
	if Match(Filter{MinSeverity: "low"}, ev) {
		t.Fatal("status-change event should be skipped when OnStatusChange is not enabled")
	}
	if !Match(Filter{OnStatusChange: true}, ev) {
		t.Fatal("status-change event should match when OnStatusChange is enabled")
	}
	// Creation events are unaffected by OnStatusChange.
	created := Snapshot{Kind: EventFindingCreated, Severity: "critical"}
	if !Match(Filter{MinSeverity: "low"}, created) {
		t.Fatal("creation event should not depend on OnStatusChange")
	}
}
