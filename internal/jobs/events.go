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
	// attempt is the claim attempt this binding belongs to (jobs.attempts
	// right after the claim); every event appended through it carries it.
	// Its identity is the (job id, token) pair the registry keys on: a
	// re-claim registers a NEW binding and an old attempt's still-open
	// stream keeps stamping its own attempt (backlog 2.49).
	attempt int
	// createdAt is the job row's created_at, taken from the claim: the
	// bound the append's max(seq) subquery filters chunks by (backlog
	// 2.50). The bound path of the append has no row read of its own, so
	// the binding must supply the value — a zero here silently disables
	// chunk pruning on the primary append path.
	createdAt time.Time
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

// streamBindingKey identifies one claim binding: the registry keys on
// (job id, token) so each attempt's stream is its own entry and a
// re-claim never evicts a still-open older one.
type streamBindingKey struct {
	jobID uuid.UUID
	token string
}

// streamRegistry maps every live claim binding to its stream state. One
// job can hold several bindings at once — its old attempt's stream may
// still be open while the requeued job was claimed again — and the reaper
// counts a job as live while ANY of them is. In-process by design (see
// SQLQueue's doc comment): the gRPC server's own token registry guards
// the bind, this registry tracks the streams afterwards.
type streamRegistry struct {
	mu      sync.Mutex
	entries map[streamBindingKey]*streamEntry
}

func newStreamRegistry() *streamRegistry {
	return &streamRegistry{entries: map[streamBindingKey]*streamEntry{}}
}

func (r *streamRegistry) register(jobID, orgID uuid.UUID, token string, attempt int, createdAt, now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries[streamBindingKey{jobID, token}] = &streamEntry{
		orgID: orgID, token: token, attempt: attempt, createdAt: createdAt,
		cancel: make(chan struct{}), bound: false, lastSeen: now,
	}
}

// get resolves a job's binding by its claim token — the identity the
// append and cancel paths carry from the bound stream.
func (r *streamRegistry) get(jobID uuid.UUID, token string) (*streamEntry, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.entries[streamBindingKey{jobID, token}]
	return e, ok
}

// latest returns the job's most recently registered binding, if any. The
// only reader is the append path for an event that arrives without a
// binding identity (a late event on an already-consumed gRPC stream); a
// still-bound latest entry adopts it, otherwise the caller falls through
// to the job row.
func (r *streamRegistry) latest(jobID uuid.UUID) (*streamEntry, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var newest *streamEntry
	for key, e := range r.entries {
		if key.jobID != jobID {
			continue
		}
		if newest == nil || e.attempt > newest.attempt {
			newest = e
		}
	}
	return newest, newest != nil
}

// removeAll drops every binding of a job — the terminal transition and
// the requeue paths' cleanup: the old claim is void either way, however
// many attempts of it still hold a binding.
func (r *streamRegistry) removeAll(jobID uuid.UUID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for key := range r.entries {
		if key.jobID == jobID {
			delete(r.entries, key)
		}
	}
}

// touchAll marks every live binding of a job seen: the cancel poll runs
// per job, and each of its open streams — old attempts included — counts
// as a liveness mark.
func (r *streamRegistry) touchAll(jobID uuid.UUID, now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for key, e := range r.entries {
		if key.jobID == jobID {
			e.touch(now)
		}
	}
}

// requestCancelAll closes the cancel signal of every binding the job
// holds, once each.
func (r *streamRegistry) requestCancelAll(jobID uuid.UUID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for key, e := range r.entries {
		if key.jobID == jobID {
			e.requestCancel()
		}
	}
}

