package checks

import "math"

// maxAbsValue is the agent's copy of the manager's bound on a value
// (internal/checks.MaxAbsValue, which says why; a test keeps the two
// equal): beyond it the manager's averages can overflow.
const maxAbsValue = 1e100

// The errors a value the manager would refuse is reported as; the same
// texts as internal/checks.ValueNotFiniteText and ValueOutOfRangeText.
const (
	errNotFinite  = "value is not a finite number"
	errOutOfRange = "value is out of range"
)

// finite returns o unchanged unless it carries a value that is ±Inf, NaN
// or beyond ±maxAbsValue, which it turns into an error observation on the
// same item and metric (backlog 3.11c). The manager would drop such a
// value as malformed; reporting it as an error keeps a plugin's bug
// visible without costing the batch.
func finite(o Observation) Observation {
	if o.Value == nil {
		return o
	}
	v := *o.Value
	switch {
	case math.IsNaN(v) || math.IsInf(v, 0):
		return Observation{Item: o.Item, Metric: o.Metric, Err: errNotFinite}
	case math.Abs(v) > maxAbsValue:
		return Observation{Item: o.Item, Metric: o.Metric, Err: errOutOfRange}
	}
	return o
}
