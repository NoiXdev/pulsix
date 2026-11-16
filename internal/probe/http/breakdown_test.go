package http_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/probe"
	"github.com/noixdev/pulsix/internal/probe/probetest"
)

const (
	keptBytes    = 1 << 20
	countedBytes = 16 << 20
)

// wantAbsent fails when any of metrics was reported.
func wantAbsent(t *testing.T, m map[string]probe.Observation, metrics ...string) {
	t.Helper()
	for _, metric := range metrics {
		if o, ok := m[metric]; ok {
			t.Fatalf("%s = %+v, want absent", metric, o)
		}
	}
}

// wantPositive fails unless metric is a value above zero: its hooks
// fired (a loopback connect or lookup still takes microseconds).
func wantPositive(t *testing.T, m map[string]probe.Observation, metric string) {
	t.Helper()
	if v := probetest.WantValue(t, m, metric); v <= 0 {
		t.Fatalf("%s = %v, want above 0", metric, v)
	}
}

// wantAtLeast fails unless metric is a value of at least floor.
func wantAtLeast(t *testing.T, m map[string]probe.Observation, metric string, floor float64) float64 {
	t.Helper()
	v := probetest.WantValue(t, m, metric)
	if v < floor {
		t.Fatalf("%s = %v, want at least %v", metric, v, floor)
	}
	return v
}

// TestHTTPBreakdownOverPlainHTTP: an IP literal over http reports no
// lookup, no handshake and no certificate; the server's delay shows in
// ttfb_ms and the body's in content_bytes.
func TestHTTPBreakdownOverPlainHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(150 * time.Millisecond)
		_, _ = w.Write([]byte("hello world"))
	}))
	defer srv.Close()
	m := probetest.WantUp(t, run(context.Background(), probetest.LoopbackGuard(), httpParams(srv.URL, nil), ""), "http")

	wantAbsent(t, m, "http.dns_ms", "http.tls_ms", "http.cert_expiry_days", "http.cert_issuer")
	wantPositive(t, m, "http.connect_ms")
	ttfb := wantAtLeast(t, m, "http.ttfb_ms", 150)
	transfer := wantAtLeast(t, m, "http.transfer_ms", 0)
	if r := probetest.WantValue(t, m, "http.response_ms"); r < ttfb+transfer {
		t.Fatalf("http.response_ms = %v, want at least ttfb %v + transfer %v", r, ttfb, transfer)
	}
	if v := probetest.WantValue(t, m, "http.redirects"); v != 0 {
		t.Fatalf("http.redirects = %v, want 0", v)
	}
	if v := probetest.WantValue(t, m, "http.content_bytes"); v != float64(len("hello world")) {
		t.Fatalf("http.content_bytes = %v, want %d", v, len("hello world"))
	}
}

// TestHTTPBreakdownResolvesAName: a URL with a name reports the
// lookup.
func TestHTTPBreakdownResolvesAName(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()
	port := srv.Listener.Addr().(*net.TCPAddr).Port
	m := probetest.WantUp(t, run(context.Background(), probetest.LoopbackGuard(), httpParams(fmt.Sprintf("http://localhost:%d/", port), nil), ""), "http")
	wantPositive(t, m, "http.dns_ms")
	wantPositive(t, m, "http.connect_ms")
	wantAbsent(t, m, "http.tls_ms")
}

// TestHTTPBreakdownSlowBody: a body that arrives slowly shows in
// transfer_ms, not in ttfb_ms.
func TestHTTPBreakdownSlowBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("first "))
		w.(http.Flusher).Flush()
		time.Sleep(200 * time.Millisecond)
		_, _ = w.Write([]byte("second"))
	}))
	defer srv.Close()
	m := probetest.WantUp(t, run(context.Background(), probetest.LoopbackGuard(), httpParams(srv.URL, nil), ""), "http")
	wantAtLeast(t, m, "http.transfer_ms", 200)
	if v := probetest.WantValue(t, m, "http.ttfb_ms"); v >= 200 {
		t.Fatalf("http.ttfb_ms = %v, want the first byte before the pause", v)
	}
	if v := probetest.WantValue(t, m, "http.content_bytes"); v != 12 {
		t.Fatalf("http.content_bytes = %v, want 12", v)
	}
}

// TestHTTPBodyReadStaysUnderTheTimeout: a body that never ends slowly is
// read only until the check's timeout; the response arrived, so the
// attempt is up with what was received.
func TestHTTPBodyReadStaysUnderTheTimeout(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("partial"))
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)

	start := time.Now()
	obs := run(context.Background(), probetest.LoopbackGuard(), httpParams(srv.URL, map[string]any{"timeout_s": 0.5, "body_contains": "partial"}), "")
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("the probe took %v, want it bound by the 0.5 s timeout", d)
	}
	m := probetest.WantUp(t, obs, "http")
	if v := probetest.WantValue(t, m, "http.content_bytes"); v != 7 {
		t.Fatalf("http.content_bytes = %v, want the 7 bytes received", v)
	}
	if v := probetest.WantValue(t, m, "http.body_match"); v != 1 {
		t.Fatalf("http.body_match = %v, want 1 on what arrived", v)
	}
	wantAtLeast(t, m, "http.transfer_ms", 300)
}

