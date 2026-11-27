package vantage_test

import (
	"maps"
	"slices"
	"testing"

	"github.com/noixdev/pulsix/internal/checkplugins"
	"github.com/noixdev/pulsix/internal/probe/all"
	"github.com/noixdev/pulsix/internal/probe/vantage"
)

// TestVantageRegistryIsTheServerPluginsButSNMP: an agent vantage runs every
// probe the manager runs except snmp, the one server plugin whose check
// always names a secret (and a check with a secret is probed only from
// the manager).
func TestVantageRegistryIsTheServerPluginsButSNMP(t *testing.T) {
	got := slices.Sorted(maps.Keys(vantage.Registry(nil)))
	var want []string
	for _, d := range checkplugins.All() {
		if d.Runs != checkplugins.Server || d.Name == "snmp" {
			continue
		}
		want = append(want, d.Name)
	}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("vantage registry = %v, want %v", got, want)
	}
	manager := all.Registry(nil)
	if _, ok := manager["snmp"]; !ok || len(manager) != len(got)+1 {
		t.Fatalf("the manager registry is not the vantage's plus snmp: %v", slices.Sorted(maps.Keys(manager)))
	}
	if _, ok := vantage.FailAll("snmp", map[string]any{}, "boom"); ok {
		t.Fatal("the vantage FailAll knows snmp")
	}
	for _, name := range []string{"http", "tcp", "dns", "tls"} {
		if _, ok := vantage.FailAll(name, map[string]any{}, "boom"); !ok {
			t.Errorf("the vantage FailAll lacks %s", name)
		}
	}
}

// TestSNMPRequiresASecret pins the premise of the split: an snmp check
// cannot be saved without a secret, so it can never name vantage groups.
func TestSNMPRequiresASecret(t *testing.T) {
	d, ok := checkplugins.Get("snmp")
	if !ok {
		t.Fatal("no snmp descriptor")
	}
	for _, p := range d.Params {
		if p.Name == checkplugins.SecretParamName {
			if !p.Required {
				t.Fatal("snmp's secret is optional; an snmp check could then be probed by agents")
			}
			return
		}
	}
	t.Fatal("snmp has no secret param")
}
