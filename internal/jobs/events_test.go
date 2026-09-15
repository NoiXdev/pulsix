package jobs_test

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/jobs"
)

// claimOne inserts one queued job and claims it on q, returning the claim.
func claimOne(t *testing.T, d *db.DB, org, zone uuid.UUID, q *jobs.SQLQueue) *jobs.Claim {
	t.Helper()
	// The queue plumbing under test is kind-agnostic; adhoc keeps the
	// claim from depending on a rendered probe target the way a
	// connection_test claim (fail-closed) would.
	j := newJob(org, zone, jobs.KindAdhoc, jobs.StatusQueued)
	if err := jobs.NewSQLStore(d).CreateJob(db.WithOrgScope(ctx, org), j, nil); err != nil {
		t.Fatal(err)
	}
	claim, err := q.Claim(ctx, []uuid.UUID{zone}, "v1")
	if err != nil || claim == nil {
		t.Fatalf("Claim = %v, %v", claim, err)
	}
	return claim
}

func newTestQueue(d *db.DB) *jobs.SQLQueue {
	queue := jobs.NewSQLQueue(d, fakeSealer{}, emptySources{}, emptySources{}, emptySources{}, emptySources{}, time.Now, rand.Reader)
	// A connection_test claim fails closed without a host-key pin source —
	// these tests claim connection_test jobs, so they carry the empty fake.
	queue.SetKeyPins(emptyPins{})
	return queue
}

func TestAppendPersistsMonotonicSeqsAndEventsPaginate(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	org, other := seedOrg(t, q, "jobs-events-org"), seedOrg(t, q, "jobs-events-other")
	zone := uuid.Must(uuid.NewV7())
	queue := newTestQueue(d)
	svc := jobs.NewService(jobs.NewSQLStore(d), queue, emptySources{}, nil, nil, nil, fakeSealer{}, time.Now, rand.Reader)

	claim := claimOne(t, d, org, zone, queue)
	jobID := uuid.MustParse(claim.Job.GetId())

	// A bare stdout line is normalized into a JSON string at rest; an
	// empty payload becomes the empty document; JSON round trips.
	events := []jobs.Event{
		{Seq: 1, Host: "web1", Task: "Gathering Facts", State: "ok", Payload: []byte("line one"), At: time.Now()},
		{Seq: 2, State: "ok", Payload: []byte(""), At: time.Now()},
		{Seq: 3, Host: "web1", Task: "task", State: "changed", Payload: []byte(`{"delta":1}`), At: time.Now()},
	}
	for _, e := range events {
		if err := queue.AppendEvent(ctx, jobID, e); err != nil {
			t.Fatalf("AppendEvent %d: %v", e.Seq, err)
		}
	}

	events, err := svc.Events(db.WithOrgScope(ctx, org), org, jobID, 0)
	if err != nil || len(events) != 3 {
		t.Fatalf("Events = %d, %v", len(events), err)
	}
	for i, e := range events {
		if e.Seq != uint64(i+1) {
			t.Fatalf("event %d has seq %d", i, e.Seq)
		}
	}
	if events[0].Payload[0] != '"' {
		t.Fatalf("non-JSON payload must be stored as a JSON string: %s", events[0].Payload)
	}
	// jsonb normalizes whitespace, so compare decoded.
	var decoded map[string]any
	if err := json.Unmarshal(events[2].Payload, &decoded); err != nil || decoded["delta"] != float64(1) {
		t.Fatalf("JSON payload must round trip: %s, %v", events[2].Payload, err)
	}

	tail, err := svc.Events(db.WithOrgScope(ctx, org), org, jobID, 2)
	if err != nil || len(tail) != 1 || tail[0].Seq != 3 {
		t.Fatalf("Events(after 2) = %+v, %v", tail, err)
	}

	// Another organization must neither see the job nor its events.
	if _, err := svc.Events(db.WithOrgScope(ctx, other), other, jobID, 0); !errors.Is(err, jobs.ErrNotFound) {
		t.Fatalf("other org's Events: %v", err)
	}

	// Events for a job with no live claim binding are rejected.
	if err := queue.AppendEvent(ctx, uuid.Must(uuid.NewV7()), jobs.Event{Seq: 1, At: time.Now()}); !errors.Is(err, jobs.ErrUnknownStream) {
		t.Fatalf("unknown stream: %v", err)
	}
}

