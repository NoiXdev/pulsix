// Package clientip resolves the address a request came from. Only when the
// TCP peer is a configured trusted proxy (PULSIX_TRUSTED_PROXIES) is
// X-Forwarded-For consulted; then the client is the right-most entry that
// is not itself a trusted proxy. Everything to the left of that entry is
// client-controlled and never trusted (spec 2026-09-18 §3 "Client IP").
package clientip

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// Resolver holds the trusted proxy ranges. The zero value trusts nothing.
type Resolver struct {
	trusted []netip.Prefix
}

// New returns a Resolver trusting the given ranges.
func New(trusted []netip.Prefix) *Resolver {
	return &Resolver{trusted: append([]netip.Prefix(nil), trusted...)}
}

// Resolve returns the client address for a TCP peer (host:port or a bare
// address) and the X-Forwarded-For header lines in order of appearance.
// The result is invalid only when the peer itself is unparsable.
func (r *Resolver) Resolve(remoteAddr string, forwardedFor []string) netip.Addr {
	peer := parseAddr(remoteAddr)
	if !peer.IsValid() || !r.trustedAddr(peer) {
		return peer
	}
	var hops []string
	for _, line := range forwardedFor {
		for _, part := range strings.Split(line, ",") {
			if p := strings.TrimSpace(part); p != "" {
				hops = append(hops, p)
			}
		}
	}
	client := peer
	for i := len(hops) - 1; i >= 0; i-- {
		a := parseAddr(hops[i])
		if !a.IsValid() {
			// A malformed hop ends the walk: the nearest address seen so
			// far is the best non-spoofable answer.
			return client
		}
		client = a
		if !r.trustedAddr(a) {
			return a
		}
	}
	return client
}

// FromRequest resolves the client address of an HTTP request.
func (r *Resolver) FromRequest(req *http.Request) netip.Addr {
	return r.Resolve(req.RemoteAddr, req.Header.Values("X-Forwarded-For"))
}

func (r *Resolver) trustedAddr(a netip.Addr) bool {
	for _, p := range r.trusted {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// parseAddr accepts "ip", "ip:port" and "[ipv6]:port" and unmaps
// IPv4-mapped IPv6 addresses.
func parseAddr(s string) netip.Addr {
	s = strings.TrimSpace(s)
	if ap, err := netip.ParseAddrPort(s); err == nil {
		return ap.Addr().Unmap()
	}
	if host, _, err := net.SplitHostPort(s); err == nil {
		s = host
	}
	a, err := netip.ParseAddr(strings.TrimSuffix(strings.TrimPrefix(s, "["), "]"))
	if err != nil {
		return netip.Addr{}
	}
	return a.Unmap()
}

// ParsePrefixes parses a comma-separated list of CIDRs or bare addresses.
// A prefix with host bits set is refused as a likely typo.
func ParsePrefixes(raw string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, part := range strings.Split(raw, ",") {
		p := strings.TrimSpace(part)
		if p == "" {
			continue
		}
		if !strings.Contains(p, "/") {
			a, err := netip.ParseAddr(p)
			if err != nil {
				return nil, fmt.Errorf("%q is neither an address nor a CIDR", p)
			}
			out = append(out, netip.PrefixFrom(a.Unmap(), a.Unmap().BitLen()))
			continue
		}
		pre, err := netip.ParsePrefix(p)
		if err != nil {
			return nil, fmt.Errorf("%q is not a CIDR: %v", p, err)
		}
		if pre.Masked() != pre {
			return nil, fmt.Errorf("%q has host bits set; did you mean %s?", p, pre.Masked())
		}
		out = append(out, pre)
	}
	return out, nil
}
