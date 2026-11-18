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

// AsksForMetric reports whether d still asks the host for metric (backlog
// 3.88): http.body_match only while body_contains is set. It is the
// metric-level companion of AsksForItem — whatever the item, a metric a
// parameter switches on stops being a current value the moment that
// parameter is removed — and the plugin's descriptor's business
// (checkplugins.AsksForMetric).
func (d Definition) AsksForMetric(metric string) bool {
	return checkplugins.AsksForMetric(d.Plugin, d.Params, metric)
}

// AsksForMetric is Definition.AsksForMetric for a resolved check.
func (r Resolved) AsksForMetric(metric string) bool {
	return checkplugins.AsksForMetric(r.Plugin, r.Params, metric)
}
