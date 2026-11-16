package http

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/probe/probetest"
)

// slowTLSServer serves handler over TLS with cert, each handshake taking
// at least delay.
func slowTLSServer(t *testing.T, cert tls.Certificate, delay time.Duration, handler http.Handler) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(handler)
	srv.TLS = &tls.Config{
		Certificates: []tls.Certificate{cert},
		GetConfigForClient: func(*tls.ClientHelloInfo) (*tls.Config, error) {
			time.Sleep(delay)
			return nil, nil
		},
	}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv
}

// TestHTTPSBreakdownSumsHandshakesOverHops: two redirects over https make
// three handshakes, each at least 60 ms; tls_ms adds them up, and the
// final hop's leaf gives the certificate metrics.
func TestHTTPSBreakdownSumsHandshakesOverHops(t *testing.T) {
	ca := probetest.NewCA(t)
	now := time.Now()
	srv := slowTLSServer(t, ca.Leaf(t, now.Add(-time.Hour), now.Add(48*time.Hour)), 60*time.Millisecond, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/r/"))
		if n > 0 {
			http.Redirect(w, r, "/r/"+strconv.Itoa(n-1), http.StatusFound)
			return
		}
		_, _ = w.Write([]byte("secure"))
	}))
	params := map[string]any{"url": srv.URL + "/r/2", "timeout_s": float64(5)}
	m := probetest.WantUp(t, probeHTTP(context.Background(), loopbackOnly(), NewJSONGate(1), params, "", nil, ca.Pool), "http")

	if v := probetest.WantValue(t, m, "http.tls_ms"); v < 3*60 {
		t.Fatalf("http.tls_ms = %v, want at least three 60 ms handshakes", v)
	}
	if v := probetest.WantValue(t, m, "http.redirects"); v != 2 {
		t.Fatalf("http.redirects = %v, want 2", v)
	}
	if v := probetest.WantValue(t, m, "http.cert_expiry_days"); v < 1.99 || v > 2 {
		t.Fatalf("http.cert_expiry_days = %v, want just under 2", v)
	}
	if v := probetest.WantValue(t, m, "http.connect_ms"); v <= 0 {
		t.Fatalf("http.connect_ms = %v, want the three connects", v)
	}
	if l := probetest.WantLabel(t, m, "http.cert_issuer"); l != "probe test CA" {
		t.Fatalf("http.cert_issuer = %q, want the CA's common name", l)
	}
	if _, ok := m["http.dns_ms"]; ok {
		t.Fatalf("http.dns_ms = %+v, want absent for an IP literal", m["http.dns_ms"])
	}
}

// TestHTTPSExpiredCertificateWithoutVerification: verify_tls false still
// reports the expiry, below zero once expired.
func TestHTTPSExpiredCertificateWithoutVerification(t *testing.T) {
	ca := probetest.NewCA(t)
	now := time.Now()
	srv := slowTLSServer(t, ca.Leaf(t, now.Add(-48*time.Hour), now.Add(-24*time.Hour)), 0, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	params := map[string]any{"url": srv.URL, "timeout_s": float64(5), "verify_tls": false}
	m := probetest.WantUp(t, probeHTTP(context.Background(), loopbackOnly(), NewJSONGate(1), params, "", nil, nil), "http")
	if v := probetest.WantValue(t, m, "http.cert_expiry_days"); v > -1 || v < -1.01 {
		t.Fatalf("http.cert_expiry_days = %v, want just past -1", v)
	}
}

// TestHTTPSRedirectToHTTPHasNoCertificate: the certificate metrics are the
// final hop's, so an https hop that redirects to http reports its
// handshake (at least 40 ms here) but no certificate.
func TestHTTPSRedirectToHTTPHasNoCertificate(t *testing.T) {
	ca := probetest.NewCA(t)
	now := time.Now()
	plain := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(plain.Close)
	secure := slowTLSServer(t, ca.Leaf(t, now.Add(-time.Hour), now.Add(time.Hour)), 40*time.Millisecond, http.RedirectHandler(plain.URL, http.StatusFound))
	params := map[string]any{"url": secure.URL, "timeout_s": float64(5)}
	m := probetest.WantUp(t, probeHTTP(context.Background(), loopbackOnly(), NewJSONGate(1), params, "", nil, ca.Pool), "http")
	if v := probetest.WantValue(t, m, "http.tls_ms"); v < 40 {
		t.Fatalf("http.tls_ms = %v, want the first hop's 40 ms handshake", v)
	}
	for _, absent := range []string{"http.cert_expiry_days", "http.cert_issuer"} {
		if o, ok := m[absent]; ok {
			t.Fatalf("%s = %+v, want absent after a redirect to http", absent, o)
		}
	}
}
