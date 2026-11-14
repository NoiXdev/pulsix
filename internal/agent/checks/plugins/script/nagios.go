package script

import (
	"math"
	"strconv"
	"strings"
	"unicode"
)

// status is script.status's label.
type status string

const (
	statusOK      status = "ok"
	statusWarn    status = "warn"
	statusCrit    status = "crit"
	statusUnknown status = "unknown"
)

// nagiosStatus maps a run's exit code the Nagios way: 0 ok, 1 warn,
// 2 crit, anything else, and no exit code at all, unknown.
func nagiosStatus(res runResult) status {
	if !res.exited {
		return statusUnknown
	}
	switch res.exitCode {
	case 0:
		return statusOK
	case 1:
		return statusWarn
	case 2:
		return statusCrit
	}
	return statusUnknown
}

// parseNagios reads a Nagios plugin's output: the first line up to its
// first '|' is the message (trimmed, not yet cleaned), the rest of that line perfdata.
// In the long output (the later lines) the first '|' starts perfdata again,
// which runs to the end of the output, as Nagios reads it. Values become
// items of command (valueSet).
func parseNagios(command string, out []byte) (message string, values []itemValue) {
	text := string(out)
	first, rest, _ := strings.Cut(text, "\n")
	first = strings.TrimSuffix(first, "\r")
	message, perf, _ := strings.Cut(first, "|")

	vs := newValueSet(command)
	parsePerfdata(perf, vs)
	if _, long, ok := strings.Cut(rest, "|"); ok {
		parsePerfdata(long, vs)
	}
	return strings.TrimSpace(message), vs.values
}

// parsePerfdata reads space-separated `'label'=value[UOM];warn;crit;min;max`
// entries into vs, skipping every entry it cannot read. A label may be
// single-quoted, a doubled quote inside standing for one; only label and
// value are used.
func parsePerfdata(s string, vs *valueSet) {
	for s != "" && !vs.full() {
		s = strings.TrimLeftFunc(s, unicode.IsSpace)
		if s == "" {
			return
		}
		var label string
		var ok bool
		if s[0] == '\'' {
			label, s, ok = quotedLabel(s)
		} else {
			label, s, ok = plainLabel(s)
		}
		if !ok {
			s = skipEntry(s)
			continue
		}
		var field string
		field, s = cutEntry(s)
		if v, ok := perfValue(field); ok {
			vs.add(label, v)
		}
	}
}

// quotedLabel reads a quoted label and the '=' after it from s, which
// starts with a quote. Without a closing quote followed by '=' ok is
// false and rest is s.
func quotedLabel(s string) (label, rest string, ok bool) {
	var b strings.Builder
	for i := 1; i < len(s); i++ {
		if s[i] != '\'' {
			b.WriteByte(s[i])
			continue
		}
		if i+1 < len(s) && s[i+1] == '\'' {
			b.WriteByte('\'')
			i++
			continue
		}
		if i+1 < len(s) && s[i+1] == '=' {
			return b.String(), s[i+2:], true
		}
		return "", s, false
	}
	return "", s, false
}

// plainLabel reads an unquoted label and its '=' from s: the text up to
// the first '=' of the entry. An entry without one is not perfdata.
func plainLabel(s string) (label, rest string, ok bool) {
	entry, _ := cutEntry(s)
	label, _, ok = strings.Cut(entry, "=")
	if !ok {
		return "", s, false
	}
	return label, s[len(label)+1:], true
}

// cutEntry splits s at its first whitespace.
func cutEntry(s string) (entry, rest string) {
	if i := strings.IndexFunc(s, unicode.IsSpace); i >= 0 {
		return s[:i], s[i:]
	}
	return s, ""
}

// skipEntry drops s's first whitespace-separated entry.
func skipEntry(s string) string {
	_, rest := cutEntry(s)
	return rest
}

// unitFactors normalises Nagios units: times to seconds, sizes to bytes
// (×1024ⁿ, the Nagios plugins' convention).
var unitFactors = map[string]float64{
	"":   1,
	"s":  1,
	"ms": 1e-3,
	"us": 1e-6,
	"%":  1,
	"c":  1,
	"B":  1,
	"KB": 1 << 10,
	"MB": 1 << 20,
	"GB": 1 << 30,
	"TB": 1 << 40,
}

// perfValue reads `value[UOM][;warn;crit;min;max]` and normalises the unit.
// A unit outside unitFactors is taken as is unless it holds a digit, a
// dot or a sign, which means the number itself was malformed ("1e3",
// "1.2.3"). The value must be a finite number.
func perfValue(field string) (float64, bool) {
	field, _, _ = strings.Cut(field, ";")
	n := strings.IndexFunc(field, func(r rune) bool {
		return (r < '0' || r > '9') && r != '.' && r != '-' && r != '+'
	})
	if n < 0 {
		n = len(field)
	}
	num, unit := field[:n], field[n:]
	v, err := strconv.ParseFloat(num, 64)
	if err != nil {
		return 0, false // also a number out of float64's range
	}
	factor, known := unitFactors[unit]
	if !known {
		if strings.ContainsAny(unit, "0123456789.-+") {
			return 0, false
		}
		factor = 1
	}
	v *= factor
	if math.IsInf(v, 0) {
		return 0, false
	}
	return v, true
}
