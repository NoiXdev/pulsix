package probe

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Bounds of the manager's check-results contract (internal/checks
// MaxItemLen and MaxLabelLen); a test keeps them equal. This package does
// not import internal/checks, which pulls in the database layer.
const (
	MaxItemLen  = 512
	MaxLabelLen = 512
)

// EmptyText is the label of a text that is empty once cleaned.
const EmptyText = "(empty)"

// CleanText turns text a target sent (a device's string, a JSON string)
// into text an item or a label may carry: invalid UTF-8 replaced, a tab
// turned into a space, other control characters and line or paragraph
// separators removed, trimmed. Devices pad names with NULs and put
// anything into their descriptions.
func CleanText(s string) string {
	s = strings.ToValidUTF8(s, "�")
	s = strings.Map(func(r rune) rune {
		if r == '\t' {
			return ' '
		}
		if isControl(r) {
			return -1
		}
		return r
	}, s)
	return strings.TrimSpace(s)
}

// Label is s cleaned and cut to MaxLabelLen bytes on a rune boundary, like
// the script plugin's output; empty becomes EmptyText.
func Label(s string) string {
	s = CleanText(s)
	if len(s) > MaxLabelLen {
		n := MaxLabelLen
		for n > 0 && !utf8.RuneStart(s[n]) {
			n--
		}
		s = strings.TrimSpace(s[:n])
	}
	if s == "" {
		return EmptyText
	}
	return s
}

// ReportableItem reports whether the manager accepts item: not empty, at
// most MaxItemLen bytes, valid UTF-8, no control character or line
// separator (the agent's checks.ReportableItem, plus not empty: an item of
// "" would be the whole-host item).
func ReportableItem(item string) bool {
	return item != "" && len(item) <= MaxItemLen && utf8.ValidString(item) && strings.IndexFunc(item, isControl) < 0
}

func isControl(r rune) bool {
	return unicode.IsControl(r) || r == '\u2028' || r == '\u2029'
}
