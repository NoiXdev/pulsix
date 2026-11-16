package dns

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"sync/atomic"
	"time"

	"github.com/noixdev/pulsix/internal/probe"
)

const (
	dnsDefaultTimeout = 5 * time.Second
	dnsDefaultPort    = 53
)

// errNameIsAddress: a dns A or AAAA check of an IP literal asks no
// server, so it cannot be attempted.
var errNameIsAddress = errors.New("name is an address")

// dialerFor returns the dialer for one connection; production passes the
// guard's Dialer, tests a dialer that refuses where the guard's up-front
// check does not.
type dialerFor func(timeout time.Duration) *net.Dialer

// probeDNS resolves name (default: the host's address) for one record type,
// through server when one is set and the manager's resolver otherwise.
func probeDNS(ctx context.Context, g *probe.Guard, params map[string]any, address string, dial dialerFor) []probe.Observation {
	expect := probe.StrParam(params, "expect", "")
	metrics := []string{"dns.up", "dns.failure", "dns.resolve_ms"}
	if expect != "" {
		metrics = append(metrics, "dns.answer_match")
	}

	name := probe.StrParam(params, "name", probe.AddressPlaceholder)
	if strings.Contains(name, probe.AddressPlaceholder) {
		host, err := probe.TargetHost(address)
		if err != nil {
			return probe.FailAll(err.Error(), metrics...)
		}
		name = strings.ReplaceAll(name, probe.AddressPlaceholder, host)
	}

	typ := probe.StrParam(params, "type", "A")
	lookup, ok := dnsLookupFor(typ)
	if !ok {
		return probe.FailAll("unsupported record type", metrics...)
	}
	// The resolver answers an A or AAAA lookup of an IP literal itself,
	// without asking any server, so such a check could never fail.
	if _, err := netip.ParseAddr(name); err == nil && (typ == "A" || typ == "AAAA") {
		return probe.FailAll(errNameIsAddress.Error(), metrics...)
	}

	resolver := net.DefaultResolver
	// refused records a dial the guard refused: Go's resolver reports it
	// only as a failed lookup, not as probe.ErrNotAllowed.
	var refused atomic.Bool
	// connFailed records why talking to the server failed: Go's resolver
	// keeps only the text of a connection's error.
	var connFailed ioFailure
	if server := probe.StrParam(params, "server", ""); server != "" {
		ap, err := parseDNSServer(server)
		if err != nil {
			return probe.FailAll(err.Error(), metrics...)
		}
		if !g.Allowed(ap.Addr()) {
			return probe.FailAll(probe.ErrNotAllowed.Error(), metrics...)
		}
		resolver = guardedResolver(dial, ap, &refused, &connFailed)
	}

	ctx, cancel := probe.Bound(ctx, params, dnsDefaultTimeout)
	defer cancel()

	start := time.Now()
	answers, err := lookup(ctx, resolver, name)
	elapsed := time.Since(start)
	if refused.Load() {
		return probe.FailAll(probe.ErrNotAllowed.Error(), metrics...)
	}
	if err != nil || len(answers) == 0 {
		return probe.Down("dns", lookupFailure(err, connFailed.label()))
	}
	// An answer that does not match is no failure: the name resolved.
	obs := []probe.Observation{probe.Value("dns.up", 1), probe.Up("dns"), probe.Value("dns.resolve_ms", probe.Millis(elapsed))}
	if expect != "" {
		obs = append(obs, probe.BoolValue("dns.answer_match", answerMatches(answers, expect)))
	}
	return obs
}

// guardedResolver sends every query to server, through the guard's dialer,
// and sets refused when that dialer refuses. The guard was already asked
// about server; the dial-time check is the backstop. Each connection's
// failures are noted in failed.
func guardedResolver(dial dialerFor, server netip.AddrPort, refused *atomic.Bool, failed *ioFailure) *net.Resolver {
	return &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			conn, err := dial(0).DialContext(ctx, network, server.String())
			if errors.Is(err, probe.ErrNotAllowed) {
				refused.Store(true)
			}
			if err != nil {
				failed.note(err)
				return nil, err
			}
			return failed.watch(conn), nil
		},
	}
}

