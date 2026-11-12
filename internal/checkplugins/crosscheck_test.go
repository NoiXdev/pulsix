package checkplugins_test

import (
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	agentplugins "github.com/noixdev/pulsix/internal/agent/checks/plugins"
	"github.com/noixdev/pulsix/internal/checkplugins"
	"github.com/noixdev/pulsix/internal/checks"
	"github.com/noixdev/pulsix/internal/rules"
)

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

// TestMetricsMatchTheRulesCatalogue: every catalogue metric is a
// descriptor's, with the same kind and the descriptor's name as its check
// key, and every descriptor metric is in the catalogue.
func TestMetricsMatchTheRulesCatalogue(t *testing.T) {
	kinds := map[checkplugins.MetricKind]rules.MetricKind{
		checkplugins.Numeric: rules.MetricNumeric, checkplugins.Discrete: rules.MetricDiscrete,
	}
	catalogue := rules.Catalogue()
	described := map[string]bool{}
	for _, d := range checkplugins.All() {
		for _, m := range d.Metrics {
			described[m.Name] = true
			got, ok := catalogue[m.Name]
			if !ok {
				t.Errorf("%s: metric %s is not in the rules catalogue", d.Name, m.Name)
				continue
			}
			if want := (rules.CatalogueEntry{CheckKey: d.Name, Kind: kinds[m.Kind]}); got != want {
				t.Errorf("%s: catalogue has %+v, descriptor says %+v", m.Name, got, want)
			}
		}
	}
	for name := range catalogue {
		if !described[name] {
			t.Errorf("catalogue metric %s has no descriptor", name)
		}
	}
}

// TestPluginSetMatchesInternalChecks: the plugins internal/checks accepts,
// in the order its error message lists them, and the ones it runs on the
// server, are the descriptors'.
func TestPluginSetMatchesInternalChecks(t *testing.T) {
	_, err := checks.ValidateDefinitionShape("snmp", "", nil, time.Minute)
	if !errors.Is(err, checks.ErrInvalidInput) {
		t.Fatalf("ValidateDefinitionShape(snmp) = %v", err)
	}
	const prefix = "plugin must be one of "
	msg := err.Error()
	i, j := strings.Index(msg, prefix), strings.LastIndex(msg, ", got ")
	if i < 0 || j < i {
		t.Fatalf("unexpected message %q", msg)
	}
	listed := strings.Split(msg[i+len(prefix):j], ", ")
	if names := checkplugins.Names(); !slices.Equal(listed, names) {
		t.Fatalf("internal/checks lists %v, descriptors %v", listed, names)
	}
	for _, d := range checkplugins.All() {
		if checks.IsSynthetic(d.Name) != (d.Runs == checkplugins.Server) {
			t.Errorf("%s: IsSynthetic = %v, Runs = %s", d.Name, checks.IsSynthetic(d.Name), d.Runs)
		}
	}
}
