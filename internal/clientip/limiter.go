package clientip

import (
	"net/netip"
	"sync"
	"time"
)

// maxTracked bounds the limiter's memory: once reached, expired windows
// are dropped before a new address is counted.
const maxTracked = 10000

// Limiter is a fixed-window call counter per client address, in process.
// The unauthenticated write paths use it (webhook deliveries and worker
// enrollment) so one source cannot flood them. It is per manager: with
// several managers, each counts on its own.
type Limiter struct {
	mu     sync.Mutex
	now    func() time.Time
	limit  int
	window time.Duration
	seen   map[netip.Addr]*window
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
	return &Limiter{now: now, limit: limit, window: per, seen: map[netip.Addr]*window{}}
}

// Window is the length of one counting window (the Retry-After bound).
func (l *Limiter) Window() time.Duration { return l.window }

// Allow counts one call from a and reports whether it is within the limit.
func (l *Limiter) Allow(a netip.Addr) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if len(l.seen) >= maxTracked {
		for k, w := range l.seen {
			if now.Sub(w.start) >= l.window {
				delete(l.seen, k)
			}
		}
	}
	w, ok := l.seen[a]
	if !ok || now.Sub(w.start) >= l.window {
		l.seen[a] = &window{start: now, count: 1}
		return true
	}
	if w.count >= l.limit {
		return false
	}
	w.count++
	return true
}
