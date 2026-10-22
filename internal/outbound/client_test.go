package outbound_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/outbound"
)

// fakeResolver answers every name with fixed addresses and counts lookups.
type fakeResolver struct {
	addrs   []netip.Addr
	err     error
	lookups atomic.Int32
}

func (f *fakeResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	f.lookups.Add(1)
	return f.addrs, f.err
}

func loopbackOnly() outbound.Policy {
	return outbound.NewPolicy([]netip.Prefix{netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("::1/128")})
}

func TestClientDialsTheResolvedAddress(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Host, "hooks.example.test:") {
			t.Errorf("Host header = %q, want the URL's name", r.Host)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	port := srv.URL[strings.LastIndex(srv.URL, ":")+1:]
	r := &fakeResolver{addrs: []netip.Addr{netip.MustParseAddr("127.0.0.1")}}
	resp, err := outbound.NewClient(loopbackOnly(), time.Second, r).Get("http://hooks.example.test:" + port + "/x")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent || r.lookups.Load() != 1 {
		t.Fatalf("status %d, lookups %d", resp.StatusCode, r.lookups.Load())
	}
}

func TestClientRefusesBlockedDestinations(t *testing.T) {
	cases := []struct {
		name     string
		url      string
		resolver *fakeResolver
	}{
		{"loopback literal", "http://127.0.0.1:9/", &fakeResolver{}},
		{"metadata literal", "http://169.254.169.254/latest/meta-data", &fakeResolver{}},
		{"name resolving to loopback", "http://evil.example.test/", &fakeResolver{addrs: []netip.Addr{netip.MustParseAddr("127.0.0.1")}}},
		{"any blocked answer refuses the name", "http://mixed.example.test/", &fakeResolver{addrs: []netip.Addr{netip.MustParseAddr("203.0.113.5"), netip.MustParseAddr("169.254.169.254")}}},
		{"ipv6 loopback literal", "http://[::1]:9/", &fakeResolver{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := outbound.NewClient(outbound.NewPolicy(nil), time.Second, c.resolver).Get(c.url)
			if !errors.Is(err, outbound.ErrBlocked) {
				t.Fatalf("Get(%s) = %v, want ErrBlocked", c.url, err)
			}
		})
	}
}

func TestClientDoesNotFollowRedirects(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Redirect(w, r, "/elsewhere", http.StatusFound)
	}))
	defer srv.Close()
	_, err := outbound.NewClient(loopbackOnly(), time.Second, nil).Get(srv.URL)
	if !errors.Is(err, outbound.ErrRedirect) || hits.Load() != 1 {
		t.Fatalf("err = %v, hits = %d; want ErrRedirect after one request", err, hits.Load())
	}
}

func TestClientTimesOut(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	defer srv.Close()
	defer close(release)
	start := time.Now()
	_, err := outbound.NewClient(loopbackOnly(), 200*time.Millisecond, nil).Get(srv.URL)
	var ue *url.Error
	if !errors.As(err, &ue) || !ue.Timeout() || time.Since(start) > 5*time.Second {
		t.Fatalf("err = %v after %s, want a timeout", err, time.Since(start))
	}
}

// TestClientIgnoresProxyEnvironment is fix-round-1 Important 2. The
// original version of this test only made a request to a loopback target
// and asserted no error — but net/http's ProxyFromEnvironment never
// proxies loopback destinations, so the assertion held whether or not
// NewClient actually set Proxy: nil. A mutation to
// Proxy: http.ProxyFromEnvironment passed it (see the fix round's RED
// evidence). This version asserts the Transport's Proxy field
// structurally, which a proxy-from-environment regression cannot pass:
// a proxy would let requests be routed past DialContext's address check
// entirely (the proxy connection, not our resolved+checked address, is
// what actually gets dialled).
func TestClientIgnoresProxyEnvironment(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	c := outbound.NewClient(loopbackOnly(), time.Second, nil)
	tr, ok := c.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("Transport is %T, want *http.Transport", c.Transport)
	}
	if tr.Proxy != nil {
		t.Fatal("Transport.Proxy must be nil: a non-nil Proxy (even http.ProxyFromEnvironment) would let a request bypass DialContext's address check")
	}
}

// loopbackZone returns the name of a real loopback network interface
// (e.g. "lo" on Linux, "lo0" on macOS/BSD), so a zoned IPv6 literal built
// from it is one the OS can actually resolve and dial — proving the
// block happens in Check, before any network operation, not because the
// zone itself was bogus.
func loopbackZone(t *testing.T) string {
	t.Helper()
	ifaces, err := net.Interfaces()
	if err != nil {
		t.Fatalf("net.Interfaces: %v", err)
	}
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagLoopback != 0 {
			return ifc.Name
		}
	}
	t.Skip("no loopback interface found")
	return ""
}

