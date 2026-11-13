package checks

import "math"

// MaxAbsValue bounds an observation's value (backlog 3.11c). The database
// stores any double, but its aggregates over the graph buckets and the rule
// windows fail with an overflow once two values near the float range meet,
// which would fail a host's graph and stall its rules; real readings stay
// many orders of magnitude below it.
const MaxAbsValue = 1e300

// Value errors: what a value Pulsix refuses to store reads as. The agent
// reports such a value as this error itself (internal/agent/checks keeps
// copies), and the host checks route shows a value stored before the
// bound as one.
const (
	ErrValueNotFinite  = "value is not a finite number"
	ErrValueOutOfRange = "value is out of range"
)

// ValueProblem says why v cannot be stored, or "" when it can: ±Inf and
// NaN have no JSON spelling, and a value beyond ±MaxAbsValue overflows the
// database's aggregates.
func ValueProblem(v float64) string {
	switch {
	case math.IsNaN(v) || math.IsInf(v, 0):
		return ErrValueNotFinite
	case math.Abs(v) > MaxAbsValue:
		return ErrValueOutOfRange
	}
	return ""
}
