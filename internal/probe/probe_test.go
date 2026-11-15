package probe_test

import (
	"context"
	"net/netip"
	"testing"

	"github.com/noixdev/pulsix/internal/probe"
)

func TestRegistryRunDispatches(t *testing.T) {
	var (
		gotGuard   *probe.Guard
		gotParams  map[string]any
		gotAddress string
		gotSecret  map[string]string
	)
	reg := probe.Registry{
		"fake": func(_ context.Context, g *probe.Guard, params map[string]any, address string, secret map[string]string) []probe.Observation {
			gotGuard, gotParams, gotAddress, gotSecret = g, params, address, secret
			return []probe.Observation{probe.Value("fake.up", 1)}
		},
		"other": func(context.Context, *probe.Guard, map[string]any, string, map[string]string) []probe.Observation {
			t.Fatal("Run called the probe of another plugin")
			return nil
		},
	}
	g := probe.NewGuardForTest(true, nil)
	params := map[string]any{"port": float64(1)}
	secret := map[string]string{"community": "public"}

	obs := reg.Run(context.Background(), g, "fake", params, "192.0.2.1", secret)
	if len(obs) != 1 || obs[0].Metric != "fake.up" || obs[0].Value == nil || *obs[0].Value != 1 {
		t.Fatalf("Run = %+v, want the probe's own observations", obs)
	}
	if gotGuard != g || gotParams["port"] != float64(1) || gotAddress != "192.0.2.1" || gotSecret["community"] != "public" {
		t.Fatalf("probe got guard %p, params %v, address %q, secret %v", gotGuard, gotParams, gotAddress, gotSecret)
	}
}

// TestRegistryRunWithoutGuardUsesTheProductionOne: a nil guard never reaches
// a probe; Run substitutes the production guard, which refuses loopback.
func TestRegistryRunWithoutGuardUsesTheProductionOne(t *testing.T) {
	var got *probe.Guard
	reg := probe.Registry{
		"fake": func(_ context.Context, g *probe.Guard, _ map[string]any, _ string, _ map[string]string) []probe.Observation {
			got = g
			return nil
		},
	}
	reg.Run(context.Background(), nil, "fake", nil, "127.0.0.1", nil)
	if got == nil {
		t.Fatal("the probe received a nil guard")
	}
	if got.Allowed(netip.MustParseAddr("127.0.0.1")) {
		t.Fatal("the substituted guard allows loopback; want the production guard")
	}
	if !got.Allowed(netip.MustParseAddr("10.0.0.1")) {
		t.Fatal("the substituted guard refuses a private address; want the production guard")
	}
}

func TestRegistryRunUnknownPlugin(t *testing.T) {
	for _, reg := range []probe.Registry{nil, {}} {
		obs := reg.Run(context.Background(), nil, "nosuch", nil, "192.0.2.1", nil)
		if len(obs) != 1 || obs[0].Metric != "nosuch.up" || obs[0].Err != "unknown probe plugin nosuch" || obs[0].Value != nil {
			t.Fatalf("Run(nosuch) = %+v, want one error for nosuch.up", obs)
		}
	}
}
