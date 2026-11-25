package checkplugins

import (
	"slices"
	"strings"
)

// AsksForItem reports whether a check of plugin with params still asks the
// host for item (backlog 3.28: a current value of an item the operator
// removed from the check's list reads as "you stopped asking", not as
// "the agent stopped reporting").
//
// A plugin without an ItemParam asks for everything it reports (cpu,
// memory, uptime, docker and the synthetic plugins), and a
// HostItemAlwaysAsked plugin always asks for its whole-host item "". A
// list that is present but not a list of strings asks for everything too.
// An ItemAsked hook then decides on its own, whether the list is absent,
// empty or not: what no list means is the plugin's business (http: no
// json_values asks for no JSON item; snmp: interfaces alone asks for
// every interface). Without a hook, an absent or empty list means every
// item the host finds — which is also what the agent's plugins do with
// the parameter — and otherwise only the listed items are asked, after
// an ItemEntry hook maps a reported item to the entry naming it.
func AsksForItem(plugin string, params map[string]any, item string) bool {
	d, ok := Get(plugin)
	if !ok || d.ItemParam == "" {
		return true
	}
	if item == "" && d.HostItemAlwaysAsked {
		return true
	}
	raw := params[d.ItemParam]
	asked, ok := stringList(raw)
	if !ok && raw != nil {
		return true
	}
	if d.ItemAsked != nil {
		return d.ItemAsked(params, asked, item)
	}
	if len(asked) == 0 {
		return true
	}
	if d.ItemEntry != nil {
		item = d.ItemEntry(item)
	}
	return slices.Contains(asked, item)
}

// AsksForMetric reports whether a check of plugin with params still asks
// for metric of an item whose current state is labels — the labels of the
// item's current discrete readings without an error, metric name to label,
// nil when none is known.
//
// Two rules decide. A metric a parameter switches on (http's
// http.body_match, reported only while body_contains is set) is no longer
// asked once the parameter is removed (backlog 3.88: the MetricAsked
// hook), on every item. And a metric the agent stops reporting while its
// item is in some state (zfs: everything but zfs.state of a pool that
// reads EXPORTED or MISSING) is not asked while the item reads that state
// (backlog 3.74: the MetricAskedForItem hook); once the item reads another
// one, it is asked again. Either way the metric's last current value reads
// as "you stopped asking" at once rather than as a reading that went
// stale. A plugin without either hook, and an unknown plugin, asks for
// every metric it reports.
func AsksForMetric(plugin string, params map[string]any, labels map[string]string, metric string) bool {
	d, ok := Get(plugin)
	if !ok {
		return true
	}
	if d.MetricAsked != nil && !d.MetricAsked(params, metric) {
		return false
	}
	return d.MetricAskedForItem == nil || d.MetricAskedForItem(params, labels, metric)
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

// entryName is the item a "name=…" entry reports on (snmp's oids, http's
// json_values): its name, before the first '=' (FormatSNMPOID's and
// FormatJSONValue's split; a name never contains one).
func entryName(entry string) string {
	name, _, _ := strings.Cut(entry, "=")
	return name
}

// namedEntryAsked reports whether one of the "name=…" entries names item.
func namedEntryAsked(entries []string, item string) bool {
	return slices.ContainsFunc(entries, func(e string) bool { return entryName(e) == item })
}

// duplicateEntryName returns the first name two "name=…" entries share:
// both would report on the same item.
func duplicateEntryName(entries []string) (string, bool) {
	seen := make(map[string]bool, len(entries))
	for _, e := range entries {
		name := entryName(e)
		if seen[name] {
			return name, true
		}
		seen[name] = true
	}
	return "", false
}
