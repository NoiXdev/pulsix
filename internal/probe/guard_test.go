package probe_test

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/probe"
	"github.com/noixdev/pulsix/internal/probe/probetest"
)

func TestGuardAllowed(t *testing.T) {
	extra := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}
	tests := []struct {
		name  string
		guard *probe.Guard
		ip    string
		want  bool
	}{
		{"loopback v4", probe.NewGuard(nil, nil), "127.0.0.1", false},
		{"loopback v4 elsewhere in 127/8", probe.NewGuard(nil, nil), "127.1.2.3", false},
		{"loopback v6", probe.NewGuard(nil, nil), "::1", false},
		{"v4-mapped loopback", probe.NewGuard(nil, nil), "::ffff:127.0.0.1", false},
		{"metadata link-local", probe.NewGuard(nil, nil), "169.254.169.254", false},
		{"link-local v6", probe.NewGuard(nil, nil), "fe80::1", false},
		{"unspecified v4", probe.NewGuard(nil, nil), "0.0.0.0", false},
		{"this network v4", probe.NewGuard(nil, nil), "0.1.2.3", false},
		{"unspecified v6", probe.NewGuard(nil, nil), "::", false},
		{"multicast v4", probe.NewGuard(nil, nil), "224.0.0.1", false},
		{"multicast v6", probe.NewGuard(nil, nil), "ff02::1", false},
		{"private v4 allowed", probe.NewGuard(nil, nil), "10.0.0.1", true},
		{"public v6 allowed", probe.NewGuard(nil, nil), "2001:db8::1", true},
		{"extra cidr refuses", probe.NewGuard(extra, nil), "10.1.2.3", false},
		{"extra cidr refuses v4-mapped", probe.NewGuard(extra, nil), "::ffff:10.1.2.3", false},
		{"extra cidr leaves others", probe.NewGuard(extra, nil), "192.168.1.1", true},
		{"test guard allows loopback", probe.NewGuardForTest(true, nil), "127.0.0.1", true},
		{"test guard allows loopback v6", probe.NewGuardForTest(true, nil), "::1", true},
		{"test guard still refuses link-local", probe.NewGuardForTest(true, nil), "169.254.169.254", false},
		{"test guard without loopback", probe.NewGuardForTest(false, nil), "127.0.0.1", false},
		{"test guard honours extra", probe.NewGuardForTest(true, extra), "10.1.2.3", false},
		{"invalid address", probe.NewGuard(nil, nil), "", false},
		{"zoned link-local", probe.NewGuard(nil, nil), "fe80::1%eth0", false},
		{"zoned global", probe.NewGuard(nil, nil), "2001:db8::1%eth0", false},
		{"test guard allows v4-mapped loopback", probe.NewGuardForTest(true, nil), "::ffff:127.0.0.1", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var ip netip.Addr
			if tc.ip != "" {
				ip = netip.MustParseAddr(tc.ip)
			}
			if got := tc.guard.Allowed(ip); got != tc.want {
				t.Fatalf("Allowed(%s) = %v, want %v", tc.ip, got, tc.want)
			}
		})
	}
}

// TestGuardRefusesWhatWebhooksRefuse: the guard is the notification egress
// policy (internal/outbound) plus PULSIX_PROBE_DENY_CIDRS, so every cloud
// metadata address and every IPv4-in-IPv6 form that reaches one is refused
// (final review, Critical #1).
func TestGuardRefusesWhatWebhooksRefuse(t *testing.T) {
	for _, ip := range []string{
		"168.63.129.16",        // Azure WireServer
		"100.100.100.200",      // Alibaba metadata
		"192.0.0.192",          // Oracle legacy metadata
		"fd00:ec2::254",        // AWS IMDS over IPv6
		"fd20:ce::254",         // Google Cloud
		"64:ff9b::a9fe:a9fe",   // NAT64 well-known, embeds 169.254.169.254
		"64:ff9b::7f00:1",      // NAT64 well-known, embeds loopback
		"2002:a9fe:a9fe::1",    // 6to4, embeds 169.254.169.254
		"::ffff:0:a9fe:a9fe",   // SIIT, embeds 169.254.169.254
		"255.255.255.255",      // broadcast
		"::ffff:168.63.129.16", // v4-mapped Azure WireServer
	} {
		t.Run(ip, func(t *testing.T) {
			if probe.NewGuard(nil, nil).Allowed(netip.MustParseAddr(ip)) {
				t.Fatalf("Allowed(%s) = true, want refused", ip)
			}
		})
	}
	if !probe.NewGuard(nil, nil).Allowed(netip.MustParseAddr("64:ff9b::a00:1")) {
		t.Fatal("a NAT64 address embedding a private address is refused; private ranges are allowed")
	}
}

