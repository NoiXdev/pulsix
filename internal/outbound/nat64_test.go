package outbound

import (
	"net/netip"
	"testing"
)

// TestRFC6052EmbeddedIPv4 is backlog 2.76's remainder. The policy could
// see through the WELL-KNOWN NAT64 prefix (64:ff9b::/96) but not through
// an operator's own network-specific prefix, so a deployment running its
// own NAT64 had a route to any blocked IPv4 address the policy could not
// recognise as one — exactly the SSRF the whole blocklist exists to stop.
//
// RFC 6052 §2.2 does not place the address contiguously: bits 64-71 are
// reserved (the "u" octet, which must be zero), so for a /40, /48 or /56
// the four octets straddle it. The cases below are the RFC's own worked
// examples (§2.4), which embed 192.0.2.33 in each of the six legal
// prefix lengths.
func TestRFC6052EmbeddedIPv4(t *testing.T) {
	const v4 = "192.0.2.33"
	for _, tc := range []struct {
		nsp  string
		addr string
	}{
		{"2001:db8::/32", "2001:db8:c000:221::"},
		{"2001:db8:100::/40", "2001:db8:1c0:2:21::"},
		{"2001:db8:122::/48", "2001:db8:122:c000:2:2100::"},
		{"2001:db8:122:300::/56", "2001:db8:122:3c0:0:221::"},
		{"2001:db8:122:344::/64", "2001:db8:122:344:c0:2:2100::"},
		{"2001:db8:122:344::/96", "2001:db8:122:344::192.0.2.33"},
	} {
		t.Run(tc.nsp, func(t *testing.T) {
			nsp := netip.MustParsePrefix(tc.nsp)
			addr := netip.MustParseAddr(tc.addr)
			got, ok := rfc6052EmbeddedIPv4(addr, nsp)
			if !ok {
				t.Fatalf("rfc6052EmbeddedIPv4(%s, %s) found nothing", addr, nsp)
			}
			if got.String() != v4 {
				t.Fatalf("extracted %s, want %s", got, v4)
			}
			// An address outside the prefix is not this prefix's to
			// read: the octets it would pull out are someone else's.
			other := netip.MustParseAddr("2a00:1450:4001:800::200e")
			if _, ok := rfc6052EmbeddedIPv4(other, nsp); ok {
				t.Fatalf("an address outside %s was read as embedding one", nsp)
			}
		})
	}
}

// TestRFC6052RefusesAnIllegalPrefixLength: §2.2 defines exactly six
// lengths. Anything else has no defined embedding, so reading octets out
// of it would be inventing an address — and the policy would then judge
// a destination that is not the one a dial reaches.
func TestRFC6052RefusesAnIllegalPrefixLength(t *testing.T) {
	addr := netip.MustParseAddr("2001:db8:c000:221::")
	for _, bits := range []int{0, 16, 36, 44, 60, 80, 97, 128} {
		nsp := netip.PrefixFrom(netip.MustParseAddr("2001:db8::"), bits).Masked()
		if _, ok := rfc6052EmbeddedIPv4(addr, nsp); ok {
			t.Fatalf("a /%d prefix was accepted; §2.2 defines only /32, /40, /48, /56, /64 and /96", bits)
		}
	}
}

// TestNAT64PrefixReachesThroughToABlockedAddress is the point of all of
// it: with the operator's prefix configured, a destination that embeds a
// loopback or metadata address is refused, and one that embeds an
// ordinary public address still goes through.
func TestNAT64PrefixReachesThroughToABlockedAddress(t *testing.T) {
	nsp := netip.MustParsePrefix("2001:db8:122::/48")
	p := NewPolicy(nil, []netip.Prefix{nsp})
	bare := NewPolicy(nil, nil)

	// 169.254.169.254 (cloud metadata) embedded in the operator's own
	// NAT64 prefix: a9.fe.a9.fe at the /48 layout.
	metadata := netip.MustParseAddr("2001:db8:122:a9fe:0:a9fe::")
	if err := p.Check(metadata); err == nil {
		t.Fatalf("Check(%s) allowed a destination that translates to the metadata address", metadata)
	}
	// Without the prefix configured it is an ordinary global address and
	// is allowed — which is the hole this closes, stated as a fact.
	if err := bare.Check(metadata); err != nil {
		t.Fatalf("without the prefix configured it should look ordinary: %v", err)
	}

	// A public address through the same prefix is still fine: the point
	// is to see through the translator, not to refuse it.
	public := netip.MustParseAddr("2001:db8:122:5db8:0:d822::") // 93.184.216.34
	if err := p.Check(public); err != nil {
		t.Fatalf("Check(%s) refused a translator destination that reaches a public address: %v", public, err)
	}
}
