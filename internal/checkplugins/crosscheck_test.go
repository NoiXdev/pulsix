package checkplugins_test

import (
	"maps"
	"slices"
	"testing"

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
