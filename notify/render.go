package notify

import (
	"strings"
	"unicode/utf8"
)

const ellipsis = "…"

// TruncateBytes truncates s to at most max bytes, guaranteeing valid UTF-8 without splitting multi-byte characters.
//
// Why we must cut on character boundaries: WeCom group bots enforce a hard limit of 4096 **bytes** (not characters)
// on markdown content, while CJK characters take 3 bytes each. Slicing directly by byte index can split a multi-byte character
// in half and produce invalid UTF-8—causing the platform to either reject the entire message or render replacement characters.
// Here we step backward from the budget position to the nearest rune start byte (utf8.RuneStart checks continuation bytes 0b10xxxxxx).
//
// max<=0 means unlimited. Appends an ellipsis when truncated, unless max is too small to fit the ellipsis itself.
func TruncateBytes(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	budget := max - len(ellipsis)
	suffix := ellipsis
	if budget < 0 {
		// max is shorter than the ellipsis itself: drop the ellipsis and do a plain truncation so the result never exceeds max.
		budget = max
		suffix = ""
	}
	cut := budget
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + suffix
}

// OneLine collapses multi-line text into a single line: collapses all whitespace and then truncates by rune count.
// Used for IM message title lines—summaries often contain newlines that would break table or header formatting if inserted raw.
// max<=0 means unlimited length.
func OneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	return TruncateRunes(s, max)
}

// TruncateRunes truncates s to at most max runes (rather than bytes), appending an ellipsis when exceeded.
// max<=0 means unlimited.
//
// The difference from TruncateBytes lies in platform measurement units: WeCom limits by bytes, whereas Telegram limits by character count.
// Using the wrong unit does not error out—it just cuts messages much shorter than expected (e.g., 3-byte UTF-8 characters
// cut at 4096 bytes leave only ~1365 characters), so both functions must be kept and chosen per channel.
func TruncateRunes(s string, max int) string {
	if max <= 0 {
		return s
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	if max <= 1 {
		return string(runes[:max])
	}
	return string(runes[:max-1]) + ellipsis
}

// TruncateHTML truncates an HTML fragment by rune count while ensuring no half-open tags or entities are left behind.
//
// Directly truncating HTML by character count can produce broken tags like `<a href="htt`, causing the platform parser
// to either reject the entire message or swallow subsequent text as an attribute value. Here we truncate by rune count first,
// then check whether there is an unclosed `<` at the tail and back up before it if so.
//
// We do not balance closing tags (like appending `</b>`): Telegram's HTML parser automatically closes unclosed tags,
// whereas implementing tag balancing ourselves would require handling quoted attributes, comments, and self-closing tags—complexity far outweighing the benefit.
func TruncateHTML(s string, max int) string {
	if max <= 0 || len([]rune(s)) <= max {
		return s
	}
	cut := TruncateRunes(s, max)
	// If the tail starts a tag fragment with `<` (no `>` after the last `<`), back up before `<`.
	if lt := strings.LastIndex(cut, "<"); lt >= 0 && !strings.Contains(cut[lt:], ">") {
		cut = cut[:lt]
	}
	// If the tail is a severed HTML entity (such as `&amp;` cut into `&amp`), back up before `&` as well.
	// An entity fragment in a strict parser can cause the **entire message** to be rejected—and since digest messages
	// exceeding the length limit are common, it is not worth losing the whole notification over this.
	if amp := strings.LastIndex(cut, "&"); amp >= 0 && !strings.Contains(cut[amp:], ";") {
		cut = cut[:amp]
	}
	return cut
}

// packItemCount calculates how many items can fit **completely** within the budget, used to pack digest messages by whole items.
//
// Why pack whole items instead of rendering the full text and truncating: truncation makes the latter items vanish into thin air
// while their delivery records are still marked as delivered—invisible in the message and invisible in delivery history,
// so findings are silently lost. By packing whole items, any items that do not fit remain in the database for the next batch,
// and the kept count returned to the caller reflects the exact number of items actually delivered in this message.
//
// Parameters: maxSize<=0 means unlimited; reserve is the amount reserved for message header/footer;
// size measures length (platforms differ: WeCom/DingTalk count bytes, Telegram counts runes—
// using the wrong unit does not error out, it just squeezes multi-byte messages far below the limit);
// render formats item idx into its actual text—lengths vary by content and cannot be estimated.
//
// Always returns at least 1 (as long as items is non-empty). Even if a single item is extremely long, that item must still be sent
// (with final truncation as a fallback by the caller); otherwise a single oversized finding would stall the entire batch forever.
func packItemCount(items []Item, maxSize, reserve int, footer string, size func(string) int, render func(Item, int) string) int {
	if maxSize <= 0 {
		return len(items)
	}
	budget := maxSize - reserve - size(footer)
	if budget < 0 {
		budget = 0
	}
	used := 0
	for i, it := range items {
		used += size(render(it, i))
		if used > budget && i > 0 {
			return i
		}
	}
	return len(items)
}

// byteSize / runeSize are the two measurement units for packItemCount, named explicitly to avoid bare
// `func(s string) int` closures at call sites where it would be hard to tell at a glance which unit is being used.
func byteSize(s string) int { return len(s) }
func runeSize(s string) int { return utf8.RuneCountInString(s) }

// assetLine renders an asset list into a single display line, omitting excess items beyond limit and noting the total count.
// A single vulnerability may be anchored to dozens of assets; listing all of them would blow up the message.
func assetLine(assets []string, limit int) string {
	if len(assets) == 0 {
		return ""
	}
	if limit <= 0 || len(assets) <= limit {
		return strings.Join(assets, ", ")
	}
	return strings.Join(assets[:limit], ", ") + " and " + itoa(len(assets)) + " total"
}

// itoa is a short alias for strconv.Itoa, used only for concatenating display text to avoid importing strconv everywhere.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
