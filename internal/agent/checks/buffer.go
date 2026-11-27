// The disconnect buffer: a bounded, time-windowed ring that keeps
// results the agent collects while no stream is up, so measuring never
// stops just because the connection did ("Disconnected agent" decision,
// v0.3b-checks design). A manager restart or a network blip during an
// incident is precisely when this data matters most.
package checks

import (
	"fmt"
	"sync"
	"time"
)

// Ring is the ring: Add appends a batch and evicts whatever has aged
// past window relative to now() — by each entry's OWN timestamp (at),
// never arrival time, the same rule the wire format and the manager's
// plausibility window both use, so an outage that outlasts window drops
// its oldest entries first and reports exactly how many. Drain empties
// the ring in FIFO order (oldest first) without evicting anything itself
// — eviction is Add's job alone, so every drop is counted exactly once,
// at the moment it happens.
//
// It is generic so the agent's probe results (internal/agent/probes) ride
// the same 15-minute rule as its check results; Buffer is the check
// results' ring. A capped ring (NewCappedRing) also bounds the summed cost
// of what it holds: the probe ring, whose size is set by up to 200
// assignments rather than by the host's own checks (backlog 3.78).
type Ring[T any] struct {
	mu     sync.Mutex
	window time.Duration
	now    func() time.Time
	at     func(T) time.Time
	// items[head:] are the entries held, oldest first; items[:head] are
	// evicted slots, zeroed so they hold no references, reclaimed by an
	// in-place compaction once they are the larger part of items. Eviction
	// is therefore O(evicted) amortised, and a ring at its cap reuses one
	// array instead of copying itself into a fresh one on every Put.
	items []T
	head  int
	// limit is the most summed cost the ring holds, 0 for no cap; held is
	// the cost of items, kept only when capped.
	limit int
	cost  func(T) int
	held  int
}

// Evicted is what one Put dropped: Aged entries fell out of the window,
// OverCap entries left to bring a capped ring back under its cap.
type Evicted struct {
	Aged    int
	OverCap int
}

// Buffer is the ring of check Results.
type Buffer = Ring[Result]

// NewRing wires a Ring holding window's worth of entries, each stamped
// by at, using now for eviction — injected so tests advance a fake clock
// instead of sleeping.
func NewRing[T any](window time.Duration, now func() time.Time, at func(T) time.Time) *Ring[T] {
	return &Ring[T]{window: window, now: now, at: at}
}

// NewCappedRing is NewRing that also holds at most limit of summed cost:
// over it, Put evicts the oldest entries — whole instants, see Put — until
// the rest fits. limit must be positive: a cap of zero or less is a wiring
// mistake (an uncapped ring is NewRing), so it panics.
func NewCappedRing[T any](window time.Duration, limit int, cost func(T) int, now func() time.Time, at func(T) time.Time) *Ring[T] {
	if limit <= 0 {
		panic(fmt.Sprintf("checks.NewCappedRing: cap %d is not positive", limit))
	}
	return &Ring[T]{window: window, now: now, at: at, limit: limit, cost: cost}
}

// NewBuffer wires a Buffer holding window's worth of Results (the
// design's 15 minutes), stamped by their own At.
func NewBuffer(window time.Duration, now func() time.Time) *Buffer {
	return NewRing(window, now, func(r Result) time.Time { return r.At })
}

// Add appends r to the ring and evicts as Put does. dropped is how many
// this call evicted — a gap in a graph must have a cause somebody can
// read, which is why this is a return value and not a swallowed side
// effect.
func (b *Ring[T]) Add(r []T) (dropped int) {
	e := b.Put(r)
	return e.Aged + e.OverCap
}

// Put appends r to the ring, then evicts the oldest entries whose own
// timestamp falls before now()-window and, on a capped ring, the oldest
// entries while the summed cost exceeds the cap. A cap eviction also takes
// every following entry stamped with the same instant as the last one it
// took: a probe round (or a check run) is one Add of results sharing one
// timestamp, so none is left half buffered.
func (b *Ring[T]) Put(r []T) Evicted {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.items = append(b.items, r...)
	if b.limit > 0 {
		for _, x := range r {
			b.held += b.cost(x)
		}
	}

	var e Evicted
	cutoff := b.now().Add(-b.window)
	i := b.head
	for i < len(b.items) && b.at(b.items[i]).Before(cutoff) {
		b.release(b.items[i])
		i++
	}
	e.Aged = i - b.head
	if b.limit > 0 && b.held > b.limit {
		for i < len(b.items) && b.held > b.limit {
			b.release(b.items[i])
			i++
		}
		last := b.at(b.items[i-1])
		for i < len(b.items) && b.at(b.items[i]).Equal(last) {
			b.release(b.items[i])
			i++
		}
		e.OverCap = i - b.head - e.Aged
	}
	if i == b.head {
		return e
	}
	clear(b.items[b.head:i])
	b.head = i
	if live := len(b.items) - b.head; b.head > live {
		// More dead slots than live ones: move the live ones to the front
		// of the same array. Each entry is moved at most once per time the
		// ring turns over, and the array's spare capacity takes the next
		// appends without growing.
		n := copy(b.items, b.items[b.head:])
		clear(b.items[n:])
		b.items, b.head = b.items[:n], 0
	}
	return e
}

// release takes x's cost off a capped ring's total.
func (b *Ring[T]) release(x T) {
	if b.limit > 0 {
		b.held -= b.cost(x)
	}
}

// Drain returns everything currently held, oldest first, and empties the
// ring — the reconnect path: replay this before anything newly collected,
// so the graph's timeline has no hole a live result could have filled.
func (b *Ring[T]) Drain() []T {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := b.items[b.head:]
	b.items, b.head = nil, 0
	b.held = 0
	return out
}
