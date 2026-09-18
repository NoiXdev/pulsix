package clientip_test

import (
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/noixdev/pulsix/internal/clientip"
)

func mustPrefixes(t *testing.T, raw string) []netip.Prefix {
	t.Helper()
	p, err := clientip.ParsePrefixes(raw)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestResolve(t *testing.T) {
	trusted := mustPrefixes(t, "10.0.0.0/8, 192.168.1.5, fd00::/8")
	cases := []struct {
		name   string
		remote string
		xff    []string
		want   string
	}{
		{"no proxy configured path: untrusted peer wins", "203.0.113.7:4711", []string{"1.2.3.4"}, "203.0.113.7"},
		{"trusted peer, one client hop", "10.1.2.3:443", []string{"198.51.100.9"}, "198.51.100.9"},
		{"right-most untrusted hop wins over a spoofed left entry", "10.1.2.3:443", []string{"6.6.6.6, 198.51.100.9"}, "198.51.100.9"},
		{"chained trusted proxies are skipped", "10.1.2.3:443", []string{"198.51.100.9, 192.168.1.5, 10.9.9.9"}, "198.51.100.9"},
		{"several header lines are one list", "10.1.2.3:443", []string{"198.51.100.9", "10.9.9.9"}, "198.51.100.9"},
		{"trusted peer without header is the client", "10.1.2.3:443", nil, "10.1.2.3"},
		{"unparsable entry stops the walk at the nearest address", "10.1.2.3:443", []string{"198.51.100.9, garbage"}, "10.1.2.3"},
		{"all hops trusted: the left-most is the client", "10.1.2.3:443", []string{"10.7.7.7, 10.8.8.8"}, "10.7.7.7"},
		{"ipv6 peer and hop", "[fd00::1]:443", []string{"2001:db8::5"}, "2001:db8::5"},
		{"ipv4-mapped ipv6 is unmapped", "[::ffff:203.0.113.7]:1", nil, "203.0.113.7"},
		{"bare address without port", "203.0.113.7", nil, "203.0.113.7"},
	}
	r := clientip.New(trusted)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := r.Resolve(c.remote, c.xff)
			if got.String() != c.want {
				t.Fatalf("Resolve(%q, %q) = %v, want %s", c.remote, c.xff, got, c.want)
			}
		})
	}
}

func TestResolveWithoutTrustedProxiesIgnoresHeader(t *testing.T) {
	r := clientip.New(nil)
	if got := r.Resolve("10.1.2.3:1", []string{"198.51.100.9"}); got.String() != "10.1.2.3" {
		t.Fatalf("got %v", got)
	}
}

func TestResolveUnparsablePeer(t *testing.T) {
	if got := clientip.New(nil).Resolve("not an address", nil); got.IsValid() {
		t.Fatalf("got %v, want invalid", got)
	}
}

func TestFromRequest(t *testing.T) {
	req := httptest.NewRequest("POST", "/", nil)
	req.RemoteAddr = "10.0.0.2:5555"
	req.Header.Add("X-Forwarded-For", "198.51.100.9")
	r := clientip.New(mustPrefixes(t, "10.0.0.0/8"))
	if got := r.FromRequest(req); got.String() != "198.51.100.9" {
		t.Fatalf("got %v", got)
	}
}

func TestParsePrefixes(t *testing.T) {
	got, err := clientip.ParsePrefixes(" 10.0.0.0/8 ,192.168.1.5,,2001:db8::1 ")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"10.0.0.0/8", "192.168.1.5/32", "2001:db8::1/128"}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i].String() != want[i] {
			t.Errorf("[%d] = %v, want %s", i, got[i], want[i])
		}
	}
	for _, bad := range []string{"10.0.0.0/33", "example.com", "10.0.0.1/8"} {
		if _, err := clientip.ParsePrefixes(bad); err == nil {
			t.Errorf("ParsePrefixes(%q) accepted", bad)
		}
	}
	if got, err := clientip.ParsePrefixes(""); err != nil || got != nil {
		t.Errorf("empty = %v, %v", got, err)
	}
}
