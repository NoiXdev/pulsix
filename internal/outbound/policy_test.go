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
	p := outbound.NewPolicy(nil, nil)
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
	p := outbound.NewPolicy([]netip.Prefix{netip.MustParsePrefix("127.0.0.0/8")}, nil)
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
	err := outbound.NewPolicy(nil, nil).Check(netip.MustParseAddr("169.254.169.254"))
	if err == nil || err.Error() != "destination address is not allowed: 169.254.169.254 is link-local (allow it with PULSIX_NOTIFY_ALLOW_CIDRS)" {
		t.Fatalf("error = %v", err)
	}
}

// TestPolicyRefusesZonedAddresses is fix-round-1 item Critical 1: a zoned
// IPv6 literal (RFC 4007, written in a URL per RFC 6874 as
// "[::1%25lo0]") is documented by net/netip to never match any
// netip.Prefix.Contains — so the ordinary blocked-range loop silently lets
// it through. Zones are local to the resolving host and never a
// meaningful webhook destination, so Check refuses every zoned address
// unconditionally — not even PULSIX_NOTIFY_ALLOW_CIDRS can readmit one,
// since the same Contains blindness would make the allowlist check itself
// a no-op for a zoned address.
func TestPolicyRefusesZonedAddresses(t *testing.T) {
	cases := []string{
		"::1%lo0",               // zoned loopback: the exact bypass the review found
		"fe80::1%eth0",          // zoned link-local
		"::ffff:127.0.0.1%lo0",  // zoned IPv4-mapped loopback
		"203.0.113.10%whatever", // zones are IPv6-only syntax in netip, but guard it anyway
	}
	for _, c := range cases {
		a, err := netip.ParseAddr(c)
		if err != nil {
			// "203.0.113.10%whatever" is not valid netip syntax for IPv4;
			// skip rather than fail the whole table over a documentation
			// case that Go itself refuses to parse.
			continue
		}
		if err := outbound.NewPolicy(nil, nil).Check(a); !errors.Is(err, outbound.ErrBlocked) {
			t.Errorf("Check(%s) = %v, want ErrBlocked", c, err)
		}
	}
}

// TestPolicyAllowlistCannotReadmitAZonedAddress: even a wide-open allowlist
// must not admit a zoned address, because Prefix.Contains never matches
// one — proving the zone refusal in Check happens before the allowlist
// loop, not as a fallback inside it.
func TestPolicyAllowlistCannotReadmitAZonedAddress(t *testing.T) {
	p := outbound.NewPolicy([]netip.Prefix{netip.MustParsePrefix("::/0")}, nil)
	if err := p.Check(netip.MustParseAddr("::1%lo0")); !errors.Is(err, outbound.ErrBlocked) {
		t.Fatalf("Check(::1%%lo0) with a ::/0 allowlist = %v, want ErrBlocked", err)
	}
}

