// Package checks collects the raw measurements the Pulsix agent's check
// plugins produce: processor, memory, disk and service state (v0.3b plan,
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
)

// Observation is the smallest unit a plugin reports: one item (the empty
// string for a whole-host metric, a mount point or a unit name otherwise),
// one metric name, and either a numeric Value or a Label — never both, and
// never neither. The service plugin is the only one that reports a label
// today (the unit's state); cpu, memory and disk are all numeric.
type Observation struct {
	Item   string
	Metric string
	Value  *float64
	Label  *string
}

// Plugin is one measurement source. Name identifies it for the registry
// and logging; Collect gathers everything it can right now. params carries
// the plugin's own configuration (disk's "mounts" allow-list, service's
// "units" list) already validated by the manager (internal/checks,
// validateParams) — a plugin still parses defensively rather than trusting
// the shape, since a wire round trip can turn a []string into a []any.
//
// Collect's returned error is advisory, mirroring facts.Collect: whatever
// observations it could gather are authoritative and must still be used,
// even when the error is non-nil. Only a dead context fails outright with
// no observations at all.
type Plugin interface {
	Name() string
	Collect(ctx context.Context, params map[string]any) ([]Observation, error)
}

// Registry returns one instance of every check plugin the agent ships,
// wired to the real providers (gopsutil, and systemd over D-Bus on Linux —
// service_stub.go's placeholder everywhere else). It is a func, not a
// package var, so every caller gets its own plugin values; the plugins
// themselves hold no mutable state between calls.
func Registry() map[string]Plugin {
	return map[string]Plugin{
		"cpu":     newCPUPlugin(),
		"memory":  newMemoryPlugin(),
		"disk":    newDiskPlugin(),
		"service": newServicePlugin(),
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
