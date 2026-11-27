package tcp_test

import (
	"context"
	"net/netip"
	"testing"

	"github.com/noixdev/pulsix/internal/probe/probetest"
)

// TestTCPLocalDeny: an agent's own deny list (backlog 3.82) refuses a
// literal and a name resolving to a denied address, with its own text.
func TestTCPLocalDeny(t *testing.T) {
	port, _ := probetest.TCPServer(t, "tcp", "127.0.0.1:0", "")
	g := probetest.LoopbackGuard().WithLocalDeny([]netip.Prefix{netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("::1/128")})
	for _, addr := range []string{"127.0.0.1", "localhost"} {
		obs := run(context.Background(), g, tcpParams(port, nil), addr)
		probetest.WantErrors(t, obs, "target address not allowed by the agent's local policy", "tcp.up", "tcp.failure", "tcp.connect_ms")
	}
}

// TestTCPLocalDenyAmongSeveralAddresses: a name resolving to an allowed
// address that refuses the connection and to one the agent's own list
// refuses reads as the local refusal, not as down (backlog 3.82 R6: any
// local refusal wins).
func TestTCPLocalDenyAmongSeveralAddresses(t *testing.T) {
	probetest.LocalhostBothFamilies(t)
	port := probetest.ClosedPort(t)
	for _, denied := range []string{"::1/128", "127.0.0.0/8"} {
		t.Run(denied, func(t *testing.T) {
			g := probetest.LoopbackGuard().WithLocalDeny([]netip.Prefix{netip.MustParsePrefix(denied)})
			obs := run(context.Background(), g, tcpParams(port, nil), "localhost")
			probetest.WantErrors(t, obs, "target address not allowed by the agent's local policy", "tcp.up", "tcp.failure", "tcp.connect_ms")
		})
	}
}