type dnsLookup func(ctx context.Context, r *net.Resolver, name string) ([]string, error)

// dnsLookupFor returns the lookup for a record type. A and AAAA query only
// their own family, so a server that ignores the other family's query does
// not stall the probe until its timeout.
func dnsLookupFor(typ string) (dnsLookup, bool) {
	switch typ {
	case "A":
		return func(ctx context.Context, r *net.Resolver, name string) ([]string, error) {
			return lookupIP(ctx, r, "ip4", name)
		}, true
	case "AAAA":
		return func(ctx context.Context, r *net.Resolver, name string) ([]string, error) {
			return lookupIP(ctx, r, "ip6", name)
		}, true
	case "CNAME":
		return func(ctx context.Context, r *net.Resolver, name string) ([]string, error) {
			cname, err := r.LookupCNAME(ctx, name)
			if err != nil || cname == "" {
				return nil, err
			}
			return []string{cname}, nil
		}, true
	case "MX":
		return func(ctx context.Context, r *net.Resolver, name string) ([]string, error) {
			mxs, err := r.LookupMX(ctx, name)
			out := make([]string, 0, len(mxs))
			for _, mx := range mxs {
				out = append(out, mx.Host)
			}
			return out, err
		}, true
	case "TXT":
		return func(ctx context.Context, r *net.Resolver, name string) ([]string, error) {
			return r.LookupTXT(ctx, name)
		}, true
	case "NS":
		return func(ctx context.Context, r *net.Resolver, name string) ([]string, error) {
			nss, err := r.LookupNS(ctx, name)
			out := make([]string, 0, len(nss))
			for _, ns := range nss {
				out = append(out, ns.Host)
			}
			return out, err
		}, true
	}
	return nil, false
}

func lookupIP(ctx context.Context, r *net.Resolver, network, name string) ([]string, error) {
	ips, err := r.LookupNetIP(ctx, network, name)
	out := make([]string, 0, len(ips))
	for _, ip := range ips {
		out = append(out, ip.Unmap().String())
	}
	return out, err
}

// parseDNSServer parses the server param: an IP with an optional port.
func parseDNSServer(s string) (netip.AddrPort, error) {
	if ap, err := netip.ParseAddrPort(s); err == nil {
		if ap.Port() == 0 {
			return netip.AddrPort{}, fmt.Errorf("invalid dns server %q", s)
		}
		return ap, nil
	}
	ip, err := netip.ParseAddr(strings.TrimSuffix(strings.TrimPrefix(s, "["), "]"))
	if err != nil {
		return netip.AddrPort{}, fmt.Errorf("invalid dns server %q", s)
	}
	return netip.AddrPortFrom(ip, dnsDefaultPort), nil
}

// answerMatches reports whether one answer equals expect, compared
// case-insensitively without a trailing dot; addresses compare as addresses,
// so any spelling of the same IPv6 address matches.
func answerMatches(answers []string, expect string) bool {
	want := normalizeAnswer(expect)
	wantIP, wantErr := netip.ParseAddr(want)
	for _, a := range answers {
		got := normalizeAnswer(a)
		if got == want {
			return true
		}
		if wantErr == nil {
			if ip, err := netip.ParseAddr(got); err == nil && ip.Unmap() == wantIP.Unmap() {
				return true
			}
		}
	}
	return false
}

func normalizeAnswer(s string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(s)), ".")
}

// Probe is the dns probe, as the registry runs it.
func Probe(ctx context.Context, g *probe.Guard, params map[string]any, address string, _ map[string]string) []probe.Observation {
	return probeDNS(ctx, g, params, address, g.Dialer)
}
