package icmp_test

import (
	"context"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/probe"
	"github.com/noixdev/pulsix/internal/probe/probetest"
)

func TestICMPLoopback(t *testing.T) {
	obs := run(context.Background(), probetest.LoopbackGuard(),
		map[string]any{"count": float64(2), "timeout_s": float64(2)}, "127.0.0.1")
	m := probetest.ByMetric(t, obs)
	if up := m["icmp.up"]; up.Value == nil && (strings.Contains(up.Err, "ping_group_range") || strings.HasPrefix(up.Err, "open icmp socket")) {
		t.Skipf("unprivileged ICMP sockets are not permitted here: %s", up.Err)
	}
	probetest.WantUp(t, obs, "icmp")
	if len(m) != 4 {
		t.Fatalf("want up, failure, loss_pct and rtt_ms, got %+v", obs)
	}
	if v := probetest.WantValue(t, m, "icmp.loss_pct"); v != 0 {
		t.Fatalf("icmp.loss_pct = %v, want 0", v)
	}
	if v := probetest.WantValue(t, m, "icmp.rtt_ms"); v < 0 {
		t.Fatalf("icmp.rtt_ms = %v, want >= 0", v)
	}
}

func TestICMPRefusedTargetIsAnError(t *testing.T) {
	for _, addr := range []string{"127.0.0.1", "169.254.169.254", "localhost"} {
		t.Run(addr, func(t *testing.T) {
			obs := run(context.Background(), probe.NewGuard(nil, nil), map[string]any{"count": float64(1)}, addr)
			probetest.WantErrors(t, obs, "target address not allowed", "icmp.up", "icmp.failure", "icmp.loss_pct", "icmp.rtt_ms")
		})
	}
}

func TestICMPEmptyAddressIsAnError(t *testing.T) {
	obs := run(context.Background(), probetest.LoopbackGuard(), nil, "  ")
	probetest.WantErrors(t, obs, "host has no address", "icmp.up", "icmp.failure", "icmp.loss_pct", "icmp.rtt_ms")
}

func TestICMPUnresolvableHostIsDown(t *testing.T) {
	obs := run(context.Background(), probetest.LoopbackGuard(), map[string]any{"timeout_s": float64(2)}, "no-such-host.invalid")
	probetest.WantDown(t, obs, "icmp", probe.FailureDNS)
}
