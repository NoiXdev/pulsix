// Spec 2026-11-25 A (decision D3): a job pinned to a commit is claimed
// only by a worker that announced supports_pinned_commit. An older worker
// would fetch and run the branch tip instead, so its claim must not see
// the row at all: the pinned job waits for an updated worker, and the
// ordinary jobs behind it are still claimed.
package jobs

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db"
)

const testCommit = "0123456789abcdef0123456789abcdef01234567"

func TestPinnedCommitRoundTripsThroughTheStore(t *testing.T) {
	w := newAtomicWorld(t, "jobs-pinned-commit-store")
	zone := testZone(t, w.d, w.org)
	pinned := w.queueRotateJob(t, zone)
	w.markPinned(t, pinned.ID)
	plain := w.queueRotateJob(t, zone)

	store := NewSQLStore(w.d)
	scoped := db.WithOrgScope(context.Background(), w.org)
	got, err := store.GetJob(scoped, w.org, pinned.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.PinnedCommit != testCommit {
		t.Fatalf("PinnedCommit = %q, want %q", got.PinnedCommit, testCommit)
	}
	if got, err := store.GetJob(scoped, w.org, plain.ID); err != nil || got.PinnedCommit != "" {
		t.Fatalf("an unpinned job reads PinnedCommit %q, %v; want none", got.PinnedCommit, err)
	}
}

func TestCreateJobStoresThePinnedCommit(t *testing.T) {
	w := newAtomicWorld(t, "jobs-pinned-commit-create")
	zone := testZone(t, w.d, w.org)
	j := Job{ID: uuid.Must(uuid.NewV7()), Trigger: TriggerManual, OrgID: w.org, ZoneID: zone, Kind: KindRun,
		HostIDs: []uuid.UUID{w.host}, Status: StatusQueued, RunGroupID: uuid.Must(uuid.NewV7()), PinnedCommit: testCommit}
	scoped := db.WithOrgScope(context.Background(), w.org)
	if err := NewSQLStore(w.d).CreateJob(scoped, j, nil); err != nil {
		t.Fatal(err)
	}
	var stored *string
	if err := w.d.Pool().QueryRow(context.Background(), `SELECT pinned_commit FROM jobs WHERE id = $1`, j.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == nil || *stored != testCommit {
		t.Fatalf("pinned_commit = %v, want %q", stored, testCommit)
	}
}

func (w *atomicWorld) markPinned(t *testing.T, id uuid.UUID) {
	t.Helper()
	if _, err := w.d.Pool().Exec(context.Background(), `UPDATE jobs SET pinned_commit = $2 WHERE id = $1`, id, testCommit); err != nil {
		t.Fatal(err)
	}
}

func TestClaimGivesAPinnedJobOnlyToAWorkerThatSupportsIt(t *testing.T) {
	ctx := context.Background()
	w := newAtomicWorld(t, "jobs-pinned-commit-claim")
	zone := testZone(t, w.d, w.org)
	worker := w.activeWorker(t, zone)

	// The pinned job is the oldest queued job; an ordinary one waits
	// behind it.
	pinned := w.queueRotateJob(t, zone)
	w.markPinned(t, pinned.ID)
	normal := w.queueRotateJob(t, zone)

	// An older worker: every other announcement, but not this one.
	old := ClaimCaps{CancelStopsPlay: true, SupportsCheckMode: true}
	c, err := w.q.ClaimFor(ctx, worker.ID, worker.OrgID, []uuid.UUID{zone}, "v1", old)
	if err != nil || c == nil {
		t.Fatalf("ClaimFor without pinned commit support = %+v, %v; want the ordinary job", c, err)
	}
	if got := c.Job.GetId(); got != normal.ID.String() {
		t.Fatalf("an older worker claimed %s, want the ordinary job %s behind the pinned one", got, normal.ID)
	}
	if again, err := w.q.ClaimFor(ctx, worker.ID, worker.OrgID, []uuid.UUID{zone}, "v1", old); err != nil || again != nil {
		t.Fatalf("second ClaimFor without support = %+v, %v; want nothing", again, err)
	}
	// The manager's own in-process claim has no worker that checks out a
	// commit either.
	if inProc, err := w.q.Claim(ctx, w.org, []uuid.UUID{zone}, "v1"); err != nil || inProc != nil {
		t.Fatalf("in-process Claim = %+v, %v; want nothing", inProc, err)
	}
	if got := w.status(t, pinned.ID); got != string(StatusQueued) {
		t.Fatalf("the pinned job is %q, want it still queued", got)
	}

	// A worker that announces support takes it.
	c, err = w.q.ClaimFor(ctx, worker.ID, worker.OrgID, []uuid.UUID{zone}, "v1", ClaimCaps{SupportsPinnedCommit: true})
	if err != nil {
		t.Fatal(err)
	}
	// Only a template run can be pinned: a pinned rotate_key row is
	// corrupt, so the claim fails it rather than ship it. What matters
	// here is that the statement handed it over.
	if c != nil {
		t.Fatalf("a pinned rotate_key row was shipped: %+v", c)
	}
	if got := w.status(t, pinned.ID); got == string(StatusQueued) {
		t.Fatal("a worker that supports pinned commits did not claim the pinned job")
	}
}
