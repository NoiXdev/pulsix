package clientip

import (
	"net/netip"
	"sync"
	"time"
)

// maxTracked bounds the limiter's memory: once reached, expired windows
// are dropped, and if every window is still live the oldest one makes
// room, so the table never grows past it.
const maxTracked = 10000

// ipv6KeyBits is the prefix an IPv6 address is counted under: one host
// usually holds a whole /64, so counting single addresses would let it
// rotate through fresh budgets.
const ipv6KeyBits = 64

// Limiter is a fixed-window call counter per client address, in process.
// The unauthenticated write paths use it (webhook deliveries and worker
// enrollment) so one source cannot flood them. It is per manager: with
// several managers, each counts on its own. IPv4 addresses are counted one
// by one, IPv6 addresses per /64.
type Limiter struct {
	mu        sync.Mutex
	now       func() time.Time
	limit     int
	window    time.Duration
	max       int
	lastSweep time.Time
	seen      map[netip.Addr]*window
}

type window struct {
	start time.Time
	count int
}

// NewLimiter allows limit calls per address and window.
func NewLimiter(limit int, per time.Duration, now func() time.Time) *Limiter {
	if now == nil {
		now = time.Now
	}
	return &Limiter{now: now, limit: limit, window: per, max: maxTracked, seen: map[netip.Addr]*window{}}
}

// Window is the length of one counting window (the Retry-After bound).
func (l *Limiter) Window() time.Duration { return l.window }

// Allow counts one call from a and reports whether it is within the limit.
func (l *Limiter) Allow(a netip.Addr) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	k := key(a)
	w, ok := l.seen[k]
	if ok && now.Sub(w.start) < l.window {
		if w.count >= l.limit {
			return false
		}
		w.count++
		return true
	}
	if !ok && len(l.seen) >= l.max {
		l.makeRoom(now)
	}
	l.seen[k] = &window{start: now, count: 1}
	return true
}

// makeRoom frees one slot for a new address. Expired windows go first, at
// most once per window so a flood of new addresses does not rescan the
// full table on every call; when none has expired, the oldest window is
// dropped — its address simply starts a fresh window on its next call.
func (l *Limiter) makeRoom(now time.Time) {
	if now.Sub(l.lastSweep) >= l.window {
		l.lastSweep = now
		for k, w := range l.seen {
			if now.Sub(w.start) >= l.window {
				delete(l.seen, k)
			}
		}
		if len(l.seen) < l.max {
			return
		}
	}
	var oldest netip.Addr
	var oldestStart time.Time
	for k, w := range l.seen {
		if !oldest.IsValid() || w.start.Before(oldestStart) {
			oldest, oldestStart = k, w.start
		}
	}
	delete(l.seen, oldest)
}

// key is the counting key of a: the IPv4 address itself (an IPv4-mapped
// IPv6 address counts as its IPv4 form), or the IPv6 address's /64.
func key(a netip.Addr) netip.Addr {
	a = a.Unmap()
	if a.Is6() {
		if p, err := a.Prefix(ipv6KeyBits); err == nil {
			return p.Addr()
		}
	}
	return a
}