func TestCancelFlowAndFinishIdempotency(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	org := seedOrg(t, q, "jobs-cancel-org")
	zone := uuid.Must(uuid.NewV7())
	queue := newTestQueue(d)
	svc := jobs.NewService(jobs.NewSQLStore(d), queue, emptySources{}, nil, nil, nil, fakeSealer{}, time.Now, rand.Reader)

	claim := claimOne(t, d, org, zone, queue)
	jobID := uuid.MustParse(claim.Job.GetId())

	// Bind the stream: the token from the claim, and only that token.
	stream, err := queue.BindStream(ctx, jobID, claim.Token)
	if err != nil {
		t.Fatalf("BindStream: %v", err)
	}
	if _, err := queue.BindStream(ctx, jobID, "wrong"); !errors.Is(err, jobs.ErrUnknownStream) {
		t.Fatalf("wrong token: %v", err)
	}
	if err := stream.Append(ctx, jobs.Event{Seq: 1, State: "ok", At: time.Now()}); err != nil {
		t.Fatalf("stream append: %v", err)
	}

	// No cancel yet.
	cancelled, err := queue.Cancelled(ctx, jobID)
	if err != nil || cancelled {
		t.Fatalf("Cancelled = %v, %v", cancelled, err)
	}
	select {
	case <-stream.Cancelled():
		t.Fatal("cancel channel must stay open")
	default:
	}

	// Cancel through the service (org-scoped), then the poll sees it.
	if err := svc.Cancel(db.WithOrgScope(ctx, org), org, jobID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	cancelled, err = queue.Cancelled(ctx, jobID)
	if err != nil || !cancelled {
		t.Fatalf("Cancelled after cancel = %v, %v", cancelled, err)
	}
	select {
	case <-stream.Cancelled():
	default:
		t.Fatal("cancel channel must be closed once the job is cancelling")
	}

	// Finish: running/cancelling → terminal, idempotent after that.
	if err := queue.Finish(ctx, jobID, jobs.StatusCancelled, map[string]any{"ok": 1}, ""); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if err := queue.Finish(ctx, jobID, jobs.StatusCancelled, nil, ""); err != nil {
		t.Fatalf("idempotent Finish: %v", err)
	}
	got, err := svc.Get(db.WithOrgScope(ctx, org), org, jobID)
	if err != nil || got.Status != jobs.StatusCancelled || got.FinishedAt == nil {
		t.Fatalf("finished job = %+v, %v", got, err)
	}
	if got.Stats["ok"] != float64(1) {
		t.Fatalf("stats = %v", got.Stats)
	}
	if got.Error != "" {
		t.Fatalf("error = %q", got.Error)
	}

	// After the finish the binding is gone: no more events, no more polls.
	if err := stream.Append(ctx, jobs.Event{Seq: 2, At: time.Now()}); !errors.Is(err, jobs.ErrUnknownStream) {
		t.Fatalf("append after finish: %v", err)
	}
	if _, err := queue.BindStream(ctx, jobID, claim.Token); !errors.Is(err, jobs.ErrUnknownStream) {
		t.Fatalf("bind after finish: %v", err)
	}

	// Finish refusals: unknown job, non-terminal status, not-running job.
	if err := queue.Finish(ctx, uuid.Must(uuid.NewV7()), jobs.StatusFailed, nil, ""); !errors.Is(err, jobs.ErrNotFound) {
		t.Fatalf("unknown job: %v", err)
	}
	if err := queue.Finish(ctx, jobID, jobs.StatusRunning, nil, ""); !errors.Is(err, jobs.ErrInvalidInput) {
		t.Fatalf("non-terminal status: %v", err)
	}
	queued := newJob(org, zone, jobs.KindAdhoc, jobs.StatusQueued)
	if err := jobs.NewSQLStore(d).CreateJob(db.WithOrgScope(ctx, org), queued, nil); err != nil {
		t.Fatal(err)
	}
	if err := queue.Finish(ctx, queued.ID, jobs.StatusSucceeded, nil, ""); !errors.Is(err, jobs.ErrNotRunning) {
		t.Fatalf("finish queued: %v", err)
	}

	// A late error report lands on the job row.
	failed := newJob(org, zone, jobs.KindAdhoc, jobs.StatusRunning)
	if err := jobs.NewSQLStore(d).CreateJob(db.WithOrgScope(ctx, org), failed, nil); err != nil {
		t.Fatal(err)
	}
	if err := queue.Finish(ctx, failed.ID, jobs.StatusError, nil, "worker exploded"); err != nil {
		t.Fatalf("Finish error: %v", err)
	}
	got, err = svc.Get(db.WithOrgScope(ctx, org), org, failed.ID)
	if err != nil || got.Status != jobs.StatusError || got.Error != "worker exploded" {
		t.Fatalf("errored job = %+v, %v", got, err)
	}
}
