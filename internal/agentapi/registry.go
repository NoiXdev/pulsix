// StreamRegistry tracks the open AgentStream connections per agent host,
// so a revocation can end them (spec v0.3a §8: "Revocation ends the
// agent's streams"). It lives in process: with several managers each ends
// only its own streams, while the interceptor refuses the revoked
// certificate's next call on every manager.
//
// Streams are keyed by the agent's host id — the stable revocation handle
// across certificate renewals (a renewal swaps the serial, never the host
// binding) and exactly what agents.Service.Revoke carries. Each entry
// remembers the serial it authenticated with for logs and tests.
package agentapi

import (
	"sync"

	"github.com/google/uuid"
)

// StreamRegistry tracks open agent streams. It implements the agents
// package's StreamCutter port (CloseHost), which Service.Revoke calls.
type StreamRegistry struct {
	mu     sync.Mutex
	next   uint64
	byHost map[uuid.UUID]map[uint64]*trackedStream
}

// trackedStream is one open stream. Its lock orders every message
// processing against the cut: processing runs under it, and shut takes it,
// so once shut has returned no processing of the stream is in flight and
// none starts.
type trackedStream struct {
	serial string
	// revoked is closed by shut: the handler ends the stream.
	revoked chan struct{}

	mu  sync.Mutex
	cut bool
}

// store runs process unless the stream was cut; a cut stream drops the
// message and reports done=false. The lock is held across process on
// purpose: a revocation waits for a processing step in flight rather than
// racing it (a heartbeat touching last_seen or a fact landing after the
// revocation committed must not happen).
func (t *trackedStream) store(process func() error) (done bool, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.cut {
		return false, nil
	}
	return true, process()
}

// shut cuts the stream. It signals the handler first, so the RPC ends (and
// its context cancels a processing step in flight) without waiting, then
// takes the lock: once shut returns, no processing of the stream runs or
// starts.
func (t *trackedStream) shut() {
	close(t.revoked)
	t.mu.Lock()
	defer t.mu.Unlock()
	t.cut = true
}

// NewStreamRegistry builds an empty registry.
func NewStreamRegistry() *StreamRegistry {
	return &StreamRegistry{byHost: map[uuid.UUID]map[uint64]*trackedStream{}}
}

// track registers one open stream of hostID (authenticated with serial);
// CloseHost cuts it when the agent is revoked, release unregisters it.
func (r *StreamRegistry) track(serial string, hostID uuid.UUID) (*trackedStream, func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.next++
	id, ts := r.next, &trackedStream{serial: serial, revoked: make(chan struct{})}
	if r.byHost[hostID] == nil {
		r.byHost[hostID] = map[uint64]*trackedStream{}
	}
	r.byHost[hostID][id] = ts
	return ts, func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		if streams, ok := r.byHost[hostID]; ok {
			delete(streams, id)
			if len(streams) == 0 {
				delete(r.byHost, hostID)
			}
		}
	}
}

// CloseHost ends every open stream of hostID and reports how many. It
// returns only once none of those streams can process another message: a
// step in flight finishes first, and no later one runs.
func (r *StreamRegistry) CloseHost(hostID uuid.UUID) int {
	r.mu.Lock()
	streams := r.byHost[hostID]
	delete(r.byHost, hostID)
	r.mu.Unlock()
	// Shut outside r.mu: waiting on one stream's processing must not stall
	// every other agent's track and release.
	for _, ts := range streams {
		ts.shut()
	}
	return len(streams)
}
