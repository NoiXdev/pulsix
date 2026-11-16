package http

import (
	"crypto/tls"
	"net/http/httptrace"
	"sync"
	"time"
)

// timing is the breakdown of one attempt: name lookups, TCP connects and
// TLS handshakes summed over every hop, the last hop's time from writing
// its request to its first response byte, and from that byte to the end
// of the body read.
type timing struct {
	dns, connect, tls, ttfb, transfer time.Duration
	// resolved: some hop looked up a name (not so for an IP literal URL);
	// secured: some hop completed a TLS handshake.
	resolved, secured bool
}

// phases times a request's phases from httptrace hooks, over all its
// hops: a redirect starts a new hop with GetConn. A hop over a connection
// already made has no lookup, connect or handshake of its own, so it
// counts once. The hooks may run on the transport's goroutines, and the
// parallel connects of one dial (both address families) concurrently: a
// hop's connect runs from its first ConnectStart to its first successful
// ConnectDone. hopTrace, beside it, owns the failure reason.
type phases struct {
	now func() time.Time

	mu                      sync.Mutex
	dns, connect, tls       time.Duration
	resolved, secured       bool
	dnsAt, connectAt, tlsAt time.Time // the current hop's start of each, zero when none is open
	wroteAt, firstByteAt    time.Time // the current hop's
}

func newPhases(now func() time.Time) *phases { return &phases{now: now} }

// span adds the time since *start to *sum and closes *start; it reports
// whether one was open.
func (p *phases) span(start *time.Time, sum *time.Duration) bool {
	if start.IsZero() {
		return false
	}
	*sum += p.now().Sub(*start)
	*start = time.Time{}
	return true
}

// at returns a hook that sets *t to now (when onlyFirst, only while unset).
func (p *phases) at(t *time.Time, onlyFirst bool) func() {
	return func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		if !onlyFirst || t.IsZero() {
			*t = p.now()
		}
	}
}

func (p *phases) trace() *httptrace.ClientTrace {
	dnsStart, connectStart, tlsStart := p.at(&p.dnsAt, false), p.at(&p.connectAt, true), p.at(&p.tlsAt, false)
	wrote, firstByte := p.at(&p.wroteAt, false), p.at(&p.firstByteAt, false)
	return &httptrace.ClientTrace{
		GetConn: func(string) {
			p.mu.Lock()
			defer p.mu.Unlock()
			p.dnsAt, p.connectAt, p.tlsAt, p.wroteAt, p.firstByteAt = time.Time{}, time.Time{}, time.Time{}, time.Time{}, time.Time{}
		},
		DNSStart: func(httptrace.DNSStartInfo) { dnsStart() },
		DNSDone: func(httptrace.DNSDoneInfo) {
			p.mu.Lock()
			defer p.mu.Unlock()
			if p.span(&p.dnsAt, &p.dns) {
				p.resolved = true
			}
		},
		ConnectStart: func(_, _ string) { connectStart() },
		ConnectDone: func(_, _ string, err error) {
			if err != nil {
				return
			}
			p.mu.Lock()
			defer p.mu.Unlock()
			p.span(&p.connectAt, &p.connect)
		},
		TLSHandshakeStart: tlsStart,
		TLSHandshakeDone: func(_ tls.ConnectionState, err error) {
			p.mu.Lock()
			defer p.mu.Unlock()
			if err != nil {
				p.tlsAt = time.Time{}
				return
			}
			if p.span(&p.tlsAt, &p.tls) {
				p.secured = true
			}
		},
		WroteRequest:         func(httptrace.WroteRequestInfo) { wrote() },
		GotFirstResponseByte: firstByte,
	}
}

// timing is the breakdown with the body read ended at end.
func (p *phases) timing(end time.Time) timing {
	p.mu.Lock()
	defer p.mu.Unlock()
	t := timing{dns: p.dns, connect: p.connect, tls: p.tls, resolved: p.resolved, secured: p.secured}
	if !p.firstByteAt.IsZero() {
		if !p.wroteAt.IsZero() {
			t.ttfb = max(p.firstByteAt.Sub(p.wroteAt), 0)
		}
		t.transfer = max(end.Sub(p.firstByteAt), 0)
	}
	return t
}