// TestHTTPRedirectsAreCounted: each redirect followed counts; a probe that
// does not follow reports the 3xx and none.
func TestHTTPRedirectsAreCounted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(redirectChain))
	defer srv.Close()
	m := probetest.WantUp(t, run(context.Background(), probetest.LoopbackGuard(), httpParams(srv.URL+"/r/3", nil), ""), "http")
	if v := probetest.WantValue(t, m, "http.redirects"); v != 3 {
		t.Fatalf("http.redirects = %v, want 3", v)
	}
	if v := probetest.WantValue(t, m, "http.content_bytes"); v != float64(len("done")) {
		t.Fatalf("http.content_bytes = %v, want the last hop's body", v)
	}

	m = probetest.WantUp(t, run(context.Background(), probetest.LoopbackGuard(), httpParams(srv.URL+"/r/3", map[string]any{"follow_redirects": false}), ""), "http")
	if v := probetest.WantValue(t, m, "http.redirects"); v != 0 {
		t.Fatalf("http.redirects = %v, want 0 without following", v)
	}
	if v := probetest.WantValue(t, m, "http.status_code"); v != http.StatusFound {
		t.Fatalf("http.status_code = %v, want 302", v)
	}
}

// TestHTTPHeadReadsNoBody: a HEAD response has no body to transfer.
func TestHTTPHeadReadsNoBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("a body HEAD never gets"))
	}))
	defer srv.Close()
	m := probetest.WantUp(t, run(context.Background(), probetest.LoopbackGuard(), httpParams(srv.URL, map[string]any{"method": "HEAD"}), ""), "http")
	if v := probetest.WantValue(t, m, "http.transfer_ms"); v != 0 {
		t.Fatalf("http.transfer_ms = %v, want 0 for HEAD", v)
	}
	if v := probetest.WantValue(t, m, "http.content_bytes"); v != 0 {
		t.Fatalf("http.content_bytes = %v, want 0 for HEAD", v)
	}
}

// TestHTTPContentBytesCountsTo16MiB: a body of exactly 16 MiB is counted;
// one byte more makes content_bytes an error and nothing else, the first
// 1 MiB still read for body_contains.
func TestHTTPContentBytesCountsTo16MiB(t *testing.T) {
	head := []byte("<html>marker")
	serve := func(n int) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write(head)
			_, _ = io.Copy(w, io.LimitReader(zeros{}, int64(n-len(head))))
		}))
	}

	exact := serve(countedBytes)
	defer exact.Close()
	m := probetest.WantUp(t, run(context.Background(), probetest.LoopbackGuard(), httpParams(exact.URL, map[string]any{"body_contains": "marker"}), ""), "http")
	if v := probetest.WantValue(t, m, "http.content_bytes"); v != countedBytes {
		t.Fatalf("http.content_bytes = %v, want %d", v, countedBytes)
	}

	over := serve(countedBytes + 1)
	defer over.Close()
	m = probetest.WantUp(t, run(context.Background(), probetest.LoopbackGuard(), httpParams(over.URL, map[string]any{"body_contains": "marker"}), ""), "http")
	if e := m["http.content_bytes"].Err; e != "larger than 16 MiB" {
		t.Fatalf("http.content_bytes = %+v, want the error %q", m["http.content_bytes"], "larger than 16 MiB")
	}
	for metric, o := range m {
		if metric != "http.content_bytes" && o.Err != "" {
			t.Fatalf("%s = %+v, want no error", metric, o)
		}
	}
	if v := probetest.WantValue(t, m, "http.body_match"); v != 1 {
		t.Fatalf("http.body_match = %v, want 1 from the first 1 MiB", v)
	}
}

// TestHTTPEndlessBodyStopsAt16MiB: a body that never ends is read only to
// the count, well before the timeout.
func TestHTTPEndlessBodyStopsAt16MiB(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := bytes.Repeat([]byte("x"), 64*1024)
		for r.Context().Err() == nil {
			if _, err := w.Write(buf); err != nil {
				return
			}
		}
	}))
	defer srv.Close()
	start := time.Now()
	m := probetest.WantUp(t, run(context.Background(), probetest.LoopbackGuard(), httpParams(srv.URL, map[string]any{"timeout_s": float64(30)}), ""), "http")
	if d := time.Since(start); d > 20*time.Second {
		t.Fatalf("the probe took %v, want it to stop at 16 MiB", d)
	}
	if e := m["http.content_bytes"].Err; e != "larger than 16 MiB" {
		t.Fatalf("http.content_bytes = %+v, want the error", m["http.content_bytes"])
	}
}

// TestHTTPFailedAttemptReportsNoBreakdown: a down attempt reports the
// reason and none of the breakdown.
func TestHTTPFailedAttemptReportsNoBreakdown(t *testing.T) {
	obs := run(context.Background(), probetest.LoopbackGuard(), httpParams(fmt.Sprintf("http://127.0.0.1:%d/", probetest.ClosedPort(t)), nil), "")
	probetest.WantDown(t, obs, "http", probe.FailureRefused)
	for _, o := range obs {
		if !strings.HasSuffix(o.Metric, ".up") && !strings.HasSuffix(o.Metric, ".failure") {
			t.Fatalf("a down attempt reported %+v", o)
		}
	}
}

// zeros is an endless reader of zero bytes.
type zeros struct{}

func (zeros) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}
