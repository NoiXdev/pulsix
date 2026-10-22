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
// receiver worth trusting), and dials the checked addresses itself —
// never the name, so a second DNS answer cannot slip in.
//
// It tries each address in turn and returns the first connection that
// succeeds (backlog 2.79). Falling back widens nothing: the loop above
// has already refused the whole destination unless EVERY resolved address
// passed the policy, so a later address was as allowed as the first. Only
// dialling addrs[0] made a receiver whose name carries an AAAA record
// this manager cannot reach and an A record it can simply unreachable.
// When none connects, the error names all of them, so a delivery's last
// error shows the whole picture instead of one arbitrary address.
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
	var errs []error
	for _, a := range addrs {
		target := netip.AddrPortFrom(a.Unmap(), uint16(port))
		conn, derr := d.net.DialContext(ctx, network, target.String())
		if derr == nil {
			return conn, nil
		}
		errs = append(errs, derr)
		// A cancelled or timed-out context is about this call, not about
		// the address: the remaining ones would fail the same way, and
		// trying them buys nothing but delay on a shutdown.
		if ctx.Err() != nil {
			break
		}
	}
	return nil, errors.Join(errs...)
}
