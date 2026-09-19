package outbound_test

import (
	"errors"
	"net/netip"
	"testing"

	"github.com/noixdev/pulsix/internal/outbound"
)

func TestPolicyCheck(t *testing.T) {
	cases := []struct {
		addr    string
		allowed bool
	}{
		{"203.0.113.10", true},
		{"10.1.2.3", true},       // private ranges are normal self-hosted receivers
		{"192.168.178.20", true}, // home lab
		{"fd12:3456::1", true},   // unique local IPv6
		{"2001:db8::1", true},
		{"127.0.0.1", false},
		{"127.8.9.10", false},
		{"::1", false},
		{"169.254.169.254", false}, // the classic metadata address
		{"fe80::1", false},
		{"0.0.0.0", false},
		{"::", false},
		{"224.0.0.1", false},
		{"ff02::1", false},
		{"255.255.255.255", false},
		{"100.100.100.200", false},
		{"fd00:ec2::254", false},
		{"::ffff:127.0.0.1", false}, // IPv4-mapped loopback is loopback
	}
	p := outbound.NewPolicy(nil)
	for _, c := range cases {
		err := p.Check(netip.MustParseAddr(c.addr))
		if c.allowed && err != nil {
			t.Errorf("Check(%s) = %v, want allowed", c.addr, err)
		}
		if !c.allowed && !errors.Is(err, outbound.ErrBlocked) {
			t.Errorf("Check(%s) = %v, want ErrBlocked", c.addr, err)
		}
	}
}

func TestPolicyAllowlistReadmitsBlockedRanges(t *testing.T) {
	p := outbound.NewPolicy([]netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")})
	if err := p.Check(netip.MustParseAddr("127.0.0.1")); err != nil {
		t.Fatalf("allowlisted loopback refused: %v", err)
	}
	if err := p.Check(netip.MustParseAddr("169.254.169.254")); !errors.Is(err, outbound.ErrBlocked) {
		t.Fatalf("metadata outside the allowlist = %v, want ErrBlocked", err)
	}
	if err := p.Check(netip.Addr{}); !errors.Is(err, outbound.ErrBlocked) {
		t.Fatalf("invalid address = %v, want ErrBlocked", err)
	}
}

func TestBlockedErrorNamesAddressAndReason(t *testing.T) {
	err := outbound.NewPolicy(nil).Check(netip.MustParseAddr("169.254.169.254"))
	if err == nil || err.Error() != "destination address is not allowed: 169.254.169.254 is link-local (allow it with PULSIX_NOTIFY_ALLOW_CIDRS)" {
		t.Fatalf("error = %v", err)
	}
}
