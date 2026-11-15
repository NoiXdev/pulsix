package http

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/checkplugins"
	"github.com/noixdev/pulsix/internal/probe"
	"github.com/noixdev/pulsix/internal/probe/probetest"
)

func loopbackOnly() *probe.Guard { return probe.NewGuardForTest(true, nil) }

func TestHTTPVerifiesAgainstInjectedRoots(t *testing.T) {
	ca := probetest.NewCA(t)
	now := time.Now()
	srv, _ := probetest.TLSServer(t, ca.Leaf(t, now.Add(-time.Hour), now.Add(24*time.Hour)))
	params := map[string]any{"url": srv.URL, "timeout_s": float64(5)}
	m := probetest.ByMetric(t, probeHTTP(context.Background(), loopbackOnly(), NewJSONGate(1), params, "", nil, ca.Pool))
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

// TestRedirectPolicyCredentials: the credential header survives a hop
// only while every hop so far kept the first request's scheme, host and
// port; the host compares without case and a default port equals none.
func TestRedirectPolicyCredentials(t *testing.T) {
	req := func(raw string) *http.Request {
		r, err := http.NewRequest(http.MethodGet, raw, nil)
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("X-Api-Key", "k3y")
		return r
	}
	for _, tc := range []struct {
		name string
		via  []string
		next string
		keep bool
	}{
		{"same origin", []string{"http://example.com/a"}, "http://example.com/b", true},
		{"host case", []string{"http://Example.COM/a"}, "http://example.com/b", true},
		{"default http port", []string{"http://example.com/a"}, "http://example.com:80/b", true},
		{"default https port", []string{"https://example.com:443/a"}, "https://example.com/b", true},
		{"another port", []string{"http://example.com/a"}, "http://example.com:8080/b", false},
		{"another host", []string{"http://example.com/a"}, "http://example.org/b", false},
		{"a subdomain", []string{"http://example.com/a"}, "http://api.example.com/b", false},
		{"https to http", []string{"https://example.com/a"}, "http://example.com/b", false},
		{"http to https", []string{"http://example.com/a"}, "https://example.com/b", false},
		{"back after leaving", []string{"http://example.com/a", "http://example.org/b"}, "http://example.com/c", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			via := make([]*http.Request, len(tc.via))
			for i, u := range tc.via {
				via[i] = req(u)
			}
			next := req(tc.next)
			if err := redirectPolicy(true, "X-Api-Key")(next, via); err != nil {
				t.Fatal(err)
			}
			if kept := next.Header.Get("X-Api-Key") != ""; kept != tc.keep {
				t.Fatalf("header kept = %v, want %v", kept, tc.keep)
			}
		})
	}
}

// TestHTTPURLRefusesUserInfo: the format refuses user info already; the
// probe refuses it too, so no credential can come from the URL.
func TestHTTPURLRefusesUserInfo(t *testing.T) {
	for _, raw := range []string{"http://user:pass@example.com/", "http://user@example.com/"} {
		if _, err := httpURL(raw, ""); err == nil {
			t.Fatalf("httpURL(%q) accepted user info", raw)
		}
	}
}

// TestJSONValuesBoundMatchesTheDescriptor: the probe reads as many
// json_values entries as the descriptor accepts.
func TestJSONValuesBoundMatchesTheDescriptor(t *testing.T) {
	d, _ := checkplugins.Get("http")
	for _, p := range d.Params {
		if p.Name == "json_values" {
			if p.MaxEntries != maxJSONValues {
				t.Fatalf("descriptor allows %d json_values, the probe reads %d", p.MaxEntries, maxJSONValues)
			}
			return
		}
	}
	t.Fatal("the http descriptor has no json_values")
}
