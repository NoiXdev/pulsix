package dns_test

import (
	"context"
	"net/netip"
	"testing"

	"github.com/noixdev/pulsix/internal/probe/probetest"
)

// TestDNSLocalDenyServer: an agent's own deny list (backlog 3.82) refuses
// a configured server with its own text.
func TestDNSLocalDenyServer(t *testing.T) {
	server := startDNSStub(t, exampleZone())
	g := probetest.LoopbackGuard().WithLocalDeny([]netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")})
	obs := run(context.Background(), g, dnsParams(server, map[string]any{"name": "example.test", "expect": "192.0.2.7"}), "")
	probetest.WantErrors(t, obs, "target address not allowed by the agent's local policy",
		"dns.up", "dns.failure", "dns.resolve_ms", "dns.answer_match")
}