// TestGuardSeesThroughOperatorNAT64: an address under the operator's own
// NAT64 prefix (PULSIX_NOTIFY_NAT64_PREFIXES) is judged by the IPv4
// address it embeds.
func TestGuardSeesThroughOperatorNAT64(t *testing.T) {
	nsp := []netip.Prefix{netip.MustParsePrefix("2001:db8:122::/48")}
	// 169.254.169.254 embedded at the /48 layout (RFC 6052 §2.2: bits
	// 48-63, skip the u octet, bits 72-87).
	meta := netip.MustParseAddr("2001:db8:122:a9fe:a9:fe00::")
	if probe.NewGuard(nil, nsp).Allowed(meta) {
		t.Fatal("an operator NAT64 address embedding the metadata address is allowed")
	}
	if !probe.NewGuard(nil, nil).Allowed(meta) {
		t.Fatal("without the NAT64 prefix the address is ordinary global space and must be allowed")
	}
	private := netip.MustParseAddr("2001:db8:122:a00:0:100::")
	if !probe.NewGuard(nil, nsp).Allowed(private) {
		t.Fatal("an operator NAT64 address embedding a private address is refused")
	}
}

func TestParseCIDRs(t *testing.T) {
	got, err := probe.ParseCIDRs(" 10.0.0.0/8, 192.168.0.0/16 ")
	if err != nil {
		t.Fatalf("ParseCIDRs: %v", err)
	}
	want := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("192.168.0.0/16")}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("ParseCIDRs = %v, want %v", got, want)
	}

	for _, empty := range []string{"", "  ", " , "} {
		got, err := probe.ParseCIDRs(empty)
		if err != nil || len(got) != 0 {
			t.Fatalf("ParseCIDRs(%q) = %v, %v; want none", empty, got, err)
		}
	}

	_, err = probe.ParseCIDRs("10.0.0.0/33")
	if err == nil {
		t.Fatal("ParseCIDRs accepted 10.0.0.0/33")
	}
	if want := "10.0.0.0/33"; !strings.Contains(err.Error(), want) {
		t.Fatalf("error %q does not name the entry %q", err, want)
	}
}

// TestParseCIDRsIPv4MappedDeny: Allowed judges an IPv4-mapped address as
// IPv4, so a deny entry written in the mapped form must become its IPv4
// prefix or it would never match.
func TestParseCIDRsIPv4MappedDeny(t *testing.T) {
	got, err := probe.ParseCIDRs("::ffff:10.0.0.0/104, ::ffff:192.0.2.7/128")
	if err != nil {
		t.Fatalf("ParseCIDRs: %v", err)
	}
	want := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("192.0.2.7/32")}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("ParseCIDRs = %v, want %v", got, want)
	}
	g := probe.NewGuard(got, nil)
	for _, ip := range []string{"10.1.2.3", "::ffff:10.1.2.3", "192.0.2.7"} {
		if g.Allowed(netip.MustParseAddr(ip)) {
			t.Fatalf("Allowed(%s) = true under a mapped deny CIDR", ip)
		}
	}
}

func TestParseCIDRsMasksHostBits(t *testing.T) {
	got, err := probe.ParseCIDRs("10.1.2.3/8")
	if err != nil {
		t.Fatalf("ParseCIDRs: %v", err)
	}
	if !probe.NewGuard(got, nil).Allowed(netip.MustParseAddr("11.0.0.1")) || probe.NewGuard(got, nil).Allowed(netip.MustParseAddr("10.9.9.9")) {
		t.Fatalf("prefix %v does not cover exactly 10.0.0.0/8", got)
	}
}

func TestGuardDialerRefusesAtDialTime(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()

	_, err = probe.NewGuard(nil, nil).Dialer(time.Second).DialContext(context.Background(), "tcp", ln.Addr().String())
	if !errors.Is(err, probe.ErrNotAllowed) {
		t.Fatalf("dial loopback: err %v, want ErrNotAllowed", err)
	}

	conn, err := probetest.LoopbackGuard().Dialer(time.Second).DialContext(context.Background(), "tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial loopback with test guard: %v", err)
	}
	_ = conn.Close()
}
