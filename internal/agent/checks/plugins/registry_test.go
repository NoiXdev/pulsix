package plugins

import (
	"testing"

	"github.com/noixdev/pulsix/internal/agent/checks"
)

// TestRegistryWiresEveryPlugin pins the seven names Task 8's scheduler and
// the manager's plugin validation (internal/checks.supportedPlugins) agree
// on; a typo'd registry key would silently orphan a plugin from every
// check definition that names it.
func TestRegistryWiresEveryPlugin(t *testing.T) {
	reg := Registry()
	want := []string{"cpu", "memory", "disk", "service", "network", "uptime", "docker"}
	if len(reg) != len(want) {
		t.Fatalf("Registry() has %d plugins, want %d: %v", len(reg), len(want), reg)
	}
	for _, name := range want {
		p, ok := reg[name]
		if !ok {
			t.Fatalf("Registry() is missing plugin %q", name)
		}
		if p == nil {
			t.Fatalf("Registry()[%q] is nil", name)
		}
		if p.Name() != name {
			t.Errorf("Registry()[%q].Name() = %q, want %q", name, p.Name(), name)
		}
	}
}

// TestRegistryNetworkIsStateful: the scheduler recognises a plugin that
// must be cloned per check key only through checks.StatefulPlugin. Now
// that network lives in its own package, a method set that drifted from
// the interface would make every network key share one baseline without
// a compile error, so the registry pins it.
func TestRegistryNetworkIsStateful(t *testing.T) {
	reg := Registry()
	if _, ok := reg["network"].(checks.StatefulPlugin); !ok {
		t.Fatalf("Registry()[network] = %T, want a checks.StatefulPlugin", reg["network"])
	}
	for name, p := range reg {
		if _, ok := p.(checks.StatefulPlugin); ok && name != "network" {
			t.Errorf("Registry()[%q] is a checks.StatefulPlugin; only network keeps state", name)
		}
	}
}
