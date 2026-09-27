// StreamRegistry tracks the open AgentStream connections per agent host,
// so a revocation can end them (spec v0.3a §8: "Revocation ends the
// agent's streams"). It lives in process: with several managers each ends
// only its own streams, while the interceptor refuses the revoked
// certificate's next call on every manager.
//
// Streams are keyed by the agent's host id — the stable revocation handle
// across certificate renewals (a renewal swaps the serial, never the host
// binding) and exactly what agents.Service.Revoke carries. Each entry
// remembers the serial it authenticated with for logs and tests, and the
// org it authenticated as (checked by PushToHost, never trusted from a
// caller-supplied argument alone).
package agentapi

import (
	"errors"
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
	// orgID is the org the connection authenticated as (agent.OrgID at
	// track time), immutable thereafter — the fact PushToHost checks
	// before ever sending, so a caller-supplied orgID that does not match
	// what this stream actually authenticated as can never reach it, no
	// matter what ResolveHost was asked to compute for.
	orgID uuid.UUID
	// revoked is closed by shut: the handler ends the stream.
	revoked chan struct{}

	mu  sync.Mutex
	cut bool

	// sendMu guards both stream and closed, and is held for the entire
	// duration of every actual Send call — not merely while reading the
	// fields. That is what makes closed a real gate rather than a
	// check-then-act race: a send that is already past the "not closed"
	// check has sendMu held, so closeSend (called from shut and from
	// track's release closure) cannot mark the stream closed until that
	// send has returned, and a send that starts after closeSend has run
	// always observes closed and never touches stream at all — including
	// one dispatched asynchronously by PushToHost that only gets around
	// to acquiring sendMu long after the handler that owns this stream
	// has already returned (see PushToHost's own doc comment for why
	// sends are dispatched, never awaited, from that path).
	sendMu sync.Mutex
	stream agentpb.AgentService_AgentStreamServer
	closed bool
}

// errStreamClosed means the tracked stream was released or cut before (or
// while) a send was attempted; PushToHost logs it at most, never treats
// it as a fault — an agent that has already disconnected is exactly what
// its own next reconnect and heartbeat re-assert exist to repair.
var errStreamClosed = errors.New("stream closed")

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
// other send on this same trackedStream and refusing once the stream has
// been released or cut (errStreamClosed) — see the sendMu field comment
// for why this check and the Send call itself must share one lock.
func (t *trackedStream) send(msg *agentpb.ManagerMessage) error {
	t.sendMu.Lock()
	defer t.sendMu.Unlock()
	if t.closed {
		return errStreamClosed
	}
	return t.stream.Send(msg)
}

// closeSend marks the stream closed for sending. Idempotent; called once
// by shut (a revocation) and once more by track's release closure (the
// handler returning control of the stream to gRPC) — whichever comes
// first. gRPC's own contract forbids calling Send after a stream's
// handler function has returned, which is the hazard this specifically
// closes off for an async PushToHost dispatch that resolves late.
func (t *trackedStream) closeSend() {
	t.sendMu.Lock()
	defer t.sendMu.Unlock()
	t.closed = true
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
// starts. closeSend also runs here (not only from release) so a
// revoked-but-not-yet-returned stream stops accepting pushes immediately,
// rather than waiting for the handler to unwind.
func (t *trackedStream) shut() {
	close(t.revoked)
	t.mu.Lock()
	defer t.mu.Unlock()
	t.cut = true
	t.closeSend()
}

// NewStreamRegistry builds an empty registry.
func NewStreamRegistry() *StreamRegistry {
	return &StreamRegistry{byHost: map[uuid.UUID]map[uint64]*trackedStream{}}
}

// track registers one open stream of hostID (authenticated with serial, as
// orgID), with the gRPC stream a push can reach it through already
// attached — atomically, since the entry is not inserted into r.byHost
// (and so not visible to a concurrent PushToHost) until after ts.stream is
// set. The returned release closure marks the stream closed for sending
// before unregistering it: by the time AgentStream's handler function
// actually returns (release runs via defer, right before that), no send
// dispatched earlier can still be in flight or start afterward — see
// closeSend's own doc comment. CloseHost cuts the stream when the agent is
// revoked.
func (r *StreamRegistry) track(serial string, orgID, hostID uuid.UUID, stream agentpb.AgentService_AgentStreamServer) (*trackedStream, func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.next++
	id, ts := r.next, &trackedStream{serial: serial, orgID: orgID, revoked: make(chan struct{}), stream: stream}
	if r.byHost[hostID] == nil {
		r.byHost[hostID] = map[uint64]*trackedStream{}
	}
	r.byHost[hostID][id] = ts
	return ts, func() {
		ts.closeSend()
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

// PushToHost dispatches msg to every currently open stream of hostID
// authenticated as orgID, and reports how many it dispatched to — zero
// for a host with no open stream (PushHost's own "not connected is a
// no-op" contract) and zero for every stream that authenticated as a
// different org (a caller-supplied orgID is never trusted merely because
// ResolveHost happened to answer something for it — see trackedStream's
// own orgID field comment: an org mismatch here would otherwise arm
// another tenant's agent with an empty CheckConfig it never asked for).
//
// The actual Send calls run on their own goroutine per stream, never on
// the calling goroutine: gRPC's SendMsg blocks on the stream's own flow
// control until the client drains it or the connection ends, which can
// be as long as the server's MaxConnectionAge (24h, no keepalive ping,
// internal/grpcsrv) if the agent is black-holed rather than merely slow.
// PushHost is called synchronously from an HTTP write path (Task 9); it
// must never block that request on a single unreachable agent's stream,
// so "dispatched to" is what this method's return value promises —
// never "delivered", which nothing here can observe without blocking on
// it anyway. A dispatch that never completes is exactly as harmless as a
// missed push always was: the agent's own heartbeat re-assert converges
// it regardless (see processHeartbeat), so nothing is lost by not
// waiting, only a redundant push is possibly avoided by not knowing.
func (r *StreamRegistry) PushToHost(orgID, hostID uuid.UUID, msg *agentpb.ManagerMessage) int {
	r.mu.Lock()
	streams := make([]*trackedStream, 0, len(r.byHost[hostID]))
	for _, ts := range r.byHost[hostID] {
		if ts.orgID != orgID {
			slog.Warn("push host: org mismatch, refusing to push",
				"host_id", hostID.String(), "want_org", orgID.String(), "stream_org", ts.orgID.String())
			continue
		}
		streams = append(streams, ts)
	}
	r.mu.Unlock()
	for _, ts := range streams {
		ts := ts
		go func() {
			if err := ts.send(msg); err != nil {
				slog.Warn("push message to agent stream", "host_id", hostID.String(), "err", err)
			}
		}()
	}
	return len(streams)
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
