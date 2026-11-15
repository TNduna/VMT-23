package notify

import (
	"fmt"
	"strings"
)

// This file implements shared message rendering for "Markdown-family" channels (DingTalk, WeCom).
// Feishu uses card JSON, Telegram uses HTML, and Email uses HTML, each rendered in its respective adapter.

// maxAssetsShown is the maximum number of assets listed in a message. A vulnerability may be anchored to dozens of assets;
// listing them all would bloat the message without adding value—nobody reads past the 3rd domain in an IM message.
const maxAssetsShown = 3

// maxSummaryRunes is the rune limit to which summaries are compressed. IM messages are prompts to check details,
// not full reports; the complete content lives in the platform.
const maxSummaryRunes = 120

// markdownReservedBytes is reserved for the message header (digest summary line + severity breakdown + optional truncation notice)
// and footer (platform link). Subtracted from the budget when packing whole items so the header and footer are never cut off—
// if they were cut, the reader wouldn't even know which batch this is or how many items were omitted.
const markdownReservedBytes = 320

// markdownEscape escapes markdown metacharacters.
//
// Why this is mandatory: vulnerability titles, summaries, classes, and asset display names all come from **untrusted sources**—
// titles and summaries come from model output (which reads responses from the target under test), and asset URLs are full URLs
// discovered during scanning (including target-controlled query strings). Without escaping, a vulnerability titled
//
//	Login SQL Injection\n[URGENT: Click to verify account](http://attacker.tld)
//
// would render as a **clickable external link** in a security engineer's DingTalk/Feishu client; and
// `![](http://attacker.tld/beacon)` would be fetched automatically upon rendering, signaling that
// "this finding has been viewed" and leaking the reader's IP. Even without malicious intent, unescaped bold or
// blockquote markers could push critical findings below the fold.
//
// The escaped set covers headings/links/emphasis/lists/blockquotes/strikethrough—characters that alter structure or create clickable
// elements. `\` must be processed first so backslashes added by subsequent replacements are not double-escaped.
func markdownEscape(s string) string {
	replacer := strings.NewReplacer(
		`\`, `\\`,
		"`", "\\`",
		"*", `\*`,
		"_", `\_`,
		"[", `\[`,
		"]", `\]`,
		"(", `\(`,
		")", `\)`,
		"!", `\!`,
		"#", `\#`,
		">", `\>`,
		"|", `\|`,
		"~", `\~`,
	)
	return replacer.Replace(s)
}

// markdownText collapses untrusted text into a single line and escapes it for use in markdown bodies.
// Single-lining is the other half of escaping: newlines alone can forge new list items or blockquotes
// that character escaping cannot prevent.
func markdownText(s string, maxRunes int) string {
	return markdownEscape(OneLine(s, maxRunes))
}

// markdownTitle returns the message title (IM platform title bar / card header) as **unescaped plain text**.
//
// Escaping is intentionally omitted here: this title is shared across four rendering contexts—markdown body, Telegram
// HTML, Feishu card plain_text, and generic Webhook JSON / email subjects. Each context has different escaping rules
// (putting markdown escapes into HTML leaves visible backslashes; putting them into JSON pollutes the data),
// so escaping must be handled by each output sink; see writeItem / feishuItemLines / telegramEscape.
// Previously, adding markdown escaping in this shared function caused visible `\(1\)` backslashes in Telegram messages.
func markdownTitle(m Message) string {
	if m.Batch {
		return fmt.Sprintf("Vulnerability Digest · %d Total", len(m.Items))
	}
	if len(m.Items) == 0 {
		return "Vulnerability Notification"
	}
	it := m.Items[0]
	return fmt.Sprintf("[%s] %s", SeverityLabel(it.Severity), OneLine(it.Title(), 0))
}

