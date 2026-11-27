package grpcsrv

import (
	"maps"
	"sync"

	"github.com/google/uuid"
)

// StreamRegistry tracks the open RunEvents streams per worker, so a
// revocation can end them (spec §4 "Revocation"). It lives in process: the
// manager that serves a revocation closes its own streams directly, and
// every other manager's workers.RevocationWatcher closes them there once
// the revocation's notification arrives (backlog 2.54).
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
	signal  sync.Once     // closes revoked once, however many shuts run
	// closing (guarded by StreamRegistry.mu) marks a stream a CloseWorker
	// is shutting: release leaves it registered, and that CloseWorker
	// removes it once shut has returned.
	closing bool

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
// lock: once shut returns, no append of the stream runs or starts. It is
// safe to call concurrently: every caller waits for the append in flight.
func (t *trackedStream) shut() {
	t.signal.Do(func() { close(t.revoked) })
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
		if ts.closing {
			return // CloseWorker removes it once the append in flight is done
		}
		r.remove(workerID, id)
	}
}

// remove unregisters one stream; r.mu must be held.
func (r *StreamRegistry) remove(workerID uuid.UUID, id uint64) {
	if streams, ok := r.byWorker[workerID]; ok {
		delete(streams, id)
		if len(streams) == 0 {
			delete(r.byWorker, workerID)
		}
	}
}

// CloseWorker ends every open stream of workerID and reports how many. It
// returns only once none of those streams can store another event: an
// append in flight finishes first, and no later one runs. A revocation
// relies on that to requeue the worker's jobs after this call without a
// stray event landing in the requeued job's log.
//
// The streams stay registered until they are shut, even once their
// handler has ended and released them (it does so as soon as revoked
// closes, while an append may still run): a CloseWorker that runs while
// another is still shutting them (the serving manager's own revocation
// watcher racing Service.Revoke, backlog 2.54) finds them too, counts them
// and waits for the same append in flight. Every call removes the streams
// it shut, so a released stream marked closing never stays behind.
func (r *StreamRegistry) CloseWorker(workerID uuid.UUID) int {
	r.mu.Lock()
	streams := maps.Clone(r.byWorker[workerID])
	for _, ts := range streams {
		ts.closing = true
	}
	r.mu.Unlock()
	// Shut outside r.mu: waiting on one stream's append must not stall
	// every other worker's track and release.
	for _, ts := range streams {
		ts.shut()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for id := range streams {
		r.remove(workerID, id)
	}
	return len(streams)
}

// Workers lists the workers with at least one open stream, each once, in
// no particular order: what the revocation watcher rescans.
func (r *StreamRegistry) Workers() []uuid.UUID {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]uuid.UUID, 0, len(r.byWorker))
	for id := range r.byWorker {
		out = append(out, id)
	}
	return out
}
