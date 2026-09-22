package jobs_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/jobs"
)

// setClaimAge backdates a job's claim so the reaper's 60 s deadline has
// passed without waiting for it.
func setClaimAge(t *testing.T, d *db.DB, jobID uuid.UUID, age time.Duration) {
	t.Helper()
	if _, err := d.Pool().Exec(ctx,
		`UPDATE jobs SET claimed_at = now() - ($2::bigint * interval '1 microsecond') WHERE id = $1`,
		jobID, age.Microseconds()); err != nil {
		t.Fatal(err)
	}
}

func TestReaperRequeuesStaleClaims(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	org := seedOrg(t, q, "jobs-reaper-org")
	zone := seedZone(t, d, org)
	queue := newTestQueue(d)
	reaper := jobs.NewReaper(queue, 0, 0)
	svc := jobs.NewService(jobs.NewSQLStore(d), queue, emptySources{}, nil, nil, nil, fakeSealer{}, time.Now, rand.Reader)

	// A claimed job whose worker went away: stale claim, no stream.
	claim := claimOne(t, d, org, zone, queue)
	jobID := uuid.MustParse(claim.Job.GetId())
	setClaimAge(t, d, jobID, 61*time.Second)

	requeued, failed, err := reaper.Sweep(ctx)
	if err != nil || requeued != 1 || failed != 0 {
		t.Fatalf("Sweep = %d/%d, %v", requeued, failed, err)
	}
	got, err := svc.Get(db.WithOrgScope(ctx, org), org, jobID)
	if err != nil || got.Status != jobs.StatusQueued || got.ClaimedAt != nil || got.StartedAt != nil {
		t.Fatalf("requeued job = %+v, %v", got, err)
	}

	// A second sweep has nothing left to do.
	if requeued, failed, err := reaper.Sweep(ctx); err != nil || requeued != 0 || failed != 0 {
		t.Fatalf("second Sweep = %d/%d, %v", requeued, failed, err)
	}

	// The requeued job is claimable again.
	if claim := claimOne(t, d, org, zone, queue); claim == nil {
		t.Fatal("requeued job must be claimable")
	}
}

func TestReaperSparesLiveStreamsAndRequeuesDeadOnes(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	org := seedOrg(t, q, "jobs-reaper-live-org")
	zone := seedZone(t, d, org)
	queue := newTestQueue(d)
	reaper := jobs.NewReaper(queue, 0, 0)

	claim := claimOne(t, d, org, zone, queue)
	jobID := uuid.MustParse(claim.Job.GetId())
	setClaimAge(t, d, jobID, 61*time.Second)

	// A bound, recently-touched stream keeps the claim alive.
	stream, err := queue.BindStream(ctx, jobID, claim.Token)
	if err != nil {
		t.Fatal(err)
	}
	if requeued, failed, err := reaper.Sweep(ctx); err != nil || requeued != 0 || failed != 0 {
		t.Fatalf("Sweep with live stream = %d/%d, %v", requeued, failed, err)
	}

	// A stream that closed (worker gone) loses the claim.
	stream.Close()
	requeued, _, err := reaper.Sweep(ctx)
	if err != nil || requeued != 1 {
		t.Fatalf("Sweep after close = %d, %v", requeued, err)
	}
}

