package plugins

import (
	"testing"

	"github.com/noixdev/pulsix/internal/agent/checks"
)

// TestRegistryWiresEveryPlugin pins the plugin names Task 8's scheduler and
// the manager's plugin descriptors (internal/checkplugins) agree
// on; a typo'd registry key would silently orphan a plugin from every
// check definition that names it.
func TestRegistryWiresEveryPlugin(t *testing.T) {
	reg := Registry()
	want := []string{"cpu", "memory", "disk", "service", "network", "uptime", "docker", "temperature", "smart", "raid", "postgres", "mysql", "redis", "nginx", "apache", "caddy"}
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

// TestRegistryStatefulPlugins: the scheduler recognises a plugin that must
// be cloned per check key only through checks.StatefulPlugin. network.go and
// mysql.go assert the interface at compile time; this test pins it as the
// registry hands the plugins out, and that no other plugin keeps state
// (network keeps interface counters, mysql the slow-query counter, redis the
// eviction counter, nginx, apache and caddy the request counters).
func TestRegistryStatefulPlugins(t *testing.T) {
	reg := Registry()
	stateful := map[string]bool{"network": true, "mysql": true, "redis": true, "nginx": true, "apache": true, "caddy": true}
	for name := range stateful {
		if _, ok := reg[name].(checks.StatefulPlugin); !ok {
			t.Fatalf("Registry()[%s] = %T, want a checks.StatefulPlugin", name, reg[name])
		}
	}
	for name, p := range reg {
		if _, ok := p.(checks.StatefulPlugin); ok && !stateful[name] {
			t.Errorf("Registry()[%q] is a checks.StatefulPlugin; only network, mysql, redis, nginx, apache and caddy keep state", name)
		}
	}
}

// TestRegistrySecretPluginsNameAnErrorMetric: the scheduler reports a
// missing secret on SecretErrorMetric, so it must never be empty.
func TestRegistrySecretPluginsNameAnErrorMetric(t *testing.T) {
	for name, p := range Registry() {
		if sp, ok := p.(checks.SecretPlugin); ok && sp.SecretErrorMetric() == "" {
			t.Errorf("Registry()[%q] has an empty SecretErrorMetric()", name)
		}
	}
}
