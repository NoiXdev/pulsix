package jobs_test

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
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
	claim, err := q.Claim(ctx, org, []uuid.UUID{zone}, "v1")
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
	zone := seedZone(t, d, org)
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
	_ = tail

	// Another organization must neither see the job nor its events.
	if _, err := svc.Events(db.WithOrgScope(ctx, other), other, jobID, 0); !errors.Is(err, jobs.ErrNotFound) {
		t.Fatalf("other org's Events: %v", err)
	}

	// Events for a job with no live claim binding are rejected.
	if err := queue.AppendEvent(ctx, uuid.Must(uuid.NewV7()), jobs.Event{Seq: 1, At: time.Now()}); !errors.Is(err, jobs.ErrUnknownStream) {
		t.Fatalf("unknown stream: %v", err)
	}

	// A binding Finish already removed is not an error: the terminal
	// result report and the stream's last events are independent server
	// paths, so the run_end bookend can arrive after the binding is gone —
	// and it is part of the run's record. The org comes from the job row.
	if err := jobs.NewSQLQueue(d, fakeSealer{}, emptySources{}, emptySources{}, emptySources{}, emptySources{}, time.Now, rand.Reader).
		Finish(ctx, jobID, jobs.StatusSucceeded, map[string]any{}, ""); err != nil {
		t.Fatalf("finish job for the late-event case: %v", err)
	}
	late := jobs.Event{Seq: 9, State: "run_end", Payload: []byte(`{"status":"succeeded"}`), At: time.Now()}
	if err := queue.AppendEvent(ctx, jobID, late); err != nil {
		t.Fatalf("event after finish: %v", err)
	}
	tail, err = svc.Events(db.WithOrgScope(ctx, org), org, jobID, 3)
	// The worker's seq 9 is not what is stored: the manager assigns the
	// next seq of the job's log.
	if err != nil || len(tail) != 1 || tail[0].Seq != 4 || tail[0].State != "run_end" {
		t.Fatalf("Events(after finish) = %+v, %v", tail, err)
	}
}

