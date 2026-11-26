package judge

import "github.com/noixdev/pulsix/internal/probe/assert"

// checkBudget bounds a check's regular-expression work, in program
// instructions times input bytes: about a second of CPU (the http
// probe's regexCheckBudget, measured there).
const checkBudget = 160_000_000

// Work is a check's budget for its regular expressions: Go's matcher
// costs up to the program's instructions times the input's bytes in one
// call that cannot be interrupted, so that product is charged before each
// match.
type Work struct {
	left int64
}

// NewWork is a check's whole budget.
func NewWork() *Work { return NewWorkOf(checkBudget) }

// NewWorkOf is a budget of left units, for tests.
func NewWorkOf(left int64) *Work { return &Work{left: left} }

// Text judges a text subject's value v, absent when !present (then exists
// fails, !exists and != pass and every other operator fails).
func (w *Work) Text(a assert.Assertion, v string, present bool) Verdict {
	if !present {
		return Verdict{Holds: a.HoldsText("", false), Actual: ActualAbsent}
	}
	if n := a.ProgramSize(); n > 0 {
		cost := int64(n) * int64(len(v)+1)
		if cost > w.left {
			return Failed(ErrRegexTooMuch)
		}
		w.left -= cost
	}
	return Verdict{Holds: a.HoldsText(v, true), Actual: TextActual(v)}
}
