// Backlog 7.36: a worker claim is bound to the worker's current registry
// state, not only to the interceptor's look at it when the call came in.
package jobs

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

// activeWorker inserts an active worker row in zone and returns its ref.
func (w *atomicWorld) activeWorker(t *testing.T, zone uuid.UUID) WorkerRef {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	if _, err := w.d.Pool().Exec(context.Background(),
		`INSERT INTO workers (id, org_id, zone_id, name, cert_serial) VALUES ($1, $2, $3, $4, $5)`,
		id, w.org, zone, "w-"+id.String()[24:], "serial-"+id.String()); err != nil {
		t.Fatal(err)
	}
	return WorkerRef{ID: id, OrgID: w.org, ZoneID: zone}
}

func TestClaimForARevokedWorkerClaimsNothing(t *testing.T) {
	w := newAtomicWorld(t, "jobs-claim-revoked")
	zone := testZone(t, w.d, w.org)
	worker := w.activeWorker(t, zone)
	j := w.queueRotateJob(t, zone)
	if _, err := w.d.Pool().Exec(context.Background(), `UPDATE workers SET status = 'revoked', revoked_at = now() WHERE id = $1`, worker.ID); err != nil {
		t.Fatal(err)
	}

	c, err := w.q.ClaimFor(context.Background(), worker.ID, worker.OrgID, []uuid.UUID{zone}, "v1", true)
	if err != nil || c != nil {
		t.Fatalf("ClaimFor by a revoked worker = %+v, %v; want nothing", c, err)
	}
	if got := w.statusOf(t, j.ID); got != StatusQueued {
		t.Fatalf("the job is %s after a revoked worker's claim, want still queued", got)
	}
}

// TestClaimWaitsForARevocationInFlight: the race itself. A revocation
// whose UPDATE has run but not committed holds the worker's row; a claim
// arriving then waits for it and, once it commits, claims nothing —
// before, it handed the revoked worker the job and its payload.
func TestClaimWaitsForARevocationInFlight(t *testing.T) {
	w := newAtomicWorld(t, "jobs-claim-revoke-race")
	zone := testZone(t, w.d, w.org)
	worker := w.activeWorker(t, zone)
	w.queueRotateJob(t, zone)
	ctx := context.Background()

	tx, err := w.d.Pool().Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `UPDATE workers SET status = 'revoked', revoked_at = now() WHERE id = $1`, worker.ID); err != nil {
		t.Fatal(err)
	}

	type result struct {
		claimed bool
		err     error
	}
	done := make(chan result, 1)
	go func() {
		c, err := w.q.ClaimFor(ctx, worker.ID, worker.OrgID, []uuid.UUID{zone}, "v1", true)
		done <- result{c != nil, err}
	}()
	select {
	case r := <-done:
		t.Fatalf("the claim finished (%+v) while the revocation was still in flight; it must wait for it", r)
	case <-time.After(300 * time.Millisecond):
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-done:
		if r.err != nil || r.claimed {
			t.Fatalf("claim after the revocation committed = claimed %v, %v; want nothing", r.claimed, r.err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the claim never finished")
	}
}

func (w *atomicWorld) statusOf(t *testing.T, id uuid.UUID) Status {
	t.Helper()
	j, err := NewSQLStore(w.d).GetJob(context.Background(), w.org, id)
	if err != nil {
		t.Fatal(err)
	}
	return j.Status
}
