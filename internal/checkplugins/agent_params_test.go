package checkplugins_test

import (
	"testing"
)

// TestValidateAgentParams ports the parameter rows of internal/checks'
// TestCreateDefinitionValidation: cpu, memory and uptime take no
// parameters; disk and network an optional list of non-blank strings;
// service a required, non-empty one.
func TestValidateAgentParams(t *testing.T) {
	runParamCases(t, []paramCase{
		{"cpu with no parameters accepted", "cpu", nil, true},
		{"cpu with an empty map accepted", "cpu", map[string]any{}, true},
		{"cpu with parameters refused", "cpu", map[string]any{"anything": true}, false},
		{"memory with no parameters accepted", "memory", nil, true},
		{"memory with parameters refused", "memory", map[string]any{"anything": true}, false},
		{"uptime with no parameters accepted", "uptime", nil, true},
		{"uptime with parameters refused", "uptime", map[string]any{"anything": true}, false},

		{"disk with no parameters accepted (mounts is optional)", "disk", nil, true},
		{"disk with a mounts allow-list accepted", "disk", map[string]any{"mounts": []string{"/", "/var"}}, true},
		{"disk with an empty mounts list accepted", "disk", map[string]any{"mounts": []any{}}, true},
		{"disk with an unknown parameter refused", "disk", map[string]any{"typo": []string{"/"}}, false},
		{"disk mounts with an empty entry refused", "disk", map[string]any{"mounts": []string{""}}, false},
		{"disk mounts with a blank entry refused", "disk", map[string]any{"mounts": []any{"/", "  "}}, false},
		{"disk mounts that is not a list refused", "disk", map[string]any{"mounts": "/"}, false},

		{"service without units refused", "service", nil, false},
		{"service with an empty units list refused", "service", map[string]any{"units": []string{}}, false},
		{"service with units, as []any the way JSON decodes it, accepted", "service", map[string]any{"units": []any{"sshd", "cron"}}, true},
		{"service units with a non-string entry refused", "service", map[string]any{"units": []any{"sshd", 5}}, false},
		{"service units with a blank entry refused", "service", map[string]any{"units": []any{"sshd", "\t"}}, false},
		{"service units null refused", "service", map[string]any{"units": nil}, false},
		{"service with an unknown parameter refused", "service", map[string]any{"units": []any{"sshd"}, "unit": "x"}, false},

		{"network with no parameters accepted (interfaces is optional)", "network", nil, true},
		{"network with an interfaces list, as []any the way JSON decodes it, accepted", "network", map[string]any{"interfaces": []any{"eth0", "wg0"}}, true},
		{"network with an unknown parameter refused", "network", map[string]any{"interface": []string{"eth0"}}, false},
		{"network interfaces with an empty entry refused", "network", map[string]any{"interfaces": []string{"eth0", " "}}, false},
		{"network interfaces that is not a list of strings refused", "network", map[string]any{"interfaces": "eth0"}, false},

		{"an unknown plugin refused", "ping", nil, false},
		{"an empty plugin refused", "", nil, false},
	})
}
