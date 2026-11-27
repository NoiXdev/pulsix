package twilio

import (
	"strings"
	"unicode/utf16"
)

// An SMS body is bounded to two segments, so one message never costs
// more than two: 2 × 153 septets in the GSM-7 alphabet, else 2 × 67
// UTF-16 units (a concatenated segment loses room to its header).
const (
	gsmLimit  = 306
	ucs2Limit = 134
)

// gsmBasic is the GSM 03.38 default alphabet (one septet each), gsmExt
// its extension table (an escape plus one septet: two).
const (
	gsmBasic = "@\u00a3$\u00a5\u00e8\u00e9\u00f9\u00ec\u00f2\u00c7\n\u00d8\u00f8\r\u00c5\u00e5" +
		"\u0394_\u03a6\u0393\u039b\u03a9\u03a0\u03a8\u03a3\u0398\u039e\u00c6\u00e6\u00df\u00c9" +
		" !\"#\u00a4%&'()*+,-./0123456789:;<=>?" +
		"\u00a1ABCDEFGHIJKLMNOPQRSTUVWXYZ\u00c4\u00d6\u00d1\u00dc\u00a7" +
		"\u00bfabcdefghijklmnopqrstuvwxyz\u00e4\u00f6\u00f1\u00fc\u00e0"
	gsmExt = "^{}\\[~]|\u20ac\f"
)

// fold turns the look-alikes Compose puts in for markup (notify.ChatText)
// and common typography back into GSM-7 characters: an SMS has no markup
// to defuse, and one character outside the alphabet halves the room.
var fold = strings.NewReplacer(
	"\uff20", "@", "\u2039", "<", "\u203a", ">", "\uff3b", "[", "\uff3d", "]",
	"\u2014", "-", "\u2013", "-", "\u00b7", "-", "\u2026", "...",
	"\u2018", "'", "\u2019", "'", "`", "'", "\u201c", "\"", "\u201d", "\"",
	"\ufffd", "?",
)

// septets is s's length in the GSM-7 alphabet, -1 when s leaves it.
func septets(s string) int {
	n := 0
	for _, r := range s {
		switch {
		case strings.ContainsRune(gsmBasic, r):
			n++
		case strings.ContainsRune(gsmExt, r):
			n += 2
		default:
			return -1
		}
	}
	return n
}

// units is s's length in UTF-16 code units.
func units(s string) int { return len(utf16.Encode([]rune(s))) }

// measure is the encoding the whole of text needs: its size function
// and the two-segment limit.
func measure(text string) (size func(string) int, limit int) {
	if septets(text) >= 0 {
		return septets, gsmLimit
	}
	return units, ucs2Limit
}

// clipTo cuts s, runes from the end, until it fits n with "..." added.
func clipTo(s string, n int, size func(string) int) string {
	if size(s) <= n {
		return s
	}
	r := []rune(s)
	for len(r) > 0 && size(string(r)+"...") > n {
		r = r[:len(r)-1]
	}
	return string(r) + "..."
}
