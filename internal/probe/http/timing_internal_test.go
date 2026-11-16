package http

import (
	"crypto/tls"
	"errors"
	"net/http/httptrace"
	"testing"
	"time"
)

// fakeClock is a clock tests move by hand.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time { return c.t }
func (c *fakeClock) advance(ms int) { c.t = c.t.Add(time.Duration(ms) * time.Millisecond) }
func newFakeClock() *fakeClock      { return &fakeClock{t: time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)} }
func ms(n int) time.Duration        { return time.Duration(n) * time.Millisecond }
func wantDuration(t *testing.T, name string, got, want time.Duration) {
	t.Helper()
	if got != want {
		t.Fatalf("%s = %v, want %v", name, got, want)
	}
}

// TestPhasesSumOverHops walks the hooks of a redirect: the first hop
// resolves a name, connects over two parallel attempts (one refused, a
// late winner's twin ignored) and shakes hands; the second hop connects
// again over TLS. Lookups, connects and handshakes add up over the hops;
// ttfb and transfer are the last hop's.
func TestPhasesSumOverHops(t *testing.T) {
	c := newFakeClock()
	p := newPhases(c.now)
	tr := p.trace()

	// Hop 1.
	tr.GetConn("a.test:443")
	tr.DNSStart(httptrace.DNSStartInfo{Host: "a.test"})
	c.advance(5)
	tr.DNSDone(httptrace.DNSDoneInfo{})
	tr.ConnectStart("tcp", "[::1]:443")
	c.advance(10)
	tr.ConnectStart("tcp", "127.0.0.1:443")
	c.advance(3)
	tr.ConnectDone("tcp", "[::1]:443", errors.New("refused"))
	c.advance(2)
	tr.ConnectDone("tcp", "127.0.0.1:443", nil)
	c.advance(1)
	tr.ConnectDone("tcp", "127.0.0.1:443", nil) // a twin that also connected: counted once
	tr.TLSHandshakeStart()
	c.advance(20)
	tr.TLSHandshakeDone(tls.ConnectionState{}, nil)
	tr.WroteRequest(httptrace.WroteRequestInfo{})
	c.advance(100)
	tr.GotFirstResponseByte()
	c.advance(1)

	// Hop 2: same name, no lookup of its own (a literal), a fresh connection.
	tr.GetConn("127.0.0.1:443")
	tr.ConnectStart("tcp", "127.0.0.1:443")
	c.advance(7)
	tr.ConnectDone("tcp", "127.0.0.1:443", nil)
	tr.TLSHandshakeStart()
	c.advance(4)
	tr.TLSHandshakeDone(tls.ConnectionState{}, nil)
	tr.WroteRequest(httptrace.WroteRequestInfo{})
	c.advance(30)
	tr.GotFirstResponseByte()
	c.advance(40)

	got := p.timing(c.now())
	wantDuration(t, "dns", got.dns, ms(5))
	wantDuration(t, "connect", got.connect, ms(15+7))
	wantDuration(t, "tls", got.tls, ms(20+4))
	wantDuration(t, "ttfb", got.ttfb, ms(30))
	wantDuration(t, "transfer", got.transfer, ms(40))
	if !got.resolved || !got.secured {
		t.Fatalf("resolved %v, secured %v, want both", got.resolved, got.secured)
	}
}

// TestPhasesWithoutLookupOrTLS: an IP literal over http resolves nothing
// and secures nothing.
func TestPhasesWithoutLookupOrTLS(t *testing.T) {
	c := newFakeClock()
	p := newPhases(c.now)
	tr := p.trace()
	tr.GetConn("127.0.0.1:80")
	tr.ConnectStart("tcp", "127.0.0.1:80")
	c.advance(2)
	tr.ConnectDone("tcp", "127.0.0.1:80", nil)
	tr.WroteRequest(httptrace.WroteRequestInfo{})
	c.advance(9)
	tr.GotFirstResponseByte()

	got := p.timing(c.now())
	if got.resolved || got.secured {
		t.Fatalf("resolved %v, secured %v, want neither", got.resolved, got.secured)
	}
	wantDuration(t, "connect", got.connect, ms(2))
	wantDuration(t, "ttfb", got.ttfb, ms(9))
	wantDuration(t, "transfer", got.transfer, 0)
}

// TestPhasesStartEachHopAfresh: a parallel connect attempt that started
// on one hop and never finished does not stretch the next hop's connect.
func TestPhasesStartEachHopAfresh(t *testing.T) {
	c := newFakeClock()
	p := newPhases(c.now)
	tr := p.trace()
	tr.GetConn("a:80")
	tr.ConnectStart("tcp", "[::1]:80")
	c.advance(2)
	tr.ConnectDone("tcp", "[::1]:80", nil)
	tr.ConnectStart("tcp", "127.0.0.1:80") // the fallback, cancelled
	tr.DNSStart(httptrace.DNSStartInfo{})  // a lookup that never finished
	tr.TLSHandshakeStart()                 // a handshake that never finished
	c.advance(50)

	tr.GetConn("b:80")
	tr.ConnectStart("tcp", "127.0.0.1:80")
	c.advance(5)
	tr.ConnectDone("tcp", "127.0.0.1:80", nil)
	tr.DNSDone(httptrace.DNSDoneInfo{}) // no start of this hop's: ignored
	tr.TLSHandshakeDone(tls.ConnectionState{}, nil)

	got := p.timing(c.now())
	wantDuration(t, "connect", got.connect, ms(2+5))
	wantDuration(t, "dns", got.dns, 0)
	wantDuration(t, "tls", got.tls, 0)
	if got.resolved || got.secured {
		t.Fatalf("resolved %v, secured %v, want neither", got.resolved, got.secured)
	}
}

// TestPhasesFailedHandshakeNotSecured: a handshake that failed (the
// attempt then fails) is neither counted nor marks TLS used.
func TestPhasesFailedHandshakeNotSecured(t *testing.T) {
	c := newFakeClock()
	p := newPhases(c.now)
	tr := p.trace()
	tr.GetConn("a:443")
	tr.TLSHandshakeStart()
	c.advance(3)
	tr.TLSHandshakeDone(tls.ConnectionState{}, errors.New("bad"))
	if got := p.timing(c.now()); got.secured || got.tls != 0 {
		t.Fatalf("secured %v, tls %v, want neither", got.secured, got.tls)
	}
}
