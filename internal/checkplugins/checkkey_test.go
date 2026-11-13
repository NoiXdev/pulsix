package checkplugins_test

import (
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/checkplugins"
)

func TestValidCheckKey(t *testing.T) {
	tests := []struct {
		key  string
		want bool
	}{
		{"nginx", true},
		{"nginx-internal", true},
		{"nginx-2", true},
		{"0", true},
		{"a" + strings.Repeat("b", 62), true},
		{"a" + strings.Repeat("b", 63), false},
		{"", false},
		{"-nginx", false},
		{"Nginx", false},
		{"nginx_internal", false},
		{"nginx.internal", false},
		{" nginx", false},
		{"nginx ", false},
		{"nginx/2", false},
	}
	for _, tt := range tests {
		if got := checkplugins.ValidCheckKey(tt.key); got != tt.want {
			t.Errorf("ValidCheckKey(%q) = %v, want %v", tt.key, got, tt.want)
		}
	}
}

// TestEveryPluginNameIsAValidCheckKey: a check's key defaults to its
// plugin's name, and a rule's check key is derived from it, so every name
// must pass the key format.
func TestEveryPluginNameIsAValidCheckKey(t *testing.T) {
	for _, name := range checkplugins.Names() {
		if !checkplugins.ValidCheckKey(name) {
			t.Errorf("plugin name %q is not a valid check key", name)
		}
	}
}

// TestNoTwoPluginsShareAMetricName: a rule names a metric, and org, group
// and host rules match a host's tuples by metric alone, whatever the
// tuple's check key (backlog 3.13e). That is only right while a metric
// name belongs to exactly one plugin.
func TestNoTwoPluginsShareAMetricName(t *testing.T) {
	owner := map[string]string{}
	for _, d := range checkplugins.All() {
		for _, m := range d.Metrics {
			if prev, ok := owner[m.Name]; ok {
				t.Errorf("metric %q is reported by both %q and %q", m.Name, prev, d.Name)
				continue
			}
			owner[m.Name] = d.Name
		}
	}
	if len(owner) == 0 {
		t.Fatal("no metrics at all; the descriptors did not load")
	}
}
