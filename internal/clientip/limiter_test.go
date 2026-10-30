package clientip_test

import (
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/clientip"
)

func TestLimiter(t *testing.T) {
	now := time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)
	l := clientip.NewLimiter(2, time.Minute, func() time.Time { return now })
	a, b := netip.MustParseAddr("198.51.100.1"), netip.MustParseAddr("198.51.100.2")
	got := []bool{l.Allow(a), l.Allow(a), l.Allow(a)}
	if !got[0] || !got[1] || got[2] {
		t.Fatalf("calls inside one window = %v, want the third refused", got)
	}
	if !l.Allow(b) {
		t.Fatal("addresses are counted separately")
	}
	now = now.Add(time.Minute)
	if !l.Allow(a) {
		t.Fatal("a new window starts after the old one ends")
	}
	if l.Window() != time.Minute {
		t.Fatalf("window = %s", l.Window())
	}
}

func TestLimiterDropsExpiredWindowsWhenFull(t *testing.T) {
	now := time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)
	l := clientip.NewLimiter(1, time.Minute, func() time.Time { return now })
	for i := 0; i < 10000; i++ {
		l.Allow(netip.AddrFrom4([4]byte{10, byte(i >> 16), byte(i >> 8), byte(i)}))
	}
	now = now.Add(time.Minute)
	first := netip.AddrFrom4([4]byte{10, 0, 0, 0})
	if !l.Allow(first) {
		t.Fatal("an expired window must not block the address")
	}
	if !l.Allow(netip.MustParseAddr("192.0.2.1")) {
		t.Fatal("a new address is counted once the table was pruned")
	}
}

// TestLimiterReservesAttemptsAndRefundsSuccesses covers the failed-attempt
// counting the auth endpoints use: every attempt reserves a slot before it
// runs, a refused address is told how long its window still runs and
// reserves nothing, and an attempt that did not fail hands its slot back.
func TestLimiterReservesAttemptsAndRefundsSuccesses(t *testing.T) {
	now := time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC)
	l := clientip.NewLimiter(2, time.Minute, func() time.Time { return now })
	a := netip.MustParseAddr("198.51.100.1")

	if retry, refused := l.Refused(a); refused || retry != 0 {
		t.Fatalf("an address with no window is not refused: %s, %v", retry, refused)
	}

	// Two reservations spend the budget of 2; the third is refused with
	// the whole window left, and the refusal reserves nothing.
	r1, _, ok1 := l.Reserve(a)
	_, _, ok2 := l.Reserve(a)
	if !ok1 || !ok2 {
		t.Fatal("the first two attempts fit the budget")
	}
	_, retry, ok := l.Reserve(a)
	if ok {
		t.Fatal("a spent budget is refused")
	}
	if got := retry.Round(time.Second); got != time.Minute {
		t.Fatalf("right after the budget is spent, retry = %s, want the full window", got)
	}
	if _, refused := l.Refused(a); !refused {
		t.Fatal("Refused reports a budget spent by reservations")
	}

	// A refunded attempt (one that did not fail) gives its slot back.
	l.Refund(r1)
	if _, _, ok := l.Reserve(a); !ok {
		t.Fatal("a refunded slot can be reserved again")
	}

	// Halfway through the window the remaining time shrinks accordingly.
	now = now.Add(30 * time.Second)
	if _, retry, ok := l.Reserve(a); ok || retry.Round(time.Second) != 30*time.Second {
		t.Fatalf("halfway through the window: ok=%v retry=%s, want refused with 30s", ok, retry)
	}

	// Once the window ends the address starts over.
	now = now.Add(30 * time.Second)
	if _, _, ok := l.Reserve(a); !ok {
		t.Fatal("a fresh window has its own budget")
	}
}