// TestReaperSparesJobWhileOldAttemptStreamIsOpen (backlog 2.49): a job
// that was requeued and re-claimed while its first attempt's stream was
// still open must not be requeued (or failed as exhausted) while ANY of
// its bindings is alive — the old stream's touch marks live. Under the
// per-attempt keying both bindings coexist; before the fix the re-claim
// evicted the old binding and the job lost its liveness.
func TestReaperSparesJobWhileOldAttemptStreamIsOpen(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	org := seedOrg(t, q, "jobs-reaper-oldattempt")
	zone := seedZone(t, d, org)
	queue := newTestQueue(d)
	reaper := jobs.NewReaper(queue, 0, 0)
	svc := jobs.NewService(jobs.NewSQLStore(d), queue, emptySources{}, nil, nil, nil, fakeSealer{}, time.Now, rand.Reader)

	// Attempt 1: claimed, stream bound and alive.
	claim := claimOne(t, d, org, zone, queue)
	jobID := uuid.MustParse(claim.Job.GetId())
	oldStream, err := queue.BindStream(ctx, jobID, claim.Token)
	if err != nil {
		t.Fatal(err)
	}

	// A revoke-style requeue drops the bindings and queues the job again
	// (same SQL the reaper and the revoke path run), then attempt 2 is
	// claimed — while the old stream is still open.
	if _, err := d.Pool().Exec(ctx,
		`UPDATE jobs SET status = 'queued', worker_id = NULL, claimed_at = NULL, started_at = NULL WHERE id = $1`, jobID); err != nil {
		t.Fatal(err)
	}
	again, err := queue.Claim(ctx, org, []uuid.UUID{zone}, "v1")
	if err != nil || again == nil {
		t.Fatalf("second claim = %v, %v", again, err)
	}
	if again.Job.GetId() != jobID.String() {
		t.Fatalf("second claim = job %s, want %s", again.Job.GetId(), jobID)
	}

	// The old attempt's stream keeps appending (it was never closed), so
	// the job stays alive for the reaper no matter how stale its claim is.
	if err := oldStream.Append(ctx, jobs.Event{Seq: 1, State: "ok", At: time.Now()}); err != nil {
		t.Fatalf("old stream append: %v", err)
	}
	setClaimAge(t, d, jobID, 61*time.Second)
	if requeued, failed, err := reaper.Sweep(ctx); err != nil || requeued != 0 || failed != 0 {
		t.Fatalf("Sweep with old-attempt stream alive = %d/%d, %v", requeued, failed, err)
	}
	got, err := svc.Get(db.WithOrgScope(ctx, org), org, jobID)
	if err != nil || got.Status != jobs.StatusRunning {
		t.Fatalf("job = %+v, %v; the old stream must keep it running", got, err)
	}

	// Once the old stream closes and the new claim goes stale, the reaper
	// acts: attempts remain, so the job is requeued.
	oldStream.Close()
	requeued, _, err := reaper.Sweep(ctx)
	if err != nil || requeued != 1 {
		t.Fatalf("Sweep after old stream closed = %d, %v", requeued, err)
	}
}

func TestReaperFailsUnconfirmedCancels(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	org := seedOrg(t, q, "jobs-reaper-cancel-org")
	zone := seedZone(t, d, org)
	queue := newTestQueue(d)
	reaper := jobs.NewReaper(queue, 0, 0)
	svc := jobs.NewService(jobs.NewSQLStore(d), queue, emptySources{}, nil, nil, nil, fakeSealer{}, time.Now, rand.Reader)

	// A cancel whose worker confirmed within the deadline survives.
	claim := claimOne(t, d, org, zone, queue)
	jobID := uuid.MustParse(claim.Job.GetId())
	setClaimAge(t, d, jobID, 61*time.Second)
	if err := svc.Cancel(db.WithOrgScope(ctx, org), org, jobID); err != nil {
		t.Fatal(err)
	}
	if _, failed, err := reaper.Sweep(ctx); err != nil || failed != 0 {
		t.Fatalf("fresh cancel must survive the sweep: failed=%d, %v", failed, err)
	}

	// A cancel requested more than a minute ago with no confirmation from
	// the worker fails the job.
	queue.NoteCancelRequested(jobID, time.Now().Add(-61*time.Second))
	requeued, failed, err := reaper.Sweep(ctx)
	if err != nil || requeued != 0 || failed != 1 {
		t.Fatalf("Sweep = %d/%d, %v", requeued, failed, err)
	}
	got, err := svc.Get(db.WithOrgScope(ctx, org), org, jobID)
	if err != nil || got.Status != jobs.StatusFailed || got.FinishedAt == nil {
		t.Fatalf("failed cancel = %+v, %v", got, err)
	}
	if got.Error == "" {
		t.Fatal("the failure must say why")
	}
	// The fallback for a cancel request the restart wiped (no recorded
	// timestamp, deadline anchored on the claim time) is covered by
	// TestReaperFallbackAnchorsOnClaimedAt in the internal test file.
}

func TestReaperRunStopsWhenContextIsDone(t *testing.T) {
	d := testdb.Open(t)
	queue := newTestQueue(d)
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		jobs.NewReaper(queue, time.Millisecond, 0).Run(ctx)
		close(done)
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run must return when the context is done")
	}
}

