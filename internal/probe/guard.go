package probe

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"syscall"
	"time"
)

// ErrNotAllowed is the dial error for a target the guard refuses. A probe
// reports it as a collection error, never as up = 0, so the manager cannot be
// used to tell open ports from closed ones on addresses it must not reach.
var ErrNotAllowed = errors.New("target address not allowed")

// thisNetwork is 0.0.0.0/8 (RFC 1122 "this host on this network"): never a
// valid destination, and on Linux 0.0.0.0 reaches the local host. It is
// refused together with the unspecified addresses.
var thisNetwork = netip.MustParsePrefix("0.0.0.0/8")

// Guard decides whether an IP may be dialled. It always refuses loopback,
// link-local, unspecified and multicast addresses, and every extra prefix.
type Guard struct {
	allowLoopback bool
	deny          []netip.Prefix
}

// NewGuard returns the production guard: the always-refused ranges plus
// extra (PULSIX_PROBE_DENY_CIDRS).
func NewGuard(extra []netip.Prefix) *Guard {
	return &Guard{deny: clonePrefixes(extra)}
}

// NewGuardForTest returns a guard that may also allow loopback, so tests can
// probe their own listeners. Every other always-refused range stays refused.
func NewGuardForTest(allowLoopback bool, extra []netip.Prefix) *Guard {
	return &Guard{allowLoopback: allowLoopback, deny: clonePrefixes(extra)}
}

func clonePrefixes(in []netip.Prefix) []netip.Prefix {
	out := make([]netip.Prefix, 0, len(in))
	for _, p := range in {
		out = append(out, p.Masked())
	}
	return out
}

// ParseCIDRs parses a comma-separated CIDR list (PULSIX_PROBE_DENY_CIDRS).
// Whitespace around entries is trimmed and empty entries are skipped; an
// invalid entry is an error naming it.
func ParseCIDRs(list string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, entry := range strings.Split(list, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		p, err := netip.ParsePrefix(entry)
		if err != nil {
			return nil, fmt.Errorf("invalid CIDR %q: %w", entry, err)
		}
		out = append(out, p.Masked())
	}
	return out, nil
}

// Allowed reports whether ip may be dialled. An IPv4-mapped IPv6 address is
// judged as the IPv4 address it carries.
func (g *Guard) Allowed(ip netip.Addr) bool {
	if !ip.IsValid() {
		return false
	}
	ip = ip.Unmap().WithZone("")
	switch {
	case ip.IsLoopback() && !g.allowLoopback,
		ip.IsLinkLocalUnicast(),
		ip.IsUnspecified(),
		thisNetwork.Contains(ip),
		ip.IsMulticast():
		return false
	}
	for _, p := range g.deny {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}

// Dialer returns a dialer that checks the resolved IP of every connection
// attempt and refuses a disallowed one with ErrNotAllowed. Because the check
// runs at dial time, it covers DNS answers, redirects and rebinding.
func (g *Guard) Dialer(timeout time.Duration) *net.Dialer {
	return &net.Dialer{Timeout: timeout, Control: g.control}
}

func (g *Guard) control(_, address string, _ syscall.RawConn) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil || !g.Allowed(ap.Addr()) {
		return ErrNotAllowed
	}
	return nil
}
