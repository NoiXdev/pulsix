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

// Buffer is the ring: Add appends a batch and evicts whatever has aged
// past window relative to now() — by each Result's OWN timestamp, never
// arrival time, the same rule the wire format and the manager's
// plausibility window both use, so an outage that outlasts window drops
// its oldest samples first and reports exactly how many. Drain empties
// the ring in FIFO order (oldest first) without evicting anything itself
// — eviction is Add's job alone, so every drop is counted exactly once,
// at the moment it happens.
type Buffer struct {
	mu     sync.Mutex
	window time.Duration
	now    func() time.Time
	items  []Result
}

// NewBuffer wires a Buffer holding window's worth of Results (the
// design's 15 minutes), using now for eviction — injected so tests
// advance a fake clock instead of sleeping.
func NewBuffer(window time.Duration, now func() time.Time) *Buffer {
	return &Buffer{window: window, now: now}
}

// Add appends r to the ring, then evicts the oldest entries whose own At
// falls before now()-window. dropped is how many this call evicted — a
// gap in a graph must have a cause somebody can read, which is why this
// is a return value and not a swallowed side effect.
func (b *Buffer) Add(r []Result) (dropped int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.items = append(b.items, r...)

	cutoff := b.now().Add(-b.window)
	i := 0
	for i < len(b.items) && b.items[i].At.Before(cutoff) {
		i++
	}
	if i == 0 {
		return 0
	}
	remaining := make([]Result, len(b.items)-i)
	copy(remaining, b.items[i:])
	b.items = remaining
	return i
}

// Drain returns everything currently held, oldest first, and empties the
// ring — the reconnect path: replay this before anything newly collected,
// so the graph's timeline has no hole a live result could have filled.
func (b *Buffer) Drain() []Result {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := b.items
	b.items = nil
	return out
}
