// Package probetest holds the helpers the probe packages share in their
// tests: a loopback guard, observation assertions, a closed port and a
// throwaway certificate authority.
package probetest

import (
	"bufio"
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
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/probe"
)

// LoopbackGuard allows loopback so tests can reach their own listeners; every
// other always-refused range stays refused.
func LoopbackGuard() *probe.Guard { return probe.NewGuardForTest(true, nil) }

// ByMetric indexes observations by metric and fails on a duplicate or on an
// observation that carries both a value and an error.
func ByMetric(t *testing.T, obs []probe.Observation) map[string]probe.Observation {
	t.Helper()
	m := make(map[string]probe.Observation, len(obs))
	for _, o := range obs {
		if _, dup := m[o.Metric]; dup {
			t.Fatalf("metric %q reported twice: %+v", o.Metric, obs)
		}
		if (o.Value == nil) == (o.Err == "") {
			t.Fatalf("observation %q must carry a value xor an error: %+v", o.Metric, o)
		}
		m[o.Metric] = o
	}
	return m
}

// WantValue fails unless metric is present with a value, and returns it.
func WantValue(t *testing.T, m map[string]probe.Observation, metric string) float64 {
	t.Helper()
	o, ok := m[metric]
	if !ok {
		t.Fatalf("metric %q missing from %+v", metric, m)
	}
	if o.Value == nil {
		t.Fatalf("metric %q has error %q, want a value", metric, o.Err)
	}
	return *o.Value
}

// WantOnly fails unless the observations are exactly the given metric with
// the given value.
func WantOnly(t *testing.T, obs []probe.Observation, metric string, value float64) {
	t.Helper()
	m := ByMetric(t, obs)
	if len(m) != 1 {
		t.Fatalf("want only %s=%v, got %+v", metric, value, obs)
	}
	if got := WantValue(t, m, metric); got != value {
		t.Fatalf("%s = %v, want %v", metric, got, value)
	}
}

// WantErrors fails unless the observations are exactly one error per metric,
// each carrying msg, and returns nothing else.
func WantErrors(t *testing.T, obs []probe.Observation, msg string, metrics ...string) {
	t.Helper()
	m := ByMetric(t, obs)
	if len(m) != len(metrics) {
		t.Fatalf("want errors for %v, got %+v", metrics, obs)
	}
	for _, metric := range metrics {
		o, ok := m[metric]
		if !ok {
			t.Fatalf("metric %q missing from %+v", metric, obs)
		}
		if o.Err != msg {
			t.Fatalf("metric %q: err %q, value %v; want err %q", metric, o.Err, o.Value, msg)
		}
	}
}

// ClosedPort returns a loopback port that nothing listens on.
func ClosedPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}

// CA is a throwaway certificate authority for a probe's injected root pool.
type CA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	Pool *x509.CertPool
}

// NewCA returns a fresh CA.
func NewCA(t *testing.T) *CA {
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
	return &CA{cert: cert, key: key, Pool: pool}
}

// Leaf issues a server certificate for probe.test and 127.0.0.1, valid
// from notBefore to notAfter.
func (ca *CA) Leaf(t *testing.T, notBefore, notAfter time.Time) tls.Certificate {
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

// TLSServer serves cert on 127.0.0.1 and returns the server and its port.
func TLSServer(t *testing.T, cert tls.Certificate) (*httptest.Server, float64) {
	t.Helper()
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv, float64(srv.Listener.Addr().(*net.TCPAddr).Port)
}

// TCPServer listens on network/addr, writes banner to every connection and
// delivers the first line each client sends on the returned channel.
func TCPServer(t *testing.T, network, addr, banner string) (port int, received <-chan string) {
	t.Helper()
	ln, err := net.Listen(network, addr)
	if err != nil {
		t.Skipf("listen %s %s: %v", network, addr, err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	got := make(chan string, 8)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				if banner != "" {
					_, _ = conn.Write([]byte(banner))
				}
				_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
				line, err := bufio.NewReader(conn).ReadString('\n')
				if err == nil {
					got <- line
				}
			}()
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port, got
}