// TestReaperFailsJobsThatExhaustTheirAttempts: a job whose worker keeps
// vanishing after the claim — a claim response the worker cannot receive,
// a payload that crashes it — is requeued only until it has been claimed
// the maximum number of times (default 3); the next stale sweep fails it
// with a clear error instead of requeuing it forever.
func TestReaperFailsJobsThatExhaustTheirAttempts(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	org := seedOrg(t, q, "jobs-reaper-attempts")
	zone := seedZone(t, d, org)
	queue := newTestQueue(d)
	reaper := jobs.NewReaper(queue, 0, 0)
	svc := jobs.NewService(jobs.NewSQLStore(d), queue, emptySources{}, nil, nil, nil, fakeSealer{}, time.Now, rand.Reader)

	claim := claimOne(t, d, org, zone, queue)
	jobID := uuid.MustParse(claim.Job.GetId())
	for attempt := 1; attempt <= 3; attempt++ {
		if attempt > 1 {
			again, err := queue.Claim(ctx, org, []uuid.UUID{zone}, "v1")
			if err != nil || again == nil || again.Job.GetId() != jobID.String() {
				t.Fatalf("attempt %d: claim = %v, %v", attempt, again, err)
			}
		}
		setClaimAge(t, d, jobID, 61*time.Second)
		requeued, failed, err := reaper.Sweep(ctx)
		if err != nil {
			t.Fatalf("attempt %d: Sweep: %v", attempt, err)
		}
		got, err := svc.Get(db.WithOrgScope(ctx, org), org, jobID)
		if err != nil {
			t.Fatal(err)
		}
		if attempt < 3 {
			if requeued != 1 || failed != 0 || got.Status != jobs.StatusQueued {
				t.Fatalf("attempt %d: Sweep = %d/%d, status %s; want requeued", attempt, requeued, failed, got.Status)
			}
			continue
		}
		if requeued != 0 || failed != 1 || got.Status != jobs.StatusError {
			t.Fatalf("attempt %d: Sweep = %d/%d, status %s; want the exhausted job failed", attempt, requeued, failed, got.Status)
		}
		if got.Attempts != 3 {
			t.Fatalf("attempts = %d, want the three claims counted", got.Attempts)
		}
		if got.Error == "" || got.FinishedAt == nil {
			t.Fatalf("the exhausted job must say why and carry finished_at: %+v", got)
		}
	}
	// Nothing is left to claim.
	if again, err := queue.Claim(ctx, org, []uuid.UUID{zone}, "v1"); err != nil || again != nil {
		t.Fatalf("claim after exhaustion = %v, %v", again, err)
	}
}

// racingSink is the SyncSink fake the exhaustion race (backlog 2.53) rides
// on: its first call runs inside the FIRST exhausted job's terminal
// transaction — exactly the window between the reaper's scan, which listed
// both jobs as stale and exhausted, and the second job's transition write.
// In that window it requeues and re-claims the OTHER job on the raw pool:
// claimed_at refreshed, attempts incremented, a fresh running claim — what
// a worker that was only slow to report would have done concurrently.
type racingSink struct {
	t      *testing.T
	d      *db.DB
	byRepo map[uuid.UUID]uuid.UUID // repo_id -> job id
	fired  atomic.Bool
}

func (s *racingSink) RecordSyncResult(_ context.Context, _ uuid.UUID, repoID uuid.UUID, _ time.Time, _, _, _ string) error {
	if !s.fired.CompareAndSwap(false, true) {
		return nil
	}
	s.t.Helper()
	trigger, ok := s.byRepo[repoID]
	if !ok {
		s.t.Fatalf("sink called with unknown repo %s", repoID)
	}
	var target uuid.UUID
	for _, id := range s.byRepo {
		if id != trigger {
			target = id
		}
	}
	for _, stmt := range []string{
		`UPDATE jobs SET status = 'queued', worker_id = NULL, claimed_at = NULL, started_at = NULL WHERE id = $1 AND status IN ('claimed', 'running')`,
		`UPDATE jobs SET status = 'running', worker_id = NULL, claimed_at = now(), started_at = now(), attempts = attempts + 1 WHERE id = $1 AND status = 'queued'`,
	} {
		tag, err := s.d.Pool().Exec(context.Background(), stmt, target)
		if err != nil {
			s.t.Errorf("concurrent re-claim: %v", err)
		}
		if tag.RowsAffected() != 1 {
			s.t.Errorf("concurrent re-claim step matched %d rows", tag.RowsAffected())
		}
	}
	return nil
}

