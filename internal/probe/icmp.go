package probe

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"math"
	"net"
	"net/netip"
	"syscall"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

const (
	icmpDefaultTimeout = 5 * time.Second
	icmpDefaultCount   = 3
	icmpMaxCount       = 10
	// icmpMaxGap paces the echo requests; it shrinks so all of them are
	// sent within the first half of the timeout.
	icmpMaxGap  = 100 * time.Millisecond
	protoICMP   = 1
	protoICMPv6 = 58
	// icmpNotPermitted is the collection error when the kernel refuses an
	// unprivileged ICMP socket.
	icmpNotPermitted = "icmp socket not permitted: the kernel setting net.ipv4.ping_group_range must include the manager's group"
)

// packetConn is the part of an ICMP socket the probe uses; tests inject a
// fake through icmpOpener.
type packetConn interface {
	ReadFrom(b []byte) (int, net.Addr, error)
	WriteTo(b []byte, dst net.Addr) (int, error)
	SetReadDeadline(t time.Time) error
	Close() error
}

// icmpOpener opens an ICMP socket; network is "udp4" or "udp6".
type icmpOpener func(network, address string) (packetConn, error)

// listenICMP opens an unprivileged datagram ICMP socket, which needs no
// CAP_NET_RAW.
func listenICMP(network, address string) (packetConn, error) {
	c, err := icmp.ListenPacket(network, address)
	if err != nil {
		return nil, err
	}
	return c, nil
}

// probeICMP sends count echo requests to address and reports how many came
// back and their average round trip.
func probeICMP(ctx context.Context, g *Guard, params map[string]any, address string, open icmpOpener) []Observation {
	metrics := []string{"icmp.up", "icmp.loss_pct", "icmp.rtt_ms"}

	host, err := targetHost(address)
	if err != nil {
		return failAll(err.Error(), metrics...)
	}
	count := numParam(params, "count", icmpDefaultCount)
	if count < 1 || count > icmpMaxCount || count != math.Trunc(count) {
		return failAll("invalid count", metrics...)
	}

	ctx, cancel := bound(ctx, params, icmpDefaultTimeout)
	defer cancel()

	target, err := resolveAllowed(ctx, g, host)
	if errors.Is(err, ErrNotAllowed) {
		return failAll(ErrNotAllowed.Error(), metrics...)
	}
	if err != nil {
		return down("icmp")
	}

	r := newEchoRound(target, int(count))
	conn, err := open(r.network, r.listen)
	if err != nil {
		return failAll(icmpSocketError(err), metrics...)
	}
	defer func() { _ = conn.Close() }()
	// Closing the socket wakes a blocked read at once on cancel.
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	r.run(ctx, conn)
	if r.replies == 0 {
		return down("icmp")
	}
	return []Observation{
		value("icmp.up", 1),
		value("icmp.loss_pct", float64(len(r.sent)-r.replies)*100/float64(len(r.sent))),
		value("icmp.rtt_ms", millis(r.rtt)/float64(r.replies)),
	}
}

// resolveAllowed returns the address to ping: host itself when it is an IP,
// else the first allowed address it resolves to, IPv4 preferred. It returns
// ErrNotAllowed when the guard refuses every candidate.
func resolveAllowed(ctx context.Context, g *Guard, host string) (netip.Addr, error) {
	if ip, err := netip.ParseAddr(host); err == nil {
		if !g.Allowed(ip) {
			return netip.Addr{}, ErrNotAllowed
		}
		return ip.Unmap(), nil
	}
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return netip.Addr{}, err
	}
	var v6 netip.Addr
	for _, ip := range ips {
		ip = ip.Unmap()
		if !g.Allowed(ip) {
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
	return netip.Addr{}, ErrNotAllowed
}

func icmpSocketError(err error) string {
	if errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EPERM) {
		return icmpNotPermitted
	}
	return "open icmp socket: " + err.Error()
}

