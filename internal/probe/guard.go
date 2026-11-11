package probe

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"syscall"
	"time"

	"github.com/noixdev/pulsix/internal/outbound"
)

// ErrNotAllowed is the dial error for a target the guard refuses. A probe
// reports it as a collection error, never as up = 0, so the manager cannot be
// used to tell open ports from closed ones on addresses it must not reach.
var ErrNotAllowed = errors.New("target address not allowed")

// Guard decides whether an IP may be dialled. It is the notification
// egress policy (internal/outbound: loopback, link-local, unspecified,
// multicast, broadcast and cloud metadata addresses, seen through every
// IPv4-in-IPv6 form it knows, including the operator's own NAT64 prefixes)
// plus the operator's extra prefixes (PULSIX_PROBE_DENY_CIDRS). Unlike a
// webhook, a probe has no allowlist: PULSIX_NOTIFY_ALLOW_CIDRS does not
// apply to it.
type Guard struct {
	policy outbound.Policy
	deny   []netip.Prefix
}

// NewGuard returns the production guard: the outbound policy, seeing
// through nat64 (PULSIX_NOTIFY_NAT64_PREFIXES), plus extra
// (PULSIX_PROBE_DENY_CIDRS).
func NewGuard(extra, nat64 []netip.Prefix) *Guard {
	return &Guard{policy: outbound.NewPolicy(nil, nat64), deny: clonePrefixes(extra)}
}

// NewGuardForTest returns a guard that may also allow loopback (127.0.0.0/8
// and ::1 on the policy's allowlist), so tests can probe their own
// listeners. Every other refused range stays refused.
func NewGuardForTest(allowLoopback bool, extra []netip.Prefix) *Guard {
	var allow []netip.Prefix
	if allowLoopback {
		allow = []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("::1/128")}
	}
	return &Guard{policy: outbound.NewPolicy(allow, nil), deny: clonePrefixes(extra)}
}

func clonePrefixes(in []netip.Prefix) []netip.Prefix {
	out := make([]netip.Prefix, 0, len(in))
	for _, p := range in {
		out = append(out, canonicalPrefix(p))
	}
	return out
}

// canonicalPrefix masks p and turns an IPv4-mapped IPv6 prefix of at least
// /96 (::ffff:10.0.0.0/104) into its IPv4 form (10.0.0.0/8): Allowed
// judges a mapped address as IPv4, so the mapped form would never match.
func canonicalPrefix(p netip.Prefix) netip.Prefix {
	if p.Addr().Is4In6() && p.Bits() >= 96 {
		p = netip.PrefixFrom(p.Addr().Unmap(), p.Bits()-96)
	}
	return p.Masked()
}

// ParseCIDRs parses a comma-separated CIDR list (PULSIX_PROBE_DENY_CIDRS).
// Whitespace around entries is trimmed and empty entries are skipped; an
// invalid entry is an error naming it. An IPv4-mapped entry of /96 or
// longer is stored in its IPv4 form.
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
		out = append(out, canonicalPrefix(p))
	}
	return out, nil
}

// Allowed reports whether ip may be dialled. An IPv4-mapped IPv6 address is
// judged as the IPv4 address it carries. A zoned address (fe80::1%eth0) is
// refused: the policy refuses every zoned address, because no prefix can
// match one. Only the boolean leaves the guard; the policy's own error
// names the address and an environment variable and must never reach an
// observation, which carries ErrNotAllowed's fixed text instead.
func (g *Guard) Allowed(ip netip.Addr) bool {
	if !ip.IsValid() || g.policy.Check(ip) != nil {
		return false
	}
	ip = ip.Unmap()
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
