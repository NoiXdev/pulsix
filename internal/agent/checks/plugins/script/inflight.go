package script

import "sync"

// inflight counts, per argv, the processes Run gave up on that have not
// exited yet (the smart runner's pattern).
type inflight struct {
	mu      sync.Mutex
	running map[string]int
}

// runState is one run's standing in its inflight set, guarded by its mutex.
type runState struct {
	exited, givenUp bool
}

func newInflight() *inflight { return &inflight{running: map[string]int{}} }

// stuck reports whether a process Run gave up on still runs with key.
func (g *inflight) stuck(key string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.running[key] > 0
}

// giveUp marks key while the run's process has not exited.
func (g *inflight) giveUp(key string, st *runState) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !st.exited && !st.givenUp {
		st.givenUp = true
		g.running[key]++
	}
}

// exited records that the run's process is gone and unmarks key if the
// run had been given up on.
func (g *inflight) exited(key string, st *runState) {
	g.mu.Lock()
	defer g.mu.Unlock()
	st.exited = true
	if st.givenUp {
		if g.running[key]--; g.running[key] <= 0 {
			delete(g.running, key)
		}
	}
}