// TestReaperExhaustionReChecksStalenessAtTheTransition (backlog 2.53): the
// exhaustion transition re-checks staleness INSIDE its UPDATE. Two jobs are
// stale and exhausted when the reaper scans; while the first is being
// failed, the second is re-claimed (its claimed_at refreshed by the racing
// sink). The second job's transition write must then find a fresh claim and
// affect 0 rows: the job survives running with its re-claim intact — the
// old read-then-write shape would have failed it for a staleness it no
// longer had.
func TestReaperExhaustionReChecksStalenessAtTheTransition(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	org := seedOrg(t, q, "jobs-reaper-stale-org")
	zone := seedZone(t, d, org)
	queue := newTestQueue(d)
	reaper := jobs.NewReaper(queue, 0, 0)
	svc := jobs.NewService(jobs.NewSQLStore(d), queue, emptySources{}, nil, nil, nil, fakeSealer{}, time.Now, rand.Reader)

	sink := &racingSink{t: t, d: d, byRepo: map[uuid.UUID]uuid.UUID{}}
	queue.SetSyncSink(sink)

	// Two running sync_repo jobs, both stale and exhausted: three claims
	// counted, no stream ever bound (a worker that claimed and went silent
	// without an event).
	store := jobs.NewSQLStore(d)
	scoped := db.WithOrgScope(ctx, org)
	repoToJob := map[uuid.UUID]uuid.UUID{}
	for range 2 {
		repo := uuid.Must(uuid.NewV7())
		j := newJob(org, zone, jobs.KindSyncRepo, jobs.StatusQueued)
		j.Payload = json.RawMessage(`{"repo_id":"` + repo.String() + `"}`)
		if err := store.CreateJob(scoped, j, j.Payload); err != nil {
			t.Fatal(err)
		}
		if _, err := d.Exec(ctx,
			`UPDATE jobs SET status = 'running', claimed_at = now() - interval '61 seconds', started_at = now(), attempts = 3 WHERE id = $1`,
			j.ID); err != nil {
			t.Fatal(err)
		}
		repoToJob[repo] = j.ID
	}
	sink.byRepo = repoToJob

	requeued, failed, err := reaper.Sweep(ctx)
	if err != nil || requeued != 0 || failed != 1 {
		t.Fatalf("Sweep = %d/%d, %v; want only the still-stale job failed and the re-claimed one skipped", requeued, failed, err)
	}

	var errored, survived int
	for repo, id := range repoToJob {
		got, err := svc.Get(scoped, org, id)
		if err != nil {
			t.Fatal(err)
		}
		switch got.Status {
		case jobs.StatusError:
			errored++
			if got.Error == "" || got.FinishedAt == nil {
				t.Fatalf("the failed job must say why and carry finished_at: %+v", got)
			}
			if got.Attempts != 3 {
				t.Fatalf("attempts = %d, want the three stale claims", got.Attempts)
			}
			if got.Payload != nil {
				t.Fatal("the failed job's payload must be wiped")
			}
		case jobs.StatusRunning:
			survived++
			if got.Error != "" || got.FinishedAt != nil {
				t.Fatalf("the re-claimed job must survive untouched: %+v", got)
			}
			if got.ClaimedAt == nil {
				t.Fatal("the re-claimed job must carry its fresh claim")
			}
			if got.Attempts != 4 {
				t.Fatalf("attempts = %d, want the re-claim counted", got.Attempts)
			}
			if got.Payload == nil {
				t.Fatal("the re-claimed job's payload must be intact")
			}
		default:
			t.Fatalf("job %s (repo %s) = %s; want only error or running", id, repo, got.Status)
		}
	}
	if errored != 1 || survived != 1 {
		t.Fatalf("outcomes = %d failed / %d survived, want one of each", errored, survived)
	}

	// The fresh claim is not stale, so the next sweep has nothing to do —
	// the job is not immediately re-failed either.
	if requeued, failed, err := reaper.Sweep(ctx); err != nil || requeued != 0 || failed != 0 {
		t.Fatalf("second Sweep = %d/%d, %v; the re-claimed job must be left alone", requeued, failed, err)
	}
}
