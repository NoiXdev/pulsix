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
)

func TestGuardAllowed(t *testing.T) {
	extra := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}
	tests := []struct {
		name  string
		guard *probe.Guard
		ip    string
		want  bool
	}{
		{"loopback v4", probe.NewGuard(nil), "127.0.0.1", false},
		{"loopback v4 elsewhere in 127/8", probe.NewGuard(nil), "127.1.2.3", false},
		{"loopback v6", probe.NewGuard(nil), "::1", false},
		{"v4-mapped loopback", probe.NewGuard(nil), "::ffff:127.0.0.1", false},
		{"metadata link-local", probe.NewGuard(nil), "169.254.169.254", false},
		{"link-local v6", probe.NewGuard(nil), "fe80::1", false},
		{"unspecified v4", probe.NewGuard(nil), "0.0.0.0", false},
		{"this network v4", probe.NewGuard(nil), "0.1.2.3", false},
		{"unspecified v6", probe.NewGuard(nil), "::", false},
		{"multicast v4", probe.NewGuard(nil), "224.0.0.1", false},
		{"multicast v6", probe.NewGuard(nil), "ff02::1", false},
		{"private v4 allowed", probe.NewGuard(nil), "10.0.0.1", true},
		{"public v6 allowed", probe.NewGuard(nil), "2001:db8::1", true},
		{"extra cidr refuses", probe.NewGuard(extra), "10.1.2.3", false},
		{"extra cidr refuses v4-mapped", probe.NewGuard(extra), "::ffff:10.1.2.3", false},
		{"extra cidr leaves others", probe.NewGuard(extra), "192.168.1.1", true},
		{"test guard allows loopback", probe.NewGuardForTest(true, nil), "127.0.0.1", true},
		{"test guard allows loopback v6", probe.NewGuardForTest(true, nil), "::1", true},
		{"test guard still refuses link-local", probe.NewGuardForTest(true, nil), "169.254.169.254", false},
		{"test guard without loopback", probe.NewGuardForTest(false, nil), "127.0.0.1", false},
		{"test guard honours extra", probe.NewGuardForTest(true, extra), "10.1.2.3", false},
		{"invalid address", probe.NewGuard(nil), "", false},
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

func TestParseCIDRsMasksHostBits(t *testing.T) {
	got, err := probe.ParseCIDRs("10.1.2.3/8")
	if err != nil {
		t.Fatalf("ParseCIDRs: %v", err)
	}
	if !probe.NewGuard(got).Allowed(netip.MustParseAddr("11.0.0.1")) || probe.NewGuard(got).Allowed(netip.MustParseAddr("10.9.9.9")) {
		t.Fatalf("prefix %v does not cover exactly 10.0.0.0/8", got)
	}
}

func TestGuardDialerRefusesAtDialTime(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()

	_, err = probe.NewGuard(nil).Dialer(time.Second).DialContext(context.Background(), "tcp", ln.Addr().String())
	if !errors.Is(err, probe.ErrNotAllowed) {
		t.Fatalf("dial loopback: err %v, want ErrNotAllowed", err)
	}

	conn, err := loopbackGuard().Dialer(time.Second).DialContext(context.Background(), "tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial loopback with test guard: %v", err)
	}
	_ = conn.Close()
}
