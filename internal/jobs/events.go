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
