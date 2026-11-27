package tls_test

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/noixdev/pulsix/internal/probe/probetest"
)

// TestTLSLocalDeny: an agent's own deny list (backlog 3.82) refuses the
// target with its own text.
func TestTLSLocalDeny(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	defer srv.Close()
	port := srv.Listener.Addr().(*net.TCPAddr).Port
	g := probetest.LoopbackGuard().WithLocalDeny([]netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")})
	obs := run(context.Background(), g, map[string]any{"port": float64(port)}, "127.0.0.1")
	probetest.WantErrors(t, obs, "target address not allowed by the agent's local policy",
		"tls.up", "tls.failure", "tls.handshake_ms", "tls.cert_valid", "tls.cert_expiry_days", "tls.cert_issuer")
}