func TestCancelFlowAndFinishIdempotency(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	org := seedOrg(t, q, "jobs-cancel-org")
	zone := seedZone(t, d, org)
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

	// After the finish the binding is gone: no more stream binds, and a
	// bound stream's late events still land (the result report and the
	// stream's last events are independent server paths; the run_end
	// bookend is part of the run's record — see
	// TestAppendPersistsMonotonicSeqsAndEventsPaginate). Only a job that
	// does not exist at all is rejected.
	if err := stream.Append(ctx, jobs.Event{Seq: 2, At: time.Now()}); err != nil {
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

// TestAppendKeepsSeqMonotonicAcrossAttempts (final review I5): a worker
// numbers its events per run from 1, so a job the reaper requeued streams
// seq 1.. again on its second attempt. The manager assigns the stored seq
// itself — strictly increasing per job across attempts — and stamps every
// event with the attempt that produced it, so the second attempt's log is
// neither hidden behind the first's seqs nor merged into it.
func TestAppendKeepsSeqMonotonicAcrossAttempts(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	org := seedOrg(t, q, "jobs-events-attempts")
	zone := seedZone(t, d, org)
	queue := newTestQueue(d)
	svc := jobs.NewService(jobs.NewSQLStore(d), queue, emptySources{}, nil, nil, nil, fakeSealer{}, time.Now, rand.Reader)

	claim := claimOne(t, d, org, zone, queue)
	jobID := uuid.MustParse(claim.Job.GetId())
	for _, seq := range []uint64{1, 2} {
		if err := queue.AppendEvent(ctx, jobID, jobs.Event{Seq: seq, State: "ok", Task: "first", At: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	// The worker vanished: the reaper requeues, a worker claims again and
	// streams its own seq 1, 2 for the second attempt.
	// (The reaper's own requeue statement; its stream-liveness bookkeeping
	// is in-process and covered by the reaper tests.)
	if _, err := d.Pool().Exec(ctx,
		`UPDATE jobs SET status = 'queued', worker_id = NULL, claimed_at = NULL, started_at = NULL WHERE id = $1`, jobID); err != nil {
		t.Fatal(err)
	}
	again, err := queue.Claim(ctx, org, []uuid.UUID{zone}, "v1")
	if err != nil || again == nil {
		t.Fatalf("second claim = %v, %v", again, err)
	}
	for _, seq := range []uint64{1, 2} {
		if err := queue.AppendEvent(ctx, jobID, jobs.Event{Seq: seq, State: "ok", Task: "second", At: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}

	events, err := svc.Events(db.WithOrgScope(ctx, org), org, jobID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 4 {
		t.Fatalf("events = %d, want both attempts' four", len(events))
	}
	wantTask := []string{"first", "first", "second", "second"}
	wantAttempt := []int{1, 1, 2, 2}
	for i, e := range events {
		if e.Seq != uint64(i+1) || e.Task != wantTask[i] || e.Attempt != wantAttempt[i] {
			t.Fatalf("event %d = seq %d task %q attempt %d; want seq %d task %q attempt %d",
				i, e.Seq, e.Task, e.Attempt, i+1, wantTask[i], wantAttempt[i])
		}
	}
	// The poll cursor after the first attempt's last seq returns exactly
	// the second attempt.
	tail, err := svc.Events(db.WithOrgScope(ctx, org), org, jobID, 2)
	if err != nil || len(tail) != 2 || tail[0].Task != "second" {
		t.Fatalf("Events(after 2) = %+v, %v", tail, err)
	}
}

// TestOldAttemptStreamStampsItsOwnAttempt (backlog 2.49): a job requeued
// and re-claimed while its first attempt's stream is still open keeps that
// stream's OWN binding — its appends are stamped attempt 1 (org from the
// binding), not the new current attempt. The second attempt's stream, bound
// with its own token, stamps attempt 2. Before the per-attempt keying the
// re-claim evicted the old binding and both streams stamped attempt 2.
func TestOldAttemptStreamStampsItsOwnAttempt(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	org := seedOrg(t, q, "jobs-events-oldattempt")
	zone := seedZone(t, d, org)
	queue := newTestQueue(d)
	svc := jobs.NewService(jobs.NewSQLStore(d), queue, emptySources{}, nil, nil, nil, fakeSealer{}, time.Now, rand.Reader)

	claim := claimOne(t, d, org, zone, queue)
	jobID := uuid.MustParse(claim.Job.GetId())
	oldStream, err := queue.BindStream(ctx, jobID, claim.Token)
	if err != nil {
		t.Fatal(err)
	}
	if err := oldStream.Append(ctx, jobs.Event{Seq: 1, State: "ok", Task: "old-1", At: time.Now()}); err != nil {
		t.Fatal(err)
	}

	// Requeue (the reaper's own statement) and re-claim while the old
	// stream is open.
	if _, err := d.Pool().Exec(ctx,
		`UPDATE jobs SET status = 'queued', worker_id = NULL, claimed_at = NULL, started_at = NULL WHERE id = $1`, jobID); err != nil {
		t.Fatal(err)
	}
	again, err := queue.Claim(ctx, org, []uuid.UUID{zone}, "v1")
	if err != nil || again == nil {
		t.Fatalf("second claim = %v, %v", again, err)
	}
	newStream, err := queue.BindStream(ctx, jobID, again.Token)
	if err != nil {
		t.Fatalf("bind new attempt: %v", err)
	}
	// The old attempt's stream continues under its own binding; the new
	// one starts fresh under the new binding.
	if err := oldStream.Append(ctx, jobs.Event{Seq: 2, State: "ok", Task: "old-2", At: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := newStream.Append(ctx, jobs.Event{Seq: 1, State: "ok", Task: "new-1", At: time.Now()}); err != nil {
		t.Fatal(err)
	}
	// An append with no binding (the gRPC append path's unbound late
	// event) still lands: org and attempt come from the job row.
	if err := queue.AppendEvent(ctx, jobID, jobs.Event{Seq: 3, State: "ok", Task: "unbound", At: time.Now()}); err != nil {
		t.Fatal(err)
	}

	events, err := svc.Events(db.WithOrgScope(ctx, org), org, jobID, 0)
	if err != nil {
		t.Fatal(err)
	}
	wantTask := []string{"old-1", "old-2", "new-1", "unbound"}
	wantAttempt := []int{1, 1, 2, 2}
	if len(events) != len(wantTask) {
		t.Fatalf("events = %d, want %d: %+v", len(events), len(wantTask), events)
	}
	for i, e := range events {
		if e.Task != wantTask[i] || e.Attempt != wantAttempt[i] {
			t.Fatalf("event %d = task %q attempt %d; want task %q attempt %d",
				i, e.Task, e.Attempt, wantTask[i], wantAttempt[i])
		}
	}
}

// TestAppendSeqQueryBoundsMaxSeqToJobChunks is the regression guard for
// backlog 2.50: the max(seq) subquery of the append's INSERT must carry the
// job's created_at bound (`AND at >= <created_at>`), so the hypertable scan
// touches only the job's own chunks instead of every chunk in the table.
// The INSERT is raw SQL in the queue (not sqlc), so the guard asserts the
// statement's text — the same pattern as the generated-constants guard in
// internal/db/gen.
func TestAppendSeqQueryBoundsMaxSeqToJobChunks(t *testing.T) {
	if !strings.Contains(jobs.AppendJobEventSQL, "job_events") || !strings.Contains(jobs.AppendJobEventSQL, "max(seq)") {
		t.Fatalf("AppendJobEventSQL no longer assigns max(seq) itself:\n%s", jobs.AppendJobEventSQL)
	}
	// The bound must compare the event's `at` against the job's
	// created_at — not a second jobs lookup inside the subquery.
	re := regexp.MustCompile(`(?is)SELECT\s+max\(seq\)\s+FROM\s+job_events\s+WHERE\s+job_id\s*=\s*\$\d+\s+AND\s+at\s*>=\s*\$\d+`)
	if !re.MatchString(jobs.AppendJobEventSQL) {
		t.Fatalf("max(seq) subquery lost its created_at bound (backlog 2.50):\n%s", jobs.AppendJobEventSQL)
	}
}
