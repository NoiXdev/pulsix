// Package outbound is the HTTP client every manager-initiated request to
// an operator-supplied URL goes through (notification webhooks, spec
// 2026-09-18 §5 "Outbound webhook safety"): the destination name is
// resolved first and the connection is dialled to the checked address, so
// a DNS answer cannot change between the check and the dial; loopback,
// link-local, unspecified, multicast and cloud metadata addresses are
// refused unless PULSIX_NOTIFY_ALLOW_CIDRS lists them; redirects are never
// followed; every request ends after a fixed timeout. Private ranges
// (10/8, 172.16/12, 192.168/16, fc00::/7) are allowed: self-hosted
// receivers are the normal case.
package outbound

import (
	"errors"
	"fmt"
	"net/netip"
)

// ErrBlocked is the refusal of a destination address. The error text
// names the address and why, never the URL (URLs can carry secrets).
var ErrBlocked = errors.New("destination address is not allowed")

// blockedRange is one refused range and the reason shown to operators.
type blockedRange struct {
	prefix netip.Prefix
	reason string
}

// blockedRanges lists the refused ranges. It is a function, not a
// package variable, so nothing can change the list at run time.
func blockedRanges() []blockedRange {
	return []blockedRange{
		{netip.MustParsePrefix("127.0.0.0/8"), "loopback"},
		{netip.MustParsePrefix("::1/128"), "loopback"},
		{netip.MustParsePrefix("169.254.0.0/16"), "link-local"},
		{netip.MustParsePrefix("fe80::/10"), "link-local"},
		{netip.MustParsePrefix("0.0.0.0/8"), "unspecified"},
		{netip.MustParsePrefix("::/128"), "unspecified"},
		{netip.MustParsePrefix("224.0.0.0/4"), "multicast"},
		{netip.MustParsePrefix("ff00::/8"), "multicast"},
		{netip.MustParsePrefix("255.255.255.255/32"), "broadcast"},
		{netip.MustParsePrefix("100.100.100.200/32"), "cloud metadata"},
		{netip.MustParsePrefix("fd00:ec2::254/128"), "cloud metadata"},
		{netip.MustParsePrefix("192.0.0.192/32"), "cloud metadata"},   // Oracle Cloud's legacy metadata address
		{netip.MustParsePrefix("168.63.129.16/32"), "cloud metadata"}, // Azure WireServer
		{netip.MustParsePrefix("fec0::/10"), "site-local (deprecated)"},
		// 64:ff9b:1::/48 (RFC 8215, NAT64 local use) is refused outright
		// rather than having its embedded IPv4 address extracted: unlike
		// the well-known 64:ff9b::/96 prefix, this format supports more
		// than one embedded prefix length, so pulling a fixed 32 bits out
		// of it could as easily under-refuse as over-refuse. See
		// embeddingPrefixes for the forms extraction does handle.
		{netip.MustParsePrefix("64:ff9b:1::/48"), "NAT64 (local use; embedding is ambiguous, refused outright)"},
	}
}

// embeddingPrefix is an IPv6 range known to carry a literal IPv4 address in
// a fixed 32-bit span, so the address it actually reaches on the far side
// of a NAT64/SIIT/6to4/Teredo translator can be checked by the very same
// policy a plain IPv4 destination would be. offset is the embedded
// address's byte offset within netip.Addr.As16(); complement means those
// four bytes are stored as their bitwise complement and must be flipped
// back before they are a real address (Teredo's own obfuscation, below).
type embeddingPrefix struct {
	prefix     netip.Prefix
	offset     int
	complement bool
}

// embeddingPrefixes lists the IPv4-in-IPv6 forms Check extracts and checks
// recursively (RFC 6052 §2.1, RFC 3056, RFC 4380). It is a function for the
// same reason blockedRanges is.
func embeddingPrefixes() []embeddingPrefix {
	return []embeddingPrefix{
		{prefix: netip.MustParsePrefix("64:ff9b::/96"), offset: 12},    // NAT64 well-known prefix
		{prefix: netip.MustParsePrefix("::/96"), offset: 12},           // IPv4-compatible (deprecated)
		{prefix: netip.MustParsePrefix("::ffff:0:0:0/96"), offset: 12}, // IPv4-translated (SIIT) — distinct from the IPv4-mapped ::ffff:a.b.c.d form Unmap already handles
		{prefix: netip.MustParsePrefix("2002::/16"), offset: 2},        // 6to4
		// Teredo (RFC 4380, backlog 2.76): 2001:0000:SERVER:FLAGS:PORT:CLIENT,
		// where CLIENT (the last 32 bits — the address a UDP-encapsulated
		// Teredo tunnel actually delivers to) is stored as its bitwise
		// complement specifically so a NAT or firewall that rewrites literal
		// IPv4 addresses in a packet body cannot mangle it in transit. The
		// server address at offset 4 is plaintext but is not what a dial
		// would reach, so it is not checked separately.
		{prefix: netip.MustParsePrefix("2001::/32"), offset: 12, complement: true},
	}
}

// embeddedIPv4 reports the IPv4 address a carries, when a falls in one of
// embeddingPrefixes or matches the ISATAP interface identifier pattern
// (isatapEmbeddedIPv4, which — unlike every entry in embeddingPrefixes —
// is not tied to any one network prefix), and whether one was found.
func embeddedIPv4(a netip.Addr) (netip.Addr, bool) {
	for _, e := range embeddingPrefixes() {
		if e.prefix.Contains(a) {
			b := a.As16()
			v4 := [4]byte{b[e.offset], b[e.offset+1], b[e.offset+2], b[e.offset+3]}
			if e.complement {
				for i := range v4 {
					v4[i] = ^v4[i]
				}
			}
			return netip.AddrFrom4(v4), true
		}
	}
	return isatapEmbeddedIPv4(a)
}