// TestLimiterRefundIgnoresAnEndedWindow: a reservation is bound to the
// window it was taken in. Refunding it after that window ended must not
// hand a slot back to the next window, which other attempts have charged.
func TestLimiterRefundIgnoresAnEndedWindow(t *testing.T) {
	now := time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC)
	l := clientip.NewLimiter(2, time.Minute, func() time.Time { return now })
	a := netip.MustParseAddr("198.51.100.3")

	old, _, _ := l.Reserve(a)
	now = now.Add(time.Minute)
	l.Reserve(a)
	l.Reserve(a)
	l.Refund(old)
	if _, _, ok := l.Reserve(a); ok {
		t.Fatal("a refund from an ended window freed a slot in the current one")
	}
}

// TestLimiterReserveHoldsUnderConcurrency is security audit run 1's 7.8:
// the budget is claimed atomically, so however many attempts arrive at
// once, only the limit gets through.
func TestLimiterReserveHoldsUnderConcurrency(t *testing.T) {
	l := clientip.NewLimiter(5, time.Minute, nil)
	a := netip.MustParseAddr("198.51.100.4")
	var wg sync.WaitGroup
	var got atomic.Int32
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, ok := l.Reserve(a); ok {
				got.Add(1)
			}
		}()
	}
	wg.Wait()
	if got.Load() != 5 {
		t.Fatalf("%d of 200 concurrent attempts were reserved, want exactly the limit of 5", got.Load())
	}
}

// TestLimiterReserveBoundsTheTable: reserving for a brand-new address when
// the tracking table is full drops an expired window (or the oldest)
// instead of growing without bound, like Allow.
func TestLimiterReserveBoundsTheTable(t *testing.T) {
	now := time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC)
	l := clientip.NewLimiter(1, time.Minute, func() time.Time { return now })
	for i := 0; i < 10000; i++ {
		l.Reserve(netip.AddrFrom4([4]byte{10, byte(i >> 16), byte(i >> 8), byte(i)}))
	}
	now = now.Add(time.Minute)
	if _, refused := l.Refused(netip.AddrFrom4([4]byte{10, 0, 0, 0})); refused {
		t.Fatal("an expired window must not keep the address refused")
	}
	l.Reserve(netip.MustParseAddr("192.0.2.1"))
	if _, refused := l.Refused(netip.MustParseAddr("192.0.2.1")); !refused {
		t.Fatal("a new address is reserved once the table was pruned")
	}
}

func TestNewLimiterDefaultsTheClock(t *testing.T) {
	if !clientip.NewLimiter(1, time.Minute, nil).Allow(netip.MustParseAddr("192.0.2.1")) {
		t.Fatal("the first call is always allowed")
	}
}

// TestLimiterKeysIPv6ByPrefix is final review minor 1: one host holds a
// whole /64, so rotating through it must not buy a fresh budget per
// address; another /64 and IPv4 keep their own.
func TestLimiterKeysIPv6ByPrefix(t *testing.T) {
	now := time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)
	l := clientip.NewLimiter(2, time.Minute, func() time.Time { return now })
	if !l.Allow(netip.MustParseAddr("2001:db8:1:2::1")) || !l.Allow(netip.MustParseAddr("2001:db8:1:2:ffff::9")) {
		t.Fatal("the first two calls of the /64 are allowed")
	}
	if l.Allow(netip.MustParseAddr("2001:db8:1:2:dead:beef:0:1")) {
		t.Fatal("a third address inside the same /64 shares its budget")
	}
	if !l.Allow(netip.MustParseAddr("2001:db8:1:3::1")) {
		t.Fatal("another /64 has its own budget")
	}
	// An IPv4-mapped address counts as the IPv4 address itself.
	if !l.Allow(netip.MustParseAddr("192.0.2.7")) || !l.Allow(netip.MustParseAddr("::ffff:192.0.2.7")) || l.Allow(netip.MustParseAddr("192.0.2.7")) {
		t.Fatal("an IPv4-mapped address must share the IPv4 address's budget")
	}
}
