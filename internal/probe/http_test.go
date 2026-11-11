package probe_test

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/probe"
)

func httpParams(url string, extra map[string]any) map[string]any {
	p := map[string]any{"url": url, "timeout_s": float64(5)}
	for k, v := range extra {
		p[k] = v
	}
	return p
}

// redirectChain serves /r/N as a 302 to /r/N-1, and /r/0 as 200.
func redirectChain(w http.ResponseWriter, r *http.Request) {
	n, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/r/"))
	if err != nil || n <= 0 {
		_, _ = w.Write([]byte("done"))
		return
	}
	http.Redirect(w, r, "/r/"+strconv.Itoa(n-1), http.StatusFound)
}

// ipv6Server starts handler on [::1], or skips when IPv6 loopback is missing.
func ipv6Server(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	ln, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Skipf("no IPv6 loopback: %v", err)
	}
	srv := httptest.NewUnstartedServer(handler)
	_ = srv.Listener.Close()
	srv.Listener = ln
	srv.Start()
	t.Cleanup(srv.Close)
	return srv
}

func TestHTTPUpWithBodyMatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok healthy"))
	}))
	defer srv.Close()
	obs := probe.Run(context.Background(), loopbackGuard(), "http", httpParams(srv.URL, map[string]any{"body_contains": "healthy"}), "")
	m := byMetric(t, obs)
	if len(m) != 4 {
		t.Fatalf("want up, status_code, response_ms and body_match, got %+v", obs)
	}
	if v := wantValue(t, m, "http.up"); v != 1 {
		t.Fatalf("http.up = %v, want 1", v)
	}
	if v := wantValue(t, m, "http.status_code"); v != 200 {
		t.Fatalf("http.status_code = %v, want 200", v)
	}
	if v := wantValue(t, m, "http.response_ms"); v < 0 {
		t.Fatalf("http.response_ms = %v, want >= 0", v)
	}
	if v := wantValue(t, m, "http.body_match"); v != 1 {
		t.Fatalf("http.body_match = %v, want 1", v)
	}
}

func TestHTTPBodyMismatchAndNoBodyContains(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok healthy"))
	}))
	defer srv.Close()
	obs := probe.Run(context.Background(), loopbackGuard(), "http", httpParams(srv.URL, map[string]any{"body_contains": "sick"}), "")
	if v := wantValue(t, byMetric(t, obs), "http.body_match"); v != 0 {
		t.Fatalf("http.body_match = %v, want 0", v)
	}

	obs = probe.Run(context.Background(), loopbackGuard(), "http", httpParams(srv.URL, nil), "")
	m := byMetric(t, obs)
	if _, ok := m["http.body_match"]; ok || len(m) != 3 {
		t.Fatalf("want up, status_code and response_ms only, got %+v", obs)
	}
}

func TestHTTPServerErrorIsUp(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	m := byMetric(t, probe.Run(context.Background(), loopbackGuard(), "http", httpParams(srv.URL, nil), ""))
	if v := wantValue(t, m, "http.up"); v != 1 {
		t.Fatalf("http.up = %v, want 1", v)
	}
	if v := wantValue(t, m, "http.status_code"); v != 503 {
		t.Fatalf("http.status_code = %v, want 503", v)
	}
}

func TestHTTPMethod(t *testing.T) {
	methods := make(chan string, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		methods <- r.Method
	}))
	defer srv.Close()
	for _, tc := range []struct {
		params map[string]any
		want   string
	}{
		{nil, http.MethodGet},
		{map[string]any{"method": "HEAD"}, http.MethodHead},
	} {
		obs := probe.Run(context.Background(), loopbackGuard(), "http", httpParams(srv.URL, tc.params), "")
		if v := wantValue(t, byMetric(t, obs), "http.up"); v != 1 {
			t.Fatalf("http.up = %v, want 1", v)
		}
		if got := <-methods; got != tc.want {
			t.Fatalf("server saw %s, want %s", got, tc.want)
		}
	}
}

