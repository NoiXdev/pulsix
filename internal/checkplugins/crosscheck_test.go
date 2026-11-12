package checkplugins_test

import (
	"maps"
	"slices"
	"testing"

	"github.com/noixdev/pulsix/internal/agent/checks"
	agentplugins "github.com/noixdev/pulsix/internal/agent/checks/plugins"
	"github.com/noixdev/pulsix/internal/checkplugins"
)

// The manager no longer keeps lists of its own to cross-check: internal/checks
// (plugin set, IsSynthetic, parameters, items) and internal/rules (the
// catalogue) derive from the descriptors. What pins them independently are
// internal/checks' literal tables (plugins_test.go, params_test.go,
// items_test.go) and internal/rules' literal catalogue
// (TestCatalogueMatchesShippedPlugins). The agent is the one consumer that
// is not derived, hence this file.

// TestAgentPluginsMatchDescriptors: the agent's plugins are not generated
// from the descriptors, so this is what keeps the two in step — every
// Runs = Agent descriptor has an agent plugin, and every agent plugin a
// descriptor.
func TestAgentPluginsMatchDescriptors(t *testing.T) {
	var described []string
	for _, d := range checkplugins.All() {
		if d.Runs == checkplugins.Agent {
			described = append(described, d.Name)
		}
	}
	slices.Sort(described)
	agent := slices.Sorted(maps.Keys(agentplugins.Registry()))
	if !slices.Equal(described, agent) {
		t.Fatalf("agent descriptors %v, agent registry %v", described, agent)
	}
}

// TestSecretPluginsAreExactlyTheDescriptorsThatTakeASecret (backlog 3.10,
// precondition 2): an agent plugin is a checks.SecretPlugin exactly when its
// descriptor takes the secret parameter, and the metric a missing secret is
// reported on is one the descriptor declares. Without the first half a plugin
// that ignored its secret would run with nil fields; without the second a
// missing-secret error would land on a metric no rule or graph knows.
func TestSecretPluginsAreExactlyTheDescriptorsThatTakeASecret(t *testing.T) {
	for name, plugin := range agentplugins.Registry() {
		d, ok := checkplugins.Get(name)
		if !ok {
			t.Fatalf("agent plugin %q has no descriptor", name)
		}
		sp, isSecret := plugin.(checks.SecretPlugin)
		if isSecret != d.UsesSecret() {
			t.Errorf("%s: SecretPlugin = %v, descriptor UsesSecret = %v", name, isSecret, d.UsesSecret())
		}
		if !isSecret {
			continue
		}
		metric := sp.SecretErrorMetric()
		found := false
		for _, m := range d.Metrics {
			found = found || m.Name == metric
		}
		if !found {
			t.Errorf("%s: SecretErrorMetric %q is not one of the descriptor's metrics", name, metric)
		}
	}
}
