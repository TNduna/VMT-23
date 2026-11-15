package notify

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// This file covers whole-item packing for digest messages: when a digest exceeds a channel's length limit,
// it must be truncated on **whole-item boundaries** and report the exact count of packed items so the caller
// marks only the delivered items as sent.

func TestMarkdownBodyPacksWholeItemsWithinByteLimit(t *testing.T) {
	// A 200-item digest easily exceeds WeCom's 4096-byte limit.
	m := batchMsg(200)
	body, kept := markdownBody(m, weComMarkdownLimit)

	if len(body) > weComMarkdownLimit {
		t.Fatalf("body length %d bytes exceeds limit %d", len(body), weComMarkdownLimit)
	}
	if !utf8.ValidString(body) {
		t.Fatal("body is not valid UTF-8")
	}
	if kept <= 0 || kept >= len(m.Items) {
		t.Fatalf("expected partial packing (0 < kept < %d), got %d", len(m.Items), kept)
	}
	// The header must state how many items are shown and how many remain—otherwise recipients would assume
	// the total in the header is everything.
	if !strings.Contains(body, "remaining") || !strings.Contains(body, "will continue in the next message") {
		t.Fatalf("header should state how many items remain for the next message:\n%s", body[:minInt(400, len(body))])
	}
	// Only the first kept items should appear.
	for i := 0; i < kept; i++ {
		if !strings.Contains(body, "Vulnerability "+itoa(i+1)) {
			t.Fatalf("item #%d should be present in this message:\n%s", i+1, body)
		}
	}
	if strings.Contains(body, "Vulnerability "+itoa(kept+1)) {
		t.Fatalf("item #%d should not appear (it belongs to the next batch)", kept+1)
	}
}

func TestMarkdownBodyKeepsEverythingWhenUnderLimit(t *testing.T) {
	m := batchMsg(3)
	body, kept := markdownBody(m, 0) // 0 = unlimited
	if kept != len(m.Items) {
		t.Fatalf("expected all items kept when unlimited, got kept=%d", kept)
	}
	if strings.Contains(body, "remaining") {
		t.Fatalf("truncation notice should not appear when nothing was truncated:\n%s", body)
	}
}

func TestMarkdownBodyAlwaysKeepsAtLeastOneItem(t *testing.T) {
	// Even when the budget is too small to fit a single item, at least one item must be sent (with final truncation as fallback);
	// otherwise a single oversized finding would stall the entire batch forever.
	m := batchMsg(5)
	_, kept := markdownBody(m, 50)
	if kept != 1 {
		t.Fatalf("at least 1 item must be kept, got %d", kept)
	}
}

func TestMarkdownBodySingleReturnsOne(t *testing.T) {
	_, kept := markdownBody(singleMsg(), 4096)
	if kept != 1 {
		t.Fatalf("single message should report 1 item delivered, got %d", kept)
	}
	// Empty message has 0 deliverable items.
	if _, k := markdownBody(Message{}, 4096); k != 0 {
		t.Fatalf("empty message should report 0 items, got %d", k)
	}
}

func TestTelegramPackingUsesRuneBudget(t *testing.T) {
	m := batchMsg(200)
	text, kept := telegramHTML(m)
	// Telegram limits by **character (rune) count**.
	if n := utf8.RuneCountInString(text); n > telegramTextLimit {
		t.Fatalf("body length %d runes exceeds limit %d", n, telegramTextLimit)
	}
	if kept <= 0 || kept >= len(m.Items) {
		t.Fatalf("expected partial packing, got %d", kept)
	}
	if !strings.Contains(text, "will continue in next message") {
		t.Fatalf("should state remaining items continue in next message:\n%.300s", text)
	}
}

func TestFeishuPackingReportsKept(t *testing.T) {
	m := batchMsg(2000)
	_, kept := feishuCard(m)
	if kept <= 0 || kept >= len(m.Items) {
		t.Fatalf("card should pack only a subset of items, got %d", kept)
	}
}

func TestWebhookAndEmailReportAllItems(t *testing.T) {
	// These two channels do not truncate the body; the entire batch counts as delivered.
	m := batchMsg(7)
	if n := len(m.Items); n != 7 {
		t.Fatal("precondition failed")
	}
	if _, k := markdownBody(m, 0); k != len(m.Items) {
		t.Fatalf("expected all items when unlimited, got %d", k)
	}
}

// TestMarkdownEscapesUntrustedContent is a regression test ensuring untrusted content cannot alter message structure.
// Titles and summaries come from model output (reading responses from the target under test), and asset names come from target URLs.
func TestMarkdownEscapesUntrustedContent(t *testing.T) {
	cases := []struct {
		name  string
		item  Item
		must  []string // Must appear in output (escaped form)
		wrong []string // Must not appear in output (unescaped form)
	}{
		{
			name: "newline + external link in title",
			item: Item{
				Severity: "high",
				Name:     "Login SQL Injection\n[URGENT: Verify account here](http://attacker.tld)",
			},
			// Newlines must be collapsed (otherwise they can forge new list items/blockquotes);
			// brackets and parentheses must be escaped (otherwise they render as clickable external links).
			must:  []string{`\[URGENT: Verify account here\]`, `\(http://attacker.tld\)`},
			wrong: []string{"\n[URGENT", "\n\n[URGENT"},
		},
		{
			name: "image beacon in title",
			item: Item{
				Severity: "high",
				Name:     "Vuln ![](http://attacker.tld/beacon)",
			},
			must:  []string{`\!`, `\(http://attacker.tld/beacon\)`},
			wrong: []string{"![]("},
		},
		{
			name: "emphasis and blockquote in asset name",
			item: Item{
				Severity: "high",
				Name:     "Normal Title",
				Assets:   []string{"a.com/*inject*>quote"},
			},
			must:  []string{`\*inject\*`, `\>`},
			wrong: []string{"*inject*"},
		},
		{
			name: "backticks and pipe in summary",
			item: Item{
				Severity: "high",
				Name:     "Title",
				Summary:  "`code` | table",
			},
			must:  []string{"\\`code\\`", `\|`},
			wrong: []string{"`code`"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := Message{Items: []Item{tc.item}}
			var b strings.Builder
			writeItem(&b, tc.item, "", true)
			got := b.String()
			for _, want := range tc.must {
				if !strings.Contains(got, want) {
					t.Errorf("missing escaped form %q:\n%s", want, got)
				}
			}
			for _, bad := range tc.wrong {
				if strings.Contains(got, bad) {
					t.Errorf("found unescaped form %q (could inject structure or external links):\n%s", bad, got)
				}
			}
			_ = m
		})
	}
}

// TestMarkdownEscapeBackslashFirst locks in the escaping order: backslashes must be escaped first,
// otherwise backslashes introduced by later replacements would be escaped a second time.
func TestMarkdownEscapeBackslashFirst(t *testing.T) {
	if got := markdownEscape(`a\b*c`); got != `a\\b\*c` {
		t.Fatalf("wrong escape order, got %q", got)
	}
}

// TestTelegramTitleHasNoMarkdownEscapes locks in a specific regression:
// markdown escapes must not leak into Telegram HTML output.
func TestTelegramTitleHasNoMarkdownEscapes(t *testing.T) {
	m := Message{Items: []Item{{Severity: "high", Name: "alert(1) *important*"}}}
	text, _ := telegramHTML(m)
	if strings.Contains(text, `\(`) || strings.Contains(text, `\*`) {
		t.Fatalf("Telegram HTML body contained markdown backslash escapes:\n%s", text)
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
