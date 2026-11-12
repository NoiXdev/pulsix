// Package checks is the agent's check engine: the Plugin contract its
// check plugins implement, the Observation they report and the helpers they
// share to build one (observe.go), the scheduler that runs them and the
// buffer that holds results while the agent is disconnected (v0.3b plan,
// Tasks 7 and 8). The plugins themselves live one package each under
// plugins/ (plugins.Registry wires them and is the list of record): cpu,
// memory, disk, service, network, uptime, docker, temperature, smart and
// raid. They import this package, so it never imports them.
//
// A plugin's only job is to observe — it never buffers, schedules or ships
// anything, and it never judges whether an observation is good or bad. The
// scheduler calls the registry's plugins on their configured interval,
// stamps each Observation with the check key and a timestamp (its own
// Result type, scheduler.go) and hands the batch to the sender.
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

import "context"

// Observation is the smallest unit a plugin reports: one item (the empty
// string for a whole-host metric, a mount point or a unit name otherwise),
// one metric name, and exactly one of a numeric Value, a Label or an Err
// — never two of the three, never none. Labels are states: service's
// unit state, docker's container state and health, raid's array state;
// every other metric is numeric.
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
// was asked, e.g. service's batched States) still reports one Err
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

// StatefulPlugin is a Plugin that keeps state between calls (network: each
// interface's previous counters, to turn them into rates). Its registry
// value (plugins.Registry) is only a prototype: the scheduler asks it for a fresh instance per
// check key (NewInstance), so two keys never share — or race over — that
// state, and one instance is only ever collected by its own key's goroutine.
type StatefulPlugin interface {
	Plugin
	NewInstance() Plugin
}
