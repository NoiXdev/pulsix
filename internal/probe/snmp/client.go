package snmp

import (
	"context"
	"errors"
	"net/netip"
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
	close()
}

// dialFunc opens a session to target. timeout bounds each request; ctx
// bounds the whole session.
type dialFunc func(ctx context.Context, g *probe.Guard, target netip.AddrPort, timeout time.Duration, c credentials) (client, error)

// gosnmpClient is a client over gosnmp.
type gosnmpClient struct {
	x        *gosnmp.GoSNMP
	stop     func() bool
	received *atomic.Bool
}

// dialDevice opens a UDP session to target, an address the guard already
// allowed; the guard's dial-time check runs again on the socket as a
// backstop. Each request waits timeout and is sent retries more times;
// ctx's deadline cuts that short, and cancelling ctx unblocks a pending
// read.
func dialDevice(ctx context.Context, g *probe.Guard, target netip.AddrPort, timeout time.Duration, c credentials) (client, error) {
	received := new(atomic.Bool)
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
		OnRecv:         func(*gosnmp.GoSNMP) { received.Store(true) },
	}
	if c.version == gosnmp.Version3 {
		x.SecurityModel = gosnmp.UserSecurityModel
		x.MsgFlags = c.flags
		x.SecurityParameters = c.usm
	}
	if err := x.Connect(); err != nil {
		return nil, err
	}
	conn := x.Conn
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Unix(1, 0)) })
	return &gosnmpClient{x: x, stop: stop, received: received}, nil
}

func (c *gosnmpClient) get(oids []string) (*gosnmp.SnmpPacket, error) { return c.x.Get(oids) }

func (c *gosnmpClient) walk(root string, fn gosnmp.WalkFunc) error { return c.x.BulkWalk(root, fn) }

func (c *gosnmpClient) answered() bool { return c.received.Load() }

func (c *gosnmpClient) close() {
	c.stop()
	_ = c.x.Close()
}

// failure is how a failed dial or first request is reported.
type failure int

const (
	// failDown: the device did not answer; snmp.up = 0.
	failDown failure = iota
	// failRefused: the guard refused the target; an error on every metric.
	failRefused
	// failCredentials: a v3 device answered, but refused the user, its
	// digest or its security level, or could not decrypt the request; an
	// error on every metric naming the secret.
	failCredentials
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
// discovery reads the same way, for that one poll.
func classifyFirst(err error, v3, answered bool) failure {
	f := classify(err)
	if f == failDown && v3 && answered {
		return failCredentials
	}
	return f
}
