package grpcsrv

import (
	"sync"

	"github.com/google/uuid"
)

// StreamRegistry tracks the open RunEvents streams per worker, so a
// revocation can end them (spec §4 "Revocation"). It lives in process: with
// several managers each ends only its own streams, while the interceptor
// refuses the revoked worker's next call on every manager.
type StreamRegistry struct {
	mu       sync.Mutex
	next     uint64
	byWorker map[uuid.UUID]map[uint64]*trackedStream
}

// trackedStream is one open stream. Its lock orders every event append
// against the cut: an append runs under it, and shut takes it, so once
// shut has returned no append of the stream is in flight and none starts.
type trackedStream struct {
	revoked chan struct{} // closed by shut: the handler ends the stream

	mu  sync.Mutex
	cut bool
}

// store runs appendEvent unless the stream was cut; a cut stream drops the
// event and reports no error. The lock is held across the append on
// purpose: a revocation waits for an append in flight rather than racing it.
func (t *trackedStream) store(appendEvent func() error) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.cut {
		return nil
	}
	return appendEvent()
}

// shut cuts the stream. It signals the handler first, so the RPC ends (and
// its context cancels an append in flight) without waiting, then takes the
// lock: once shut returns, no append of the stream runs or starts.
func (t *trackedStream) shut() {
	close(t.revoked)
	t.mu.Lock()
	defer t.mu.Unlock()
	t.cut = true
}

// NewStreamRegistry builds an empty registry.
func NewStreamRegistry() *StreamRegistry {
	return &StreamRegistry{byWorker: map[uuid.UUID]map[uint64]*trackedStream{}}
}

// track registers one stream of workerID; CloseWorker cuts it when the
// worker is revoked, release unregisters it.
func (r *StreamRegistry) track(workerID uuid.UUID) (*trackedStream, func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.next++
	id, ts := r.next, &trackedStream{revoked: make(chan struct{})}
	if r.byWorker[workerID] == nil {
		r.byWorker[workerID] = map[uint64]*trackedStream{}
	}
	r.byWorker[workerID][id] = ts
	return ts, func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		if streams, ok := r.byWorker[workerID]; ok {
			delete(streams, id)
			if len(streams) == 0 {
				delete(r.byWorker, workerID)
			}
		}
	}
}

// CloseWorker ends every open stream of workerID and reports how many. It
// returns only once none of those streams can store another event: an
// append in flight finishes first, and no later one runs. A revocation
// relies on that to requeue the worker's jobs after this call without a
// stray event landing in the requeued job's log.
func (r *StreamRegistry) CloseWorker(workerID uuid.UUID) int {
	r.mu.Lock()
	streams := r.byWorker[workerID]
	delete(r.byWorker, workerID)
	r.mu.Unlock()
	// Shut outside r.mu: waiting on one stream's append must not stall
	// every other worker's track and release.
	for _, ts := range streams {
		ts.shut()
	}
	return len(streams)
}
