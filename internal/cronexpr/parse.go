// Package cronexpr evaluates 5-field cron expressions in an IANA time zone
// with the DST rules of spec 2026-09-18 §2: a wall time skipped by a
// spring-forward runs at the first valid minute after the gap, a wall time
// repeated by a fall-back runs once, at its first occurrence. Day of month
// and day of week combine with OR when both are restricted (Vixie cron).
package cronexpr

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// ErrSyntax wraps every parse failure.
var ErrSyntax = errors.New("invalid cron expression")

// Expr is a parsed expression. Bit v of a field set means value v matches.
type Expr struct {
	minute, hour, dom, month, dow uint64
	domStar, dowStar              bool
	spec                          string
}

var macros = map[string]string{
	"@hourly":  "0 * * * *",
	"@daily":   "0 0 * * *",
	"@weekly":  "0 0 * * 0",
	"@monthly": "0 0 1 * *",
}

type field struct {
	name          string
	min, max      int
	names         map[string]int
	allowQuestion bool // '?' as an alias for '*' (Quartz), day fields only
}

var (
	minuteField = field{name: "minute", min: 0, max: 59}
	hourField   = field{name: "hour", min: 0, max: 23}
	domField    = field{name: "day of month", min: 1, max: 31, allowQuestion: true}
	monthField  = field{name: "month", min: 1, max: 12, names: map[string]int{
		"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6,
		"jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12,
	}}
	dowField = field{name: "day of week", min: 0, max: 7, allowQuestion: true, names: map[string]int{
		"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6,
	}}
)

// Parse parses a 5-field expression or one of @hourly, @daily, @weekly,
// @monthly.
func Parse(spec string) (Expr, error) {
	s := strings.TrimSpace(spec)
	if strings.HasPrefix(s, "@") {
		m, ok := macros[strings.ToLower(s)]
		if !ok {
			return Expr{}, fmt.Errorf("%w: unsupported macro %q (use @hourly, @daily, @weekly or @monthly)", ErrSyntax, s)
		}
		e, err := Parse(m)
		e.spec = strings.ToLower(s)
		return e, err
	}
	parts := strings.Fields(s)
	if len(parts) != 5 {
		return Expr{}, fmt.Errorf("%w: want 5 fields (minute hour day-of-month month day-of-week), got %d", ErrSyntax, len(parts))
	}
	var e Expr
	var err error
	if e.minute, _, err = parseField(parts[0], minuteField); err != nil {
		return Expr{}, err
	}
	if e.hour, _, err = parseField(parts[1], hourField); err != nil {
		return Expr{}, err
	}
	if e.dom, e.domStar, err = parseField(parts[2], domField); err != nil {
		return Expr{}, err
	}
	if e.month, _, err = parseField(parts[3], monthField); err != nil {
		return Expr{}, err
	}
	if e.dow, e.dowStar, err = parseField(parts[4], dowField); err != nil {
		return Expr{}, err
	}
	if e.dow&(1<<7) != 0 { // 7 is Sunday
		e.dow = e.dow&^(1<<7) | 1
	}
	e.spec = strings.Join(parts, " ")
	return e, nil
}

// String returns the canonical form of the expression.
func (e Expr) String() string { return e.spec }

func parseField(raw string, f field) (bits uint64, star bool, err error) {
	if raw == "?" {
		if !f.allowQuestion {
			return 0, false, fmt.Errorf("%w: %s does not accept '?'", ErrSyntax, f.name)
		}
		star = true
		raw = "*"
	} else {
		star = strings.HasPrefix(raw, "*")
	}
	for _, item := range strings.Split(raw, ",") {
		b, err := parseItem(item, f)
		if err != nil {
			return 0, false, err
		}
		bits |= b
	}
	return bits, star, nil
}

func parseItem(item string, f field) (uint64, error) {
	rng, stepRaw, hasStep := strings.Cut(item, "/")
	step := 1
	if hasStep {
		n, ok := parseUint(stepRaw)
		if !ok || n < 1 || n > f.max {
			return 0, fmt.Errorf("%w: %s step %q", ErrSyntax, f.name, stepRaw)
		}
		step = n
	}
	lo, hi := f.min, f.max
	switch {
	case rng == "*":
	case strings.Contains(rng, "-"):
		a, b, _ := strings.Cut(rng, "-")
		var err error
		if lo, err = f.value(a); err != nil {
			return 0, err
		}
		if hi, err = f.value(b); err != nil {
			return 0, err
		}
		if lo > hi {
			return 0, fmt.Errorf("%w: %s range %q runs backwards", ErrSyntax, f.name, rng)
		}
	default:
		v, err := f.value(rng)
		if err != nil {
			return 0, err
		}
		lo = v
		if !hasStep {
			hi = v
		}
	}
	return span(lo, hi, step), nil
}

func (f field) value(s string) (int, error) {
	if n, ok := f.names[strings.ToLower(s)]; ok {
		return n, nil
	}
	n, ok := parseUint(s)
	if !ok || n < f.min || n > f.max {
		return 0, fmt.Errorf("%w: %s value %q (allowed %d-%d)", ErrSyntax, f.name, s, f.min, f.max)
	}
	return n, nil
}

// parseUint parses s as an unsigned decimal integer: digits only, no sign.
// strconv.Atoi alone accepts a leading '+' or '-', which cron fields never
// carry ("+5" and "-5" are not valid field values).
func parseUint(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, false
		}
	}
	n, err := strconv.Atoi(s)
	return n, err == nil
}

func span(lo, hi, step int) uint64 {
	var b uint64
	for v := lo; v <= hi; v += step {
		b |= 1 << uint(v)
	}
	return b
}

func has(bits uint64, v int) bool { return bits&(1<<uint(v)) != 0 }
