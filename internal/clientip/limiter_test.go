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
