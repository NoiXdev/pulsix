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
