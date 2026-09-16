// Run events: the bound-stream registry the queue-side event path runs on,
// the Stream a bound run writes into, and the org-scoped event read.
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Stream is one run's event channel, bound by a claim token. Append writes
// an event for exactly the bound job; Cancelled closes when the job is
// asked to cancel; Close marks the stream ended (the reaper may then
// requeue a still-running job).
type Stream interface {
	Append(ctx context.Context, ev Event) error
	Cancelled() <-chan struct{}
	Close()
}

// streamEntry is the registry's live state for one claim: the binding
// token, the cancel signal, and the liveness marks the reaper reads.
type streamEntry struct {
	orgID uuid.UUID
	token string
	// cancel is closed exactly once, when a Cancelled poll first observes
	// the job in cancelling state.
	cancel     chan struct{}
	cancelOnce sync.Once
	mu         sync.Mutex
	// bound is set the first time the stream is seen alive — a BindStream
	// call, an event or a cancel poll — and cleared by Close. lastSeen is
	// refreshed by every event and every cancel poll, which the gRPC
	// server runs every DefaultCancelPollInterval while a stream is open,
	// so it doubles as a stream heartbeat.
	bound    bool
	lastSeen time.Time
}

func (e *streamEntry) touch(now time.Time) {
	e.mu.Lock()
	e.bound = true
	e.lastSeen = now
	e.mu.Unlock()
}

func (e *streamEntry) markClosed() {
	e.mu.Lock()
	e.bound = false
	e.mu.Unlock()
}

func (e *streamEntry) alive(cutoff time.Time) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.bound && e.lastSeen.After(cutoff)
}

func (e *streamEntry) requestCancel() { e.cancelOnce.Do(func() { close(e.cancel) }) }

// streamRegistry maps a claimed job to its live stream state. In-process
// by design (see SQLQueue's doc comment): the gRPC server's own token
// registry guards the bind, this registry tracks the stream afterwards.
type streamRegistry struct {
	mu      sync.Mutex
	entries map[uuid.UUID]*streamEntry
}

func newStreamRegistry() *streamRegistry {
	return &streamRegistry{entries: map[uuid.UUID]*streamEntry{}}
}

func (r *streamRegistry) register(jobID, orgID uuid.UUID, token string, now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries[jobID] = &streamEntry{orgID: orgID, token: token, cancel: make(chan struct{}), bound: false, lastSeen: now}
}

func (r *streamRegistry) get(jobID uuid.UUID) (*streamEntry, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.entries[jobID]
	return e, ok
}

func (r *streamRegistry) remove(jobID uuid.UUID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.entries, jobID)
}

// aliveIDs returns the jobs whose stream is marked alive at cutoff.
func (r *streamRegistry) aliveIDs(cutoff time.Time) []uuid.UUID {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []uuid.UUID
	for id, e := range r.entries {
		if e.alive(cutoff) {
			out = append(out, id)
		}
	}
	return out
}

// cancelRegistry remembers when a job's cancel was requested, the reaper's
// deadline anchor. Same in-process limitation as the stream registry.
type cancelRegistry struct {
	mu sync.Mutex
	at map[uuid.UUID]time.Time
}

func newCancelRegistry() *cancelRegistry {
	return &cancelRegistry{at: map[uuid.UUID]time.Time{}}
}

func (r *cancelRegistry) note(jobID uuid.UUID, at time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.at[jobID] = at
}

func (r *cancelRegistry) get(jobID uuid.UUID) (time.Time, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	at, ok := r.at[jobID]
	return at, ok
}

func (r *cancelRegistry) remove(jobID uuid.UUID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.at, jobID)
}

// BindStream validates a claim's binding token and returns the run's
// stream. The gRPC server binds streams through its own token registry
// (single-use, 30 s TTL) and then talks to the queue through the
// EventRecorder port; BindStream is the direct form of the same binding
// for tests and future callers.
func (q *SQLQueue) BindStream(ctx context.Context, jobID uuid.UUID, token string) (Stream, error) {
	e, ok := q.streams.get(jobID)
	if !ok || e.token != token {
		return nil, ErrUnknownStream
	}
	e.touch(q.now())
	return boundStream{q: q, jobID: jobID, entry: e}, nil
}

// boundStream is the Stream BindStream hands out: job identity fixed by
// the binding.
type boundStream struct {
	q     *SQLQueue
	jobID uuid.UUID
	entry *streamEntry
}

func (s boundStream) Append(ctx context.Context, ev Event) error {
	return s.q.AppendEvent(ctx, s.jobID, ev)
}

func (s boundStream) Cancelled() <-chan struct{} { return s.entry.cancel }

func (s boundStream) Close() { s.entry.markClosed() }

// Broker fans the events appended to a job's stream out to that job's
// in-process SSE subscribers (the run view's live log). It is fed by
// SQLQueue.AppendEvent — the same call the gRPC EventRecorder port lands
// on — so a subscriber sees exactly what the worker streamed, in stream
// order. Like the stream and cancel registries it is in-process by
// design (see SQLQueue's doc comment): a run view subscribed on another
// manager instance polls /events instead.
//
// Delivery to one subscriber is best-effort, never blocking: a send onto
// a subscriber whose buffer is full is dropped, because the publisher is
// the worker's event stream — stalling it on a slow run view would
// throttle the run itself. A subscriber that misses events recovers by
// reading the poll fallback (GET /events?after=) with the last seq it
// saw. Buffer capacity BrokerBufferCap is sized for a burst of ansible
// output between reads.
type Broker struct {
	mu   sync.Mutex
	subs map[uuid.UUID]map[*brokerSub]struct{}
}