// markdownBody renders the message body, returning the body text and the **number of items actually included**.
//
// The returned kept count is the number of items actually delivered in this push, allowing the caller to mark only
// the first kept items as delivered—items excluded by the channel's length limit must remain for the next batch
// rather than being marked as delivered together. That was the root cause of silent drops: the message was truncated,
// yet delivery records showed everything succeeded, leaving no trace that the second half was never sent.
//
// maxBytes<=0 means unlimited.
func markdownBody(m Message, maxBytes int) (string, int) {
	if !m.Batch {
		if len(m.Items) == 0 {
			return "", 0
		}
		var b strings.Builder
		writeItem(&b, m.Items[0], "", true)
		// A single-item message is sent even if it exceeds the limit (with final truncation as a fallback):
		// partial info for one finding is better than sending nothing at all.
		return TruncateBytes(b.String(), maxBytes), 1
	}

	footer := ""
	if m.HomeURL != "" {
		footer = fmt.Sprintf("\n[View all in platform](%s)\n", m.HomeURL)
	}
	kept := packItemCount(m.Items, maxBytes, markdownReservedBytes, footer, byteSize, func(it Item, idx int) string {
		var b strings.Builder
		writeItem(&b, it, fmt.Sprintf("%d. ", idx+1), false)
		return b.String()
	})

	items := m.Items[:kept]
	var b strings.Builder
	b.WriteString(markdownBatchIntro(m, items, len(m.Items)))
	for i, it := range items {
		writeItem(&b, it, fmt.Sprintf("%d. ", i+1), false)
	}
	b.WriteString(footer)
	return TruncateBytes(b.String(), maxBytes), kept
}

// markdownBatchIntro renders the opening of a digest message: time window, item count, and severity breakdown.
// With this summary, recipients can tell whether the batch needs immediate attention without opening the platform.
//
// items are the items **actually packed**, while total is the total count in this batch. When they differ, the header
// must explicitly state how many items remain for the next message—otherwise the reader would assume the number in the header
// is the complete set, while the omitted items never appear in chat.
func markdownBatchIntro(m Message, items []Item, total int) string {
	var b strings.Builder
	if m.WindowMinutes > 0 {
		fmt.Fprintf(&b, "**%d new vulnerabilities in the last %d minutes**", total, m.WindowMinutes)
	} else {
		fmt.Fprintf(&b, "**%d new vulnerabilities**", total)
	}
	if extra := total - len(items); extra > 0 {
		fmt.Fprintf(&b, " (showing first %d; remaining %d will continue in the next message)", len(items), extra)
	}
	// Show breakdown by severity so readers can spot critical items at a glance. Count only items **actually included**
	// in this message so "Critical 3" matches the items listed below.
	counts := map[string]int{}
	for _, it := range items {
		counts[it.Severity]++
	}
	var parts []string
	for _, sev := range []string{"critical", "high", "medium", "low"} {
		if n := counts[sev]; n > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", SeverityLabel(sev), n))
		}
	}
	if len(parts) > 0 {
		b.WriteString("\n" + strings.Join(parts, " · "))
	}
	b.WriteString("\n\n")
	return b.String()
}

// writeItem renders a single vulnerability item.
//
// prefix is used for numbering in digest lists; single=true renders the full version (including summary and detail link),
// whereas digest lists render a compact one-line summary—otherwise a 50-item digest would turn into a long document.
//
// All externally sourced content (title/class/assets/summary) passes through markdownText:
// single-lined + escaped. The detail link is constructed from the admin-configured public_base_url, not untrusted content,
// and must remain a clickable link, so it is output as-is.
func writeItem(b *strings.Builder, it Item, prefix string, single bool) {
	line := fmt.Sprintf("%s**%s · %s**", prefix, SeverityLabel(it.Severity), markdownText(it.Title(), 0))
	if !single {
		// Digest mode: single-line presentation with compressed assets and summary appended.
		var extras []string
		if a := assetLine(it.Assets, maxAssetsShown); a != "" {
			extras = append(extras, markdownText(a, 0))
		}
		if it.Summary != "" {
			extras = append(extras, markdownText(it.Summary, 60))
		}
		if len(extras) > 0 {
			line += " — " + strings.Join(extras, " · ")
		}
		b.WriteString(line + "\n")
		return
	}
	b.WriteString(line + "\n")
	if it.IsStatusChange() {
		fmt.Fprintf(b, "**Status Change**: %s → %s\n",
			markdownText(StatusLabel(it.FromStatus), 0), markdownText(StatusLabel(it.ToStatus), 0))
	}
	if it.VulnClass != "" && it.VulnClass != it.Title() {
		fmt.Fprintf(b, "**Type**: %s\n", markdownText(it.VulnClass, 0))
	}
	if a := assetLine(it.Assets, maxAssetsShown); a != "" {
		fmt.Fprintf(b, "**Assets**: %s\n", markdownText(a, 0))
	}
	if it.Summary != "" {
		if s := markdownText(it.Summary, maxSummaryRunes); s != "" {
			fmt.Fprintf(b, "**Summary**: %s\n", s)
		}
	}
	if it.DetailURL != "" {
		fmt.Fprintf(b, "[View Details](%s)\n", it.DetailURL)
	}
}