// isatapInterfaceID is the fixed 32-bit prefix (RFC 5214 §6.1) that marks
// the low 64 bits of an address as an ISATAP interface identifier: either
// 0000:5EFE (EUI-64 "non-universal", the public-IPv4 convention) or
// 0200:5EFE (the "universal" bit set, used for a private/NAT'd IPv4). The
// embedded address follows in the last 32 bits.
var isatapInterfaceIDs = [2][4]byte{
	{0x00, 0x00, 0x5e, 0xfe},
	{0x02, 0x00, 0x5e, 0xfe},
}

// isatapEmbeddedIPv4 reports the IPv4 address an ISATAP (RFC 5214)
// interface identifier carries. Unlike NAT64/6to4/SIIT/Teredo, ISATAP has
// no dedicated network prefix of its own — an ISATAP interface identifier
// is meant to sit under whatever /64 an operator's own network already
// uses — so this checks the low 64 bits' own fixed pattern directly,
// regardless of what the high 64 bits (the network prefix) happen to be,
// rather than being one more entry in embeddingPrefixes (which is keyed
// entirely on matching a fixed prefix first).
func isatapEmbeddedIPv4(a netip.Addr) (netip.Addr, bool) {
	if !a.Is6() {
		return netip.Addr{}, false
	}
	b := a.As16()
	id := [4]byte{b[8], b[9], b[10], b[11]}
	if id != isatapInterfaceIDs[0] && id != isatapInterfaceIDs[1] {
		return netip.Addr{}, false
	}
	return netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]}), true
}

// Policy decides which destination addresses may be dialled. The zero
// value refuses every blocked range and allows everything else.
type Policy struct {
	allow []netip.Prefix
	// nat64 are the operator's own NAT64 translation prefixes
	// (PULSIX_NOTIFY_NAT64_PREFIXES, RFC 6052 network-specific
	// prefixes). The well-known 64:ff9b::/96 is recognised without
	// configuration; a deployment that runs its own NSP has to say so,
	// because an NSP is indistinguishable from ordinary global space
	// without being told (backlog 2.76).
	nat64 []netip.Prefix
}

// NewPolicy returns a Policy whose allowlist (PULSIX_NOTIFY_ALLOW_CIDRS)
// re-admits addresses inside the blocked ranges, and which sees through
// the operator's own NAT64 prefixes (PULSIX_NOTIFY_NAT64_PREFIXES) as
// well as the well-known one.
func NewPolicy(allow, nat64 []netip.Prefix) Policy {
	return Policy{
		allow: append([]netip.Prefix(nil), allow...),
		nat64: append([]netip.Prefix(nil), nat64...),
	}
}

// Check reports whether a may be dialled: nil, or an error wrapping
// ErrBlocked. IPv4-mapped IPv6 addresses are judged as IPv4; other
// IPv4-in-IPv6 forms (NAT64, 6to4, IPv4-compatible, SIIT, Teredo, ISATAP)
// have their embedded IPv4 address judged the same way, recursively, so a
// translator on the far side cannot be used to reach a blocked address.
//
// A zoned address (RFC 4007, "::1%lo0") is refused unconditionally,
// before the allowlist is even consulted: net/netip documents that
// netip.Prefix.Contains never matches an address that carries a zone, so
// neither the blocked-range check nor PULSIX_NOTIFY_ALLOW_CIDRS could
// ever see it — every zoned address would otherwise pass Check with no
// prefix able to catch or admit it. A zone is meaningful only on the
// host that resolved it, never to a remote webhook receiver, so there is
// no legitimate destination this would refuse.
func (p Policy) Check(a netip.Addr) error {
	if a.Zone() != "" {
		return fmt.Errorf("%w: %s carries a zone, which is refused unconditionally and cannot be allowed with PULSIX_NOTIFY_ALLOW_CIDRS", ErrBlocked, a)
	}
	a = a.Unmap()
	if !a.IsValid() {
		return fmt.Errorf("%w: invalid address", ErrBlocked)
	}
	for _, pre := range p.allow {
		if pre.Contains(a) {
			return nil
		}
	}
	for _, b := range blockedRanges() {
		if b.prefix.Contains(a) {
			return fmt.Errorf("%w: %s is %s (allow it with PULSIX_NOTIFY_ALLOW_CIDRS)", ErrBlocked, a, b.reason)
		}
	}
	if embedded, ok := p.embedded(a); ok {
		if err := p.Check(embedded); err != nil {
			return fmt.Errorf("%s embeds the IPv4 address %s, which is not allowed: %w", a, embedded, err)
		}
	}
	return nil
}

// embedded is embeddedIPv4 plus the operator's own NAT64 prefixes. The
// configured ones are tried FIRST: an NSP may sit anywhere in global
// space, including inside a range one of the fixed forms would also
// match, and the operator's own statement about their network is the
// better answer where the two disagree.
func (p Policy) embedded(a netip.Addr) (netip.Addr, bool) {
	for _, nsp := range p.nat64 {
		if v4, ok := rfc6052EmbeddedIPv4(a, nsp); ok {
			return v4, true
		}
	}
	return embeddedIPv4(a)
}
