package snmp

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gosnmp/gosnmp"

	"github.com/noixdev/pulsix/internal/probe"
)

const (
	// retries is how often a request is sent again before it counts as
	// unanswered.
	retries = 1
	// maxRepetitions is GetBulk's rows per request while walking a column:
	// below gosnmp's 50, which some devices answer with tooBig.
	maxRepetitions = 25
)

// client is the part of an SNMP session the probe uses; tests answer it
// from tables.
type client interface {
	get(oids []string) (*gosnmp.SnmpPacket, error)
	walk(root string, fn gosnmp.WalkFunc) error
	// answered reports whether any packet came back from the device.
	answered() bool
	// roundTrip is the time from the last request sent to the last answer
	// received: after a Get, that request's own round trip, without the
	// v3 engine discovery gosnmp may have sent before it. 0 when no
	// answer came.
	roundTrip() time.Duration
	// socketFailure is the failure label of the session socket's last
	// error that was neither a timeout nor unknown (a refused or
	// unreachable request), "" when there was none. gosnmp returns only
	// its last error, and a retry that timed out would hide the ICMP port
	// unreachable the request before it got.
	socketFailure() string
	close()
}

// dialFunc opens a session to target. timeout bounds each request; ctx
// bounds the whole session.
type dialFunc func(ctx context.Context, g *probe.Guard, target netip.AddrPort, timeout time.Duration, c credentials) (client, error)

// gosnmpClient is a client over gosnmp.
type gosnmpClient struct {
	x    *gosnmp.GoSNMP
	conn *watchedConn
	stop func() bool
	// timing is written by gosnmp's send hooks, which run on the
	// goroutine calling get, and read by that goroutine after it.
	timing *timing
}

// watchedConn is the session's socket, noting its failures for
// socketFailure. A plain net.Conn, not a PacketConn: gosnmp then reads
// and writes the connected socket with Read and Write.
type watchedConn struct {
	net.Conn
	mu      sync.Mutex
	failure string
}

func (c *watchedConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	c.note(err)
	return n, err
}

func (c *watchedConn) Write(b []byte) (int, error) {
	n, err := c.Conn.Write(b)
	c.note(err)
	return n, err
}

func (c *watchedConn) note(err error) {
	if err == nil {
		return
	}
	l := probe.Classify(err)
	if l == probe.FailureTimeout || l == probe.FailureError {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.failure = l
}

func (c *watchedConn) last() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.failure
}

// timing records the session's sends and receives.
type timing struct {
	sent     time.Time
	rtt      time.Duration
	received atomic.Bool
}

// dialDevice opens a UDP session to target, an address the guard already
// allowed; the guard's dial-time check runs again on the socket as a
// backstop. Each request waits timeout and is sent retries more times;
// ctx's deadline cuts that short (gosnmp clamps each read's deadline to
// it), and cancelling ctx closes the socket, which ends a pending read at
// once — a deadline set into the past could be overwritten by gosnmp's
// next per-request deadline.
func dialDevice(ctx context.Context, g *probe.Guard, target netip.AddrPort, timeout time.Duration, c credentials) (client, error) {
	tm := new(timing)
	x := &gosnmp.GoSNMP{
		Target:         target.Addr().String(),
		Port:           target.Port(),
		Transport:      "udp",
		Version:        c.version,
		Community:      c.community,
		Context:        ctx,
		Timeout:        timeout,
		Retries:        retries,
		MaxOids:        oidsPerRequest,
		MaxRepetitions: maxRepetitions,
		Control:        g.Dialer(0).Control,
		OnSent:         func(*gosnmp.GoSNMP) { tm.sent = time.Now() },
		OnRecv: func(*gosnmp.GoSNMP) {
			tm.rtt = time.Since(tm.sent)
			tm.received.Store(true)
		},
	}
	if c.version == gosnmp.Version3 {
		x.SecurityModel = gosnmp.UserSecurityModel
		x.MsgFlags = c.flags
		x.SecurityParameters = c.usm
	}
	if err := x.Connect(); err != nil {
		return nil, err
	}
	conn := &watchedConn{Conn: x.Conn}
	x.Conn = conn
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	return &gosnmpClient{x: x, conn: conn, stop: stop, timing: tm}, nil
}

func (c *gosnmpClient) get(oids []string) (*gosnmp.SnmpPacket, error) { return c.x.Get(oids) }

func (c *gosnmpClient) walk(root string, fn gosnmp.WalkFunc) error { return c.x.BulkWalk(root, fn) }

func (c *gosnmpClient) answered() bool { return c.timing.received.Load() }

func (c *gosnmpClient) roundTrip() time.Duration { return c.timing.rtt }

func (c *gosnmpClient) socketFailure() string { return c.conn.last() }

// close ends the session; the socket may already be closed by a cancel,
// so gosnmp's second close is ignored.
func (c *gosnmpClient) close() {
	c.stop()
	_ = c.x.Close()
}

// failure is how a failed dial or first request is reported.
type failure int

const (
	// failDown: the device did not answer; snmp.up = 0 with the reason.
	failDown failure = iota
	// failRefused: the guard refused the target; an error on every metric.
	failRefused
	// failCredentials: a v3 device answered, but refused the user, its
	// digest or its security level, or could not decrypt the request;
	// snmp.up = 0 with probe.FailureAuth.
	failCredentials
	// failAuthOrLost: a v3 device answered the engine discovery, but the
	// first request failed with no refusal gosnmp names; snmp.up = 0 with
	// probe.FailureAuthOrLost once one more attempt failed the same way.
	failAuthOrLost
)

// classify sorts a gosnmp error. Its text is never used: it can quote a
// whole packet, community included.
func classify(err error) failure {
	switch {
	case errors.Is(err, probe.ErrNotAllowed):
		return failRefused
	case errors.Is(err, gosnmp.ErrUnknownUsername), errors.Is(err, gosnmp.ErrWrongDigest),
		errors.Is(err, gosnmp.ErrUnknownSecurityLevel), errors.Is(err, gosnmp.ErrDecryption):
		return failCredentials
	}
	return failDown
}

// classifyFirst sorts the error of a session's first request. A v3 device
// that refuses a user or a digest answers with an unauthenticated report,
// which gosnmp discards as not authentic once it knows the device's engine
// (it names the refusal only during discovery), and net-snmp drops a
// request it cannot decrypt (a wrong priv_password) without a report
// gosnmp accepts, so the request times out. Either way the device answered
// the engine discovery: a v3 first request that failed although the device
// answered something is credentials it did not accept, not a device that
// is down. Losing a request and its retry right after a successful
// discovery reads the same way, so that case is failAuthOrLost, not
// failCredentials: the poll sends the request once more before reporting
// it, and names both possible causes (credentials not accepted, or the
// requests were lost). A refusal gosnmp names stays failCredentials.
func classifyFirst(err error, v3, answered bool) failure {
	f := classify(err)
	if f == failDown && v3 && answered {
		return failAuthOrLost
	}
	return f
}
