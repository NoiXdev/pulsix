package dns

import (
	"errors"
	"net"
	"sync"

	"github.com/noixdev/pulsix/internal/probe"
)

// errServerMisbehaving is the text Go's resolver gives a DNSError when the
// server answered with an error code other than NXDOMAIN (SERVFAIL,
// REFUSED, NOTIMP, FORMERR); the resolver exports no value for it.
const errServerMisbehaving = "server misbehaving"

// lookupFailure is the failure label of a lookup that failed with err (nil
// when it succeeded without an answer); connLabel is how talking to the
// server failed, "" when it did not or nothing watched it.
//
// A name that does not exist and a name without a record of the type are
// both FailureNXDomain: Go's resolver reports both as not found.
func lookupFailure(err error, connLabel string) string {
	if err == nil {
		return probe.FailureNXDomain
	}
	de, ok := errors.AsType[*net.DNSError](err)
	if !ok {
		return probe.Classify(err)
	}
	switch {
	case de.IsNotFound:
		return probe.FailureNXDomain
	case de.Err == errServerMisbehaving:
		return probe.FailureServFail
	case connLabel != "":
		return connLabel
	case de.IsTimeout:
		return probe.FailureTimeout
	}
	// A deadline the resolver wrapped, or FailureError.
	if u := de.Unwrap(); u != nil {
		return probe.Classify(u)
	}
	return probe.FailureError
}

// ioFailure notes the last failure of the connections to a dns server as
// a label. The resolver may use several connections, one after another.
// Only labels of the dns vocabulary are noted (refused, unreachable,
// timeout): a reset or early close of the TCP fallback, or a failure
// Classify does not know (a connection the resolver closed), is not, so
// it cannot hide one that is.
type ioFailure struct {
	mu   sync.Mutex
	last string
}

func (f *ioFailure) note(err error) {
	if err == nil {
		return
	}
	l := probe.Classify(err)
	if l != probe.FailureRefused && l != probe.FailureUnreachable && l != probe.FailureTimeout {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.last = l
}

func (f *ioFailure) label() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.last
}

// watch wraps conn so its read and write failures are noted. A packet
// connection stays one: the resolver frames its queries by that.
func (f *ioFailure) watch(conn net.Conn) net.Conn {
	w := watchedConn{Conn: conn, f: f}
	if pc, ok := conn.(net.PacketConn); ok {
		return watchedPacketConn{watchedConn: w, pc: pc}
	}
	return w
}

type watchedConn struct {
	net.Conn
	f *ioFailure
}

func (c watchedConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	c.f.note(err)
	return n, err
}

func (c watchedConn) Write(b []byte) (int, error) {
	n, err := c.Conn.Write(b)
	c.f.note(err)
	return n, err
}

type watchedPacketConn struct {
	watchedConn
	pc net.PacketConn
}

func (c watchedPacketConn) ReadFrom(b []byte) (int, net.Addr, error) {
	n, addr, err := c.pc.ReadFrom(b)
	c.f.note(err)
	return n, addr, err
}

func (c watchedPacketConn) WriteTo(b []byte, addr net.Addr) (int, error) {
	n, err := c.pc.WriteTo(b, addr)
	c.f.note(err)
	return n, err
}
