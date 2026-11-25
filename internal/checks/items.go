package checks

import "github.com/noixdev/pulsix/internal/checkplugins"

// AsksForItem reports whether d still asks the host for `item`.
//
// Ingest never deletes a check_current row, deliberately: no event says
// "this mount is gone", the agent simply stops sending that (key, item,
// metric) — see Current's own doc comment and migration 00044. Only a
// forget or the expiry, long after, removes one (forget.go,
// forget_metric.go). That leaves
// the read side to tell two situations apart which look identical in the
// table. A mount the host no longer has is the agent's silence, and the
// stale badge is the right answer for it. A mount the operator removed
// from the check's `mounts` list is not silence at all: nobody is asking
// any more, and showing it as a current value — eventually badged stale —
// reads as "the agent stopped reporting this" when the truth is "you
// stopped asking for it" (backlog 3.28).
//
// Which parameter bounds a plugin's items, whether its whole-host item ""
// is asked whatever the list says (temperature, smart, raid, snmp), how a
// reported item maps to a list entry (ItemEntry: smart's "<path>
// [<type>]", script's "<command>:<label>"), and whether the plugin decides
// with its own rule in place of that comparison (ItemAsked: snmp's oids
// entries "name=OID" ask for item "name", and interfaces: true asks for
// any item, the device naming its interfaces; http's json_values entries
// "name=JSONPath" ask for item "name", and no entry asks for none) is the
// plugin's descriptor's business: checkplugins.AsksForItem. A plugin whose
// items are not enumerable in its parameters asks for everything it
// reports, and so does one without an ItemAsked hook whose list is absent
// or empty.
//
// Samples are untouched either way: a graph of a mount dropped last week
// is still a real graph, and GET /hosts/{id}/metrics still serves it.
func (d Definition) AsksForItem(item string) bool {
	return checkplugins.AsksForItem(d.Plugin, d.Params, item)
}

// AsksForItem is Definition.AsksForItem for a resolved check: the same
// rule over its plugin and params, for a check that has no definition —
// one defined in the host's agent.yaml (backlog 3.5a).
func (r Resolved) AsksForItem(item string) bool {
	return checkplugins.AsksForItem(r.Plugin, r.Params, item)
}

// AsksForMetric reports whether d still asks the host for metric of an
// item whose current state is labels (CurrentLabels.Of): http.body_match
// only while body_contains is set (backlog 3.88), and no zfs metric but
// zfs.state of a pool that reads EXPORTED or MISSING (backlog 3.74). It is
// the metric-level companion of AsksForItem — a metric a parameter
// switches on stops being a current value the moment that parameter is
// removed, and one the agent does not report in the item's present state
// stops being one while the item reads it — and the plugin's descriptor's
// business (checkplugins.AsksForMetric).
func (d Definition) AsksForMetric(labels map[string]string, metric string) bool {
	return checkplugins.AsksForMetric(d.Plugin, d.Params, labels, metric)
}

// AsksForMetric is Definition.AsksForMetric for a resolved check.
func (r Resolved) AsksForMetric(labels map[string]string, metric string) bool {
	return checkplugins.AsksForMetric(r.Plugin, r.Params, labels, metric)
}

// HeldOn is the state label of an item that reads labels which metric
// waits on while AsksForMetric says no only because of that state — a
// sleeping disk's "standby", an exported pool's "EXPORTED" — and "" when
// the metric is not held (checkplugins.HeldOn, backlog 3.9b ruling). A
// held metric keeps its last value, state and alert.
func (d Definition) HeldOn(labels map[string]string, metric string) string {
	return checkplugins.HeldOn(d.Plugin, d.Params, labels, metric)
}

// HeldOn is Definition.HeldOn for a resolved check.
func (r Resolved) HeldOn(labels map[string]string, metric string) string {
	return checkplugins.HeldOn(r.Plugin, r.Params, labels, metric)
}

// ItemLabels is what each item reads now (CurrentLabels).
type ItemLabels map[itemOfKey]map[string]string

// itemOfKey is one item of one check key.
type itemOfKey struct{ checkKey, item string }

// CurrentLabels collects, per check key and item, the labels of values'
// current discrete readings without an error, metric name to label — the
// item's present state AsksForMetric reads (backlog 3.74). A numeric
// value or an error carries none. internal/rules' itemLabels applies the
// same rule for the evaluation (rules never imports checks); keep both in
// step.
func CurrentLabels(values []Current) ItemLabels {
	out := ItemLabels{}
	for _, v := range values {
		if v.Label == nil || v.Err != "" {
			continue
		}
		k := itemOfKey{checkKey: v.CheckKey, item: v.Item}
		if out[k] == nil {
			out[k] = map[string]string{}
		}
		out[k][v.Metric] = *v.Label
	}
	return out
}

// Of is checkKey's item's labels, nil when it has none.
func (l ItemLabels) Of(checkKey, item string) map[string]string {
	return l[itemOfKey{checkKey: checkKey, item: item}]
}
