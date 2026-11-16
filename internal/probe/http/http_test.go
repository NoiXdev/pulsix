package http_test

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/probe"
	"github.com/noixdev/pulsix/internal/probe/probetest"
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
	obs := run(context.Background(), probetest.LoopbackGuard(), httpParams(srv.URL, map[string]any{"body_contains": "healthy"}), "")
	m := probetest.WantUp(t, obs, "http")
	if len(m) != upHostMetrics+1 {
		t.Fatalf("want the host metrics and body_match, got %+v", obs)
	}
	if v := probetest.WantValue(t, m, "http.status_code"); v != 200 {
		t.Fatalf("http.status_code = %v, want 200", v)
	}
	if v := probetest.WantValue(t, m, "http.response_ms"); v < 0 {
		t.Fatalf("http.response_ms = %v, want >= 0", v)
	}
	if v := probetest.WantValue(t, m, "http.body_match"); v != 1 {
		t.Fatalf("http.body_match = %v, want 1", v)
	}
}

func TestHTTPBodyMismatchAndNoBodyContains(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok healthy"))
	}))
	defer srv.Close()
	obs := run(context.Background(), probetest.LoopbackGuard(), httpParams(srv.URL, map[string]any{"body_contains": "sick"}), "")
	if v := probetest.WantValue(t, probetest.ByMetric(t, obs), "http.body_match"); v != 0 {
		t.Fatalf("http.body_match = %v, want 0", v)
	}

	obs = run(context.Background(), probetest.LoopbackGuard(), httpParams(srv.URL, nil), "")
	m := probetest.ByMetric(t, obs)
	if _, ok := m["http.body_match"]; ok || len(m) != upHostMetrics {
		t.Fatalf("want the host metrics only, got %+v", obs)
	}
}

func TestHTTPServerErrorIsUp(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	// A status code is no failure: a response arrived.
	m := probetest.WantUp(t, run(context.Background(), probetest.LoopbackGuard(), httpParams(srv.URL, nil), ""), "http")
	if v := probetest.WantValue(t, m, "http.status_code"); v != 503 {
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
		obs := run(context.Background(), probetest.LoopbackGuard(), httpParams(srv.URL, tc.params), "")
		if v := probetest.WantValue(t, probetest.ByMetric(t, obs), "http.up"); v != 1 {
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
	obs := run(context.Background(), probetest.LoopbackGuard(), httpParams(fmt.Sprintf("http://{address}:%d/", port4), nil), "127.0.0.1")
	if v := probetest.WantValue(t, probetest.ByMetric(t, obs), "http.up"); v != 1 {
		t.Fatalf("ipv4: http.up = %v, want 1", v)
	}

	v6 := ipv6Server(t, handler)
	port6 := v6.Listener.Addr().(*net.TCPAddr).Port
	for _, addr := range []string{"::1", "[::1]"} {
		t.Run(addr, func(t *testing.T) {
			obs := run(context.Background(), probetest.LoopbackGuard(), httpParams(fmt.Sprintf("http://{address}:%d/", port6), nil), addr)
			if v := probetest.WantValue(t, probetest.ByMetric(t, obs), "http.up"); v != 1 {
				t.Fatalf("http.up = %v, want 1", v)
			}
		})
	}
}

func TestHTTPEmptyAddressIsAnErrorOnlyWhenNeeded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	defer srv.Close()
	obs := run(context.Background(), probetest.LoopbackGuard(), httpParams("http://{address}/", map[string]any{"body_contains": "x"}), "")
	probetest.WantErrors(t, obs, "host has no address", append(slices.Clone(failAllHostMetrics), "http.body_match")...)

	obs = run(context.Background(), probetest.LoopbackGuard(), httpParams(srv.URL, nil), "")
	if v := probetest.WantValue(t, probetest.ByMetric(t, obs), "http.up"); v != 1 {
		t.Fatalf("a fixed url needs no address: http.up = %v, want 1", v)
	}
}

func TestHTTPUnparsableURLIsAnError(t *testing.T) {
	for _, u := range []string{"", "not a url", "ftp://example.com/", "http://", "http://exa mple.com/"} {
		t.Run(u, func(t *testing.T) {
			obs := run(context.Background(), probetest.LoopbackGuard(), httpParams(u, nil), "127.0.0.1")
			m := probetest.ByMetric(t, obs)
			if len(m) != len(failAllHostMetrics) {
				t.Fatalf("want one error per metric, got %+v", obs)
			}
			for _, metric := range failAllHostMetrics {
				if m[metric].Err == "" {
					t.Fatalf("%s = %+v, want an error", metric, m[metric])
				}
			}
		})
	}
}

func TestHTTPRefusedTargetIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	defer srv.Close()
	port := srv.Listener.Addr().(*net.TCPAddr).Port
	for u, metrics := range map[string][]string{srv.URL: failAllHostMetrics, fmt.Sprintf("http://localhost:%d/", port): failAllNamedMetrics} {
		t.Run(u, func(t *testing.T) {
			obs := run(context.Background(), probe.NewGuard(nil, nil), httpParams(u, map[string]any{"body_contains": "x"}), "")
			probetest.WantErrors(t, obs, "target address not allowed", append(slices.Clone(metrics), "http.body_match")...)
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
		obs := run(context.Background(), probetest.LoopbackGuard(), httpParams(srv.URL, nil), "")
		probetest.WantErrors(t, obs, "target address not allowed", failAllHostMetrics...)
	})
	t.Run("denied loopback port", func(t *testing.T) {
		target := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
		defer target.Close()
		srv := ipv6Server(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, target.URL+"/", http.StatusFound)
		}))
		g := probe.NewGuardForTest(true, []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")})
		obs := run(context.Background(), g, httpParams(srv.URL, map[string]any{"body_contains": "x"}), "")
		probetest.WantErrors(t, obs, "target address not allowed", append(slices.Clone(failAllHostMetrics), "http.body_match")...)
	})
}

