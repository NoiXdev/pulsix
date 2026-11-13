package checks

import "math"

// MaxAbsValue bounds an observation's value (backlog 3.11c). The database
// stores any double, but PostgreSQL's avg over float8 (the graph buckets,
// the rule windows) accumulates the squares of a scaled difference as it
// goes, |v·N − Σv|², and fails with an overflow once that passes the float
// range — at N = 2 already for values near 1e200, long before the sum
// itself would. Values within ±1e100 keep it below ~1.3e154 for any
// N < 6e53, so no number of stored samples can stall a host's graph or
// rules; real readings (byte counters near 2^64) stay far below it.
const MaxAbsValue = 1e100

// What a value Pulsix refuses to store reads as. The agent reports such a
// value as this error text itself (internal/agent/checks keeps copies),
// and the host checks route shows a value stored before the bound as one.
const (
	ValueNotFiniteText  = "value is not a finite number"
	ValueOutOfRangeText = "value is out of range"
)

// ValueProblem says why v cannot be stored, or "" when it can: ±Inf and
// NaN have no JSON spelling, and a value beyond ±MaxAbsValue can overflow
// the database's averages.
func ValueProblem(v float64) string {
	switch {
	case math.IsNaN(v) || math.IsInf(v, 0):
		return ValueNotFiniteText
	case math.Abs(v) > MaxAbsValue:
		return ValueOutOfRangeText
	}
	return ""
}
