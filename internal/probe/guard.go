package probe

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/noixdev/pulsix/internal/outbound"
)

// ErrNotAllowed is the dial error for a target the guard refuses. A probe
// reports it as a collection error, never as up = 0, so the manager cannot be
// used to tell open ports from closed ones on addresses it must not reach.
var ErrNotAllowed = errors.New("target address not allowed")

// ErrLocalPolicy is the dial error for a target an agent's own deny list
// refuses (agent.yaml probes.deny_cidrs, backlog 3.82). It is an
// ErrNotAllowed, so everything that treats a refusal as a collection error
// still does; its fixed text tells the manager, and whoever reads the
// vantage's result, that this agent refused — never which range matched.
var ErrLocalPolicy = fmt.Errorf("%w by the agent's local policy", ErrNotAllowed)

// RefusalText is the observation text of a guard refusal err:
// ErrLocalPolicy's when err is one, ErrNotAllowed's otherwise. Neither
// quotes an address.
func RefusalText(err error) string {
	if errors.Is(err, ErrLocalPolicy) {
		return ErrLocalPolicy.Error()
	}
	return ErrNotAllowed.Error()
}

// Refusal records whether any of a probe's dials was refused by the guard,
// for the probes whose library does not keep the dial error in what it
// returns (net/http, Go's resolver). A local refusal among them makes the
// text the local one. The zero value is ready and safe for concurrent use.
type Refusal struct {
	refused, local atomic.Bool
}

// Note records err when it is a guard refusal; any other error, and nil,
// is ignored.
func (r *Refusal) Note(err error) {
	if !errors.Is(err, ErrNotAllowed) {
		return
	}
	if errors.Is(err, ErrLocalPolicy) {
		r.local.Store(true)
	}
	r.refused.Store(true)
}

// Refused reports whether a refusal was noted.
func (r *Refusal) Refused() bool { return r.refused.Load() }

// Text is the observation text of the noted refusals (RefusalText).
func (r *Refusal) Text() string {
	if r.local.Load() {
		return ErrLocalPolicy.Error()
	}
	return ErrNotAllowed.Error()
}

// Guard decides whether an IP may be dialled. It is the notification
// egress policy (internal/outbound: loopback, link-local, unspecified,
// multicast, broadcast and cloud metadata addresses, seen through every
// IPv4-in-IPv6 form it knows, including the operator's own NAT64 prefixes)
// plus the operator's extra prefixes (PULSIX_PROBE_DENY_CIDRS). Unlike a
// webhook, a probe has no allowlist: PULSIX_NOTIFY_ALLOW_CIDRS does not
// apply to it.
//
// It also carries whether the manager's operator allows probes that may
// change something on their target (PULSIX_PROBE_WRITE_METHODS, backlog
// 3.84: an http check's POST, PUT, PATCH, DELETE or body); off unless
// WithWrites turns it on, so an agent's guard never allows them.
//
// On an agent it also carries the host's own deny list (agent.yaml
// probes.deny_cidrs, backlog 3.82, WithLocalDeny), whose refusal is
// ErrLocalPolicy rather than ErrNotAllowed. Both deny lists judge an
// IPv4-in-IPv6 address by the IPv4 address it embeds too, as the policy
// does.
type Guard struct {
	policy outbound.Policy
	deny   []netip.Prefix
	local  []netip.Prefix
	writes bool
}

// WithWrites returns a copy of g that allows state-changing requests when
// on is true, and refuses them when false; g itself is unchanged.
func (g *Guard) WithWrites(on bool) *Guard {
	c := g.clone()
	c.writes = on
	return c
}

// WithLocalDeny returns a copy of g that also refuses the addresses in
// local, with ErrLocalPolicy; g itself is unchanged. Prefixes are stored
// as ParseCIDRs stores them (masked, a mapped /96 or longer as IPv4).
func (g *Guard) WithLocalDeny(local []netip.Prefix) *Guard {
	c := g.clone()
	c.local = clonePrefixes(local)
	return c
}

func (g *Guard) clone() *Guard {
	c := *g
	c.deny = clonePrefixes(g.deny)
	c.local = clonePrefixes(g.local)
	return &c
}

// WritesAllowed reports whether g allows state-changing requests; a nil
// guard does not.
func (g *Guard) WritesAllowed() bool { return g != nil && g.writes }

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
	return g.Check(ip) == nil
}

// Check is Allowed with the reason: nil, ErrNotAllowed for the policy and
// the operator's prefixes, or ErrLocalPolicy for the agent's own deny list
// (the first two win where both refuse). Neither error names the address.
func (g *Guard) Check(ip netip.Addr) error {
	if !ip.IsValid() || g.policy.Check(ip) != nil {
		return ErrNotAllowed
	}
	ip = ip.Unmap()
	embedded, hasEmbedded := g.policy.EmbeddedIPv4(ip)
	denied := func(list []netip.Prefix) bool {
		for _, p := range list {
			if p.Contains(ip) || (hasEmbedded && p.Contains(embedded)) {
				return true
			}
		}
		return false
	}
	switch {
	case denied(g.deny):
		return ErrNotAllowed
	case denied(g.local):
		return ErrLocalPolicy
	}
	return nil
}

// Dialer returns a dialer that checks the resolved IP of every connection
// attempt and refuses a disallowed one with ErrNotAllowed (ErrLocalPolicy
// for the agent's own deny list, Check). Because the check
// runs at dial time, it covers DNS answers, redirects and rebinding.
func (g *Guard) Dialer(timeout time.Duration) *net.Dialer {
	return &net.Dialer{Timeout: timeout, Control: g.control}
}

func (g *Guard) control(_, address string, _ syscall.RawConn) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return ErrNotAllowed
	}
	return g.Check(ap.Addr())
}

// ResolveAllowed returns the address to dial for host: host itself when it
// is an IP literal (unmapped), else the first address it resolves to that
// the guard allows, IPv4 preferred. It returns ErrNotAllowed when the guard
// refuses every candidate — ErrLocalPolicy when the agent's own deny list
// refused one of them (Check) — and the resolver's error when host does not
// resolve. Probes that do not dial through Dialer (icmp, snmp) check their
// target with it before sending anything.
func ResolveAllowed(ctx context.Context, g *Guard, host string) (netip.Addr, error) {
	if ip, err := netip.ParseAddr(host); err == nil {
		if err := g.Check(ip); err != nil {
			return netip.Addr{}, err
		}
		return ip.Unmap(), nil
	}
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return netip.Addr{}, err
	}
	var v6 netip.Addr
	refusal := ErrNotAllowed
	for _, ip := range ips {
		ip = ip.Unmap()
		if err := g.Check(ip); err != nil {
			if errors.Is(err, ErrLocalPolicy) {
				refusal = err
			}
			continue
		}
		if ip.Is4() {
			return ip, nil
		}
		if !v6.IsValid() {
			v6 = ip
		}
	}
	if v6.IsValid() {
		return v6, nil
	}
	return netip.Addr{}, refusal
}
