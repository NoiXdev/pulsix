package app

import (
	"net/netip"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/config"
	"github.com/noixdev/pulsix/internal/probe"
)

// TestProbeGuardDefaultIsProduction: without WithProbeGuard the probe
// runner gets the production guard built from the config — loopback
// refused, a public address allowed, PULSIX_PROBE_DENY_CIDRS honoured —
// and the tick and refresh stay zero, which probes.NewRunner turns into
// its own defaults.
func TestProbeGuardDefaultIsProduction(t *testing.T) {
	cfg := config.Config{ProbeDenyCIDRs: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}}
	var o buildOptions
	g := o.probeGuardFor(cfg)
	for _, tc := range []struct {
		ip   string
		want bool
	}{
		{"127.0.0.1", false},
		{"::1", false},
		{"169.254.169.254", false},
		{"10.1.2.3", false},
		{"192.0.2.10", true},
	} {
		if got := g.Allowed(netip.MustParseAddr(tc.ip)); got != tc.want {
			t.Errorf("default guard Allowed(%s) = %v, want %v", tc.ip, got, tc.want)
		}
	}
	if o.probeTick != 0 || o.probeRefresh != 0 {
		t.Fatalf("probe tick/refresh = %v/%v without options, want 0/0 (the runner's defaults)", o.probeTick, o.probeRefresh)
	}
}

// TestProbeOptionsApply: WithProbeGuard, WithProbeTick and
// WithProbeRefresh set exactly their own field, and the injected guard is
// the one the runner gets.
func TestProbeOptionsApply(t *testing.T) {
	injected := probe.NewGuardForTest(true, nil)
	var o buildOptions
	for _, opt := range []Option{WithProbeGuard(injected), WithProbeTick(250 * time.Millisecond), WithProbeRefresh(time.Second)} {
		opt(&o)
	}
	if g := o.probeGuardFor(config.Config{}); g != injected {
		t.Fatal("probeGuardFor did not return the injected guard")
	}
	if o.probeTick != 250*time.Millisecond || o.probeRefresh != time.Second {
		t.Fatalf("probe tick/refresh = %v/%v, want 250ms/1s", o.probeTick, o.probeRefresh)
	}
	if o.scheduleNow != nil || o.chainTick != 0 {
		t.Fatal("the probe options touched other fields")
	}
}
