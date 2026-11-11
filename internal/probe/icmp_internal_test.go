package probe

import (
	"context"
	"errors"
	"net"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

// echoConn answers every echo request written to it, except the sequence
// numbers in drop, the way a reachable target would.
type echoConn struct {
	v6   bool
	drop map[int]bool

	mu       sync.Mutex
	deadline time.Time
	replies  chan reply
	closed   chan struct{}
	once     sync.Once
}

type reply struct {
	b    []byte
	from net.Addr
}

func newEchoConn(v6 bool, drop ...int) *echoConn {
	c := &echoConn{v6: v6, drop: map[int]bool{}, replies: make(chan reply, 32), closed: make(chan struct{})}
	for _, s := range drop {
		c.drop[s] = true
	}
	return c
}

func (c *echoConn) WriteTo(b []byte, dst net.Addr) (int, error) {
	proto, replyType := 1, icmp.Type(ipv4.ICMPTypeEchoReply)
	if c.v6 {
		proto, replyType = 58, ipv6.ICMPTypeEchoReply
	}
	m, err := icmp.ParseMessage(proto, b)
	if err != nil {
		return 0, err
	}
	echo, ok := m.Body.(*icmp.Echo)
	if !ok {
		return 0, errors.New("not an echo")
	}
	if !c.drop[echo.Seq] {
		out, err := (&icmp.Message{Type: replyType, Body: echo}).Marshal(nil)
		if err != nil {
			return 0, err
		}
		c.replies <- reply{b: out, from: dst}
	}
	return len(b), nil
}

func (c *echoConn) ReadFrom(b []byte) (int, net.Addr, error) {
	c.mu.Lock()
	wait := time.Until(c.deadline)
	c.mu.Unlock()
	if wait <= 0 {
		return 0, nil, os.ErrDeadlineExceeded
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case r := <-c.replies:
		return copy(b, r.b), r.from, nil
	case <-timer.C:
		return 0, nil, os.ErrDeadlineExceeded
	case <-c.closed:
		return 0, nil, net.ErrClosed
	}
}

func (c *echoConn) SetReadDeadline(t time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.deadline = t
	return nil
}

func (c *echoConn) Close() error {
	c.once.Do(func() { close(c.closed) })
	return nil
}

// opener returns an icmpOpener that hands out conn and records the network.
func opener(conn packetConn, err error, network *string) icmpOpener {
	return func(n, _ string) (packetConn, error) {
		if network != nil {
			*network = n
		}
		if err != nil {
			return nil, err
		}
		return conn, nil
	}
}

func icmpByMetric(t *testing.T, obs []Observation) map[string]Observation {
	t.Helper()
	m := map[string]Observation{}
	for _, o := range obs {
		if (o.Value == nil) == (o.Err == "") {
			t.Fatalf("observation %q must carry a value xor an error: %+v", o.Metric, o)
		}
		m[o.Metric] = o
	}
	if len(m) != len(obs) {
		t.Fatalf("duplicate metric in %+v", obs)
	}
	return m
}

func TestICMPSocketNotPermittedNamesTheSysctl(t *testing.T) {
	for _, errno := range []syscall.Errno{syscall.EACCES, syscall.EPERM} {
		t.Run(errno.Error(), func(t *testing.T) {
			refused := os.NewSyscallError("socket", errno)
			obs := probeICMP(context.Background(), NewGuardForTest(true, nil), nil, "127.0.0.1", opener(nil, refused, nil))
			m := icmpByMetric(t, obs)
			if len(m) != 3 {
				t.Fatalf("want one error per metric, got %+v", obs)
			}
			for _, metric := range []string{"icmp.up", "icmp.loss_pct", "icmp.rtt_ms"} {
				if !strings.Contains(m[metric].Err, "net.ipv4.ping_group_range") {
					t.Fatalf("%s: err %q does not name net.ipv4.ping_group_range", metric, m[metric].Err)
				}
			}
		})
	}
}

func TestICMPOtherSocketErrorIsAnError(t *testing.T) {
	obs := probeICMP(context.Background(), NewGuardForTest(true, nil), nil, "127.0.0.1",
		opener(nil, os.NewSyscallError("socket", syscall.EMFILE), nil))
	m := icmpByMetric(t, obs)
	if len(m) != 3 || m["icmp.up"].Err == "" || strings.Contains(m["icmp.up"].Err, "ping_group_range") {
		t.Fatalf("want a plain error per metric, got %+v", obs)
	}
}

func TestICMPCountsReplies(t *testing.T) {
	tests := []struct {
		name     string
		address  string
		v6       bool
		count    float64
		drop     []int
		wantNet  string
		wantLoss float64
	}{
		{"all answered", "192.0.2.1", false, 3, nil, "udp4", 0},
		{"one of two lost", "192.0.2.1", false, 2, []int{1}, "udp4", 50},
		{"ipv6 target", "2001:db8::1", true, 4, []int{0}, "udp6", 25},
		{"bracketed ipv6 target", "[2001:db8::1]", true, 1, nil, "udp6", 0},
		{"v4-mapped target", "::ffff:192.0.2.1", false, 1, nil, "udp4", 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var network string
			conn := newEchoConn(tc.v6, tc.drop...)
			obs := probeICMP(context.Background(), NewGuard(nil),
				map[string]any{"count": tc.count, "timeout_s": float64(1)}, tc.address, opener(conn, nil, &network))
			if network != tc.wantNet {
				t.Fatalf("opened %q, want %q", network, tc.wantNet)
			}
			m := icmpByMetric(t, obs)
			if len(m) != 3 {
				t.Fatalf("want up, loss_pct and rtt_ms, got %+v", obs)
			}
			if v := *m["icmp.up"].Value; v != 1 {
				t.Fatalf("icmp.up = %v, want 1", v)
			}
			if v := *m["icmp.loss_pct"].Value; v != tc.wantLoss {
				t.Fatalf("icmp.loss_pct = %v, want %v", v, tc.wantLoss)
			}
			if v := *m["icmp.rtt_ms"].Value; v < 0 {
				t.Fatalf("icmp.rtt_ms = %v, want >= 0", v)
			}
			select {
			case <-conn.closed:
			default:
				t.Fatal("socket left open")
			}
		})
	}
}

