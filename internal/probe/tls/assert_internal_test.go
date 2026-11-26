package tls

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/probe"
	"github.com/noixdev/pulsix/internal/probe/judge"
	"github.com/noixdev/pulsix/internal/probe/probetest"
)

func entries(list ...string) []any {
	out := make([]any, len(list))
	for i, e := range list {
		out[i] = e
	}
	return out
}

// verdicts are the assertion items' observations: "item metric" → label,
// or "!error".
func verdicts(obs []probe.Observation) map[string]string {
	out := map[string]string{}
	for _, o := range obs {
		if o.Item == "" {
			continue
		}
		switch {
		case o.Err != "":
			out[o.Item+" "+o.Metric] = "!" + o.Err
		case o.Label != nil:
			out[o.Item+" "+o.Metric] = *o.Label
		}
	}
	return out
}

// TestTLSAssertions (backlog 3.87): the handshake and the leaf
// certificate's fields, judged against the injected roots.
func TestTLSAssertions(t *testing.T) {
	ca := probetest.NewCA(t)
	now := time.Now()
	_, port := probetest.TLSServer(t, ca.Leaf(t, now.Add(-time.Hour), now.Add(10*24*time.Hour)))
	params := map[string]any{"port": port, "server_name": "probe.test", "assertions": entries(
		"h=handshake_ms < 5000",
		"v=cert_valid == 1",
		"d=cert_days > 30 @warn",
		`i=cert_issuer == "probe test CA"`,
		`s=cert_subject == "probe.test"`,
		`n=cert_sans == "probe.test, 127.0.0.1, ::1"`,
		"p=version >= 1.2",
		`c=cipher matches "^TLS_"`,
		"x=status == 200",
	)}
	obs := probeTLS(context.Background(), loopbackOnly(), params, "127.0.0.1", ca.Pool)
	probetest.WantUp(t, obs[:6], "tls")
	got := verdicts(obs)
	want := map[string]string{
		"h tls.assertion": "pass",
		"v tls.assertion": "pass", "v tls.assertion_actual": "1",
		"d tls.assertion": "warn", "d tls.assertion_actual": "10",
		"i tls.assertion": "pass", "i tls.assertion_actual": `"probe test CA"`,
		"s tls.assertion": "pass", "s tls.assertion_actual": `"probe.test"`,
		"n tls.assertion": "pass", "n tls.assertion_actual": `"probe.test, 127.0.0.1, ::1"`,
		"p tls.assertion": "pass", "p tls.assertion_actual": "1.3",
		"c tls.assertion": "pass",
		"x tls.assertion": "!" + judge.ErrInvalid,
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	if len(got) != 18 {
		t.Errorf("assertion observations = %v", got)
	}
}

// TestTLSVersionAssertion: a server capped at TLS 1.2 reads 1.2, and a
// cipher assertion sees the suite's name.
func TestTLSVersionAssertion(t *testing.T) {
	ca := probetest.NewCA(t)
	now := time.Now()
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	srv.TLS = &tls.Config{
		Certificates: []tls.Certificate{ca.Leaf(t, now.Add(-time.Hour), now.Add(24*time.Hour))},
		MaxVersion:   tls.VersionTLS12,
		CipherSuites: []uint16{tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256},
	}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	port := float64(srv.Listener.Addr().(*net.TCPAddr).Port)
	params := map[string]any{"port": port, "assertions": entries("p=version >= 1.3", `c=cipher == "TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256"`)}
	got := verdicts(probeTLS(context.Background(), loopbackOnly(), params, "127.0.0.1", ca.Pool))
	if got["p tls.assertion"] != "crit" || got["p tls.assertion_actual"] != "1.2" {
		t.Errorf("version = %v", got)
	}
	if got["c tls.assertion"] != "pass" {
		t.Errorf("cipher = %v", got)
	}
}

// TestTLSSubjectFields: a subject without a common name falls back to its
// organization, a leaf without SANs reads cert_sans absent.
func TestTLSSubjectFields(t *testing.T) {
	cert := &x509.Certificate{}
	cert.Subject.Organization = []string{"Acme Co"}
	if got := subjectLabel(cert); got != "Acme Co" {
		t.Errorf("subjectLabel = %q", got)
	}
	if got, ok := sansText(cert); ok || got != "" {
		t.Errorf("sansText(no SANs) = %q %v", got, ok)
	}
	for v, want := range map[uint16]float64{tls.VersionTLS10: 1.0, tls.VersionTLS11: 1.1, tls.VersionTLS12: 1.2, tls.VersionTLS13: 1.3} {
		if got, ok := versionNumber(v); !ok || got != want {
			t.Errorf("versionNumber(%x) = %v %v, want %v", v, got, ok, want)
		}
	}
	if _, ok := versionNumber(0x0300); ok {
		t.Error("versionNumber(SSL 3.0) = ok")
	}
}

// TestTLSAssertionsOnFailures: a failed handshake reports no assertion
// item; a refused target puts the error on both metrics of each.
func TestTLSAssertionsOnFailures(t *testing.T) {
	list := entries("v=cert_valid == 1")
	obs := probeTLS(context.Background(), loopbackOnly(), map[string]any{"port": float64(probetest.ClosedPort(t)), "assertions": list}, "127.0.0.1", nil)
	probetest.WantDown(t, obs, "tls", probe.FailureRefused)
	if got := verdicts(obs); len(got) != 0 {
		t.Errorf("down attempt reported %v", got)
	}
	ca := probetest.NewCA(t)
	_, port := probetest.TLSServer(t, ca.Leaf(t, time.Now().Add(-time.Hour), time.Now().Add(time.Hour)))
	obs = probeTLS(context.Background(), probe.NewGuard(nil, nil), map[string]any{"port": port, "assertions": list}, "127.0.0.1", nil)
	if got := verdicts(obs); got["v tls.assertion"] != "!target address not allowed" || got["v tls.assertion_actual"] != "!target address not allowed" {
		t.Errorf("refused attempt = %v", got)
	}
}
