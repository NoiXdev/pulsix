package checks

import "math"

// errNotFinite is the error a non-finite value is reported as.
const errNotFinite = "value is not a finite number"

// finite returns o unchanged unless it carries a value that is ±Inf or NaN,
// which it turns into an error observation on the same item and metric
// (backlog 3.11c). The manager would store such a value, and the host's
// checks could no longer be encoded as JSON; reporting it as an error keeps
// a plugin's bug visible without costing the batch.
func finite(o Observation) Observation {
	if o.Value == nil || !math.IsNaN(*o.Value) && !math.IsInf(*o.Value, 0) {
		return o
	}
	return Observation{Item: o.Item, Metric: o.Metric, Err: errNotFinite}
}