func TestHTTPRedirects(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(redirectChain))
	defer srv.Close()

	m := probetest.ByMetric(t, run(context.Background(), probetest.LoopbackGuard(), httpParams(srv.URL+"/r/10", nil), ""))
	if v := probetest.WantValue(t, m, "http.status_code"); v != 200 {
		t.Fatalf("10 redirects: http.status_code = %v, want 200", v)
	}

	m = probetest.ByMetric(t, run(context.Background(), probetest.LoopbackGuard(), httpParams(srv.URL+"/r/1", map[string]any{"follow_redirects": false}), ""))
	if v := probetest.WantValue(t, m, "http.up"); v != 1 {
		t.Fatalf("not following: http.up = %v, want 1", v)
	}
	if v := probetest.WantValue(t, m, "http.status_code"); v != 302 {
		t.Fatalf("not following: http.status_code = %v, want 302", v)
	}

	obs := run(context.Background(), probetest.LoopbackGuard(), httpParams(srv.URL+"/r/11", map[string]any{"body_contains": "done"}), "")
	probetest.WantDown(t, obs, "http", probe.FailureRedirects)
}

func TestHTTPSelfSignedCertificate(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	defer srv.Close()

	obs := run(context.Background(), probetest.LoopbackGuard(), httpParams(srv.URL, map[string]any{"body_contains": "x"}), "")
	probetest.WantDown(t, obs, "http", probe.FailureCertificate)

	m := probetest.WantUp(t, run(context.Background(), probetest.LoopbackGuard(), httpParams(srv.URL, map[string]any{"verify_tls": false}), ""), "http")
	if v := probetest.WantValue(t, m, "http.status_code"); v != 200 {
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
			obs := run(context.Background(), probetest.LoopbackGuard(), httpParams(srv.URL+tc.path, map[string]any{"body_contains": "needle"}), "")
			if d := time.Since(start); d > 3*time.Second {
				t.Fatalf("probe took %v: the body read is not capped", d)
			}
			m := probetest.ByMetric(t, obs)
			if v := probetest.WantValue(t, m, "http.up"); v != 1 {
				t.Fatalf("http.up = %v, want 1", v)
			}
			if v := probetest.WantValue(t, m, "http.body_match"); v != tc.want {
				t.Fatalf("http.body_match = %v, want %v", v, tc.want)
			}
		})
	}
}

