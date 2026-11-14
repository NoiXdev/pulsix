package checks

import "github.com/noixdev/pulsix/internal/checkplugins"

// AsksForItem reports whether d still asks the host for `item`.
//
// Nothing ever deletes a check_current row, deliberately: no event says
// "this mount is gone", the agent simply stops sending that (key, item,
// metric) — see Current's own doc comment and migration 00044. That leaves
// the read side to tell two situations apart which look identical in the
// table. A mount the host no longer has is the agent's silence, and the
// stale badge is the right answer for it. A mount the operator removed
// from the check's `mounts` list is not silence at all: nobody is asking
// any more, and showing it as a current value — eventually badged stale —
// reads as "the agent stopped reporting this" when the truth is "you
// stopped asking for it" (backlog 3.28).
//
// Which parameter enumerates a plugin's items, whether its whole-host item
// "" is asked whatever the list says (temperature, smart, raid), and how a
// reported item maps to a list entry (smart's "<path> [<type>]") is the
// plugin's descriptor's business: checkplugins.AsksForItem. A plugin whose
// items are not enumerable in its parameters asks for everything it
// reports, and so does one whose list is absent or empty.
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
