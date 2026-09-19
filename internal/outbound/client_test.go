package outbound_test

import (
	"context"
	"errors"
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

func TestClientIgnoresProxyEnvironment(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer srv.Close()
	resp, err := outbound.NewClient(loopbackOnly(), time.Second, nil).Get(srv.URL)
	if err != nil {
		t.Fatalf("a proxy variable must not reroute the request: %v", err)
	}
	_ = resp.Body.Close()
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