func TestHTTPSubstitutesAddress(t *testing.T) {
	handler := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {})
	v4 := httptest.NewServer(handler)
	defer v4.Close()
	port4 := v4.Listener.Addr().(*net.TCPAddr).Port
	obs := probe.Run(context.Background(), loopbackGuard(), "http", httpParams(fmt.Sprintf("http://{address}:%d/", port4), nil), "127.0.0.1")
	if v := wantValue(t, byMetric(t, obs), "http.up"); v != 1 {
		t.Fatalf("ipv4: http.up = %v, want 1", v)
	}

	v6 := ipv6Server(t, handler)
	port6 := v6.Listener.Addr().(*net.TCPAddr).Port
	for _, addr := range []string{"::1", "[::1]"} {
		t.Run(addr, func(t *testing.T) {
			obs := probe.Run(context.Background(), loopbackGuard(), "http", httpParams(fmt.Sprintf("http://{address}:%d/", port6), nil), addr)
			if v := wantValue(t, byMetric(t, obs), "http.up"); v != 1 {
				t.Fatalf("http.up = %v, want 1", v)
			}
		})
	}
}

func TestHTTPEmptyAddressIsAnErrorOnlyWhenNeeded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	defer srv.Close()
	obs := probe.Run(context.Background(), loopbackGuard(), "http", httpParams("http://{address}/", map[string]any{"body_contains": "x"}), "")
	wantErrors(t, obs, "host has no address", "http.up", "http.status_code", "http.response_ms", "http.body_match")

	obs = probe.Run(context.Background(), loopbackGuard(), "http", httpParams(srv.URL, nil), "")
	if v := wantValue(t, byMetric(t, obs), "http.up"); v != 1 {
		t.Fatalf("a fixed url needs no address: http.up = %v, want 1", v)
	}
}

func TestHTTPUnparsableURLIsAnError(t *testing.T) {
	for _, u := range []string{"", "not a url", "ftp://example.com/", "http://", "http://exa mple.com/"} {
		t.Run(u, func(t *testing.T) {
			obs := probe.Run(context.Background(), loopbackGuard(), "http", httpParams(u, nil), "127.0.0.1")
			m := byMetric(t, obs)
			if len(m) != 3 || m["http.up"].Err == "" || m["http.status_code"].Err == "" || m["http.response_ms"].Err == "" {
				t.Fatalf("want one error per metric, got %+v", obs)
			}
		})
	}
}

func TestHTTPRefusedTargetIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	defer srv.Close()
	port := srv.Listener.Addr().(*net.TCPAddr).Port
	for _, u := range []string{srv.URL, fmt.Sprintf("http://localhost:%d/", port)} {
		t.Run(u, func(t *testing.T) {
			obs := probe.Run(context.Background(), probe.NewGuard(nil, nil), "http", httpParams(u, map[string]any{"body_contains": "x"}), "")
			wantErrors(t, obs, "target address not allowed", "http.up", "http.status_code", "http.response_ms", "http.body_match")
		})
	}
}

func TestHTTPRefusedRedirectIsAnError(t *testing.T) {
	// The first hop is allowed; the redirect target is refused at dial time,
	// which must read as a collection error, never as up = 0.
	t.Run("link-local", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "http://169.254.169.254/latest/meta-data/", http.StatusFound)
		}))
		defer srv.Close()
		obs := probe.Run(context.Background(), loopbackGuard(), "http", httpParams(srv.URL, nil), "")
		wantErrors(t, obs, "target address not allowed", "http.up", "http.status_code", "http.response_ms")
	})
	t.Run("denied loopback port", func(t *testing.T) {
		target := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
		defer target.Close()
		srv := ipv6Server(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, target.URL+"/", http.StatusFound)
		}))
		g := probe.NewGuardForTest(true, []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")})
		obs := probe.Run(context.Background(), g, "http", httpParams(srv.URL, map[string]any{"body_contains": "x"}), "")
		wantErrors(t, obs, "target address not allowed", "http.up", "http.status_code", "http.response_ms", "http.body_match")
	})
}

