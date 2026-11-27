package snmp

import (
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/noixdev/pulsix/internal/probe"
	"github.com/noixdev/pulsix/internal/probe/assert"
	"github.com/noixdev/pulsix/internal/probe/judge"
)

// Assertion errors (backlog 3.134, plan
// docs/superpowers/plans/2026-11-26-snmp-assertions-3.134.md). Like every
// error of this probe they are fixed: none quotes a value or the secret.
const (
	// errNotANumber: a numeric comparison of a value the device sent as
	// text that is not a decimal number.
	errNotANumber = "the device answered with text, not a number: compare it with a quoted string"
	// errNoSuchEntry: value("x") names no oids entry; the descriptor
	// refuses such an assertion, so it bypassed validation.
	errNoSuchEntry = "the assertion names no oids entry"
)

// decimal is a decimal number as the assertion grammar writes one.
var decimal = regexp.MustCompile(`^[+-]?[0-9]+(\.[0-9]+)?$`)

// reading is what an up poll read for the assertions: the first request's
// round trip, the sysUpTime observation and each oids entry's value, by
// name. Nothing of the secret is part of it.
type reading struct {
	rtt    time.Duration
	uptime probe.Observation
	values map[string]value
}

// judge evaluates one of snmp's subjects.
func (r reading) judge(a assert.Assertion, w *judge.Work) judge.Verdict {
	switch a.Subject() {
	case assert.SubjectResponseMS:
		return judge.Number(a, probe.Millis(r.rtt))
	case assert.SubjectUptime:
		switch {
		case r.uptime.Err == errNoSuchObject:
			return judge.Absent(a)
		case r.uptime.Err != "" || r.uptime.Value == nil:
			return judge.Failed(r.uptime.Err)
		}
		return judge.Number(a, *r.uptime.Value)
	case assert.SubjectValue:
		v, ok := r.values[a.Arg()]
		if !ok {
			return judge.Failed(errNoSuchEntry)
		}
		return judgeValue(a, v, w)
	}
	return judge.Failed(judge.ErrInvalid)
}

// judgeValue judges an oids entry's value by the assertion's kind (plan
// R3–R5): a number compared as text by its decimal spelling, text compared
// as a number when it is one; a missing object is absent and fails every
// operator, any other read error is the assertion's. The actual shows the
// value by its own type: a number bare, a text quoted.
func judgeValue(a assert.Assertion, v value, w *judge.Work) judge.Verdict {
	switch {
	case v.err == errNoSuchObject:
		return judge.Absent(a)
	case v.err != "":
		return judge.Failed(v.err)
	case !v.isText && a.Numeric():
		return judge.Number(a, v.num)
	case !v.isText:
		verdict := w.Text(a, strconv.FormatFloat(v.num, 'f', -1, 64), true)
		if verdict.Err == "" {
			verdict.Actual = judge.NumberActual(a.Subject(), v.num)
		}
		return verdict
	}
	text := shownText(v.text)
	if !a.Numeric() {
		return w.Text(a, text, true)
	}
	n, ok := number(text)
	if !ok {
		return judge.Failed(errNotANumber)
	}
	return judge.Verdict{Holds: a.HoldsNumber(n), Actual: judge.TextActual(text)}
}

// shownText is a device's text as snmp.text shows it, without the
// placeholder of an empty one: cleaned and at most probe.MaxLabelLen
// bytes.
func shownText(s string) string {
	s = probe.CleanText(s)
	if len(s) > probe.MaxLabelLen {
		n := probe.MaxLabelLen
		for n > 0 && !utf8.RuneStart(s[n]) {
			n--
		}
		s = strings.TrimSpace(s[:n])
	}
	return s
}

// number reads text that is a decimal number.
func number(text string) (float64, bool) {
	if !decimal.MatchString(text) {
		return 0, false
	}
	n, err := strconv.ParseFloat(text, 64)
	if err != nil || math.IsInf(n, 0) {
		return 0, false
	}
	return n, true
}
