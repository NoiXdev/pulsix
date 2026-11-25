// Backlog 3.126: a check-mode job (a playbook dry run) is claimed only by
// a worker that announced supports_check_mode. An older worker would
// ignore the check flag and run the play for real, so its claim must not
// see the row at all: the dry run stays queued for a capable worker, and
// the ordinary jobs behind it are still claimed.
package jobs

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

// markCheckMode flips a queued row to check mode. The gate under test is
// the claim statement's, which reads the column and nothing else.
func (w *atomicWorld) markCheckMode(t *testing.T, id uuid.UUID) {
	t.Helper()
	if _, err := w.d.Pool().Exec(context.Background(), `UPDATE jobs SET check_mode = true WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
}

func (w *atomicWorld) status(t *testing.T, id uuid.UUID) string {
	t.Helper()
	var s string
	if err := w.d.Pool().QueryRow(context.Background(), `SELECT status FROM jobs WHERE id = $1`, id).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestClaimGivesACheckModeJobOnlyToAWorkerThatSupportsIt(t *testing.T) {
	ctx := context.Background()
	w := newAtomicWorld(t, "jobs-check-mode-claim")
	zone := testZone(t, w.d, w.org)
	worker := w.activeWorker(t, zone)

	// The dry run is the oldest queued job; a real job waits behind it.
	dry := w.queueRotateJob(t, zone)
	w.markCheckMode(t, dry.ID)
	normal := w.queueRotateJob(t, zone)

	// An older worker: no announcement (and cancel_stops_play set, so the
	// two answers cannot stand in for each other).
	c, err := w.q.ClaimFor(ctx, worker.ID, worker.OrgID, []uuid.UUID{zone}, "v1", ClaimCaps{CancelStopsPlay: true})
	if err != nil || c == nil {
		t.Fatalf("ClaimFor without check mode support = %+v, %v; want the real job", c, err)
	}
	if got := c.Job.GetId(); got != normal.ID.String() {
		t.Fatalf("an older worker claimed %s, want the real job %s behind the dry run", got, normal.ID)
	}
	if c.Job.GetCheck() {
		t.Fatal("the real job's claim carries check")
	}
	if got := w.status(t, dry.ID); got != string(StatusQueued) {
		t.Fatalf("the dry run is %q after an older worker's claim, want it still queued", got)
	}
	// Nothing else is left for it: the dry run is never its job.
	if again, err := w.q.ClaimFor(ctx, worker.ID, worker.OrgID, []uuid.UUID{zone}, "v1", ClaimCaps{CancelStopsPlay: true}); err != nil || again != nil {
		t.Fatalf("second ClaimFor without support = %+v, %v; want nothing", again, err)
	}
	// The manager's own in-process claim has nobody to honour the flag
	// either.
	if inProc, err := w.q.Claim(ctx, w.org, []uuid.UUID{zone}, "v1"); err != nil || inProc != nil {
		t.Fatalf("in-process Claim = %+v, %v; want nothing", inProc, err)
	}
	if got := w.status(t, dry.ID); got != string(StatusQueued) {
		t.Fatalf("the dry run is %q, want it still queued", got)
	}

	// A worker that announces support takes it.
	c, err = w.q.ClaimFor(ctx, worker.ID, worker.OrgID, []uuid.UUID{zone}, "v1", ClaimCaps{SupportsCheckMode: true})
	if err != nil {
		t.Fatal(err)
	}
	// A rotate_key row in check mode is corrupt — only a template run can
	// be a dry run — so the claim fails it rather than ship it without
	// the flag; what matters here is that the statement handed it over.
	if c != nil {
		t.Fatalf("a check-mode rotate_key row was shipped: %+v", c)
	}
	if got := w.status(t, dry.ID); got == string(StatusQueued) {
		t.Fatal("a worker that supports check mode did not claim the dry run")
	}
}