// aliveIDs returns the jobs with ANY live binding at cutoff: an old
// attempt's still-streaming run keeps its job out of the reaper's reach
// exactly like the current attempt's would (backlog 2.49).
func (r *streamRegistry) aliveIDs(cutoff time.Time) []uuid.UUID {
	r.mu.Lock()
	defer r.mu.Unlock()
	seen := map[uuid.UUID]struct{}{}
	var out []uuid.UUID
	for key, e := range r.entries {
		if _, dup := seen[key.jobID]; dup || !e.alive(cutoff) {
			continue
		}
		seen[key.jobID] = struct{}{}
		out = append(out, key.jobID)
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
// for tests and future callers. The returned stream is pinned to ITS
// binding — the attempt it was claimed under — so appends through it keep
// stamping that attempt even after a re-claim registered a newer one.
func (q *SQLQueue) BindStream(_ context.Context, jobID uuid.UUID, token string) (Stream, error) {
	e, ok := q.streams.get(jobID, token)
	if !ok {
		return nil, ErrUnknownStream
	}
	e.touch(q.now())
	return boundStream{q: q, jobID: jobID, entry: e}, nil
}

// boundStream is the Stream BindStream hands out: job identity and the
// claim binding (org, attempt) fixed by the token it was bound with.
type boundStream struct {
	q     *SQLQueue
	jobID uuid.UUID
	entry *streamEntry
}

// Append persists the event under the stream's OWN binding: org, attempt
// and the job's created_at come from the entry the bind resolved, so a
// re-claimed job's old stream keeps stamping the attempt it ran under
// (backlog 2.49) and every bound append feeds the max(seq) chunk bound
// the real created_at (backlog 2.50). The touch feeds the reaper's
// liveness marks.
func (s boundStream) Append(ctx context.Context, ev Event) error {
	return s.q.appendEvent(ctx, s.jobID, s.entry.orgID, s.entry.attempt, s.entry.createdAt, ev)
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

// AppendEvent persists one streamed event for a job, resolving its org
// and attempt stamp from a live claim binding. Org-unscoped: the caller
// is the gRPC server, authenticated by the binding token rather than a
// request context. Two shapes arrive here:
//
//   - A bound stream's appends go through boundStream.Append, which pins
//     the stamp to ITS binding (org, attempt) — a re-claimed job's old
//     stream keeps stamping the attempt it ran under (backlog 2.49).
//   - The gRPC EventRecorder port's late appends (the terminal result
//     report and the stream's last events are independent server-side
//     paths, so the result can land — and Finish remove the binding —
//     before the stream handler has drained its last events) call this
//     directly. Those events are part of the run's record (the same
//     intent the cancel path's "events after the directive are still
//     recorded" comment states), so the org and attempt are resolved
//     here: the job's newest LIVE binding adopts them — its touch also
//     feeds the reaper's liveness marks — and otherwise the job row,
//     stamped with the job's CURRENT attempt. The gRPC transport is what
//     authenticates this path — every append arrives on a stream whose
//     binding token was validated and consumed before the first event.
//
// The stored seq is the MANAGER's (final review I5): a worker numbers its
// events per run from 1, so a requeued job's next attempt would repeat the
// first attempt's seqs — hidden behind the run view's seq cursor and
// deduplicated away. The append assigns max(seq)+1 for the job instead,
// under a per-job advisory transaction lock, so seqs stay strictly
// increasing across attempts (the worker's own seq only orders a stream,
// which one gRPC stream delivers in order anyway). The max(seq) subquery
// is bounded by the job's created_at (backlog 2.50) so the hypertable scan
// touches only the job's own chunks.
func (q *SQLQueue) AppendEvent(ctx context.Context, jobID uuid.UUID, ev Event) error {
	orgID, attempt, createdAt, err := q.eventBinding(ctx, jobID)
	if err != nil {
		return err
	}
	return q.appendEvent(ctx, jobID, orgID, attempt, createdAt, ev)
}

// appendEvent is the shared insert: org and attempt already resolved (by
// the pinned binding or the row read), createdAt carrying the job's
// created_at for the max(seq) chunk bound. Every append path supplies the
// real value: a pinned binding carries it from the claim row, the
// unbound adoption rides the binding's, and the row fallback reads it.
func (q *SQLQueue) appendEvent(ctx context.Context, jobID uuid.UUID, orgID uuid.UUID, attempt int, createdAt time.Time, ev Event) error {
	tx, err := q.db.Pool().Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin event append: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, eventSeqLockKeyPrefix+jobID.String()); err != nil {
		return fmt.Errorf("lock job event sequence: %w", err)
	}
	var seq int64
	if err := tx.QueryRow(ctx, AppendJobEventSQL,
		jobID, orgID, ev.At, ev.Host, ev.Task, ev.State, normalizePayload(ev.Payload), attempt, createdAt,
	).Scan(&seq); err != nil {
		return fmt.Errorf("append job event: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit job event: %w", err)
	}
	ev.Seq, ev.Attempt = uint64(seq), attempt
	q.broker.Publish(jobID, ev)
	return nil
}

// eventSeqLockKeyPrefix namespaces the per-job event sequence lock within
// hashtext's key space.
const eventSeqLockKeyPrefix = "job-event-seq:"

// AppendJobEventSQL is the append statement: the manager assigns the next
// seq for the job under the advisory lock. The max(seq) subquery filters
// `at >= $9` — the job's created_at, carried by every append path (the
// pinned binding from the claim row, the adoption branch, or the row
// fallback) — so TimescaleDB prunes the scan to the chunks the job's own
// events live in instead of running max() across every chunk (backlog
// 2.50). The bound is safe as an exclusion: an event's `at` is never
// before the job existed. Exported only for the SQL-text regression guard.
const AppendJobEventSQL = `INSERT INTO job_events (job_id, seq, org_id, at, host, task, state, payload, attempt)
		 SELECT $1, COALESCE((SELECT max(seq) FROM job_events WHERE job_id = $1 AND at >= $9), 0) + 1, $2, $3, $4, $5, $6, $7, $8
		 RETURNING seq`

// eventBinding resolves the organization and attempt an event row is
// stamped with for the UNBOUND append path: the job's newest live binding
// wins (its touch also feeds the reaper's liveness marks; its created_at
// rides along for the seq bound), otherwise the job row supplies the org,
// its CURRENT attempt and the created_at the append's max(seq) chunk
// bound needs (backlog 2.50).
func (q *SQLQueue) eventBinding(ctx context.Context, jobID uuid.UUID) (uuid.UUID, int, time.Time, error) {
	if e, ok := q.streams.latest(jobID); ok && e.alive(q.now().Add(-claimStaleAfter)) {
		e.touch(q.now())
		return e.orgID, e.attempt, e.createdAt, nil
	}
	var orgID uuid.UUID
	var attempts int
	var createdAt time.Time
	if err := q.db.Pool().QueryRow(ctx,
		`SELECT org_id, attempts, created_at FROM jobs WHERE id = $1`, jobID).Scan(&orgID, &attempts, &createdAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, 0, time.Time{}, ErrUnknownStream
		}
		return uuid.Nil, 0, time.Time{}, fmt.Errorf("resolve event org: %w", err)
	}
	return orgID, attempts, createdAt, nil
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
// live bindings' cancel signal on the first yes — that is the one cancel
// directive the gRPC server sends. Org-unscoped, same reasoning as
// AppendEvent. Every live binding of the job is closed: a re-claimed job
// whose old stream is still open must not leave that stream waiting on a
// directive the current attempt's stream already received.
func (q *SQLQueue) Cancelled(ctx context.Context, jobID uuid.UUID) (bool, error) {
	q.streams.touchAll(jobID, q.now())
	var status string
	if err := q.db.Pool().QueryRow(ctx, `SELECT status FROM jobs WHERE id = $1`, jobID).Scan(&status); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, ErrNotFound
		}
		return false, fmt.Errorf("look up job status: %w", err)
	}
	if Status(status) == StatusCancelling {
		q.streams.requestCancelAll(jobID)
		return true, nil
	}
	return false, nil
}
