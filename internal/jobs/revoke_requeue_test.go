package jobs_test

import (
	"context"
	"crypto/rand"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/crypto"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/jobs"
)

// TestRequeueWorkerJobs: a revoked worker's running job goes back to the
// queue, an exhausted one ends as error, a cancelling one as cancelled,
// and another worker's job is untouched. The ended jobs go through the
// shared terminal transition, so their sealed payload is wiped; the
// requeued one keeps it for the next claim.
func TestRequeueWorkerJobs(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, gen.New(d.Pool()), "jobs-revoke")
	zone := seedZone(t, d, org)
	store := jobs.NewSQLStore(d)
	q := jobs.NewSQLQueue(d, crypto.NewBox(testBoxKey("revoke")), emptySources{}, emptySources{}, emptySources{}, emptySources{}, time.Now, rand.Reader)
	scoped := db.WithOrgScope(ctx, org)
	revoked, other := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	mk := func(worker uuid.UUID, status string, attempts int) uuid.UUID {
		t.Helper()
		j := newJob(org, zone, jobs.KindAdhoc, jobs.StatusQueued)
		if err := store.CreateJob(scoped, j, nil); err != nil {
			t.Fatal(err)
		}
		if _, err := d.Pool().Exec(ctx, `UPDATE jobs SET status = $2, worker_id = $3, attempts = $4, claimed_at = now(), payload = '{"sealed":"x"}' WHERE id = $1`,
			j.ID, status, worker, attempts); err != nil {
			t.Fatal(err)
		}
		return j.ID
	}
	running := mk(revoked, "running", 1)
	exhausted := mk(revoked, "running", 3)
	cancelling := mk(revoked, "cancelling", 1)
	untouched := mk(other, "running", 1)

	requeued, ended, err := q.RequeueWorkerJobs(ctx, org, revoked, "worker revoked", 3)
	if err != nil || requeued != 1 || ended != 2 {
		t.Fatalf("RequeueWorkerJobs = %d, %d, %v; want 1 requeued, 2 ended", requeued, ended, err)
	}
	for id, want := range map[uuid.UUID][2]string{
		running:    {"queued", "requeued: worker revoked"},
		exhausted:  {"error", "worker revoked"},
		cancelling: {"cancelled", "worker revoked"},
		untouched:  {"running", ""},
	} {
		var status, errText string
		var worker *uuid.UUID
		var hasPayload bool
		if err := d.Pool().QueryRow(ctx, `SELECT status, error, worker_id, payload IS NOT NULL FROM jobs WHERE id = $1`, id).Scan(&status, &errText, &worker, &hasPayload); err != nil {
			t.Fatal(err)
		}
		if status != want[0] || errText != want[1] {
			t.Errorf("job %s = %s %q, want %s %q", id, status, errText, want[0], want[1])
		}
		if id == running && worker != nil {
			t.Error("a requeued job must drop its worker")
		}
		if terminal := id == exhausted || id == cancelling; hasPayload == terminal {
			t.Errorf("job %s (%s): payload kept = %v, want the payload wiped exactly when the job ended", id, status, hasPayload)
		}
	}

	// A second pass finds nothing left to hand back.
	if requeued, ended, err := q.RequeueWorkerJobs(ctx, org, revoked, "worker revoked", 3); err != nil || requeued != 0 || ended != 0 {
		t.Fatalf("second pass = %d, %d, %v; want nothing", requeued, ended, err)
	}
	// A database failure is reported, not swallowed.
	dead, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, err := q.RequeueWorkerJobs(dead, org, revoked, "worker revoked", 3); err == nil {
		t.Fatal("a failed requeue must return its error")
	}
}