// TestClientRefusesZonedLoopbackLiteral is fix-round-1 Critical 1's
// end-to-end regression: a real listener on [::1], reached through a URL
// carrying a zoned literal exactly as RFC 6874 percent-encodes it in a
// browser or Go's own URL parser (http://[::1%25<zone>]/). Before the
// fix, Policy.Check silently admitted any zoned address (Prefix.Contains
// never matches one), and this request reached the loopback server with a
// 200. After the fix it must never leave DialContext.
func TestClientRefusesZonedLoopbackLiteral(t *testing.T) {
	zone := loopbackZone(t)
	ln, err := net.Listen("tcp", "[::1]:0")
	if err != nil {
		t.Skipf("no IPv6 loopback available: %v", err)
	}
	defer ln.Close() //nolint:errcheck
	reached := make(chan struct{}, 1)
	go func() {
		_ = http.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			reached <- struct{}{}
			w.WriteHeader(http.StatusOK)
		}))
	}()
	_, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	target := fmt.Sprintf("http://[::1%%25%s]:%s/", zone, port)
	_, err = outbound.NewClient(outbound.NewPolicy(nil), time.Second, nil).Get(target)
	if !errors.Is(err, outbound.ErrBlocked) {
		t.Fatalf("Get(%s) = %v, want ErrBlocked", target, err)
	}
	select {
	case <-reached:
		t.Fatal("the zoned literal reached the loopback listener")
	default:
	}
}

// TestClientTimesOutMidBody is Minor 5: TestClientTimesOut only ever
// stalls before headers are sent; this proves the timeout also bounds a
// response whose body never finishes, the "full body" half of
// NewClient's timeout contract.
func TestClientTimesOutMidBody(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("partial"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-release
	}))
	defer srv.Close()
	defer close(release)
	start := time.Now()
	resp, err := outbound.NewClient(loopbackOnly(), 200*time.Millisecond, nil).Get(srv.URL)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer resp.Body.Close() //nolint:errcheck
	_, err = io.ReadAll(resp.Body)
	if err == nil || time.Since(start) > 5*time.Second {
		t.Fatalf("ReadAll err = %v after %s, want a timeout reading a body that never finishes", err, time.Since(start))
	}
}

func TestClientReportsResolverFailures(t *testing.T) {
	r := &fakeResolver{err: errors.New("no such host")}
	if _, err := outbound.NewClient(outbound.NewPolicy(nil), time.Second, r).Get("http://unknown.example.test/"); err == nil || !strings.Contains(err.Error(), "no such host") {
		t.Fatalf("err = %v", err)
	}
	empty := &fakeResolver{}
	if _, err := outbound.NewClient(outbound.NewPolicy(nil), time.Second, empty).Get("http://empty.example.test/"); err == nil || !strings.Contains(err.Error(), "no addresses") {
		t.Fatalf("err = %v", err)
	}
}

func TestNewClientDefaultsTheTimeout(t *testing.T) {
	if c := outbound.NewClient(outbound.NewPolicy(nil), 0, nil); c.Timeout != outbound.DefaultTimeout {
		t.Fatalf("timeout = %s", c.Timeout)
	}
}

// TestClientFallsBackToAnotherResolvedAddress is backlog 2.79. The dialer
// checks the policy against EVERY address a name resolves to and refuses
// the destination unless all of them pass — then dialled only the first
// one. A receiver whose name carries an AAAA record the manager cannot
// reach and an A record it can was therefore simply unreachable, even
// though the working address had already been checked and approved. Since
// every address in the list has passed the policy, trying the next one
// after a dial failure cannot reach anywhere the first one was not already
// allowed to.
func TestClientFallsBackToAnotherResolvedAddress(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	port := srv.URL[strings.LastIndex(srv.URL, ":")+1:]
	// ::1 first, where nothing listens on this port; the server is on
	// 127.0.0.1. Both are inside the policy's loopback range.
	r := &fakeResolver{addrs: []netip.Addr{netip.MustParseAddr("::1"), netip.MustParseAddr("127.0.0.1")}}
	resp, err := outbound.NewClient(loopbackOnly(), 5*time.Second, r).Get("http://hooks.example.test:" + port + "/x")
	if err != nil {
		t.Fatalf("the second resolved address serves this request, so it must not fail: %v", err)
	}
	defer resp.Body.Close() //nolint:errcheck // test
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", resp.StatusCode)
	}
}

// TestClientReportsEveryAddressWhenNoneConnects pins the other side of the
// fallback: when no resolved address accepts the connection, the error
// names them all rather than only the one the old code happened to try,
// so an operator reading a delivery's last error sees the whole picture.
func TestClientReportsEveryAddressWhenNoneConnects(t *testing.T) {
	// A port nothing listens on: taken from a listener closed right away.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	r := &fakeResolver{addrs: []netip.Addr{netip.MustParseAddr("::1"), netip.MustParseAddr("127.0.0.1")}}
	_, err = outbound.NewClient(loopbackOnly(), 5*time.Second, r).Get(fmt.Sprintf("http://hooks.example.test:%d/x", port))
	if err == nil {
		t.Fatal("want a dial error, got none")
	}
	if !strings.Contains(err.Error(), "::1") || !strings.Contains(err.Error(), "127.0.0.1") {
		t.Fatalf("error names only some addresses: %v", err)
	}
}