func TestHTTPConnectionRefusedIsDown(t *testing.T) {
	obs := run(context.Background(), probetest.LoopbackGuard(),
		httpParams(fmt.Sprintf("http://127.0.0.1:%d/", probetest.ClosedPort(t)), map[string]any{"body_contains": "x"}), "")
	probetest.WantDown(t, obs, "http", probe.FailureRefused)
}

// rawServer accepts connections on loopback and hands each to serve.
func rawServer(t *testing.T, serve func(net.Conn)) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = c.Close() }()
				serve(c)
			}()
		}
	}()
	return ln.Addr().String()
}

// readRequest reads a request's head, so the client is done writing.
func readRequest(c net.Conn) {
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	r := bufio.NewReader(c)
	for {
		line, err := r.ReadString('\n')
		if err != nil || line == "\r\n" {
			return
		}
	}
}

// TestHTTPFailureReasons: each way a request can fail is reported as its
// label, never as the client's error text.
func TestHTTPFailureReasons(t *testing.T) {
	notHTTP := rawServer(t, func(c net.Conn) {
		readRequest(c)
		_, _ = c.Write([]byte("SSH-2.0-OpenSSH\r\n\r\n"))
	})
	closed := rawServer(t, func(c net.Conn) {
		readRequest(c)
		_ = c.(*net.TCPConn).SetLinger(0)
	})
	banner := rawServer(t, func(c net.Conn) {
		_, _ = c.Write([]byte("SSH-2.0-OpenSSH\r\n"))
		readRequest(c)
	})
	for _, tc := range []struct {
		name, url, want string
	}{
		{"not http", "http://" + notHTTP + "/?token=s3cret", probe.FailureProtocol},
		{"closed early", "http://" + closed + "/", probe.FailureReset},
		{"https to a server not speaking tls", "https://" + banner + "/", probe.FailureTLS},
	} {
		t.Run(tc.name, func(t *testing.T) {
			obs := run(context.Background(), probetest.LoopbackGuard(), httpParams(tc.url, map[string]any{"timeout_s": float64(3)}), "")
			probetest.WantDown(t, obs, "http", tc.want)
		})
	}
}

// TestHTTPSameHostRedirectNotHTTPIsProtocol: a redirect to the same host
// whose answer is not HTTP fails as protocol, like a first hop would.
func TestHTTPSameHostRedirectNotHTTPIsProtocol(t *testing.T) {
	var hits atomic.Int32
	addr := rawServer(t, func(c net.Conn) {
		readRequest(c)
		if hits.Add(1) == 1 {
			_, _ = c.Write([]byte("HTTP/1.1 302 Found\r\nLocation: /bad\r\nContent-Length: 0\r\n\r\n"))
			// Keep the connection for a client that reuses it.
			readRequest(c)
		}
		_, _ = c.Write([]byte("SSH-2.0-OpenSSH\r\n\r\n"))
	})
	obs := run(context.Background(), probetest.LoopbackGuard(), httpParams("http://"+addr+"/", map[string]any{"timeout_s": float64(3)}), "")
	probetest.WantDown(t, obs, "http", probe.FailureProtocol)
}

// TestHTTPRedirectHopFailureIsItsOwn: a hop after a redirect that fails
// reports that hop's reason, not the first hop's success.
func TestHTTPRedirectHopFailureIsItsOwn(t *testing.T) {
	closed := probetest.ClosedPort(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, fmt.Sprintf("http://127.0.0.1:%d/", closed), http.StatusFound)
	}))
	defer srv.Close()
	probetest.WantDown(t, run(context.Background(), probetest.LoopbackGuard(), httpParams(srv.URL, nil), ""), "http", probe.FailureRefused)
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
	obs := run(ctx, probetest.LoopbackGuard(), httpParams(srv.URL, map[string]any{"timeout_s": float64(30)}), "")
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("probe took %v, beyond the context deadline", d)
	}
	probetest.WantDown(t, obs, "http", probe.FailureTimeout)
}
