package http_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
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
// transfer_ms, not in ttfb_ms. The 200 ms pause bounds response_ms
// exactly; transfer_ms is the difference of two client-side readings, the
// first taken on the transport's read goroutine, which may be scheduled
// late under load and move part of the pause into ttfb, so it gets a
// floor with slack.
func TestHTTPBreakdownSlowBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("first "))
		w.(http.Flusher).Flush()
		time.Sleep(200 * time.Millisecond)
		_, _ = w.Write([]byte("second"))
	}))
	defer srv.Close()
	m := probetest.WantUp(t, run(context.Background(), probetest.LoopbackGuard(), httpParams(srv.URL, nil), ""), "http")
	wantAtLeast(t, m, "http.response_ms", 200)
	wantAtLeast(t, m, "http.transfer_ms", 100)
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

// sizedServer serves a body of n bytes that starts with "<html>marker",
// with a Content-Length of declared when it is not 0 (the server lets a
// large body go out chunked, with none).
func sizedServer(t *testing.T, n, declared int) *httptest.Server {
	t.Helper()
	head := []byte("<html>marker")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if declared != 0 {
			w.Header().Set("Content-Length", strconv.Itoa(declared))
		}
		_, _ = w.Write(head)
		_, _ = io.Copy(w, io.LimitReader(zeros{}, int64(n-len(head))))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// wantNoError fails when any observation carries an error.
func wantNoError(t *testing.T, obs []probe.Observation) {
	t.Helper()
	for _, o := range obs {
		if o.Err != "" {
			t.Fatalf("%s %q = %+v, want no error", o.Metric, o.Item, o)
		}
	}
}

// wantUpWithSize fails unless the attempt was up and content_bytes is want.
func wantUpWithSize(t *testing.T, m map[mi]probe.Observation, want float64) {
	t.Helper()
	if o := m[mi{"http.up", ""}]; o.Value == nil || *o.Value != 1 {
		t.Fatalf("http.up = %+v, want 1", o)
	}
	if o := m[mi{"http.content_bytes", ""}]; o.Value == nil || *o.Value != want {
		t.Fatalf("http.content_bytes = %+v, want %v", o, want)
	}
}

// TestHTTPContentBytesWithoutASizeAssertion: without a size assertion the
// body is read to 1 MiB, as before content_bytes existed. Past that,
// content_bytes is the declared Content-Length, else 1 MiB, and never an
// error.
func TestHTTPContentBytesWithoutASizeAssertion(t *testing.T) {
	for _, tc := range []struct {
		name        string
		n, declared int
		want        float64
	}{
		{"exactly 1 MiB", keptBytes, 0, keptBytes},
		{"past 1 MiB, nothing declared", keptBytes + 1, 0, keptBytes},
		{"past 1 MiB, declared", 3 * keptBytes, 3 * keptBytes, 3 * keptBytes},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := sizedServer(t, tc.n, tc.declared)
			obs := run(context.Background(), probetest.LoopbackGuard(), httpParams(srv.URL, map[string]any{"body_contains": "marker"}), "")
			wantNoError(t, obs)
			m := probetest.WantUp(t, obs, "http")
			if v := probetest.WantValue(t, m, "http.content_bytes"); v != tc.want {
				t.Fatalf("http.content_bytes = %v, want %v", v, tc.want)
			}
			if v := probetest.WantValue(t, m, "http.body_match"); v != 1 {
				t.Fatalf("http.body_match = %v, want 1 from the first 1 MiB", v)
			}
		})
	}
}

// TestHTTPSizeAssertionCountsTo16MiB: a size assertion reads the body to
// 16 MiB to count it. Past that, the declared Content-Length is judged;
// without one the assertion, and nothing else, is an error, and
// content_bytes reports 16 MiB.
func TestHTTPSizeAssertionCountsTo16MiB(t *testing.T) {
	assertions := values("big=size >= "+strconv.Itoa(countedBytes), `hit=body contains "marker"`)

	exact := sizedServer(t, countedBytes, 0)
	obs := run(context.Background(), probetest.LoopbackGuard(), httpParams(exact.URL, map[string]any{"assertions": assertions}), "")
	wantNoError(t, obs)
	m := byItem(t, obs)
	wantUpWithSize(t, m, countedBytes)
	wantVerdict(t, m, "big", "pass", strconv.Itoa(countedBytes))
	wantVerdict(t, m, "hit", "pass", "")

	over := sizedServer(t, countedBytes+1, 0)
	m = byItem(t, run(context.Background(), probetest.LoopbackGuard(), httpParams(over.URL, map[string]any{"assertions": assertions}), ""))
	wantUpWithSize(t, m, countedBytes)
	wantAssertionError(t, m, "big", "the response is larger than 16 MiB")
	wantVerdict(t, m, "hit", "pass", "")
	for k, o := range m {
		if k.item != "big" && o.Err != "" {
			t.Fatalf("%v = %+v, want no error", k, o)
		}
	}

	declared := sizedServer(t, countedBytes+1, countedBytes+1)
	obs = run(context.Background(), probetest.LoopbackGuard(), httpParams(declared.URL, map[string]any{"assertions": assertions}), "")
	wantNoError(t, obs)
	m = byItem(t, obs)
	wantUpWithSize(t, m, countedBytes+1)
	wantVerdict(t, m, "big", "pass", strconv.Itoa(countedBytes+1))
}

// TestHTTPEndlessBodyStopsAtTheReadLimit: a body that never ends is read
// only to the limit, 1 MiB or with a size assertion 16 MiB, well before
// the timeout.
func TestHTTPEndlessBodyStopsAtTheReadLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := bytes.Repeat([]byte("x"), 64*1024)
		for r.Context().Err() == nil {
			if _, err := w.Write(buf); err != nil {
				return
			}
		}
	}))
	defer srv.Close()
	for _, tc := range []struct {
		name  string
		extra map[string]any
		want  float64
	}{
		{"no size assertion", map[string]any{}, keptBytes},
		{"a size assertion", map[string]any{"assertions": values("big=size > 1")}, countedBytes},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.extra["timeout_s"] = float64(30)
			start := time.Now()
			m := byItem(t, run(context.Background(), probetest.LoopbackGuard(), httpParams(srv.URL, tc.extra), ""))
			if d := time.Since(start); d > 20*time.Second {
				t.Fatalf("the probe took %v, want it to stop at the limit", d)
			}
			wantUpWithSize(t, m, tc.want)
		})
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
