package outbound

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"time"
)

// DefaultTimeout bounds one request from dial to the end of the body.
const DefaultTimeout = 10 * time.Second

// ErrRedirect is returned for any 3xx: redirects are never followed, the
// response is a failed delivery.
var ErrRedirect = errors.New("the endpoint answered with a redirect, which is not followed")

// Resolver looks names up; *net.Resolver implements it.
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// NewClient returns an http.Client that dials only addresses p admits,
// never follows redirects, ignores proxy environment variables (a proxy
// would dial on our behalf, past the check) and gives up after timeout
// (DefaultTimeout when zero). r nil means net.DefaultResolver.
func NewClient(p Policy, timeout time.Duration, r Resolver) *http.Client {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	if r == nil {
		r = net.DefaultResolver
	}
	d := &dialer{policy: p, resolver: r, net: &net.Dialer{Timeout: timeout}}
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           d.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          10,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   timeout,
		ResponseHeaderTimeout: timeout,
	}
	return &http.Client{
		Transport: transport,
		Timeout:   timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return ErrRedirect
		},
	}
}

type dialer struct {
	policy   Policy
	resolver Resolver
	net      *net.Dialer
}

// DialContext resolves host, refuses the destination when ANY resolved
// address is blocked (a name that also points at loopback is not a
// receiver worth trusting), and dials the first checked address itself —
// never the name, so a second DNS answer cannot slip in.
func (d *dialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	port, err := strconv.ParseUint(portStr, 10, 16)
	if err != nil {
		return nil, fmt.Errorf("invalid port %q", portStr)
	}
	var addrs []netip.Addr
	if a, perr := netip.ParseAddr(host); perr == nil {
		addrs = []netip.Addr{a}
	} else {
		addrs, err = d.resolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, fmt.Errorf("resolve %s: %w", host, err)
		}
		if len(addrs) == 0 {
			return nil, fmt.Errorf("resolve %s: no addresses", host)
		}
	}
	for _, a := range addrs {
		if err := d.policy.Check(a); err != nil {
			return nil, err
		}
	}
	target := netip.AddrPortFrom(addrs[0].Unmap(), uint16(port))
	return d.net.DialContext(ctx, network, target.String())
}
