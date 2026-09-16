package jobs_test

import (
	"context"
	"crypto/rand"
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
	zone := uuid.Must(uuid.NewV7())
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
	zone := uuid.Must(uuid.NewV7())
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

func TestReaperFailsUnconfirmedCancels(t *testing.T) {
	d := testdb.Open(t)
	q := gen.New(d.Pool())
	org := seedOrg(t, q, "jobs-reaper-cancel-org")
	zone := uuid.Must(uuid.NewV7())
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
	zone := uuid.Must(uuid.NewV7())
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
