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
	"log/slog"
	"sync"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/pb/agentpb"
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

	// sendMu serializes every access to stream: the receive loop's own ack
	// (with an optional CheckConfig riding along) and an out-of-band
	// PushToHost call must never race on the same gRPC stream — both the
	// concurrent-Send gRPC forbids and the plain data race the field
	// itself would otherwise be under (attach's write against send's
	// read). track sets stream directly (no lock needed: the object is
	// not yet reachable by any other goroutine at that point, since it is
	// published into the registry's map under the very same r.mu), so in
	// the ordinary AgentStream path stream is never nil by the time a
	// PushToHost call can reach it at all; attach exists for the direct
	// receive-loop unit tests, which build a trackedStream without going
	// through track.
	sendMu sync.Mutex
	stream agentpb.AgentService_AgentStreamServer
}

// attach records the gRPC stream a tracked entry authenticates. track
// already does this for every stream reachable through the registry;
// attach is what the direct-construction unit tests
// (receiveMessages called on a bare &trackedStream{}) use instead.
func (t *trackedStream) attach(stream agentpb.AgentService_AgentStreamServer) {
	t.sendMu.Lock()
	defer t.sendMu.Unlock()
	t.stream = stream
}

// send writes msg to the underlying stream, serialized against every
// other send on this same trackedStream.
func (t *trackedStream) send(msg *agentpb.ManagerMessage) error {
	t.sendMu.Lock()
	defer t.sendMu.Unlock()
	return t.stream.Send(msg)
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

// track registers one open stream of hostID (authenticated with serial),
// with the gRPC stream a push can reach it through already attached —
// atomically, since the entry is not inserted into r.byHost (and so not
// visible to a concurrent PushToHost) until after ts.stream is set.
// CloseHost cuts it when the agent is revoked, release unregisters it.
func (r *StreamRegistry) track(serial string, hostID uuid.UUID, stream agentpb.AgentService_AgentStreamServer) (*trackedStream, func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.next++
	id, ts := r.next, &trackedStream{serial: serial, revoked: make(chan struct{}), stream: stream}
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

// PushToHost sends msg on every currently open, not-yet-cut stream of
// hostID and reports how many it reached — zero for a host with no open
// stream, which is exactly PushHost's own "not connected is a no-op"
// contract; nothing here remembers a miss, since the agent's own
// heartbeat re-assert heals it. A send failure on one stream is logged
// and does not stop the others.
func (r *StreamRegistry) PushToHost(hostID uuid.UUID, msg *agentpb.ManagerMessage) int {
	r.mu.Lock()
	streams := make([]*trackedStream, 0, len(r.byHost[hostID]))
	for _, ts := range r.byHost[hostID] {
		streams = append(streams, ts)
	}
	r.mu.Unlock()
	sent := 0
	for _, ts := range streams {
		ts.mu.Lock()
		cut := ts.cut
		ts.mu.Unlock()
		if cut {
			continue
		}
		if err := ts.send(msg); err != nil {
			slog.Warn("push message to agent stream", "host_id", hostID.String(), "err", err)
			continue
		}
		sent++
	}
	return sent
}

// CloseHostExcept ends every open stream of hostID authenticated with a
// serial other than keepSerial and reports how many it cut — the
// re-enrollment path's cut: the swap revokes the replaced serial, so its
// streams end, while the fresh serial's (if any) stay registered. The
// same in-flight guarantee as CloseHost applies to the cut streams.
func (r *StreamRegistry) CloseHostExcept(hostID uuid.UUID, keepSerial string) int {
	r.mu.Lock()
	streams := r.byHost[hostID]
	var stale []*trackedStream
	for id, ts := range streams {
		if ts.serial != keepSerial {
			stale = append(stale, ts)
			delete(streams, id)
		}
	}
	if len(streams) == 0 {
		delete(r.byHost, hostID)
	}
	r.mu.Unlock()
	for _, ts := range stale {
		ts.shut()
	}
	return len(stale)
}
