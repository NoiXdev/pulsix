package clientip

import (
	"net/netip"
	"testing"
	"time"
)

// TestLimiterMapIsHardCapped is final review minor 1: when every tracked
// window is still live, a new address must not grow the table past its
// cap — the oldest entry makes room instead.
func TestLimiterMapIsHardCapped(t *testing.T) {
	now := time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)
	l := NewLimiter(1, time.Minute, func() time.Time { return now })
	l.max = 4
	for i := 0; i < 50; i++ {
		now = now.Add(time.Second)
		if !l.Allow(netip.AddrFrom4([4]byte{10, 0, 0, byte(i)})) {
			t.Fatalf("address %d: a first call is allowed", i)
		}
		if len(l.seen) > l.max {
			t.Fatalf("after %d addresses the table holds %d entries, cap %d", i+1, len(l.seen), l.max)
		}
	}
	// The newest addresses are still counted: the eviction takes the
	// oldest window, not the one just charged.
	if l.Allow(netip.AddrFrom4([4]byte{10, 0, 0, 49})) {
		t.Fatal("the most recent address must still be limited")
	}
}
