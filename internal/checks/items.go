package checks

import "strings"

// itemParam names the parameter that enumerates a plugin's items: the
// mounts a disk check watches, the interfaces a network check watches, the
// units a service check watches, the sensors, devices and arrays a
// temperature, smart or raid check watches. cpu, memory and uptime take no
// parameters and report one whole-host item (""), and so do the synthetic
// plugins (icmp, tcp, http, tls, dns), whatever their parameters, so they
// are all absent here and bound nothing.
var itemParam = map[string]string{
	PluginDisk:        "mounts",
	PluginNetwork:     "interfaces",
	PluginService:     "units",
	PluginTemperature: "sensors",
	PluginSmart:       "devices",
	PluginRaid:        "arrays",
}

// hostItemToo marks the plugins in itemParam that also report on the
// whole-host item "" whatever their list says: temp.sensors,
// smart.devices and raid.arrays, and the host-level collection errors on
// them. Their list never names "", so it must not bound it.
var hostItemToo = map[string]bool{
	PluginTemperature: true,
	PluginSmart:       true,
	PluginRaid:        true,
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
// The hardware plugins (temperature, smart, raid) are bounded by their
// list like disk, except that their whole-host item "" is always asked
// (hostItemToo), and that a smart disk behind a RAID controller counts as
// asked when its path is listed (smartItemPath).
//
// A plugin whose items are not enumerable in its parameters asks for
// everything it reports: cpu, memory and uptime have no item, and a disk
// check with no `mounts` list means every filesystem the host finds (a
// network check with no `interfaces` list, every interface it does not
// filter out). Only an explicit, non-empty list bounds anything — which is
// also what the disk and network plugins themselves do with the parameter
// (internal/agent/checks/plugins/disk/, network/).
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
	if item == "" && hostItemToo[d.Plugin] {
		return true
	}
	if d.Plugin == PluginSmart {
		item = smartItemPath(item)
	}
	for _, a := range asked {
		if a == item {
			return true
		}
	}
	return false
}

// smartItemPath is the device path of a smart item. A disk behind a RAID
// controller is reported as "<path> [<type>]" (the agent's smart plugin,
// devices.go) and no devices entry can name it — entries are bare paths,
// queried without a type — so the path before " [" is what the list is
// compared with. Any other item is its path already.
func smartItemPath(item string) string {
	if path, _, ok := strings.Cut(item, " ["); ok && strings.HasSuffix(item, "]") {
		return path
	}
	return item
}