func TestHTTPRedirects(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(redirectChain))
	defer srv.Close()

	m := byMetric(t, probe.Run(context.Background(), loopbackGuard(), "http", httpParams(srv.URL+"/r/10", nil), ""))
	if v := wantValue(t, m, "http.status_code"); v != 200 {
		t.Fatalf("10 redirects: http.status_code = %v, want 200", v)
	}

	m = byMetric(t, probe.Run(context.Background(), loopbackGuard(), "http", httpParams(srv.URL+"/r/1", map[string]any{"follow_redirects": false}), ""))
	if v := wantValue(t, m, "http.up"); v != 1 {
		t.Fatalf("not following: http.up = %v, want 1", v)
	}
	if v := wantValue(t, m, "http.status_code"); v != 302 {
		t.Fatalf("not following: http.status_code = %v, want 302", v)
	}

	obs := probe.Run(context.Background(), loopbackGuard(), "http", httpParams(srv.URL+"/r/11", map[string]any{"body_contains": "done"}), "")
	wantOnly(t, obs, "http.up", 0)
}

func TestHTTPSelfSignedCertificate(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	defer srv.Close()

	obs := probe.Run(context.Background(), loopbackGuard(), "http", httpParams(srv.URL, map[string]any{"body_contains": "x"}), "")
	wantOnly(t, obs, "http.up", 0)

	m := byMetric(t, probe.Run(context.Background(), loopbackGuard(), "http", httpParams(srv.URL, map[string]any{"verify_tls": false}), ""))
	if v := wantValue(t, m, "http.up"); v != 1 {
		t.Fatalf("verify_tls false: http.up = %v, want 1", v)
	}
	if v := wantValue(t, m, "http.status_code"); v != 200 {
		t.Fatalf("verify_tls false: http.status_code = %v, want 200", v)
	}
}

func TestHTTPBodyReadIsCapped(t *testing.T) {
	const mib = 1 << 20
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		filler := strings.Repeat("a", 32<<10)
		switch r.URL.Path {
		case "/within":
			_, _ = w.Write([]byte(strings.Repeat("a", mib-6) + "needle"))
		case "/past":
			_, _ = w.Write([]byte(strings.Repeat("a", mib) + "needle"))
		case "/endless":
			// Until the probe hangs up.
			for {
				if _, err := w.Write([]byte(filler)); err != nil {
					return
				}
			}
		}
	}))
	defer srv.Close()

	for _, tc := range []struct {
		path string
		want float64
	}{{"/within", 1}, {"/past", 0}, {"/endless", 0}} {
		t.Run(tc.path, func(t *testing.T) {
			start := time.Now()
			obs := probe.Run(context.Background(), loopbackGuard(), "http", httpParams(srv.URL+tc.path, map[string]any{"body_contains": "needle"}), "")
			if d := time.Since(start); d > 3*time.Second {
				t.Fatalf("probe took %v: the body read is not capped", d)
			}
			m := byMetric(t, obs)
			if v := wantValue(t, m, "http.up"); v != 1 {
				t.Fatalf("http.up = %v, want 1", v)
			}
			if v := wantValue(t, m, "http.body_match"); v != tc.want {
				t.Fatalf("http.body_match = %v, want %v", v, tc.want)
			}
		})
	}
}

func TestHTTPConnectionRefusedIsDown(t *testing.T) {
	obs := probe.Run(context.Background(), loopbackGuard(), "http",
		httpParams(fmt.Sprintf("http://127.0.0.1:%d/", closedPort(t)), map[string]any{"body_contains": "x"}), "")
	wantOnly(t, obs, "http.up", 0)
}

func TestHTTPHonoursContextDeadline(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	obs := probe.Run(ctx, loopbackGuard(), "http", httpParams(srv.URL, map[string]any{"timeout_s": float64(30)}), "")
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("probe took %v, beyond the context deadline", d)
	}
	wantOnly(t, obs, "http.up", 0)
}
