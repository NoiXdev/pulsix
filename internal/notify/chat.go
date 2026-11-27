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

// MaxRenderedLines is the bytes, as JSON, a chat renderer lets its
// message lines take: well under maxBodyBytes (64 KiB), leaving room for
// the title, links, the note and the JSON around them. A service's own
// character limit usually binds first; this one binds when the characters
// are wide (CJK, four-byte runes) or the links long (backlog 4.7 review I1).
const MaxRenderedLines = 40 << 10

// Budget is one limit a message's lines must fit, measured with Size.
type Budget struct {
	Limit int
	Size  func(string) int
}

// Fit is FitWithin with a single budget.
func Fit(lines []string, budget int, size func(string) int, omitted int) (kept []string, more int) {
	return FitWithin(lines, omitted, Budget{Limit: budget, Size: size})
}

// FitWithin keeps the leading lines that fit every budget, where each
// line costs one more for its separator. When anything is left out —
// lines that did not fit, or omitted entries the event itself already
// left out — room for MoreNote(more) and its separator is kept in every
// budget too, and more is the count the note should name (0: no note).
func FitWithin(lines []string, omitted int, budgets ...Budget) (kept []string, more int) {
	fits := func(n, noteCount int) bool {
		for _, b := range budgets {
			used := 0
			for _, l := range lines[:n] {
				used += b.Size(l) + 1
			}
			if noteCount > 0 {
				used += b.Size(MoreNote(noteCount)) + 1
			}
			if used > b.Limit {
				return false
			}
		}
		return true
	}
	if fits(len(lines), omitted) {
		return lines, omitted
	}
	// The note's count is at most every line plus what the event left
	// out; reserving room for that widest form keeps the arithmetic simple.
	reserve := len(lines) + omitted
	n := 0
	for n < len(lines) && fits(n+1, reserve) {
		n++
	}
	return lines[:n], len(lines) - n + omitted
}

// JSONSize is how many bytes s takes inside a JSON string as the
// renderers encode it (no HTML escaping): a quote, a backslash and the
// short escapes take two, any other control character six.
func JSONSize(s string) int {
	n := len(s)
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '"', c == '\\', c == '\n', c == '\r', c == '\t', c == '\b', c == '\f':
			n++
		case c < 0x20:
			n += 5
		}
	}
	return n
}