// brokerSub is one subscriber: its channel and the seq floor it
// subscribed with (it only ever sees events with seq > after).
type brokerSub struct {
	ch    chan Event
	after int64
}

// BrokerBufferCap is each subscriber's buffer, in events: comfortably
// more than a busy play's task callbacks between two SSE flushes, small
// enough that a dead subscriber's memory is bounded.
const BrokerBufferCap = 256

// NewBroker builds an empty broker. The jobs service owns one; the queue
// publishes into it through SetEventBroker.
func NewBroker() *Broker {
	return &Broker{subs: map[uuid.UUID]map[*brokerSub]struct{}{}}
}

// Subscribe registers a subscriber for jobID that receives only events
// with seq > after (0 = everything from subscription time on), and the
// function that unsubscribes it. The unsubscribe is idempotent, removes
// the subscriber before anything else and closes the channel once — a
// late Publish can neither block nor resurrect it.
func (b *Broker) Subscribe(jobID uuid.UUID, after int64) (<-chan Event, func()) {
	sub := &brokerSub{ch: make(chan Event, BrokerBufferCap), after: after}
	b.mu.Lock()
	if b.subs == nil {
		b.subs = map[uuid.UUID]map[*brokerSub]struct{}{}
	}
	set := b.subs[jobID]
	if set == nil {
		set = map[*brokerSub]struct{}{}
		b.subs[jobID] = set
	}
	set[sub] = struct{}{}
	b.mu.Unlock()
	var once sync.Once
	return sub.ch, func() {
		once.Do(func() {
			b.mu.Lock()
			if set, ok := b.subs[jobID]; ok {
				delete(set, sub)
				if len(set) == 0 {
					delete(b.subs, jobID)
				}
			}
			b.mu.Unlock()
			close(sub.ch)
		})
	}
}

// Publish delivers one appended event to every current subscriber of the
// job. Non-blocking: a full buffer drops the event for that subscriber
// (never for the worker), see Broker's doc comment. Safe on a nil
// receiver, so the append path needs no wiring check.
func (b *Broker) Publish(jobID uuid.UUID, ev Event) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for sub := range b.subs[jobID] {
		if int64(ev.Seq) <= sub.after {
			continue // the subscriber asked to skip this seq range
		}
		select {
		case sub.ch <- ev:
		default: // slow subscriber: drop, it recovers via the poll fallback
		}
	}
}

// Len reports how many subscribers jobID currently has. It exists for the
// no-leak assertion (a cancelled HTTP request unsubscribes); it is not
// used by the append path.
func (b *Broker) Len(jobID uuid.UUID) int {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subs[jobID])
}

// Subscribe is the org-scoped live tail the run view's SSE stream is
// built on: the job is resolved first, so a job id from another
// organization is exactly as invisible as one that does not exist
// (Events' own rule), and only then is the in-process broker — fed by the
// gRPC append path — subscribed to. The returned cancel unsubscribes; a
// caller that drops it without calling it (an SSE handler's deferred
// cancel is the normal user) keeps the subscriber until the process
// ends, so the transport always defers it.
func (s *Service) Subscribe(ctx context.Context, orgID, jobID uuid.UUID, after int64) (<-chan Event, func(), error) {
	if _, err := s.store.GetJob(ctx, orgID, jobID); err != nil {
		return nil, nil, err
	}
	ch, cancel := s.broker.Subscribe(jobID, after)
	return ch, cancel, nil
}

// EventsBroker exposes the Service's fan-out registry for the one
// post-construction wiring step internal/app and the apitest harness do
// (queue.SetEventBroker(svc.EventsBroker())). Nothing else should touch
// the broker directly: Subscribe is the read surface.
func (s *Service) EventsBroker() *Broker { return s.broker }

// AppendEvent persists one streamed event for a job with a live claim
// binding. Org-unscoped: the caller is the gRPC server, authenticated by
// the binding token rather than a request context; the registry entry
// carries the organization the claim belongs to, and that is the org_id
// the row is stamped with.
func (q *SQLQueue) AppendEvent(ctx context.Context, jobID uuid.UUID, ev Event) error {
	e, ok := q.streams.get(jobID)
	if !ok {
		return ErrUnknownStream
	}
	e.touch(q.now())
	_, err := q.db.Pool().Exec(ctx,
		`INSERT INTO job_events (job_id, seq, org_id, at, host, task, state, payload)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		jobID, int64(ev.Seq), e.orgID, ev.At, ev.Host, ev.Task, ev.State, normalizePayload(ev.Payload))
	if err != nil {
		return fmt.Errorf("append job event: %w", err)
	}
	q.broker.Publish(jobID, ev)
	return nil
}

// normalizePayload stores the event payload as jsonb: valid JSON passes
// through, anything else (a bare stdout line) becomes a JSON string, and
// nothing becomes the empty document.
func normalizePayload(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage(`{}`)
	}
	if json.Valid(raw) {
		return raw
	}
	b, err := json.Marshal(string(raw))
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return b
}

// Cancelled reports whether the job is in cancelling state, and closes the
// bound stream's cancel signal on the first yes — that is the one cancel
// directive the gRPC server sends. Org-unscoped, same reasoning as
// AppendEvent.
func (q *SQLQueue) Cancelled(ctx context.Context, jobID uuid.UUID) (bool, error) {
	e, ok := q.streams.get(jobID)
	if !ok {
		return false, ErrUnknownStream
	}
	e.touch(q.now())
	var status string
	if err := q.db.Pool().QueryRow(ctx, `SELECT status FROM jobs WHERE id = $1`, jobID).Scan(&status); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, ErrNotFound
		}
		return false, fmt.Errorf("look up job status: %w", err)
	}
	if Status(status) == StatusCancelling {
		e.requestCancel()
		return true, nil
	}
	return false, nil
}
