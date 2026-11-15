package zfs

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// maxQuote bounds the text an error quotes: stderr's first line, or an
// output line the parser could not read.
const maxQuote = 256

// cleanStderr returns the first line of a command's standard error made
// fit for an error text: invalid UTF-8 replaced, a tab turned into a
// space, other control characters and line or paragraph separators
// removed, trimmed, cut to maxQuote bytes on a rune boundary.
func cleanStderr(b []byte) string {
	line, _, _ := strings.Cut(string(b), "\n")
	line = strings.ToValidUTF8(line, "�")
	line = strings.Map(func(r rune) rune {
		if r == '\t' {
			return ' '
		}
		if unicode.IsControl(r) || r == ' ' || r == ' ' {
			return -1
		}
		return r
	}, line)
	return strings.TrimSpace(cutText(strings.TrimSpace(line), maxQuote))
}

// cutText cuts s to at most n bytes, dropping a rune that would straddle
// the bound rather than splitting it.
func cutText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
