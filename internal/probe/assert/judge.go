package assert

import "strings"

// HoldsNumber reports whether a numeric assertion holds for v.
func (a Assertion) HoldsNumber(v float64) bool {
	switch a.op {
	case OpLt:
		return v < a.num
	case OpLe:
		return v <= a.num
	case OpGt:
		return v > a.num
	case OpGe:
		return v >= a.num
	case OpEq:
		return v == a.num
	case OpNe:
		return v != a.num
	case OpIn:
		return a.lo <= v && v <= a.hi
	}
	return false
}

// HoldsText reports whether a text assertion holds for v; present false
// means the header, JSON path or selector matched nothing, and then
// exists fails, !exists and != pass and every other operator fails.
func (a Assertion) HoldsText(v string, present bool) bool {
	if !present {
		return a.op == OpNotExists || a.op == OpNe
	}
	switch a.op {
	case OpEq:
		return v == a.text
	case OpNe:
		return v != a.text
	case OpContains:
		return strings.Contains(v, a.text)
	case OpNotContains:
		return !strings.Contains(v, a.text)
	case OpMatches:
		return a.re.MatchString(v)
	case OpNotMatches:
		return !a.re.MatchString(v)
	case OpExists:
		return true
	}
	return false
}
