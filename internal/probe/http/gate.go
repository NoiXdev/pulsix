package http

import (
	"context"
	"runtime"
	"slices"
	"sync"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/probe"
)

// errTooBusy is every JSON item's error when the probe's deadline passes
// while it waits for a JSON stage.
const errTooBusy = "the manager was too busy to read the JSON response"

// JSONGate bounds the JSON stages (decoding a body and evaluating all of a
// check's json_values entries) that run at once in the manager, and the
// assertions' parse stage (the body as JSON and as HTML, and every json
// and html assertion of a check on it; backlog 3.13b). A hostile
// 1 MiB body decodes to tens of MiB and its entries take up to a second
// of CPU, while the runner runs up to 32 probes at once inside the
// manager, next to the API: without the gate one organization could push
// the manager to gigabytes. The manager builds one gate and hands it to
// the http probe, as it does the Guard.
//
// The gate is fair between organizations (the idea of the probe runner's
// fairShare; the organization comes from probe.OrgFrom of the stage's
// context). While a slot is free it is taken at once. When the gate is
// full, a freed slot goes to the waiting organization with the fewest
// slots in use, the freed slot still counted for its organization, so one
// organization with many slow targets cannot keep another's stage waiting
// past one turnover, even on a one-slot gate; organizations tied on that
// are served longest waiter first. Within an organization stages are
// first in, first out. A stage whose deadline passes while it waits fails
// with errTooBusy.
type JSONGate struct {
	capacity int

	mu    sync.Mutex
	inUse int
	held  map[uuid.UUID]int // slots in use per organization
	queue []*gateWaiter     // in arrival order
}

// gateWaiter is one stage waiting for a slot. granted is set under the
// gate's lock when the slot is handed over, and ready is closed then.
type gateWaiter struct {
	org     uuid.UUID
	ready   chan struct{}
	granted bool
}

// NewJSONGate returns a gate of n slots, at least one.
func NewJSONGate(n int) *JSONGate {
	return &JSONGate{capacity: max(n, 1), held: map[uuid.UUID]int{}}
}

// DefaultJSONStages is the manager's gate size: two stages, or one on a
// single core, so they never take more than about two cores and a few
// hundred MiB.
func DefaultJSONStages() int {
	return min(2, runtime.GOMAXPROCS(0))
}

// enter takes a slot for ctx's organization, waiting until ctx ends; leave
// frees it. A free slot is taken even when ctx has ended, so readJSON, not
// the gate, reports a deadline that passed before the stage — at once,
// without decoding the body.
func (g *JSONGate) enter(ctx context.Context) (leave func(), ok bool) {
	org := probe.OrgFrom(ctx)
	leave = func() { g.release(org) }
	g.mu.Lock()
	if g.inUse < g.capacity {
		g.take(org)
		g.mu.Unlock()
		return leave, true
	}
	w := &gateWaiter{org: org, ready: make(chan struct{})}
	g.queue = append(g.queue, w)
	g.mu.Unlock()

	select {
	case <-w.ready:
		return leave, true
	case <-ctx.Done():
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if w.granted {
		// The slot was handed over as the deadline passed; it is ours.
		return leave, true
	}
	g.queue = slices.DeleteFunc(g.queue, func(q *gateWaiter) bool { return q == w })
	return nil, false
}

// take counts a slot in use for org; the caller holds g.mu.
func (g *JSONGate) take(org uuid.UUID) {
	g.inUse++
	g.held[org]++
}

// release frees org's slot and hands it to the next waiter.
func (g *JSONGate) release(org uuid.UUID) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.queue) > 0 {
		// The freed slot still counts in held[org] while choosing, and is
		// handed over without becoming free.
		best := 0
		for i, w := range g.queue {
			// Strictly fewer: the first (longest waiting) of equals wins,
			// which is also the first of its organization.
			if g.held[w.org] < g.held[g.queue[best].org] {
				best = i
			}
		}
		w := g.queue[best]
		g.queue = slices.Delete(g.queue, best, best+1)
		g.held[org]--
		if g.held[org] == 0 {
			delete(g.held, org)
		}
		g.held[w.org]++
		w.granted = true
		close(w.ready)
		return
	}
	g.inUse--
	if g.held[org]--; g.held[org] == 0 {
		delete(g.held, org)
	}
}

// stage runs read in one of the gate's slots. When ctx ends before a slot
// frees, read does not run and every entry is errTooBusy.
func (g *JSONGate) stage(ctx context.Context, entries []jsonEntry, read func() []probe.Observation) []probe.Observation {
	leave, ok := g.enter(ctx)
	if !ok {
		return failEntries(entries, errTooBusy)
	}
	defer leave()
	return read()
}
