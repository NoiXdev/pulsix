package icmp_test

import (
	"context"
	"net/netip"
	"testing"

	"github.com/noixdev/pulsix/internal/probe/probetest"
)

// TestICMPLocalDeny: an agent's own deny list (backlog 3.82) refuses a
// literal and a name resolving to a denied address before anything is
// sent, with its own text.
func TestICMPLocalDeny(t *testing.T) {
	g := probetest.LoopbackGuard().WithLocalDeny([]netip.Prefix{netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("::1/128")})
	for _, addr := range []string{"127.0.0.1", "localhost"} {
		t.Run(addr, func(t *testing.T) {
			obs := run(context.Background(), g, map[string]any{"count": float64(1)}, addr)
			probetest.WantErrors(t, obs, "target address not allowed by the agent's local policy",
				"icmp.up", "icmp.failure", "icmp.loss_pct", "icmp.rtt_ms")
		})
	}
}
