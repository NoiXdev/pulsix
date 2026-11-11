// Package checks collects the raw measurements the Pulsix agent's check
// plugins produce: processor, memory, disk, service, network and uptime state (v0.3b plan,
// Task 7). A plugin's only job is to observe — it never buffers, schedules
// or ships anything, and it never judges whether an observation is good or
// bad. The scheduler (Task 8) calls Registry's plugins on their configured
// interval, stamps each Observation with the check key and a timestamp
// (its own Result type, deliberately not defined here) and hands the batch
// to the sender.
//
// A type named Observation already exists on the manager side
// (internal/checks) carrying a host and a timestamp; that one groups
// samples already ingested for a host. This one is the agent's raw
// measurement, produced before any of that context exists. They share a
// name and nothing else — do not unify them.
//
// Every plugin follows internal/agent/facts' shape: a narrow provider
// interface per external call (gopsutil for cpu/memory/disk, systemd over
// D-Bus for service), a default adapter wrapping the real library, and a
// struct that can be built with fixture providers so tests never touch a
// real /proc, real disks or a real systemd. Collection is tolerant per
// observation the way facts.Collect is tolerant per fact: one bad mount or
// one bad load-average call must not cost every other measurement, so
// faults join into an advisory error while every reading that succeeded is
// still returned.
package checks

import (
	"context"
	"fmt"
	"unicode/utf8"
)

// Observation is the smallest unit a plugin reports: one item (the empty
// string for a whole-host metric, a mount point or a unit name otherwise),
// one metric name, and exactly one of a numeric Value, a Label or an Err
// — never two of the three, never none. The service plugin is the only
// one that reports a label today (the unit's state); cpu, memory and disk
// are all numeric.
//
// Err is set when THIS specific item/metric could not be collected this
// tick, even though the plugin call itself is advisory-tolerant overall
// (Collect still returns whatever else it managed): a mount that failed
// to stat, a load-average call that errored while the percent call still
// answered. It carries the failure's own text, mirroring the wire
// CheckResult's "error" oneof arm and internal/checks.Observation.Err on
// the manager side one-for-one — the manager stores no sample for it and
// shows the text on the current value instead, so the host page reads
// "collection failed" rather than a stale number that looks fresh. A
// plugin that cannot attribute a fault to a specific item/metric (a
// probe that is genuinely one indivisible round trip for everything it
// was asked, e.g. service's batched ListUnits) still reports one Err
// observation per item/metric that call would have produced — not
// nothing, and not one combined-but-unattributed error.
type Observation struct {
	Item   string
	Metric string
	Value  *float64
	Label  *string
	Err    string
}

// Plugin is one measurement source. Name identifies it for the registry
// and logging; Collect gathers everything it can right now. params carries
// the plugin's own configuration (disk's "mounts" allow-list, service's
// "units" list) already validated by the manager (internal/checks,
// validateParams) — a plugin still parses defensively rather than trusting
// the shape, since a wire round trip can turn a []string into a []any.
//
// Collect's returned error is advisory, mirroring facts.Collect: whatever
// observations it could gather (including error observations for the
// specific items/metrics that failed) are authoritative and must still be
// used, even when the error is non-nil. Only a dead context fails
// outright with no observations at all.
type Plugin interface {
	Name() string
	Collect(ctx context.Context, params map[string]any) ([]Observation, error)
}

// Registry returns one instance of every check plugin the agent ships,
// wired to the real providers (gopsutil, and systemd over D-Bus on Linux —
// service_stub.go's placeholder everywhere else). It is a func, not a
// package var, so every caller gets its own plugin values; the plugins
// hold no mutable state between calls, except network, which keeps each
// interface's previous counters to turn them into rates; the scheduler
// shares one Registry's instance across every tick, so that state survives.
func Registry() map[string]Plugin {
	return map[string]Plugin{
		"cpu":     newCPUPlugin(),
		"memory":  newMemoryPlugin(),
		"disk":    newDiskPlugin(),
		"service": newServicePlugin(),
		"network": newNetworkPlugin(),
		"uptime":  newUptimePlugin(),
	}
}

// valueObservation builds a numeric Observation; v is copied so the
// caller's loop variable is never aliased into the returned slice.
func valueObservation(item, metric string, v float64) Observation {
	return Observation{Item: item, Metric: metric, Value: &v}
}

// labelObservation builds a label Observation (service.state today).
func labelObservation(item, metric, label string) Observation {
	return Observation{Item: item, Metric: metric, Label: &label}
}

// maxErrLen is the manager's bound on an observation's error text
// (internal/checks.MaxErrLen; a test keeps the two equal). The manager cuts
// a longer one itself (backlog 7.110); the agent cuts first so a fault that
// quotes a long mount point never travels the wire at full length.
const maxErrLen = 2048

// errObservation builds an error Observation: collection failed for this
// exact item/metric, so no sample should be stored and the manager's
// current value shows the failure text instead of a stale reading. The
// text is cut at maxErrLen on a UTF-8 boundary.
func errObservation(item, metric string, err error) Observation {
	return Observation{Item: item, Metric: metric, Err: cutErrText(err.Error())}
}

// cutErrText cuts s to maxErrLen bytes, dropping a rune that would
// straddle the bound rather than splitting it.
func cutErrText(s string) string {
	if len(s) <= maxErrLen {
		return s
	}
	n := maxErrLen
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// paramStrings reads params[key] as a list of strings. present is false
// only when the key is absent or nil — the caller decides what "absent"
// means (disk: watch every non-pseudo mount; service: nothing to check).
// Both shapes a caller can reasonably hand in are accepted: []string, built
// directly (tests, and internal/checks' own in-process callers), and
// []any with string elements, the shape a JSON/JSONB round trip produces
// (internal/checks.Resolved.Params after a store read) — anything else is
// a plugin-level error, since the manager's own validation
// (internal/checks.validateParams) means this plugin should never actually
// see one.
func paramStrings(params map[string]any, key string) (values []string, present bool, err error) {
	v, ok := params[key]
	if !ok || v == nil {
		return nil, false, nil
	}
	switch vv := v.(type) {
	case []string:
		return append([]string(nil), vv...), true, nil
	case []any:
		out := make([]string, len(vv))
		for i, e := range vv {
			s, ok := e.(string)
			if !ok {
				return nil, true, fmt.Errorf("param %q: element %d is not a string", key, i)
			}
			out[i] = s
		}
		return out, true, nil
	default:
		return nil, true, fmt.Errorf("param %q: must be a list of strings", key)
	}
}
