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
// The unauthenticated write paths use it (webhook deliveries, worker
// enrollment and, with Reserve/Refund, the authentication endpoints) so one
// source cannot flood them. It is per manager: with several managers, each
// counts on its own. IPv4 addresses are counted one by one, IPv6 addresses
// per /64.
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

// Refused reports whether a's budget for the current window is spent and,
// when it is, how much of the window still runs — the ceil of that
// duration is a refused call's Retry-After. It charges nothing: repeated
// refused calls neither deepen the lockout nor extend the window, so once
// the window ends the address simply starts over.
func (l *Limiter) Refused(a netip.Addr) (time.Duration, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	w, ok := l.seen[key(a)]
	if !ok || now.Sub(w.start) >= l.window || w.count < l.limit {
		return 0, false
	}
	return l.window - now.Sub(w.start), true
}

// Reservation is one attempt Reserve claimed from an address's budget, bound
// to the window it was claimed in, so Refund can hand it back to exactly
// that window.
type Reservation struct {
	key   netip.Addr
	start time.Time
}

// Reserve claims one attempt from a's budget before the attempt runs,
// starting a fresh window when the old one has ended (or a has not been
// seen). It reports false, with how much of the window still runs, when the
// budget is spent; a refused call reserves nothing, so it neither deepens
// the lockout nor extends the window. Claiming before the attempt, under the
// same lock as the check, is what keeps a burst of concurrent attempts from
// all getting through before the first failure is counted (security audit
// run 1, backlog 7.8). Callers that count only failed attempts Refund the
// reservation of an attempt that did not fail.
func (l *Limiter) Reserve(a netip.Addr) (Reservation, time.Duration, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	k := key(a)
	w, ok := l.seen[k]
	if ok && now.Sub(w.start) < l.window {
		if w.count >= l.limit {
			return Reservation{}, l.window - now.Sub(w.start), false
		}
		w.count++
		return Reservation{key: k, start: w.start}, 0, true
	}
	if !ok && len(l.seen) >= l.max {
		l.makeRoom(now)
	}
	l.seen[k] = &window{start: now, count: 1}
	return Reservation{key: k, start: now}, 0, true
}

// Refund hands back a reservation whose attempt did not fail. It only acts
// on the window the reservation was taken in: once that window has ended
// (or the entry was evicted), the slot already returned with it, and the
// next window's count belongs to other attempts.
func (l *Limiter) Refund(r Reservation) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if w, ok := l.seen[r.key]; ok && w.start.Equal(r.start) && w.count > 0 {
		w.count--
	}
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
