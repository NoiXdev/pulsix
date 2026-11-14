// Package probe runs synthetic checks from the manager: one connection
// attempt per call against a host's address, reported as observations. It
// knows nothing of check definitions or the database; the params it receives
// are already validated by internal/checks.
package probe

import "context"

// Observation is one metric of one probe: a Value, or an Err when the
// attempt could not be made at all. Never both.
//
// Item names what a metric measures when a probe reports several of the
// same metric (an interface, a sensor); it is empty for a probe with one
// subject. Label is the discrete reading of a metric that is not a number
// (a state name); it is nil for numeric metrics.
type Observation struct {
	Metric, Item string
	Value        *float64
	Label        *string
	Err          string
}

// Func is one probe: it never returns a Go error, every outcome is in the
// observations. secret holds the fields of the check's named secret and is
// nil for probes that use none.
type Func func(ctx context.Context, g *Guard, params map[string]any, address string, secret map[string]string) []Observation

// Registry maps a plugin name to its probe. It is built by the caller
// (internal/probe/all in production) so this package imports no probe.
type Registry map[string]Func

// Run probes address with plugin and params, already validated by
// internal/checks. It never returns a Go error: every outcome is in the
// observations. A failed attempt (refused, timed out, no answer) is
// `<plugin>.up = 0` alone, except that icmp also reports a measured
// `icmp.loss_pct = 100` when its requests went out and none came back. An
// attempt that could not be made is one error observation per metric the
// params would have produced.
//
// The probe's timeout is its params' timeout_s (or the plugin's default),
// cut to ctx's deadline when that is earlier.
func (r Registry) Run(ctx context.Context, g *Guard, plugin string, params map[string]any, address string, secret map[string]string) []Observation {
	if g == nil {
		g = NewGuard(nil, nil)
	}
	if f, ok := r[plugin]; ok {
		return f(ctx, g, params, address, secret)
	}
	return FailAll("unknown probe plugin "+plugin, plugin+".up")
}
