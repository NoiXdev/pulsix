// Backlog 2.113: the claim records whether a cancel stops the play on the
// claiming worker (migration 00072), and a requeue forgets it with the
// worker that said it.
package jobs

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

// claimForWith is claimRotateJobFor with the worker's cancel_stops_play
// answer chosen by the test.
func (w *atomicWorld) claimForWith(t *testing.T, worker WorkerRef, cancelStopsPlay bool) Job {
	t.Helper()
	j := w.queueRotateJob(t, worker.ZoneID)
	if c, err := w.q.ClaimFor(context.Background(), worker.ID, worker.OrgID, []uuid.UUID{worker.ZoneID}, "v1", cancelStopsPlay); err != nil || c == nil || c.Job.GetId() != j.ID.String() {
		t.Fatalf("ClaimFor = %+v, %v", c, err)
	}
	return j
}

func (w *atomicWorld) cancelStopsPlay(t *testing.T, id uuid.UUID) bool {
	t.Helper()
	j, err := NewSQLStore(w.d).GetJob(context.Background(), w.org, id)
	if err != nil {
		t.Fatal(err)
	}
	return j.CancelStopsPlay
}

func TestClaimRecordsWhetherCancelStopsThePlay(t *testing.T) {
	w := newAtomicWorld(t, "jobs-cancel-stops-play")
	zone := testZone(t, w.d, w.org)

	for _, want := range []bool{true, false} {
		worker := w.activeWorker(t, zone)
		j := w.claimForWith(t, worker, want)
		if got := w.cancelStopsPlay(t, j.ID); got != want {
			t.Errorf("claimed by a worker that said %v: CancelStopsPlay = %v", want, got)
		}
	}

	// The manager's own in-process claim has no worker to say it.
	j := w.queueRotateJob(t, zone)
	if c, err := w.q.Claim(context.Background(), w.org, []uuid.UUID{zone}, "v1"); err != nil || c == nil || c.Job.GetId() != j.ID.String() {
		t.Fatalf("Claim = %+v, %v", c, err)
	}
	if w.cancelStopsPlay(t, j.ID) {
		t.Error("an in-process claim: CancelStopsPlay = true, want false (nobody said)")
	}
}

func TestRequeueForgetsWhetherCancelStopsThePlay(t *testing.T) {
	w := newAtomicWorld(t, "jobs-cancel-stops-play-requeue")
	zone := testZone(t, w.d, w.org)
	worker := w.activeWorker(t, zone)
	j := w.claimForWith(t, worker, true)

	if requeued, _, err := w.q.RequeueWorkerJobs(context.Background(), w.org, worker.ID, "worker revoked", 3); err != nil || requeued != 1 {
		t.Fatalf("RequeueWorkerJobs = %d, %v; want 1 requeued", requeued, err)
	}
	// The revoked worker's play may still be running (backlog 2.57); the
	// job no longer says a cancel would stop it.
	if w.cancelStopsPlay(t, j.ID) {
		t.Error("after a requeue: CancelStopsPlay = true, want false")
	}
}
