package zfs

import (
	"sync"
	"time"
)

// scrubMemory remembers, per pool, the end of the last finished scrub the
// plugin saw, so a resilver or a canceled scrub that replaces the scan
// line does not send the age back to the pool's creation. It lives in
// memory only: an agent restart forgets it until the next finished scrub
// is seen. One plugin instance may be collected by several checks at
// once, so it is guarded.
type scrubMemory struct {
	mu   sync.Mutex
	ends map[string]time.Time
}

func newScrubMemory() *scrubMemory { return &scrubMemory{ends: map[string]time.Time{}} }

// remember records the pool's last finished scrub; a nil memory forgets.
func (m *scrubMemory) remember(pool string, end time.Time) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ends[pool] = end
}

// get returns the pool's remembered last finished scrub.
func (m *scrubMemory) get(pool string) (time.Time, bool) {
	if m == nil {
		return time.Time{}, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	end, ok := m.ends[pool]
	return end, ok
}
