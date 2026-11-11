package probe

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"
)

// testCA is a throwaway certificate authority for the probe's injected
// root pool.
type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pool *x509.CertPool
}

func newTestCA(t *testing.T) *testCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "probe test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return &testCA{cert: cert, key: key, pool: pool}
}

// leaf issues a server certificate for probe.test and 127.0.0.1, valid
// from notBefore to notAfter.
func (ca *testCA) leaf(t *testing.T, notBefore, notAfter time.Time) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "probe.test"},
		DNSNames:     []string{"probe.test"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
		NotBefore:    notBefore,
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// tlsServer serves cert on 127.0.0.1 and returns the server and its port.
func tlsServer(t *testing.T, cert tls.Certificate) (*httptest.Server, float64) {
	t.Helper()
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv, float64(srv.Listener.Addr().(*net.TCPAddr).Port)
}

func loopbackOnly() *Guard { return NewGuardForTest(true, nil) }

func TestTLSCertValidAgainstInjectedRoots(t *testing.T) {
	ca := newTestCA(t)
	now := time.Now()
	_, port := tlsServer(t, ca.leaf(t, now.Add(-time.Hour), now.Add(10*24*time.Hour)))

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
			m := icmpByMetric(t, probeTLS(context.Background(), loopbackOnly(), tc.params, "127.0.0.1", ca.pool))
			if len(m) != 4 || m["tls.up"].Value == nil || *m["tls.up"].Value != 1 {
				t.Fatalf("want tls up with four metrics, got %+v", m)
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
	m := icmpByMetric(t, probeTLS(context.Background(), loopbackOnly(), map[string]any{"port": port, "server_name": "probe.test"}, "127.0.0.1", nil))
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
	ca := newTestCA(t)
	now := time.Now()
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	_ = srv.Listener.Close()
	srv.Listener = ln
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{ca.leaf(t, now.Add(-time.Hour), now.Add(24*time.Hour))}}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	port := float64(ln.Addr().(*net.TCPAddr).Port)
	m := icmpByMetric(t, probeTLS(context.Background(), loopbackOnly(), map[string]any{"port": port, "server_name": "{address}"}, "[::1]", ca.pool))
	if v := m["tls.up"].Value; v == nil || *v != 1 {
		t.Fatalf("tls.up = %v, want 1", m)
	}
	if v := m["tls.cert_valid"].Value; v == nil || *v != 1 {
		t.Fatalf("tls.cert_valid = %v, want 1: {address} must become ::1, unbracketed", v)
	}
}

func TestTLSExpiredLeafHasNegativeExpiry(t *testing.T) {
	ca := newTestCA(t)
	now := time.Now()
	_, port := tlsServer(t, ca.leaf(t, now.Add(-10*24*time.Hour), now.Add(-2*24*time.Hour)))
	m := icmpByMetric(t, probeTLS(context.Background(), loopbackOnly(), map[string]any{"port": port, "server_name": "probe.test"}, "127.0.0.1", ca.pool))
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

func TestHTTPVerifiesAgainstInjectedRoots(t *testing.T) {
	ca := newTestCA(t)
	now := time.Now()
	srv, _ := tlsServer(t, ca.leaf(t, now.Add(-time.Hour), now.Add(24*time.Hour)))
	params := map[string]any{"url": srv.URL, "timeout_s": float64(5)}
	m := icmpByMetric(t, probeHTTP(context.Background(), loopbackOnly(), params, "", ca.pool))
	if v := m["http.up"].Value; v == nil || *v != 1 {
		t.Fatalf("http.up = %v, want 1 with the CA in the roots", m)
	}
	if v := m["http.status_code"].Value; v == nil || *v != 200 {
		t.Fatalf("http.status_code = %v, want 200", v)
	}
}

func TestHTTPTransportDialsOnlyThroughTheGuard(t *testing.T) {
	var refused atomic.Bool
	tr := newHTTPTransport(NewGuard(nil, nil), true, nil, &refused)
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
// old LAN appliances read down, so the tls probe (which verifies the chain
// itself) and an http probe that does not verify accept TLS 1.0; an http
// probe that verifies keeps Go's default floor.
func TestOldTLSVersionsAreAccepted(t *testing.T) {
	if v := tlsProbeConfig("probe.test").MinVersion; v != tls.VersionTLS10 {
		t.Fatalf("tls probe MinVersion = %x, want TLS 1.0", v)
	}
	var refused atomic.Bool
	if v := newHTTPTransport(loopbackOnly(), false, nil, &refused).TLSClientConfig.MinVersion; v != tls.VersionTLS10 {
		t.Fatalf("http probe without verify_tls: MinVersion = %x, want TLS 1.0", v)
	}
	if v := newHTTPTransport(loopbackOnly(), true, nil, &refused).TLSClientConfig.MinVersion; v != 0 {
		t.Fatalf("http probe with verify_tls: MinVersion = %x, want Go's default", v)
	}
}
