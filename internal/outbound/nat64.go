package outbound

import "net/netip"

// rfc6052Lengths are the six prefix lengths RFC 6052 §2.2 defines an
// IPv4-embedded-in-IPv6 layout for. Any other length has no defined
// embedding, so reading octets out of it would invent an address — and
// the policy would then judge a destination that is not the one a dial
// reaches.
var rfc6052Lengths = map[int]bool{32: true, 40: true, 48: true, 56: true, 64: true, 96: true}

// ValidNAT64Prefix reports whether p is usable as an operator's own
// NAT64 translation prefix: IPv6, in canonical masked form, and one of
// the six lengths RFC 6052 §2.2 defines.
func ValidNAT64Prefix(p netip.Prefix) bool {
	return p.IsValid() && p.Addr().Is6() && !p.Addr().Is4In6() && rfc6052Lengths[p.Bits()] && p == p.Masked()
}

// rfc6052EmbeddedIPv4 extracts the IPv4 address a carries under the
// NAT64 translation prefix nsp (backlog 2.76).
//
// §2.2 does not lay the address down contiguously. Bits 64-71 are
// reserved — the "u" octet, which the RFC requires to be zero — so for a
// /40, /48 or /56 the four octets straddle it: the address resumes at
// bit 72. Only /32 (bits 32-63) and /96 (bits 96-127) are a plain
// contiguous span, which is why embeddingPrefixes' single `offset` can
// carry the well-known 64:ff9b::/96 and nothing wider.
//
// The u octet is NOT checked against zero. A translator that sets it is
// violating the RFC, but the address it would deliver to is still the
// one these octets name, and refusing to look would leave exactly the
// hole this closes.
func rfc6052EmbeddedIPv4(a netip.Addr, nsp netip.Prefix) (netip.Addr, bool) {
	if !ValidNAT64Prefix(nsp) || !a.Is6() || a.Is4In6() || !nsp.Contains(a) {
		return netip.Addr{}, false
	}
	b := a.As16()
	var v4 [4]byte
	// The prefix ends on an octet boundary at every legal length, so the
	// address begins at the octet right after it and runs for four,
	// stepping over the reserved octet at index 8 if it falls inside
	// that run. A /32 never reaches it, a /96 starts past it, and the
	// three lengths in between straddle it.
	o := nsp.Bits() / 8
	for i := range v4 {
		if o == 8 {
			o++
		}
		v4[i] = b[o]
		o++
	}
	return netip.AddrFrom4(v4), true
}
