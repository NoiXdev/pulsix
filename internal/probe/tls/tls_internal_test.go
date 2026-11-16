package tls

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/probe"
	"github.com/noixdev/pulsix/internal/probe/probetest"
)

func loopbackOnly() *probe.Guard { return probe.NewGuardForTest(true, nil) }

func TestTLSCertValidAgainstInjectedRoots(t *testing.T) {
	ca := probetest.NewCA(t)
	now := time.Now()
	_, port := probetest.TLSServer(t, ca.Leaf(t, now.Add(-time.Hour), now.Add(10*24*time.Hour)))

	tests := []struct {
		name   string
		params map[string]any
		want   float64
	}{
		{"matching server_name", map[string]any{"port": port, "server_name": "probe.test"}, 1},
		{"address as server_name", map[string]any{"port": port}, 1},
		{"other server_name", map[string]any{"port": port, "server_name": "other.test"}, 0},
		{"address placeholder as server_name", map[string]any{"port": port, "server_name": "{address}"}, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// An invalid chain is no failure: the handshake completed.
			m := probetest.WantUp(t, probeTLS(context.Background(), loopbackOnly(), tc.params, "127.0.0.1", ca.Pool), "tls")
			if len(m) != 6 {
				t.Fatalf("want tls up with six metrics, got %+v", m)
			}
			if got := probetest.WantLabel(t, m, "tls.cert_issuer"); got != "probe test CA" {
				t.Fatalf("tls.cert_issuer = %q, want the CA's common name", got)
			}
			if v := m["tls.cert_valid"].Value; v == nil || *v != tc.want {
				t.Fatalf("tls.cert_valid = %v, want %v", v, tc.want)
			}
			if v := m["tls.cert_expiry_days"].Value; v == nil || *v < 9 || *v > 10 {
				t.Fatalf("tls.cert_expiry_days = %v, want about 10", v)
			}
		})
	}

	// The same server against the system roots (nil) does not verify.
	m := probetest.ByMetric(t, probeTLS(context.Background(), loopbackOnly(), map[string]any{"port": port, "server_name": "probe.test"}, "127.0.0.1", nil))
	if v := m["tls.cert_valid"].Value; v == nil || *v != 0 {
		t.Fatalf("against the system roots: tls.cert_valid = %v, want 0", v)
	}
}

// TestTLSServerNameAddressIPv6: {address} in server_name becomes the
// host's IPv6 address without brackets, which is what SNI and the
// certificate's IP SAN are checked against.
func TestTLSServerNameAddressIPv6(t *testing.T) {
	ln, err := net.Listen("tcp", "[::1]:0")
	if err != nil {
		t.Skipf("no IPv6 loopback: %v", err)
	}
	ca := probetest.NewCA(t)
	now := time.Now()
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	_ = srv.Listener.Close()
	srv.Listener = ln
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{ca.Leaf(t, now.Add(-time.Hour), now.Add(24*time.Hour))}}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	port := float64(ln.Addr().(*net.TCPAddr).Port)
	m := probetest.ByMetric(t, probeTLS(context.Background(), loopbackOnly(), map[string]any{"port": port, "server_name": "{address}"}, "[::1]", ca.Pool))
	if v := m["tls.up"].Value; v == nil || *v != 1 {
		t.Fatalf("tls.up = %v, want 1", m)
	}
	if v := m["tls.cert_valid"].Value; v == nil || *v != 1 {
		t.Fatalf("tls.cert_valid = %v, want 1: {address} must become ::1, unbracketed", v)
	}
}

func TestTLSExpiredLeafHasNegativeExpiry(t *testing.T) {
	ca := probetest.NewCA(t)
	now := time.Now()
	_, port := probetest.TLSServer(t, ca.Leaf(t, now.Add(-10*24*time.Hour), now.Add(-2*24*time.Hour)))
	m := probetest.ByMetric(t, probeTLS(context.Background(), loopbackOnly(), map[string]any{"port": port, "server_name": "probe.test"}, "127.0.0.1", ca.Pool))
	if v := m["tls.up"].Value; v == nil || *v != 1 {
		t.Fatalf("tls.up = %v, want 1", v)
	}
	if v := m["tls.cert_valid"].Value; v == nil || *v != 0 {
		t.Fatalf("tls.cert_valid = %v, want 0 for an expired leaf", v)
	}
	if v := m["tls.cert_expiry_days"].Value; v == nil || *v > -1.9 || *v < -2.1 {
		t.Fatalf("tls.cert_expiry_days = %v, want about -2", v)
	}
}

// TestOldTLSVersionsAreAccepted: Go's client floor of TLS 1.2 would make
// old LAN appliances read down, so the tls probe (which verifies the chain
// itself) accepts TLS 1.0.
func TestOldTLSVersionsAreAccepted(t *testing.T) {
	if v := tlsProbeConfig("probe.test").MinVersion; v != tls.VersionTLS10 {
		t.Fatalf("tls probe MinVersion = %x, want TLS 1.0", v)
	}
}
