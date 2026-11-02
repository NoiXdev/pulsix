// Backlog 7.36: a worker claim is bound to the worker's current registry
// state, not only to the interceptor's look at it when the call came in.
package jobs

import (
	"context"
	"crypto/rand"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/inventory/render"
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

// revokingRenderer revokes a worker while the claim's payload is being
// built — after the claim's transaction committed — and renders like the
// renderer it wraps.
type revokingRenderer struct {
	fakeRenderer
	w      *atomicWorld
	worker uuid.UUID
}

func (r revokingRenderer) Snapshot(ctx context.Context, org uuid.UUID, scope render.Scope) (render.Inventory, error) {
	if _, err := r.w.d.Pool().Exec(ctx, `UPDATE workers SET status = 'revoked', revoked_at = now() WHERE id = $1`, r.worker); err != nil {
		return render.Inventory{}, err
	}
	return r.fakeRenderer.Snapshot(ctx, org, scope)
}

// TestClaimDoesNotHandARevokedWorkerItsPayload: a revocation that commits
// while the payload is being built, after the claim's own commit, still
// keeps the payload — credentials and all — from the revoked worker
// (backlog 7.36's re-check).
func TestClaimDoesNotHandARevokedWorkerItsPayload(t *testing.T) {
	w := newAtomicWorld(t, "jobs-claim-revoke-mid-build")
	zone := testZone(t, w.d, w.org)
	worker := w.activeWorker(t, zone)
	w.queueRotateJob(t, zone)
	inner := fakeRenderer{inv: render.Inventory{
		Doc:         map[string]any{"all": map[string]any{}},
		Connections: map[string]render.Connection{"web1": {HostID: w.host}},
	}}
	q := NewSQLQueue(w.d, fakeBox{}, revokingRenderer{fakeRenderer: inner, w: w, worker: worker.ID}, fullProbeCreds{}, fakeTemplates{}, fakeBastions{}, time.Now, rand.Reader)
	q.SetKeyPins(fakePins{})

	c, err := q.ClaimFor(context.Background(), worker.ID, worker.OrgID, []uuid.UUID{zone}, "v1", true)
	if err != nil || c != nil {
		t.Fatalf("ClaimFor across a revocation mid-build = %+v, %v; want nothing handed out", c, err)
	}
}

func TestClaimWithNoZonesClaimsNothing(t *testing.T) {
	w := newAtomicWorld(t, "jobs-claim-no-zones")
	w.queueRotateJob(t, testZone(t, w.d, w.org))
	if c, err := w.q.Claim(context.Background(), w.org, nil, "v1"); err != nil || c != nil {
		t.Fatalf("Claim with no zones = %+v, %v; want nothing", c, err)
	}
}

// TestClearTerminalPayloadOfAnUnknownJobIsHarmless: the cancel it serves
// already stands, so a job that is not there is logged, not a failure.
func TestClearTerminalPayloadOfAnUnknownJobIsHarmless(t *testing.T) {
	w := newAtomicWorld(t, "jobs-clear-unknown")
	j := w.queueRotateJob(t, testZone(t, w.d, w.org))
	w.q.ClearTerminalPayload(context.Background(), uuid.Must(uuid.NewV7()))
	if got := w.statusOf(t, j.ID); got != StatusQueued {
		t.Fatalf("an unrelated job is %s after clearing an unknown one", got)
	}
}

// TestRotationReadsReportAFailedQuery: the three reads a rotation's enqueue
// plans its targets from fail with an error when their query does — never
// with an empty list, which would read as "nothing to rotate".
func TestRotationReadsReportAFailedQuery(t *testing.T) {
	w := newAtomicWorld(t, "jobs-rotation-reads-fail")
	gone, cancel := context.WithCancel(context.Background())
	cancel()
	cred := uuid.Must(uuid.NewV7())
	if hs, err := w.q.ListCredentialHosts(gone, w.org, cred); err == nil {
		t.Errorf("ListCredentialHosts on a dead connection = %v, nil", hs)
	}
	if bs, err := w.q.ListCredentialBastions(gone, w.org, cred); err == nil {
		t.Errorf("ListCredentialBastions on a dead connection = %v, nil", bs)
	}
	if bs, err := w.q.ListOrgBastions(gone, w.org); err == nil {
		t.Errorf("ListOrgBastions on a dead connection = %v, nil", bs)
	}
}

// TestEnqueueRevocationsQueuesOncePerGroup: a group that already holds its
// revocations — a second finalize observing the completed group — queues
// none, and a failed count fails the finalize instead of queueing blind.
func TestEnqueueRevocationsQueuesOncePerGroup(t *testing.T) {
	w := newAtomicWorld(t, "jobs-revocations-once")
	j := w.queueRotateJob(t, testZone(t, w.d, w.org))
	if _, err := w.d.Pool().Exec(context.Background(), `UPDATE jobs SET kind = 'revoke_key' WHERE id = $1`, j.ID); err != nil {
		t.Fatal(err)
	}
	rp := rotationJobPayload{CredentialID: uuid.Must(uuid.NewV7()), OldPublicKey: "ssh-ed25519 AAAA old"}

	if err := w.q.enqueueRevocations(context.Background(), w.org, j.RunGroupID, rp); err != nil {
		t.Fatalf("enqueueRevocations on a group with revocations = %v", err)
	}
	var n int
	if err := w.d.Pool().QueryRow(context.Background(), `SELECT count(*) FROM jobs WHERE run_group_id = $1`, j.RunGroupID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("the group holds %d jobs (%v), want only the existing revocation", n, err)
	}

	gone, cancel := context.WithCancel(context.Background())
	cancel()
	if err := w.q.enqueueRevocations(gone, w.org, j.RunGroupID, rp); err == nil {
		t.Fatal("enqueueRevocations with a failed count succeeded")
	}
}