// TestPolicyChecksEmbeddedIPv4 is fix-round-1 Important 1: IPv6 forms that
// carry a 32-bit IPv4 address must have that address checked by the same
// policy, so a NAT64/6to4/SIIT translator on the far side cannot be used
// to reach a blocked IPv4 destination. A private embedded address is a
// normal self-hosted receiver and must still be allowed.
func TestPolicyChecksEmbeddedIPv4(t *testing.T) {
	cases := []struct {
		addr    string
		allowed bool
		note    string
	}{
		{"64:ff9b::a9fe:a9fe", false, "NAT64 well-known, embeds the metadata address"},
		{"64:ff9b::7f00:1", false, "NAT64 well-known, embeds loopback"},
		{"64:ff9b::a00:1", true, "NAT64 well-known, embeds a private address"},
		{"64:ff9b:1::a9fe:a9fe", false, "NAT64 local-use (RFC 8215): whole prefix refused, extraction is ambiguous"},
		{"64:ff9b:1::1", false, "NAT64 local-use: refused regardless of the low bits"},
		{"::7f00:1", false, "IPv4-compatible, embeds loopback"},
		{"::ffff:0:7f00:1", false, "IPv4-translated / SIIT, embeds loopback"},
		{"2002:a9fe:a9fe::", false, "6to4, embeds the metadata address"},
		{"2002:a00:1::", true, "6to4, embeds a private address"},
		{"fec0::1", false, "deprecated site-local, defence in depth"},
		// Teredo (RFC 4380, backlog 2.76): the client's embedded IPv4 is
		// stored as its bitwise complement. complement(169.254.169.254) =
		// (0x56,0x01,0x56,0x01); complement(10.0.0.1) = (0xf5,0xff,0xff,0xfe).
		// Server address and port (the other 2001::/32 bits, here all zero)
		// are irrelevant to what Check actually reaches.
		{"2001::5601:5601", false, "Teredo, embeds (complemented) the metadata address"},
		{"2001::f5ff:fffe", true, "Teredo, embeds (complemented) a private address"},
		// ISATAP (RFC 5214): the interface identifier (the low 64 bits, any
		// network prefix) is 0000:5EFE or 0200:5EFE followed by the plain
		// embedded IPv4 — unlike Teredo, not obfuscated.
		{"2001:db8:1:1:0:5efe:a9fe:a9fe", false, "ISATAP (0000:5EFE), embeds the metadata address"},
		{"2001:db8:1:1:200:5efe:a00:1", true, "ISATAP (0200:5EFE), embeds a private address"},
		{"2001:db8:1:1::1", true, "an ordinary address under the same /64 as the ISATAP cases, no interface-id match"},
	}
	p := outbound.NewPolicy(nil, nil)
	for _, c := range cases {
		err := p.Check(netip.MustParseAddr(c.addr))
		if c.allowed && err != nil {
			t.Errorf("%s: Check(%s) = %v, want allowed", c.note, c.addr, err)
		}
		if !c.allowed && !errors.Is(err, outbound.ErrBlocked) {
			t.Errorf("%s: Check(%s) = %v, want ErrBlocked", c.note, c.addr, err)
		}
	}
}

// TestPolicyEmbeddedIPv4RespectsAllowlist: the embedded address is checked
// through the same Policy, allowlist included — an operator who allowed a
// private range should not have NAT64/6to4 access to it refused.
func TestPolicyEmbeddedIPv4RespectsAllowlist(t *testing.T) {
	p := outbound.NewPolicy([]netip.Prefix{netip.MustParsePrefix("169.254.169.254/32")}, nil)
	if err := p.Check(netip.MustParseAddr("64:ff9b::a9fe:a9fe")); err != nil {
		t.Fatalf("allowlisted embedded address refused: %v", err)
	}
}

// TestPolicyRefusesIPv6CloudInstanceServices is backlog 7.43 and 7.60: the
// only IPv6 metadata entry was AWS's IMDS address, and the rest of fc00::/7
// is allowed as private space, so the IPv6 metadata and instance-service
// addresses of other providers — and AWS's own neighbours of that
// address — were dialled. Each provider's instance-service /64 is refused;
// ordinary unique-local space stays allowed.
func TestPolicyRefusesIPv6CloudInstanceServices(t *testing.T) {
	p := outbound.NewPolicy(nil, nil)
	for _, addr := range []string{
		"fd00:ec2::254",      // AWS instance metadata
		"fd00:ec2::253",      // AWS DNS resolver
		"fd00:ec2::23",       // AWS, same instance-service range
		"fd20:ce::254",       // Google Cloud metadata
		"fd00:c1::a9fe:a9fe", // Oracle Cloud metadata
		"fd00:a9fe:a9fe::1",  // Akamai/Linode metadata
		"fd00:42::42",        // Scaleway metadata
	} {
		if err := p.Check(netip.MustParseAddr(addr)); !errors.Is(err, outbound.ErrBlocked) {
			t.Errorf("Check(%s) = %v, want ErrBlocked", addr, err)
		}
	}
	for _, addr := range []string{"fd00:ec3::1", "fd20:cf::254", "fd00:c2::1", "fd00:43::1", "fd12:3456::1"} {
		if err := p.Check(netip.MustParseAddr(addr)); err != nil {
			t.Errorf("Check(%s) = %v, want allowed (ordinary unique-local space)", addr, err)
		}
	}
}
