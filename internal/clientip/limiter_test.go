package clientip_test

import (
	"net/netip"
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

// TestLimiterChargesFailedCallsOnly covers the failed-attempt counting the
// auth endpoints use: a budget is spent by Charge alone, a refused address
// is told how long its window still runs, refused calls neither deepen the
// lockout nor extend the window, and a success never starts one.
func TestLimiterChargesFailedCallsOnly(t *testing.T) {
	now := time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC)
	l := clientip.NewLimiter(2, time.Minute, func() time.Time { return now })
	a := netip.MustParseAddr("198.51.100.1")

	if retry, refused := l.Refused(a); refused || retry != 0 {
		t.Fatalf("an address with no window is not refused: %s, %v", retry, refused)
	}

	// Failures charge: two failures spend the budget of 2.
	l.Charge(a)
	l.Charge(a)
	retry, refused := l.Refused(a)
	if !refused {
		t.Fatal("a spent budget is refused")
	}
	if got := retry.Round(time.Second); got != time.Minute {
		t.Fatalf("right after the budget is spent, retry = %s, want the full window", got)
	}
	// The refusal answers nothing new: refused calls neither deepen the
	// lockout nor push the window out.
	l.Charge(a)
	if retry, refused := l.Refused(a); !refused || retry.Round(time.Second) != time.Minute {
		t.Fatalf("a refused call's charge is a no-op: %s, %v", retry, refused)
	}

	// Halfway through the window the remaining time shrinks accordingly.
	now = now.Add(30 * time.Second)
	retry, refused = l.Refused(a)
	if !refused {
		t.Fatal("the budget is still spent halfway through the window")
	}
	if got := retry.Round(time.Second); got != 30*time.Second {
		t.Fatalf("halfway through the window, retry = %s, want 30s", got)
	}

	// Once the window ends the address starts over.
	now = now.Add(30 * time.Second)
	if _, refused := l.Refused(a); refused {
		t.Fatal("an expired window is not refused")
	}
	l.Charge(a)
	if _, refused := l.Refused(a); refused {
		t.Fatal("a fresh window has its own budget")
	}

	// And a success (no charge at all) never spends anything.
	b := netip.MustParseAddr("198.51.100.2")
	if _, refused := l.Refused(b); refused {
		t.Fatal("an address that only made successful calls is never refused")
	}
}

// TestLimiterChargeBoundsTheTable: charging a brand-new address when the
// tracking table is full drops an expired window (or the oldest) instead of
// growing without bound, like Allow.
func TestLimiterChargeBoundsTheTable(t *testing.T) {
	now := time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC)
	l := clientip.NewLimiter(1, time.Minute, func() time.Time { return now })
	for i := 0; i < 10000; i++ {
		l.Charge(netip.AddrFrom4([4]byte{10, byte(i >> 16), byte(i >> 8), byte(i)}))
	}
	now = now.Add(time.Minute)
	if _, refused := l.Refused(netip.AddrFrom4([4]byte{10, 0, 0, 0})); refused {
		t.Fatal("an expired window must not keep the address refused")
	}
	l.Charge(netip.MustParseAddr("192.0.2.1"))
	if _, refused := l.Refused(netip.MustParseAddr("192.0.2.1")); !refused {
		t.Fatal("a new address is charged once the table was pruned")
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