func TestICMPNoReplyIsDown(t *testing.T) {
	conn := newEchoConn(false, 0, 1)
	start := time.Now()
	obs := probeICMP(context.Background(), NewGuard(nil),
		map[string]any{"count": float64(2), "timeout_s": float64(1)}, "192.0.2.1", opener(conn, nil, nil))
	wantNoReply(t, obs)
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("probe took %v, beyond its 1 s timeout", d)
	}
}

func TestICMPIgnoresForeignReplies(t *testing.T) {
	// A reply carrying another probe's payload must not count as ours.
	conn := newEchoConn(false, 0)
	foreign, err := (&icmp.Message{Type: ipv4.ICMPTypeEchoReply, Body: &icmp.Echo{ID: 1, Seq: 0, Data: []byte("someone else")}}).Marshal(nil)
	if err != nil {
		t.Fatal(err)
	}
	conn.replies <- reply{b: foreign, from: &net.UDPAddr{IP: net.ParseIP("192.0.2.1")}}
	obs := probeICMP(context.Background(), NewGuard(nil),
		map[string]any{"count": float64(1), "timeout_s": float64(1)}, "192.0.2.1", opener(conn, nil, nil))
	wantNoReply(t, obs)
}

// wantNoReply fails unless obs is exactly icmp.up=0 and icmp.loss_pct=100:
// requests went out and none came back, so the loss was measured but no
// round trip was.
func wantNoReply(t *testing.T, obs []Observation) {
	t.Helper()
	m := icmpByMetric(t, obs)
	up, loss := m["icmp.up"], m["icmp.loss_pct"]
	if len(m) != 2 || up.Value == nil || *up.Value != 0 || loss.Value == nil || *loss.Value != 100 {
		t.Fatalf("want exactly icmp.up=0 and icmp.loss_pct=100, got %+v", obs)
	}
}

func TestICMPCancelReturnsPromptly(t *testing.T) {
	conn := newEchoConn(false, 0)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	start := time.Now()
	_ = probeICMP(ctx, NewGuard(nil), map[string]any{"count": float64(1), "timeout_s": float64(30)}, "192.0.2.1", opener(conn, nil, nil))
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("probe took %v after cancel", d)
	}
}
