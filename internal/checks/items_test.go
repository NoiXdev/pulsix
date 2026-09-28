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
		})
	}
}
