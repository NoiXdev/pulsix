package http

import (
	"context"
	"crypto/tls"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/probe"
	"github.com/noixdev/pulsix/internal/probe/probetest"
)

func loopbackOnly() *probe.Guard { return probe.NewGuardForTest(true, nil) }

func TestHTTPVerifiesAgainstInjectedRoots(t *testing.T) {
	ca := probetest.NewCA(t)
	now := time.Now()
	srv, _ := probetest.TLSServer(t, ca.Leaf(t, now.Add(-time.Hour), now.Add(24*time.Hour)))
	params := map[string]any{"url": srv.URL, "timeout_s": float64(5)}
	m := probetest.ByMetric(t, probeHTTP(context.Background(), loopbackOnly(), params, "", ca.Pool))
	if v := m["http.up"].Value; v == nil || *v != 1 {
		t.Fatalf("http.up = %v, want 1 with the CA in the roots", m)
	}
	if v := m["http.status_code"].Value; v == nil || *v != 200 {
		t.Fatalf("http.status_code = %v, want 200", v)
	}
}

func TestHTTPTransportDialsOnlyThroughTheGuard(t *testing.T) {
	var refused atomic.Bool
	tr := newHTTPTransport(probe.NewGuard(nil, nil), true, nil, &refused)
	if tr.Proxy != nil {
		t.Fatal("the transport must not use a proxy")
	}
	if !tr.DisableKeepAlives {
		t.Fatal("the transport must not reuse connections")
	}
	if tr.DialTLSContext != nil || tr.DialContext == nil {
		t.Fatal("every connection must go through DialContext")
	}
	_, err := tr.DialContext(context.Background(), "tcp", netip.MustParseAddrPort("127.0.0.1:9").String())
	if err == nil || !refused.Load() {
		t.Fatalf("a refused dial must be recorded: err %v, refused %v", err, refused.Load())
	}
}

// TestOldTLSVersionsAreAccepted: Go's client floor of TLS 1.2 would make
// old LAN appliances read down, so an http probe that does not verify
// accepts TLS 1.0; an http probe that verifies keeps Go's default floor.
func TestHTTPOldTLSVersionsAreAccepted(t *testing.T) {
	var refused atomic.Bool
	if v := newHTTPTransport(loopbackOnly(), false, nil, &refused).TLSClientConfig.MinVersion; v != tls.VersionTLS10 {
		t.Fatalf("http probe without verify_tls: MinVersion = %x, want TLS 1.0", v)
	}
	if v := newHTTPTransport(loopbackOnly(), true, nil, &refused).TLSClientConfig.MinVersion; v != 0 {
		t.Fatalf("http probe with verify_tls: MinVersion = %x, want Go's default", v)
	}
}
