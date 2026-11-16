package http

import (
	"math"
	"strconv"
	"unicode/utf8"

	"github.com/noixdev/pulsix/internal/probe"
	"github.com/noixdev/pulsix/internal/probe/assert"
)

// The actuals that are no value the target sent.
const (
	// actualAbsent: the header, JSON path or selector matched nothing.
	actualAbsent = "absent"
	// actualHidden stands for a Set-Cookie header's value, which is never
	// stored.
	actualHidden = "(hidden)"
)

// numberActual prints a numeric subject's value without a unit:
// milliseconds whole, certificate days to a tenth, the rest (whole
// numbers) as they are.
func numberActual(s assert.Subject, v float64) string {
	switch s {
	case assert.SubjectResponseMS, assert.SubjectTTFBMS:
		v = math.Round(v)
	case assert.SubjectCertDays:
		v = math.Round(v*10) / 10
	}
	if v == 0 {
		v = 0 // no "-0"
	}
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// textActual is a text the target sent, quoted as a JSON string, cleaned
// and at most probe.ShortLabelLen bytes: a longer text is cut so that its
// quoted start fits, never inside an escape or a rune, and the closing
// quote stays. Only the first ShortLabelLen bytes are quoted: no more can
// be shown.
func textActual(s string) string {
	if len(s) > probe.ShortLabelLen {
		n := probe.ShortLabelLen
		for n > 0 && !utf8.RuneStart(s[n]) {
			n--
		}
		s = s[:n]
	}
	q := assert.Quote(s)
	if len(q) <= probe.ShortLabelLen {
		return shortActual(q)
	}
	inner := q[1 : len(q)-1]
	end := 0
	for end < len(inner) {
		next := end + escapeLen(inner, end)
		if next+2 > probe.ShortLabelLen {
			break
		}
		end = next
	}
	return shortActual(`"` + inner[:end] + `"`)
}

// escapeLen is the length of the JSON string token at i of a quoted
// string's inside: an escape (\uXXXX or \x) or one rune.
func escapeLen(inner string, i int) int {
	if inner[i] == '\\' {
		if i+1 < len(inner) && inner[i+1] == 'u' {
			return 6
		}
		return 2
	}
	_, n := utf8.DecodeRuneInString(inner[i:])
	return n
}

// shortActual is an actual cleaned and cut like every label quoting what
// a target sent.
func shortActual(s string) string { return probe.ShortLabel(s) }