// echoRound is one probe's echo requests and the replies matched to them.
type echoRound struct {
	target            netip.Addr
	network, listen   string
	proto             int
	echoType, replyTy icmp.Type
	// nonce is the payload of every request; a reply must carry it back, so
	// replies to another probe's requests are never counted.
	nonce []byte

	sent    []time.Time
	seen    []bool
	replies int
	rtt     time.Duration
}

func newEchoRound(target netip.Addr, count int) *echoRound {
	r := &echoRound{
		target: target,
		nonce:  make([]byte, 16),
		sent:   make([]time.Time, count),
		seen:   make([]bool, count),
	}
	_, _ = rand.Read(r.nonce) // never fails (crypto/rand since Go 1.24)
	if target.Is4() {
		r.network, r.listen, r.proto = "udp4", "0.0.0.0", protoICMP
		r.echoType, r.replyTy = ipv4.ICMPTypeEcho, ipv4.ICMPTypeEchoReply
	} else {
		r.network, r.listen, r.proto = "udp6", "::", protoICMPv6
		r.echoType, r.replyTy = ipv6.ICMPTypeEchoRequest, ipv6.ICMPTypeEchoReply
	}
	return r
}

// run sends the requests, paced, and collects replies until all came back
// or ctx's deadline passes.
func (r *echoRound) run(ctx context.Context, conn packetConn) {
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(icmpDefaultTimeout)
	}
	gap := min(icmpMaxGap, time.Until(deadline)/time.Duration(2*len(r.sent)))
	dst := &net.UDPAddr{IP: r.target.AsSlice(), Zone: r.target.Zone()}
	// On Linux the kernel replaces the identifier with the socket's own.
	id := int(binary.BigEndian.Uint16(r.nonce))
	buf := make([]byte, 1500)

	for seq := range r.sent {
		if ctx.Err() != nil {
			return
		}
		msg, err := (&icmp.Message{Type: r.echoType, Body: &icmp.Echo{ID: id, Seq: seq, Data: r.nonce}}).Marshal(nil)
		if err != nil {
			return
		}
		r.sent[seq] = time.Now()
		// A request that cannot be sent counts as lost.
		_, _ = conn.WriteTo(msg, dst)

		until := deadline
		if seq < len(r.sent)-1 {
			until = minTime(r.sent[seq].Add(gap), deadline)
		}
		r.collect(ctx, conn, buf, until)
	}
}

// collect reads replies until until, until every request was answered, or
// until the socket fails or ctx ends.
func (r *echoRound) collect(ctx context.Context, conn packetConn, buf []byte, until time.Time) {
	if err := conn.SetReadDeadline(until); err != nil {
		return
	}
	for r.replies < len(r.sent) && ctx.Err() == nil {
		n, peer, err := conn.ReadFrom(buf)
		if err != nil {
			return
		}
		r.match(buf[:n], peer, time.Now())
	}
}

// match counts b as a reply when it answers one of this round's requests.
func (r *echoRound) match(b []byte, peer net.Addr, at time.Time) {
	m, err := icmp.ParseMessage(r.proto, b)
	if err != nil || m.Type != r.replyTy {
		return
	}
	echo, ok := m.Body.(*icmp.Echo)
	if !ok || !bytes.Equal(echo.Data, r.nonce) {
		return
	}
	if echo.Seq < 0 || echo.Seq >= len(r.sent) || r.sent[echo.Seq].IsZero() || r.seen[echo.Seq] {
		return
	}
	if from, ok := peerAddr(peer); !ok || from != r.target.WithZone("") {
		return
	}
	r.seen[echo.Seq] = true
	r.replies++
	r.rtt += at.Sub(r.sent[echo.Seq])
}

func peerAddr(a net.Addr) (netip.Addr, bool) {
	var ip net.IP
	switch v := a.(type) {
	case *net.UDPAddr:
		ip = v.IP
	case *net.IPAddr:
		ip = v.IP
	default:
		return netip.Addr{}, false
	}
	addr, ok := netip.AddrFromSlice(ip)
	return addr.Unmap(), ok
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
