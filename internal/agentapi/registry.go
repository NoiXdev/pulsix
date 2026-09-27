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

// trackedStream is one open stream. Its mu orders every message
// processing against the cut: processing runs under it, and shut takes it,
// so once shut has returned no processing of the stream is in flight and
// none starts. Everything below mu is the *outgoing* side, which is a
// separate concern with its own, deliberately lock-light, teardown story
// — see the mailbox fields' own comment for why.
type trackedStream struct {
	serial string
	// orgID is the org the connection authenticated as (agent.OrgID at
	// track time), immutable thereafter — the fact PushToHost checks
	// before ever queueing a push, so a caller-supplied orgID that does
	// not match what this stream actually authenticated as can never
	// reach it, no matter what ResolveHost was asked to compute for.
	orgID uuid.UUID
	// revoked is closed by shut: the handler ends the stream.
	revoked chan struct{}

	mu  sync.Mutex
	cut bool

	stream agentpb.AgentService_AgentStreamServer

	// sendMu serializes the two things that ever call Send on stream: the
	// receive loop's own ack (send, below — a direct, synchronous call
	// from receiveMessages, exactly as before this stream carried
	// out-of-band pushes at all) and pushWriter's own sends, one at a
	// time, as gRPC requires. It is held only for the duration of one
	// Send call, and — this is the property the rest of this type exists
	// to preserve — nothing that tears the stream down ever waits on it.
	sendMu sync.Mutex

	// The outgoing-push mailbox: a single slot for the latest configuration
	// PushToHost wants delivered, not a queue. Only the newest push is
	// ever worth delivering — an older one that has not gone out yet is
	// already obsolete the moment a newer one arrives — so queuePush
	// replaces whatever is queued rather than appending to it: the slot
	// can never fill up, and there is nothing to block a caller on or
	// reject. wake (buffered 1) tells pushWriter a value is waiting;
	// closed, closed exactly once via closeOnce, tells it to stop once it
	// has drained whatever is there. Closing a channel can never block
	// regardless of what pushWriter is doing at that instant — including
	// stuck inside a wedged Send — which is what makes release/shut safe
	// to call unconditionally: before this design, closing the send side
	// took sendMu itself, which a wedged Send could hold indefinitely,
	// turning teardown (a revocation, or the handler's own return) into
	// exactly the deadlock this comment is here to explain the absence
	// of. writerDone is closed by pushWriter right before it returns —
	// its own witness, for tests, that the goroutine actually goes away
	// rather than leaking.
	mailboxMu  sync.Mutex
	mailbox    *agentpb.ManagerMessage
	wake       chan struct{}
	closed     chan struct{}
	closeOnce  sync.Once
	writerDone chan struct{}
}

// attach records the gRPC stream a tracked entry authenticates. track
// already does this for every stream reachable through the registry;
// attach is what the direct-construction unit tests
// (receiveMessages called on a bare &trackedStream{}) use instead. Those
// bare instances never go through track and so never get a pushWriter —
// attach exists purely so their own (synchronous, ack-only) send has
// something to call.
func (t *trackedStream) attach(stream agentpb.AgentService_AgentStreamServer) {
	t.sendMu.Lock()
	defer t.sendMu.Unlock()
	t.stream = stream
}

// send writes msg to the underlying stream directly, serialized against
// pushWriter's own sends by sendMu. This is the receive loop's own path
// (an ack, with an optional CheckConfig riding along) — synchronous on
// purpose, so a failure here ends receiveMessages's loop immediately, the
// same contract it has always had.
func (t *trackedStream) send(msg *agentpb.ManagerMessage) error {
	t.sendMu.Lock()
	defer t.sendMu.Unlock()
	return t.stream.Send(msg)
}

// queuePush replaces the mailbox's pending value (coalescing: only the
// newest configuration is ever worth keeping) and wakes pushWriter if it
// is not already awake. It never blocks: the mailbox is a single slot
// under its own short-lived mutex, and waking is a non-blocking send on a
// buffered-1 channel. A push queued after closed has already fired is
// simply never collected — harmless, not a leak (the whole trackedStream
// becomes garbage once it is unregistered).
func (t *trackedStream) queuePush(msg *agentpb.ManagerMessage) {
	select {
	case <-t.closed:
		return
	default:
	}
	t.mailboxMu.Lock()
	t.mailbox = msg
	t.mailboxMu.Unlock()
	select {
	case t.wake <- struct{}{}:
	default: // a wake is already pending; pushWriter will see the latest value regardless
	}
}

// takeMailbox atomically empties the mailbox and returns whatever was
// there (nil if nothing is, or nothing new since the last take).
func (t *trackedStream) takeMailbox() *agentpb.ManagerMessage {
	t.mailboxMu.Lock()
	defer t.mailboxMu.Unlock()
	msg := t.mailbox
	t.mailbox = nil
	return msg
}

