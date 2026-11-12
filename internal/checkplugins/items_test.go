package checkplugins_test

import (
	"testing"

	"github.com/noixdev/pulsix/internal/checkplugins"
)

// TestAsksForItem ports internal/checks' TestDefinitionAsksForItem: a
// current value survives an operator narrowing a check's parameters only
// if the definition still asks for that item (backlog 3.28).
func TestAsksForItem(t *testing.T) {
	tests := []struct {
		name   string
		plugin string
		params map[string]any
		item   string
		want   bool
	}{
		{"disk asks for a mount on its list", "disk", map[string]any{"mounts": []string{"/", "/var"}}, "/var", true},
		{"disk stopped asking for a mount off its list", "disk", map[string]any{"mounts": []string{"/"}}, "/var", false},
		// No list means "every mount the host reports".
		{"disk without a list asks for every mount", "disk", nil, "/var", true},
		{"disk with an empty list asks for every mount", "disk", map[string]any{"mounts": []string{}}, "/var", true},
		{"service asks for a unit on its list", "service", map[string]any{"units": []string{"sshd", "nginx"}}, "nginx", true},
		{"service stopped asking for a removed unit", "service", map[string]any{"units": []string{"sshd"}}, "nginx", false},
		// cpu, memory and uptime report one whole-host item ("").
		{"cpu asks for its whole-host item", "cpu", nil, "", true},
		{"memory asks for its whole-host item", "memory", nil, "", true},
		{"uptime asks for its whole-host item", "uptime", nil, "", true},
		{"network asks for an interface on its list", "network", map[string]any{"interfaces": []string{"eth0", "wg0"}}, "wg0", true},
		{"network stopped asking for an interface off its list", "network", map[string]any{"interfaces": []string{"eth0"}}, "wg0", false},
		{"network without a list asks for every interface", "network", nil, "eth1", true},
		// The synthetic plugins report one whole-host item whatever their
		// parameters say.
		{"icmp asks for its whole-host item", "icmp", map[string]any{"count": 3.0}, "", true},
		{"tcp asks for its whole-host item", "tcp", map[string]any{"port": 22.0}, "", true},
		{"http asks for its whole-host item", "http", map[string]any{"url": "https://{address}/"}, "", true},
		{"tls asks for its whole-host item", "tls", nil, "", true},
		{"dns asks for its whole-host item", "dns", map[string]any{"name": "example.com"}, "", true},
		// The hardware plugins bound their items by their list, but also
		// report a whole-host item "", which every list still asks for.
		{"temperature asks for a sensor on its list", "temperature", map[string]any{"sensors": []any{"coretemp_core_0"}}, "coretemp_core_0", true},
		{"temperature stopped asking for a sensor off its list", "temperature", map[string]any{"sensors": []any{"coretemp_core_0"}}, "nvme_composite", false},
		{"temperature with a list still asks for its whole-host item", "temperature", map[string]any{"sensors": []any{"coretemp_core_0"}}, "", true},
		{"temperature without a list asks for every sensor", "temperature", nil, "nvme_composite", true},
		{"smart asks for a device on its list", "smart", map[string]any{"devices": []any{"/dev/sda"}}, "/dev/sda", true},
		{"smart stopped asking for a device off its list", "smart", map[string]any{"devices": []any{"/dev/sda"}}, "/dev/sdb", false},
		{"smart with a list still asks for its whole-host item", "smart", map[string]any{"devices": []any{"/dev/sda"}}, "", true},
		// A disk behind a RAID controller is "<path> [<type>]"; its path on
		// the list counts as asking.
		{"smart asks for a controller disk whose path is listed", "smart", map[string]any{"devices": []any{"/dev/bus/0"}}, "/dev/bus/0 [megaraid,1]", true},
		{"smart stopped asking for a controller disk whose path is not listed", "smart", map[string]any{"devices": []any{"/dev/sda"}}, "/dev/bus/0 [megaraid,1]", false},
		{"smart does not match a path that only starts the same", "smart", map[string]any{"devices": []any{"/dev/bus/0"}}, "/dev/bus/01 [megaraid,1]", false},
		{"smart without a closing bracket is a path of its own", "smart", map[string]any{"devices": []any{"/dev/bus/0"}}, "/dev/bus/0 [megaraid,1", false},
		{"raid asks for an array on its list", "raid", map[string]any{"arrays": []any{"md0"}}, "md0", true},
		{"raid stopped asking for an array off its list", "raid", map[string]any{"arrays": []any{"md0"}}, "md1", false},
		{"raid with a list still asks for its whole-host item", "raid", map[string]any{"arrays": []any{"md0"}}, "", true},
		// disk and network report no whole-host item, so "" is only asked
		// when nothing bounds them.
		{"disk with a list does not ask for item \"\"", "disk", map[string]any{"mounts": []string{"/"}}, "", false},
		// The controller-disk rule is smart's alone.
		{"disk does not strip a bracket suffix", "disk", map[string]any{"mounts": []string{"/x"}}, "/x [y]", false},
		// JSON round-trips a params list as []any.
		{"a list decoded from JSON matches too", "service", map[string]any{"units": []any{"sshd"}}, "sshd", true},
		{"a list that is not strings bounds nothing", "disk", map[string]any{"mounts": []any{1.0}}, "/var", true},
		{"an unknown plugin asks for everything", "snmp", map[string]any{"oids": []any{"x"}}, "y", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := checkplugins.AsksForItem(tc.plugin, tc.params, tc.item); got != tc.want {
				t.Fatalf("AsksForItem(%s, %v, %q) = %v, want %v", tc.plugin, tc.params, tc.item, got, tc.want)
			}
		})
	}
}
