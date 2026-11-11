package checks

// itemParam names the parameter that enumerates a plugin's items: the
// mounts a disk check watches, the interfaces a network check watches, the
// units a service check watches. cpu, memory and uptime take no parameters
// and report one whole-host item (""), so they are absent here and bound
// nothing.
var itemParam = map[string]string{
	PluginDisk:    "mounts",
	PluginNetwork: "interfaces",
	PluginService: "units",
}

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
// A plugin whose items are not enumerable in its parameters asks for
// everything it reports: cpu, memory and uptime have no item, and a disk
// check with no `mounts` list means every filesystem the host finds (a
// network check with no `interfaces` list, every interface it does not
// filter out). Only an explicit, non-empty list bounds anything — which is
// also what the disk and network plugins themselves do with the parameter
// (internal/agent/checks/disk.go, network.go).
//
// Samples are untouched either way: a graph of a mount dropped last week
// is still a real graph, and GET /hosts/{id}/metrics still serves it.
func (d Definition) AsksForItem(item string) bool {
	param, enumerable := itemParam[d.Plugin]
	if !enumerable {
		return true
	}
	asked, ok := stringList(d.Params[param])
	if !ok || len(asked) == 0 {
		return true
	}
	for _, a := range asked {
		if a == item {
			return true
		}
	}
	return false
}
