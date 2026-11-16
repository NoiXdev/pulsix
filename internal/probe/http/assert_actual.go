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
// and cut to probe.ShortLabelLen bytes. Only the first ShortLabelLen
// bytes are quoted: no more can be shown.
func textActual(s string) string {
	if len(s) > probe.ShortLabelLen {
		n := probe.ShortLabelLen
		for n > 0 && !utf8.RuneStart(s[n]) {
			n--
		}
		s = s[:n]
	}
	return shortActual(assert.Quote(s))
}

// shortActual is an actual cleaned and cut like every label quoting what
// a target sent.
func shortActual(s string) string { return probe.ShortLabel(s) }
