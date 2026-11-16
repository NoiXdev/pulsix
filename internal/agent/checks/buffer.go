// The disconnect buffer: a bounded, time-windowed ring that keeps
// results the agent collects while no stream is up, so measuring never
// stops just because the connection did ("Disconnected agent" decision,
// v0.3b-checks design). A manager restart or a network blip during an
// incident is precisely when this data matters most.
package checks

import (
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
// results' ring.
type Ring[T any] struct {
	mu     sync.Mutex
	window time.Duration
	now    func() time.Time
	at     func(T) time.Time
	items  []T
}

// Buffer is the ring of check Results.
type Buffer = Ring[Result]

// NewRing wires a Ring holding window's worth of entries, each stamped
// by at, using now for eviction — injected so tests advance a fake clock
// instead of sleeping.
func NewRing[T any](window time.Duration, now func() time.Time, at func(T) time.Time) *Ring[T] {
	return &Ring[T]{window: window, now: now, at: at}
}

// NewBuffer wires a Buffer holding window's worth of Results (the
// design's 15 minutes), stamped by their own At.
func NewBuffer(window time.Duration, now func() time.Time) *Buffer {
	return NewRing(window, now, func(r Result) time.Time { return r.At })
}

// Add appends r to the ring, then evicts the oldest entries whose own
// timestamp falls before now()-window. dropped is how many this call
// evicted — a gap in a graph must have a cause somebody can read, which
// is why this is a return value and not a swallowed side effect.
func (b *Ring[T]) Add(r []T) (dropped int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.items = append(b.items, r...)

	cutoff := b.now().Add(-b.window)
	i := 0
	for i < len(b.items) && b.at(b.items[i]).Before(cutoff) {
		i++
	}
	if i == 0 {
		return 0
	}
	remaining := make([]T, len(b.items)-i)
	copy(remaining, b.items[i:])
	b.items = remaining
	return i
}

// Drain returns everything currently held, oldest first, and empties the
// ring — the reconnect path: replay this before anything newly collected,
// so the graph's timeline has no hole a live result could have filled.
func (b *Ring[T]) Drain() []T {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := b.items
	b.items = nil
	return out
}
