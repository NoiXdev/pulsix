package probe_test

import (
	"context"
	"strings"
	"testing"

	"github.com/noixdev/pulsix/internal/probe"
)

func TestICMPLoopback(t *testing.T) {
	obs := probe.Run(context.Background(), loopbackGuard(), "icmp",
		map[string]any{"count": float64(2), "timeout_s": float64(2)}, "127.0.0.1")
	m := byMetric(t, obs)
	if up := m["icmp.up"]; up.Value == nil && (strings.Contains(up.Err, "ping_group_range") || strings.HasPrefix(up.Err, "open icmp socket")) {
		t.Skipf("unprivileged ICMP sockets are not permitted here: %s", up.Err)
	}
	if len(m) != 3 {
		t.Fatalf("want up, loss_pct and rtt_ms, got %+v", obs)
	}
	if v := wantValue(t, m, "icmp.up"); v != 1 {
		t.Fatalf("icmp.up = %v, want 1", v)
	}
	if v := wantValue(t, m, "icmp.loss_pct"); v != 0 {
		t.Fatalf("icmp.loss_pct = %v, want 0", v)
	}
	if v := wantValue(t, m, "icmp.rtt_ms"); v < 0 {
		t.Fatalf("icmp.rtt_ms = %v, want >= 0", v)
	}
}

func TestICMPRefusedTargetIsAnError(t *testing.T) {
	for _, addr := range []string{"127.0.0.1", "169.254.169.254", "localhost"} {
		t.Run(addr, func(t *testing.T) {
			obs := probe.Run(context.Background(), probe.NewGuard(nil, nil), "icmp", map[string]any{"count": float64(1)}, addr)
			wantErrors(t, obs, "target address not allowed", "icmp.up", "icmp.loss_pct", "icmp.rtt_ms")
		})
	}
}

func TestICMPEmptyAddressIsAnError(t *testing.T) {
	obs := probe.Run(context.Background(), loopbackGuard(), "icmp", nil, "  ")
	wantErrors(t, obs, "host has no address", "icmp.up", "icmp.loss_pct", "icmp.rtt_ms")
}

func TestICMPUnresolvableHostIsDown(t *testing.T) {
	obs := probe.Run(context.Background(), loopbackGuard(), "icmp", map[string]any{"timeout_s": float64(2)}, "no-such-host.invalid")
	wantOnly(t, obs, "icmp.up", 0)
}
