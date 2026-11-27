package checks

import (
	"sort"
	"time"
)

// RunningCheck is one check the scheduler runs, as the agent's status page
// lists it (backlog 3.132).
type RunningCheck struct {
	Key      string
	Plugin   string
	Interval time.Duration
	// Local is a check from the host's agent.yaml.
	Local bool
}

// Checks lists the checks the scheduler runs — the union of the manager's
// set and the host's local one, a shadowed local key not among them — in
// key order.
func (s *Scheduler) Checks() []RunningCheck {
	s.mu.Lock()
	out := make([]RunningCheck, 0, len(s.checks))
	for key, sc := range s.checks {
		out = append(out, RunningCheck{Key: key, Plugin: sc.spec.Plugin, Interval: sc.spec.Interval, Local: sc.local})
	}
	s.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// Stats reports how many entries the ring holds and the time of the
// oldest, zero when it is empty.
func (b *Ring[T]) Stats() (n int, oldest time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n = len(b.items) - b.head
	if n > 0 {
		oldest = b.at(b.items[b.head])
	}
	return n, oldest
}
