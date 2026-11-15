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
// memory, uptime, docker and the synthetic plugins). So does one whose list
// is absent, not a list of strings, or empty: no list means every item the
// host finds — which is also what the agent's plugins do with the
// parameter. Otherwise only the listed items are asked, except that a
// HostItemAlwaysAsked plugin's whole-host item "" always is, that an
// ItemEntry hook maps a reported item to the entry naming it first, and
// that an ItemAsked hook decides in place of the comparison.
func AsksForItem(plugin string, params map[string]any, item string) bool {
	d, ok := Get(plugin)
	if !ok || d.ItemParam == "" {
		return true
	}
	asked, ok := stringList(params[d.ItemParam])
	if !ok || len(asked) == 0 {
		return true
	}
	if item == "" && d.HostItemAlwaysAsked {
		return true
	}
	if d.ItemAsked != nil {
		return d.ItemAsked(params, asked, item)
	}
	if d.ItemEntry != nil {
		item = d.ItemEntry(item)
	}
	return slices.Contains(asked, item)
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
