package runrequests_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/runrequests"
)

// syncFirst makes a sync_first run request of token, asking for a
// 15-minute expiry.
func (w *wiring) syncFirst(t *testing.T, token *uuid.UUID) runrequests.Request {
	t.Helper()
	r, err := w.createSyncFirst(token)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func (w *wiring) createSyncFirst(token *uuid.UUID) (runrequests.Request, error) {
	return w.svc.Create(context.Background(), w.org, runrequests.CreateInput{
		Kind: runrequests.KindRun, TemplateID: w.val.template, Reason: "deploy the fix",
		RequestedByUser: uuid.Must(uuid.NewV7()), RequestedByToken: token, SyncFirst: true, ExpiresInMinutes: 15,
	})
}

// startSync makes the fake syncer hand out a new sync job in status.
func (w *wiring) startSync(status jobs.Status) uuid.UUID {
	job := uuid.Must(uuid.NewV7())
	w.syncs.job = &job
	w.store.syncs[job] = memSync{status: status}
	return job
}

func (w *wiring) get(t *testing.T, id uuid.UUID) runrequests.Request {
	t.Helper()
	r, _, _, err := w.svc.Get(context.Background(), w.org, id)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// TestSyncFirstWaitsThenBecomesPending: a sync_first request starts the
// sync of its template's repository as its requester and waits for it,
// without an expiry and unapprovable; when that sync succeeds it becomes
// pending and its expiry starts then, after which it is approved as any.
func TestSyncFirstWaitsThenBecomesPending(t *testing.T) {
	w := newWiring()
	ctx := context.Background()
	job := w.startSync(jobs.StatusQueued)
	tok := uuid.Must(uuid.NewV7())
	r := w.syncFirst(t, &tok)
	if w.syncs.calls != 1 || w.syncs.template != w.val.template || w.syncs.actor != r.RequestedByUser {
		t.Fatalf("sync started %d times for %s by %s", w.syncs.calls, w.syncs.template, w.syncs.actor)
	}
	if r.Status != runrequests.StatusWaitingForSync || !r.ExpiresAt.IsZero() || r.SyncJobID == nil || *r.SyncJobID != job {
		t.Fatalf("created = %+v", r)
	}
	_, _, fp, err := w.svc.Get(ctx, w.org, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.Approve(ctx, w.org, r.ID, uuid.Must(uuid.NewV7()), fp, nil); !errors.Is(err, runrequests.ErrWaitingForSync) {
		t.Fatalf("approve while waiting = %v, want ErrWaitingForSync", err)
	}
	if n, err := w.svc.PendingCount(ctx, w.org); err != nil || n != 0 {
		t.Fatalf("badge while waiting = %d %v", n, err)
	}

	// The sync runs for a while; nothing moves until it has finished.
	w.now = w.now.Add(10 * time.Minute)
	w.store.syncs[job] = memSync{status: jobs.StatusRunning}
	if err := w.svc.SyncFinished(ctx, w.org, job); err != nil {
		t.Fatal(err)
	}
	if got := w.get(t, r.ID); got.Status != runrequests.StatusWaitingForSync {
		t.Fatalf("while the sync runs = %+v", got)
	}
	w.store.syncs[job] = memSync{status: jobs.StatusSucceeded}
	if err := w.svc.SyncFinished(ctx, w.org, job); err != nil {
		t.Fatal(err)
	}
	got := w.get(t, r.ID)
	if got.Status != runrequests.StatusPending || !got.ExpiresAt.Equal(w.now.Add(15*time.Minute)) {
		t.Fatalf("after the sync = %+v, want pending until %v", got, w.now.Add(15*time.Minute))
	}
	_, _, fp, _ = w.svc.Get(ctx, w.org, r.ID)
	if _, err := w.svc.Approve(ctx, w.org, r.ID, uuid.Must(uuid.NewV7()), fp, nil); err != nil {
		t.Fatalf("approve after the sync = %v", err)
	}
}

// TestSyncFirstFailsWithTheSync: a sync that ends in anything but success
// fails the request with the reason; a failed request is neither
// approvable nor rejectable.
func TestSyncFirstFailsWithTheSync(t *testing.T) {
	for name, tc := range map[string]struct {
		sync memSync
		want string
	}{
		"failed":           {memSync{jobs.StatusFailed, "git fetch: authentication required"}, "the repository's sync failed: git fetch: authentication required"},
		"error":            {memSync{jobs.StatusError, "worker lost"}, "the repository's sync failed: worker lost"},
		"failed, no words": {memSync{jobs.StatusFailed, ""}, "the repository's sync failed"},
		"cancelled":        {memSync{jobs.StatusCancelled, ""}, "the repository's sync was cancelled"},
	} {
		t.Run(name, func(t *testing.T) {
			w := newWiring()
			ctx := context.Background()
			job := w.startSync(jobs.StatusRunning)
			r := w.syncFirst(t, nil)
			w.store.syncs[job] = tc.sync
			if err := w.svc.SyncFinished(ctx, w.org, job); err != nil {
				t.Fatal(err)
			}
			got := w.get(t, r.ID)
			if got.Status != runrequests.StatusFailed || got.SyncError != tc.want || !got.ExpiresAt.Equal(w.now) {
				t.Fatalf("after the sync = %+v, want failed with %q", got, tc.want)
			}
			_, _, fp, _ := w.svc.Get(ctx, w.org, r.ID)
			if _, err := w.svc.Approve(ctx, w.org, r.ID, uuid.Must(uuid.NewV7()), fp, nil); !errors.Is(err, runrequests.ErrSyncFailed) {
				t.Fatalf("approve a failed request = %v, want ErrSyncFailed", err)
			}
			if _, err := w.svc.Reject(ctx, w.org, r.ID, uuid.Must(uuid.NewV7())); !errors.Is(err, runrequests.ErrDecided) {
				t.Fatalf("reject a failed request = %v, want ErrDecided", err)
			}
		})
	}
}

// TestSyncFirstAdvancesEveryRequestOfTheSync: several requests waiting on
// one sync all move when it finishes; a request waiting on another sync
// does not.
func TestSyncFirstAdvancesEveryRequestOfTheSync(t *testing.T) {
	w := newWiring()
	ctx := context.Background()
	shared := w.startSync(jobs.StatusRunning)
	a, b := w.syncFirst(t, nil), w.syncFirst(t, nil)
	other := w.startSync(jobs.StatusRunning)
	c := w.syncFirst(t, nil)
	w.store.syncs[shared] = memSync{status: jobs.StatusSucceeded}
	if err := w.svc.SyncFinished(ctx, w.org, shared); err != nil {
		t.Fatal(err)
	}
	for _, id := range []uuid.UUID{a.ID, b.ID} {
		if got := w.get(t, id); got.Status != runrequests.StatusPending {
			t.Fatalf("request on the finished sync = %+v", got)
		}
	}
	if got := w.get(t, c.ID); got.Status != runrequests.StatusWaitingForSync || *got.SyncJobID != other {
		t.Fatalf("request on another sync = %+v", got)
	}
}

// TestSweepEndsWaitsTheSyncCannotEnd: the periodic sweep catches what the
// sync's finish did not: a sync that ended without it (cancelled while
// queued, finished before the request was stored), a sync job that is
// gone, and a sync that does not finish within MaxSyncWait.
func TestSweepEndsWaitsTheSyncCannotEnd(t *testing.T) {
	w := newWiring()
	ctx := context.Background()
	done := w.startSync(jobs.StatusRunning)
	succeeded := w.syncFirst(t, nil)
	cancelled := w.startSync(jobs.StatusQueued)
	cancelledReq := w.syncFirst(t, nil)
	gone := w.startSync(jobs.StatusQueued)
	goneReq := w.syncFirst(t, nil)
	slow := w.startSync(jobs.StatusRunning)
	slowReq := w.syncFirst(t, nil)
	w.now = w.now.Add(time.Minute)
	young := w.syncFirst(t, nil) // on the same slow sync, a minute later

	w.store.syncs[done] = memSync{status: jobs.StatusSucceeded}
	w.store.syncs[cancelled] = memSync{status: jobs.StatusCancelled}
	delete(w.store.syncs, gone)
	w.now = w.now.Add(runrequests.MaxSyncWait - time.Minute) // slowReq has waited MaxSyncWait, young a minute less
	if err := w.svc.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[uuid.UUID]struct {
		status runrequests.Status
		reason string
	}{
		succeeded.ID:    {runrequests.StatusPending, ""},
		cancelledReq.ID: {runrequests.StatusFailed, "the repository's sync was cancelled"},
		goneReq.ID:      {runrequests.StatusFailed, "the repository's sync job no longer exists"},
		slowReq.ID:      {runrequests.StatusFailed, "the repository's sync did not finish within 1 hour"},
		young.ID:        {runrequests.StatusWaitingForSync, ""},
	} {
		if got := w.get(t, id); got.Status != want.status || got.SyncError != want.reason {
			t.Fatalf("%s = %s %q, want %s %q", id, got.Status, got.SyncError, want.status, want.reason)
		}
	}
	if slow == uuid.Nil {
		t.Fatal("no slow sync")
	}
}

// TestSyncFirstWithoutARepositoryIsPendingAtOnce: a template whose
// playbook comes from no repository has nothing to sync; the request is
// pending at once, its expiry running.
func TestSyncFirstWithoutARepositoryIsPendingAtOnce(t *testing.T) {
	w := newWiring()
	r := w.syncFirst(t, nil)
	if w.syncs.calls != 1 || r.Status != runrequests.StatusPending || r.SyncJobID != nil || !r.ExpiresAt.Equal(w.now.Add(15*time.Minute)) {
		t.Fatalf("created = %+v", r)
	}
}

// TestSyncFirstOfAFinishedSyncIsPendingAtOnce: a sync that finished before
// the request was stored (the request attached to a sync someone else
// started) moves the request at once.
func TestSyncFirstOfAFinishedSyncIsPendingAtOnce(t *testing.T) {
	w := newWiring()
	w.startSync(jobs.StatusSucceeded)
	r := w.syncFirst(t, nil)
	if r.Status != runrequests.StatusPending || !r.ExpiresAt.Equal(w.now.Add(15*time.Minute)) {
		t.Fatalf("created = %+v", r)
	}
}

// TestSyncFirstCountsAgainstTheCap: waiting requests count against their
// token's cap, and a refused request starts no sync.
func TestSyncFirstCountsAgainstTheCap(t *testing.T) {
	w := newWiring()
	w.startSync(jobs.StatusRunning)
	tok := uuid.Must(uuid.NewV7())
	for range runrequests.MaxPendingPerToken {
		w.syncFirst(t, &tok)
	}
	if _, err := w.createSyncFirst(&tok); !errors.Is(err, runrequests.ErrTooManyPending) {
		t.Fatalf("request %d = %v, want ErrTooManyPending", runrequests.MaxPendingPerToken+1, err)
	}
	if w.syncs.calls != runrequests.MaxPendingPerToken {
		t.Fatalf("syncs started = %d, want %d", w.syncs.calls, runrequests.MaxPendingPerToken)
	}
	if _, err := w.createFor(&tok); !errors.Is(err, runrequests.ErrTooManyPending) {
		t.Fatalf("a plain request over the cap = %v, want ErrTooManyPending", err)
	}
}

// TestSyncFirstRefusals: a retry does not sync first, a failing sync start
// stores nothing, and a person may reject a request while it waits.
func TestSyncFirstRefusals(t *testing.T) {
	w := newWiring()
	ctx := context.Background()
	job := uuid.Must(uuid.NewV7())
	if _, err := w.svc.Create(ctx, w.org, runrequests.CreateInput{
		Kind: runrequests.KindRetryFailed, JobID: &job, Reason: "again", RequestedByUser: uuid.Must(uuid.NewV7()), SyncFirst: true,
	}); !errors.Is(err, runrequests.ErrInvalidInput) || !strings.Contains(err.Error(), "sync_first") {
		t.Fatalf("retry with sync_first = %v", err)
	}

	w.syncs.err = jobs.ErrNoZone
	if _, err := w.createSyncFirst(nil); !errors.Is(err, jobs.ErrNoZone) {
		t.Fatalf("sync start failing = %v", err)
	}
	if len(w.store.rows) != 0 {
		t.Fatalf("stored %d requests", len(w.store.rows))
	}

	w.syncs.err = nil
	w.startSync(jobs.StatusRunning)
	r := w.syncFirst(t, nil)
	got, err := w.svc.Reject(ctx, w.org, r.ID, uuid.Must(uuid.NewV7()))
	if err != nil || got.Status != runrequests.StatusRejected {
		t.Fatalf("reject while waiting = %+v %v", got, err)
	}
	if stored := w.get(t, r.ID); stored.Status != runrequests.StatusRejected || !stored.ExpiresAt.Equal(w.now) {
		t.Fatalf("stored after the rejection = %+v", stored)
	}
}
