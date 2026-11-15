package checks

import "testing"

// A current value survives an operator narrowing a check's parameters only
// if the definition still asks for that item. Nothing ever deletes a
// check_current row — no event says "this mount is gone", the agent simply
// stops sending it — so the read side is where "you stopped asking for
// this" is told apart from "the agent stopped reporting this" (backlog
// 3.28).
func TestDefinitionAsksForItem(t *testing.T) {
	tests := []struct {
		name   string
		plugin string
		params map[string]any
		item   string
		want   bool
	}{
		{"disk asks for a mount on its list", PluginDisk, map[string]any{"mounts": []string{"/", "/var"}}, "/var", true},
		{"disk stopped asking for a mount off its list", PluginDisk, map[string]any{"mounts": []string{"/"}}, "/var", false},
		// No list means "every mount the host reports", so there is
		// nothing to filter against — a mount that vanished from the
		// host is the agent's silence, not the operator's edit, and the
		// stale badge is the right answer for it.
		{"disk without a list asks for every mount", PluginDisk, nil, "/var", true},
		{"disk with an empty list asks for every mount", PluginDisk, map[string]any{"mounts": []string{}}, "/var", true},
		{"service asks for a unit on its list", PluginService, map[string]any{"units": []string{"sshd", "nginx"}}, "nginx", true},
		{"service stopped asking for a removed unit", PluginService, map[string]any{"units": []string{"sshd"}}, "nginx", false},
		// cpu and memory take no parameters and report one whole-host
		// item (""), so there is never anything to drop.
		{"cpu asks for its whole-host item", PluginCPU, nil, "", true},
		{"memory asks for its whole-host item", PluginMemory, nil, "", true},
		{"uptime asks for its whole-host item", PluginUptime, nil, "", true},
		{"network asks for an interface on its list", PluginNetwork, map[string]any{"interfaces": []string{"eth0", "wg0"}}, "wg0", true},
		{"network stopped asking for an interface off its list", PluginNetwork, map[string]any{"interfaces": []string{"eth0"}}, "wg0", false},
		{"network without a list asks for every interface", PluginNetwork, nil, "eth1", true},
		// The synthetic plugins (backlog 3.13) report one whole-host
		// item ("") whatever their parameters say.
		{"icmp asks for its whole-host item", PluginICMP, map[string]any{"count": 3.0}, "", true},
		{"tcp asks for its whole-host item", PluginTCP, map[string]any{"port": 22.0}, "", true},
		{"http asks for its whole-host item", PluginHTTP, map[string]any{"url": "https://{address}/"}, "", true},
		{"tls asks for its whole-host item", PluginTLS, nil, "", true},
		{"dns asks for its whole-host item", PluginDNS, map[string]any{"name": "example.com"}, "", true},
		// http's json_values (backlog 3.6h): an entry "name=JSONPath"
		// asks for item "name"; the request's metrics stay on "".
		{"http asks for a json_values entry's name", PluginHTTP, map[string]any{"json_values": []any{"status=$.status"}}, "status", true},
		{"http stopped asking for a name off its list", PluginHTTP, map[string]any{"json_values": []any{"status=$.status"}}, "depth", false},
		{"http with a list still asks for its whole-host item", PluginHTTP, map[string]any{"json_values": []any{"status=$.status"}}, "", true},
		// The last entry removed: no list asks for no JSON item, so the
		// removed name's value goes at once (not after the item expiry).
		{"http without a list stopped asking for a JSON item", PluginHTTP, map[string]any{"url": "https://{address}/"}, "status", false},
		{"http with an empty list stopped asking for a JSON item", PluginHTTP, map[string]any{"json_values": []any{}}, "status", false},
		// snmp (backlog 3.13d): an oids entry "name=OID" asks for item
		// "name"; interfaces asks for any item, named by the device.
		{"snmp asks for an oids entry's name", PluginSNMP, map[string]any{"oids": []any{"load=1.3.6.1.4.1.2021.10.1.3.1"}}, "load", true},
		{"snmp stopped asking for a name off its list", PluginSNMP, map[string]any{"oids": []any{"load=1.3.6.1.4.1.2021.10.1.3.1"}}, "temp", false},
		{"snmp with interfaces asks for any interface", PluginSNMP, map[string]any{"oids": []any{"load=1.3.6"}, "interfaces": true}, "Gi0/1", true},
		{"snmp without interfaces stopped asking for an interface", PluginSNMP, map[string]any{"oids": []any{"load=1.3.6"}, "interfaces": false}, "Gi0/1", false},
		{"snmp with a list still asks for its whole-host item", PluginSNMP, map[string]any{"oids": []any{"load=1.3.6"}}, "", true},
		// The hardware plugins (backlog 3.9) bound their items by their
		// list, but also report a whole-host count on item "" (and their
		// host-level errors there), which every list still asks for.
		{"temperature asks for a sensor on its list", PluginTemperature, map[string]any{"sensors": []any{"coretemp_core_0"}}, "coretemp_core_0", true},
		{"temperature stopped asking for a sensor off its list", PluginTemperature, map[string]any{"sensors": []any{"coretemp_core_0"}}, "nvme_composite", false},
		{"temperature with a list still asks for its whole-host item", PluginTemperature, map[string]any{"sensors": []any{"coretemp_core_0"}}, "", true},
		{"temperature without a list asks for every sensor", PluginTemperature, nil, "nvme_composite", true},
		{"smart asks for a device on its list", PluginSmart, map[string]any{"devices": []any{"/dev/sda"}}, "/dev/sda", true},
		{"smart stopped asking for a device off its list", PluginSmart, map[string]any{"devices": []any{"/dev/sda"}}, "/dev/sdb", false},
		{"smart with a list still asks for its whole-host item", PluginSmart, map[string]any{"devices": []any{"/dev/sda"}}, "", true},
		// A disk behind a RAID controller is "<path> [<type>]"; the list
		// cannot name it, so its path on the list counts as asking.
		{"smart asks for a controller disk whose path is listed", PluginSmart, map[string]any{"devices": []any{"/dev/bus/0"}}, "/dev/bus/0 [megaraid,1]", true},
		{"smart stopped asking for a controller disk whose path is not listed", PluginSmart, map[string]any{"devices": []any{"/dev/sda"}}, "/dev/bus/0 [megaraid,1]", false},
		{"smart does not match a path that only starts the same", PluginSmart, map[string]any{"devices": []any{"/dev/bus/0"}}, "/dev/bus/01 [megaraid,1]", false},
		{"raid asks for an array on its list", PluginRaid, map[string]any{"arrays": []any{"md0"}}, "md0", true},
		{"raid stopped asking for an array off its list", PluginRaid, map[string]any{"arrays": []any{"md0"}}, "md1", false},
		{"raid with a list still asks for its whole-host item", PluginRaid, map[string]any{"arrays": []any{"md0"}}, "", true},
		// disk and network report no whole-host item, so "" is only
		// asked when nothing bounds them.
		{"disk with a list does not ask for item \"\"", PluginDisk, map[string]any{"mounts": []string{"/"}}, "", false},
		// JSON round-trips a params list as []any, which is what the
		// store hands back — the same list must still match.
		{"a list decoded from JSON matches too", PluginService, map[string]any{"units": []any{"sshd"}}, "sshd", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := Definition{Plugin: tc.plugin, Params: tc.params}
			if got := d.AsksForItem(tc.item); got != tc.want {
				t.Fatalf("AsksForItem(%q) = %v, want %v", tc.item, got, tc.want)
			}
			// A resolved check — a local one (backlog 3.5a) has no
			// definition — answers by the same rule.
			r := Resolved{Plugin: tc.plugin, Params: tc.params}
			if got := r.AsksForItem(tc.item); got != tc.want {
				t.Fatalf("Resolved.AsksForItem(%q) = %v, want %v", tc.item, got, tc.want)
			}
		})
	}
}
