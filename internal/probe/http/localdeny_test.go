package http_test

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/noixdev/pulsix/internal/probe/probetest"
)

const localRefusal = "target address not allowed by the agent's local policy"

// TestHTTPLocalDeny: an agent's own deny list (backlog 3.82) is applied
// on every hop — a redirect to a denied address and a name resolving to
// one — and reported with its own fixed text, never as up = 0.
func TestHTTPLocalDeny(t *testing.T) {
	t.Run("redirect", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "http://127.0.0.2:9/", http.StatusFound)
		}))
		defer srv.Close()
		g := probetest.LoopbackGuard().WithLocalDeny([]netip.Prefix{netip.MustParsePrefix("127.0.0.2/32")})
		obs := run(context.Background(), g, httpParams(srv.URL, nil), "")
		probetest.WantErrors(t, obs, localRefusal, failAllHostMetrics...)
	})
	t.Run("name", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
		defer srv.Close()
		port := srv.Listener.Addr().(*net.TCPAddr).Port
		g := probetest.LoopbackGuard().WithLocalDeny([]netip.Prefix{netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("::1/128")})
		obs := run(context.Background(), g, httpParams(fmt.Sprintf("http://localhost:%d/", port), nil), "")
		probetest.WantErrors(t, obs, localRefusal, failAllNamedMetrics...)
	})
}
