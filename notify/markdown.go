package notify

import (
	"fmt"
	"strings"
)

// This file holds the message rendering shared by the "Markdown family" channels (DingTalk, WeCom).
// Feishu uses card JSON, Telegram uses HTML and email uses HTML, each rendered in its own adapter.

// maxAssetsShown is the maximum number of assets listed in a message. One finding may be anchored to dozens of
// assets; listing them all overflows the message with no informational value, since nobody reads the 4th domain onward in IM.
const maxAssetsShown = 3

// maxSummaryRunes is how many characters the summary is compressed to. An IM message is a "go look at the details" prompt,
// not the report itself; the full content lives in the platform.
const maxSummaryRunes = 120

// markdownReservedBytes is reserved for the message header (digest line + severity breakdown + possible truncation note)
// and footer (platform link). It is subtracted from the budget during whole-item packing so the header and footer are
// never cut; once they are, readers can't even tell "which batch this is and how many items weren't shown".
const markdownReservedBytes = 320

// markdownEscape escapes markdown metacharacters.
//
// Why it is required: finding titles, summaries, classes and asset display names all come from **untrusted
// sources**: titles and summaries come from model output (and the model reads responses from the target under
// test), while an asset url is the full scanned URL (including a target-controlled query string). Without escaping,
// a finding titled
//
//	Login SQL injection\n[URGENT: click to verify your account](http://attacker.tld)
//
// would render as a **clickable external link** in the security engineer's DingTalk/Feishu; and
// `![](http://attacker.tld/beacon)` would be fetched by the client on render, announcing
// "this finding has been viewed" and leaking the reader's IP. Even with benign content, injected bold or
// blockquotes can push the critical findings below the fold.
//
// The escape set covers characters for headings/links/emphasis/lists/quotes/strikethrough that change structure or
// produce clickable elements. `\` must be handled first, otherwise the backslashes added later would be escaped again.
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

// markdownText collapses untrusted text to a single line and escapes it, for use in markdown bodies.
// Single-lining is the other half besides escaping: a newline by itself can forge a new list item or blockquote,
// and escaping characters cannot stop that.
func markdownText(s string, maxRunes int) string {
	return markdownEscape(OneLine(s, maxRunes))
}

// markdownTitle returns the message title (IM platform title bar / card title); the content is **unescaped raw text**.
//
// It is deliberately not escaped here: this title is shared by renderers for four contexts: markdown bodies,
// Telegram HTML, Feishu card plain_text, and generic webhook JSON plus email subjects. Each context has different
// escaping rules (markdown escaping inside HTML leaves visible backslashes, inside JSON it corrupts the data), so
// escaping must be done by each output side; see writeItem / feishuItemLines / telegramEscape. Markdown escaping
// was once added to the shared function, and Telegram messages ended up showing visible backslashes like `\(1\)`.
func markdownTitle(m Message) string {
	if m.Batch {
		return fmt.Sprintf("Findings digest · %d total", len(m.Items))
	}
	if len(m.Items) == 0 {
		return "Finding notification"
	}
	it := m.Items[0]
	return fmt.Sprintf("[%s] %s", SeverityLabel(it.Severity), OneLine(it.Title(), 0))
}

// markdownBody renders the message body, returning the body and the **number of items actually written**.
//
// The returned kept is the number of items this delivery actually delivers; the caller marks only the first kept
// items as delivered. Items blocked by the channel's length limit must wait for the next batch rather than being
// marked successful along with the rest. That is exactly where "silent loss" comes from: the message was truncated
// but the delivery records show everything delivered, with nowhere to see that the tail was never sent.
//
// maxBytes<=0 means no limit.
func markdownBody(m Message, maxBytes int) (string, int) {
	if !m.Batch {
		if len(m.Items) == 0 {
			return "", 0
		}
		var b strings.Builder
		writeItem(&b, m.Items[0], "", true)
		// A single message is sent even if too long (final truncation is the fallback): partial info
		// about one finding beats sending nothing.
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

// markdownBatchIntro renders the start of a digest: time window, item count and severity breakdown.
// With these, recipients can judge whether the batch needs immediate attention without opening the platform.
//
// items are the items **actually fitted**, total is the full count this batch should have. When they differ we must
// say "how many more are in the next message"; otherwise readers assume the header's number is everything, and the
// items never sent simply don't exist in the UI.
func markdownBatchIntro(m Message, items []Item, total int) string {
	var b strings.Builder
	if m.WindowMinutes > 0 {
		fmt.Fprintf(&b, "**%[2]d new findings in the last %[1]d minutes**", m.WindowMinutes, total)
	} else {
		fmt.Fprintf(&b, "**%d new findings**", total)
	}
	if extra := total - len(items); extra > 0 {
		fmt.Fprintf(&b, " (showing the first %d here; the remaining %d continue in the next message)", len(items), extra)
	}
	// Give the breakdown by severity so readers see at a glance whether there are critical items. Only items
	// **actually included in this message** are counted, so "Critical 3" matches the items listed below.
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

// writeItem renders a single finding item.
//
// prefix is the number in a digest list; with single=true the full version is rendered (with summary and link),
// while digest lists render a one-line summary only; otherwise a 50-item digest would become a long document.
//
// All external content (title/class/assets/summary) goes through markdownText:
// single-lined + escaped. The link is built from the admin-configured public_base_url, is not untrusted content,
// and must be clickable, so it is output as-is.
func writeItem(b *strings.Builder, it Item, prefix string, single bool) {
	line := fmt.Sprintf("%s**%s · %s**", prefix, SeverityLabel(it.Severity), markdownText(it.Title(), 0))
	if !single {
		// Digest mode: single line, followed by compressed assets and summary.
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
		fmt.Fprintf(b, "**Status change**: %s → %s\n",
			markdownText(StatusLabel(it.FromStatus), 0), markdownText(StatusLabel(it.ToStatus), 0))
	}
	if it.VulnClass != "" && it.VulnClass != it.Title() {
		fmt.Fprintf(b, "**Class**: %s\n", markdownText(it.VulnClass, 0))
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
		fmt.Fprintf(b, "[View details](%s)\n", it.DetailURL)
	}
}
