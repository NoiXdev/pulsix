package snmp

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Bounds of the manager's check-results contract (internal/checks
// MaxItemLen and MaxLabelLen); a test keeps them equal. This package does
// not import internal/checks, which pulls in the database layer.
const (
	maxItemLen  = 512
	maxLabelLen = 512
)

// emptyText is the snmp.text label of a value that is empty once cleaned.
const emptyText = "(empty)"

// cleanText turns a device string into text an item or a label may carry:
// invalid UTF-8 replaced, a tab turned into a space, other control
// characters and line or paragraph separators removed, trimmed. Devices
// pad names with NULs and put anything into their descriptions.
func cleanText(s string) string {
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

// label is s cleaned and cut to maxLabelLen bytes on a rune boundary, like
// the script plugin's output; empty becomes emptyText.
func label(s string) string {
	s = cleanText(s)
	if len(s) > maxLabelLen {
		n := maxLabelLen
		for n > 0 && !utf8.RuneStart(s[n]) {
			n--
		}
		s = strings.TrimSpace(s[:n])
	}
	if s == "" {
		return emptyText
	}
	return s
}

// reportableItem reports whether the manager accepts item: not empty, at
// most maxItemLen bytes, valid UTF-8, no control character or line
// separator (the agent's checks.ReportableItem, plus not empty: an
// interface item of "" would be the whole-host item).
func reportableItem(item string) bool {
	return item != "" && len(item) <= maxItemLen && utf8.ValidString(item) && strings.IndexFunc(item, isControl) < 0
}

func isControl(r rune) bool {
	return unicode.IsControl(r) || r == ' ' || r == ' '
}