// pushWriter is the one goroutine, for this stream's whole lifetime, that
// ever sends a queued push — one per stream, never one per push. It
// drains the mailbox as pushes arrive and stops once closed fires,
// signalling writerDone either way. A Send failure ends it immediately
// (the stream is broken; the receive loop's own Recv will independently
// notice the same breakage and end the RPC — pushWriter does not need to
// tell it to).
func (t *trackedStream) pushWriter() {
	defer close(t.writerDone)
	for {
		select {
		case <-t.wake:
			msg := t.takeMailbox()
			if msg == nil {
				continue // coalesced away, or a stale wake from a push already taken
			}
			t.sendMu.Lock()
			err := t.stream.Send(msg)
			t.sendMu.Unlock()
			if err != nil {
				slog.Warn("push message to agent stream", "serial", t.serial, "err", err)
				return
			}
		case <-t.closed:
			return
		}
	}
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
// starts. closeMailbox (a plain, non-blocking channel close, never sendMu)
// also runs here, not only from track's release closure, so a
// revoked-but-not-yet-returned stream stops accepting new pushes
// immediately rather than waiting for the handler to unwind.
func (t *trackedStream) shut() {
	close(t.revoked)
	t.mu.Lock()
	defer t.mu.Unlock()
	t.cut = true
	t.closeMailbox()
}

// closeMailbox stops pushWriter (idempotent — shut and track's release
// closure can each call it). It is a plain close() and nothing else: no
// lock is taken, so this can never be held up by a Send in flight,
// wedged or otherwise — the property this whole design exists to give
// teardown. A bare &trackedStream{} built directly by the fault-path unit
// tests (never through track, so closed is nil and there is no pushWriter
// to stop) is a no-op here rather than a nil-channel panic.
func (t *trackedStream) closeMailbox() {
	t.closeOnce.Do(func() {
		if t.closed != nil {
			close(t.closed)
		}
	})
}

// NewStreamRegistry builds an empty registry.
func NewStreamRegistry() *StreamRegistry {
	return &StreamRegistry{byHost: map[uuid.UUID]map[uint64]*trackedStream{}}
}

// track registers one open stream of hostID (authenticated with serial, as
// orgID), with the gRPC stream a push can reach it through already
// attached — atomically, since the entry is not inserted into r.byHost
// (and so not visible to a concurrent PushToHost) until after ts.stream is
// set — and its pushWriter goroutine already running. The returned release
// closure closes the mailbox before unregistering the entry: a plain,
// non-blocking channel close, so the handler's own defer release() can
// never be the thing that hangs (see trackedStream's own mailbox comment).
// CloseHost cuts the stream when the agent is revoked.
func (r *StreamRegistry) track(serial string, orgID, hostID uuid.UUID, stream agentpb.AgentService_AgentStreamServer) (*trackedStream, func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.next++
	id := r.next
	ts := &trackedStream{
		serial: serial, orgID: orgID, revoked: make(chan struct{}), stream: stream,
		wake: make(chan struct{}, 1), closed: make(chan struct{}), writerDone: make(chan struct{}),
	}
	go ts.pushWriter()
	if r.byHost[hostID] == nil {
		r.byHost[hostID] = map[uint64]*trackedStream{}
	}
	r.byHost[hostID][id] = ts
	return ts, func() {
		ts.closeMailbox()
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

// PushToHost queues msg into the mailbox of every currently open stream of
// hostID authenticated as orgID, and reports how many it queued into —
// zero for a host with no open stream (PushHost's own "not connected is a
// no-op" contract) and zero for every stream that authenticated as a
// different org (a caller-supplied orgID is never trusted merely because
// ResolveHost happened to answer something for it — see trackedStream's
// own orgID field comment: an org mismatch here would otherwise arm
// another tenant's agent with a CheckConfig it never asked for).
//
// Queueing is synchronous and effectively instant (a short-lived mutex
// plus a non-blocking channel send — see queuePush); the actual Send runs
// later, on that stream's own long-lived pushWriter goroutine, never on
// this call's goroutine. That is what keeps PushHost — called
// synchronously from an HTTP write path (Task 9) — from ever blocking on
// a single unreachable agent's stream, which can otherwise wedge for as
// long as the connection itself survives (internal/grpcsrv sets
// MaxConnectionAge but no MaxConnectionAgeGrace and no keepalive ping;
// GOAWAY does not touch an RPC already in flight, so a black-holed agent
// is not a bounded wait at all). "Queued" is what this method's return
// value promises — never "delivered", which nothing here can observe
// without waiting on it, the exact thing this design avoids.
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
		ts.queuePush(msg)
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
