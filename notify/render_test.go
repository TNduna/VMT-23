package notify

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTruncateBytesKeepsValidUTF8(t *testing.T) {
	// This is the most critical invariant of this package. WeChat Work limits length by BYTES (multi-byte UTF-8).
	// Any naive byte slicing cuts characters in half, producing invalid UTF-8 rejected by the platform.
	// Test various mixed-length inputs across every possible slice boundary.
	inputs := []string{
		"Multi-byte test content: café, naïve, résumé",
		"mixed multi-byte content: München, Zürich",
		"a-é-b-ü-c-ö-d-ç-e",
		"🔴🟠🟡🔵", // 4-byte emoji, making slicing errors even more obvious
		strings.Repeat("vuln", 100),
	}
	for _, in := range inputs {
		for max := 1; max <= len(in)+2; max++ {
			got := TruncateBytes(in, max)
			if !utf8.ValidString(got) {
				t.Fatalf("input %q max=%d: produced invalid UTF-8 %q", in, max, got)
			}
			if len(got) > max {
				t.Fatalf("input %q max=%d: result %d bytes exceeds limit", in, max, len(got))
			}
			// When not truncated, content must remain unchanged.
			if len(in) <= max && got != in {
				t.Fatalf("input %q max=%d: changed content when within limit -> %q", in, max, got)
			}
		}
	}
}

func TestTruncateBytesZeroMeansUnlimited(t *testing.T) {
	long := strings.Repeat("x", 10000)
	if got := TruncateBytes(long, 0); got != long {
		t.Fatal("max=0 should mean unlimited")
	}
	if got := TruncateBytes(long, -5); got != long {
		t.Fatal("max<0 should mean unlimited")
	}
}

func TestTruncateBytesEllipsisBudget(t *testing.T) {
	// When max is smaller than the ellipsis itself, appending an ellipsis must not exceed the limit.
	got := TruncateBytes("abcdefgh", 1)
	if len(got) > 1 {
		t.Fatalf("when max=1 result %q length %d exceeds limit", got, len(got))
	}
	// Normal cases should carry the ellipsis.
	if got := TruncateBytes("abcdefgh", 5); !strings.HasSuffix(got, ellipsis) {
		t.Fatalf("expected ellipsis suffix, got %q", got)
	}
}

func TestTruncateRunesCountsCharactersNotBytes(t *testing.T) {
	// The distinction from TruncateBytes must be preserved: Telegram limits length by characters.
	// Using byte limits would prematurely truncate multi-byte text to one third.
	s := "αβγδεζηθικ"
	got := TruncateRunes(s, 5)
	if n := utf8.RuneCountInString(got); n != 5 {
		t.Fatalf("expected 5 characters, got %d (%q)", n, got)
	}
	// The same string measured by bytes should be significantly shorter.
	if utf8.RuneCountInString(TruncateBytes(s, 5)) >= 5 {
		t.Fatal("byte limit should not yield the same character count as rune limit")
	}
}

func TestOneLineCollapsesWhitespace(t *testing.T) {
	got := OneLine("line 1\n\nline 2\twith tabs   extra spaces", 0)
	if strings.ContainsAny(got, "\n\t") {
		t.Fatalf("should collapse all whitespace, got %q", got)
	}
	if strings.Contains(got, "  ") {
		t.Fatalf("should not retain consecutive spaces, got %q", got)
	}
	// After truncation it must remain readable and valid.
	got = OneLine("1234567890", 4)
	if n := utf8.RuneCountInString(got); n != 4 {
		t.Fatalf("expected 4 characters, got %d (%q)", n, got)
	}
}

func TestTruncateHTMLNeverCutsTagInHalf(t *testing.T) {
	// Direct HTML truncation could produce broken tags like `<a href="htt`, which platforms reject.
	s := `<b>Title</b>Body text<a href="https://example.com/very/long/path">View details</a>`
	for max := 1; max <= utf8.RuneCountInString(s)+2; max++ {
		got := TruncateHTML(s, max)
		if n := utf8.RuneCountInString(got); max > 0 && n > max {
			t.Fatalf("max=%d: result %d characters exceeds limit", max, n)
		}
		// Tail must not have an unclosed `<` (i.e. `<` in final fragment without `>`).
		if lt := strings.LastIndex(got, "<"); lt >= 0 && !strings.Contains(got[lt:], ">") {
			t.Fatalf("max=%d: trailing tag was truncated -> %q", max, got)
		}
	}
}

func TestAssetLineOmitsExcess(t *testing.T) {
	if got := assetLine(nil, 3); got != "" {
		t.Fatalf("no assets should return empty string, got %q", got)
	}
	if got := assetLine([]string{"a", "b"}, 3); got != "a, b" {
		t.Fatalf("within limit should list all, got %q", got)
	}
	// When exceeding limit, must note total count so reader knows how many assets remain.
	got := assetLine([]string{"a", "b", "c", "d", "e"}, 2)
	if !strings.Contains(got, "and 5 total") {
		t.Fatalf("should note total 5, got %q", got)
	}
}

func TestSeverityAndStatusLabels(t *testing.T) {
	if AtLeast("", "low") {
		t.Fatal("empty level order is 0, should be blocked by any threshold")
	}
	if !AtLeast("critical", "") {
		t.Fatal("empty threshold should pass")
	}
	if got := StatusLabel("fixed"); got != "Fixed" {
		t.Fatalf("unexpected status mapping, got %q", got)
	}
	// Unknown status is echoed as-is without inventing labels.
	if got := StatusLabel("weird_status"); got != "weird_status" {
		t.Fatalf("unknown status should be echoed as-is, got %q", got)
	}
}

// TestTruncateHTMLNeverCutsEntity covers an audit finding: truncation must avoid
// not only half-cut tags, but also severed HTML entities.
//
// When `&amp;` is cut into `&amp`, an entity-validating parser may reject the ENTIRE message.
func TestTruncateHTMLNeverCutsEntity(t *testing.T) {
	s := "aaaa&amp;bbbb&lt;cccc&quot;dddd"
	for max := 1; max <= utf8.RuneCountInString(s)+2; max++ {
		got := TruncateHTML(s, max)
		// Tail must not have entity fragments like `&` without matching `;`.
		if amp := strings.LastIndex(got, "&"); amp >= 0 && !strings.Contains(got[amp:], ";") {
			t.Fatalf("max=%d: trailing entity fragment %q", max, got[amp:])
		}
		if strings.Contains(got, "&amp\x00") {
			t.Fatalf("max=%d: malformed entity present", max)
		}
	}
}
