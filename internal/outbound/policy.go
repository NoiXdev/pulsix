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
	}
}

// Policy decides which destination addresses may be dialled. The zero
// value refuses every blocked range and allows everything else.
type Policy struct {
	allow []netip.Prefix
}

// NewPolicy returns a Policy whose allowlist (PULSIX_NOTIFY_ALLOW_CIDRS)
// re-admits addresses inside the blocked ranges.
func NewPolicy(allow []netip.Prefix) Policy {
	return Policy{allow: append([]netip.Prefix(nil), allow...)}
}

// Check reports whether a may be dialled: nil, or an error wrapping
// ErrBlocked. IPv4-mapped IPv6 addresses are judged as IPv4.
func (p Policy) Check(a netip.Addr) error {
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
	return nil
}
