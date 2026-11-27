package notify

import (
	"fmt"
	"net/url"
)

// The helpers below are shared by the chat preset renderers
// (internal/notify/presets/…, backlog 4.7). Compose already passes every
// chosen string through ChatText and Clip; a renderer escapes for its own
// format and bounds the message with Fit.

// ChatText defuses a string an operator or a monitored host chose before
// it reaches a chat service: '@', '<', '>', '[' and ']' become look-alikes
// no service reads as markup, and control and format characters (line
// breaks, bidi overrides) become U+FFFD — agentText (backlog 7.42).
func ChatText(s string) string { return agentText(s) }

// Clip cuts s to at most n runes, the last of them an ellipsis when
// anything was cut.
func Clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 0 {
		return ""
	}
	return string(r[:n-1]) + "…"
}

// MoreNote is what a message says about the n entries it does not show.
func MoreNote(n int) string { return fmt.Sprintf("… and %d more", n) }

// maxLinkLength bounds a link a chat message carries (Slack's button URL
// limit is 3000).
const maxLinkLength = 2048

// SafeLink reports whether u may become a link in a chat message: an
// absolute http(s) URL with a host, made only of characters no supported
// format treats as a delimiter — no space, quote, angle bracket, pipe or
// parenthesis, which would end a Slack "<url|text>", a Markdown "(url)" or
// an HTML attribute early.
func SafeLink(u string) bool {
	if u == "" || len(u) > maxLinkLength {
		return false
	}
	for i := 0; i < len(u); i++ {
		c := u[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '-', c == '.', c == '_', c == '~', c == ':', c == '/', c == '?', c == '#',
			c == '&', c == '=', c == '%', c == '+', c == ',', c == ';', c == '[', c == ']':
		default:
			return false
		}
	}
	p, err := url.Parse(u)
	return err == nil && (p.Scheme == "http" || p.Scheme == "https") && p.Host != "" && p.User == nil
}

// Fit keeps the leading lines that fit budget, measured with size, where
// each line costs one more for its separator. When anything is left out —
// lines that did not fit, or omitted entries the event itself already left
// out — room for MoreNote(more) and its separator is kept too, and more is
// the count the note should name (0: no note).
func Fit(lines []string, budget int, size func(string) int, omitted int) (kept []string, more int) {
	used := 0
	for _, l := range lines {
		used += size(l) + 1
	}
	note := func(n int) int { return size(MoreNote(n)) + 1 }
	if omitted == 0 && used <= budget {
		return lines, 0
	}
	if omitted > 0 && used+note(omitted) <= budget {
		return lines, omitted
	}
	reserve := note(len(lines) + omitted)
	used = 0
	n := 0
	for _, l := range lines {
		cost := size(l) + 1
		if used+cost+reserve > budget {
			break
		}
		used += cost
		n++
	}
	return lines[:n], len(lines) - n + omitted
}
