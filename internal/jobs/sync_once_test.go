package jobs_test

import (
	"context"
	"crypto/rand"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/crypto"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/jobs"
)

// TestEnqueueSyncReturnsTheQueuedSync (spec 2026-11-25 G, controller
// ruling on the fresh head): a sync of a repository with a sync queued
// hands that job back instead of queuing another. A sync a worker already
// took (claimed, running) or that is going away (cancelling) may have
// fetched before the change the caller wants, so a new one is queued, and
// the next caller attaches to that one. Another repository's sync is its
// own.
func TestEnqueueSyncReturnsTheQueuedSync(t *testing.T) {
	org, repoID, other, actor := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	zs := &fakeZones{}
	zs.add(org, uuid.Must(uuid.NewV7()), hosts.DefaultZoneSlug)
	store := newFakeStore()
	svc := newService(store, &fakeQueue{}, &fakeTemplates{}, &fakeResolver{}, &fakeHosts{}, zs)
	setStatus := func(id uuid.UUID, st jobs.Status) {
		store.mu.Lock()
		defer store.mu.Unlock()
		sj := store.jobs[id]
		sj.j.Status = st
		store.jobs[id] = sj
	}

	first, already, err := svc.EnqueueSync(ctx, org, repoID, &actor)
	if err != nil || already {
		t.Fatalf("first sync = %+v %v %v", first, already, err)
	}
	again, already, err := svc.EnqueueSync(ctx, org, repoID, &actor)
	if err != nil || !already || again.ID != first.ID {
		t.Fatalf("second sync = %s %v %v, want %s already queued", again.ID, already, err, first.ID)
	}
	if theirs, already, err := svc.EnqueueSync(ctx, org, other, &actor); err != nil || already || theirs.ID == first.ID {
		t.Fatalf("another repository's sync = %+v %v %v", theirs, already, err)
	}
	prev := first.ID
	for _, st := range []jobs.Status{jobs.StatusClaimed, jobs.StatusRunning, jobs.StatusCancelling, jobs.StatusSucceeded} {
		setStatus(prev, st)
		next, already, err := svc.EnqueueSync(ctx, org, repoID, &actor)
		if err != nil || already || next.ID == prev {
			t.Fatalf("with the sync %s = %+v %v %v, want a new queued sync", st, next, already, err)
		}
		if attached, already, err := svc.EnqueueSync(ctx, org, repoID, &actor); err != nil || !already || attached.ID != next.ID {
			t.Fatalf("after a new sync was queued (%s) = %+v %v %v, want %s", st, attached, already, err, next.ID)
		}
		prev = next.ID
	}
}

// TestEnqueueSyncAttachesOnlyToAQueuedSyncInSQL is the same rule through the
// SQL store: only a queued sync of the repository is returned.
func TestEnqueueSyncAttachesOnlyToAQueuedSyncInSQL(t *testing.T) {
	f := newVarSourcesFixture(t, "jobs-sync-queued-org")
	scoped := db.WithOrgScope(ctx, f.org)
	hostsSvc := hosts.NewService(hosts.NewSQLStore(f.d), time.Now)
	if _, err := hostsSvc.CreateZone(scoped, f.org, hosts.DefaultZoneSlug, hosts.DefaultZoneName); err != nil {
		t.Fatal(err)
	}
	svc := jobs.NewService(f.store, f.queue, &fakeTemplates{}, &fakeResolver{}, &fakeHosts{}, hostsSvc,
		crypto.NewBox(testBoxKey("jobs-sync-queued-key")), time.Now, rand.Reader)
	first, already, err := svc.EnqueueSync(scoped, f.org, f.repoID, nil)
	if err != nil || already {
		t.Fatalf("first = %+v %v %v", first, already, err)
	}
	if again, already, err := svc.EnqueueSync(scoped, f.org, f.repoID, nil); err != nil || !already || again.ID != first.ID {
		t.Fatalf("while queued = %+v %v %v", again, already, err)
	}
	prev := first.ID
	for _, st := range []jobs.Status{jobs.StatusClaimed, jobs.StatusRunning, jobs.StatusCancelling, jobs.StatusFailed} {
		if _, err := f.d.Pool().Exec(ctx, `UPDATE jobs SET status = $2 WHERE id = $1`, prev, string(st)); err != nil {
			t.Fatal(err)
		}
		next, already, err := svc.EnqueueSync(scoped, f.org, f.repoID, nil)
		if err != nil || already || next.ID == prev || next.Status != jobs.StatusQueued {
			t.Fatalf("with the sync %s = %+v %v %v, want a new queued sync", st, next, already, err)
		}
		prev = next.ID
	}
}

// TestEnqueueSyncOnceUnderConcurrency: concurrent syncs of one repository
// through the SQL store create exactly one job, and every caller gets it.
func TestEnqueueSyncOnceUnderConcurrency(t *testing.T) {
	f := newVarSourcesFixture(t, "jobs-sync-once-org")
	scoped := db.WithOrgScope(ctx, f.org)
	hostsSvc := hosts.NewService(hosts.NewSQLStore(f.d), time.Now)
	if _, err := hostsSvc.CreateZone(scoped, f.org, hosts.DefaultZoneSlug, hosts.DefaultZoneName); err != nil {
		t.Fatal(err)
	}
	svc := jobs.NewService(f.store, f.queue, &fakeTemplates{}, &fakeResolver{}, &fakeHosts{}, hostsSvc,
		crypto.NewBox(testBoxKey("jobs-sync-once-key")), time.Now, rand.Reader)

	const callers = 12
	ids := make(chan uuid.UUID, callers)
	news := make(chan bool, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			j, already, err := svc.EnqueueSync(scoped, f.org, f.repoID, nil)
			if err != nil {
				t.Error(err)
				return
			}
			ids <- j.ID
			news <- !already
		}()
	}
	wg.Wait()
	close(ids)
	close(news)
	seen := map[uuid.UUID]bool{}
	for id := range ids {
		seen[id] = true
	}
	created := 0
	for n := range news {
		if n {
			created++
		}
	}
	var stored int
	if err := f.d.Pool().QueryRow(ctx, `SELECT count(*) FROM jobs WHERE org_id = $1 AND kind = 'sync_repo'`, f.org).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 || created != 1 || stored != 1 {
		t.Fatalf("callers saw %d jobs, %d created one, %d stored; want 1, 1, 1", len(seen), created, stored)
	}
	var j jobs.Job
	for id := range seen {
		got, err := svc.Get(scoped, f.org, id)
		if err != nil {
			t.Fatal(err)
		}
		j = got
	}
	if j.Kind != jobs.KindSyncRepo || j.Status != jobs.StatusQueued {
		t.Fatalf("the sync = %+v", j)
	}
}

// recordingWaiters is a jobs.SyncWaiters that records what it was told
// and what the job's status was inside the finish's transaction.
type recordingWaiters struct {
	d   *db.DB
	err error
	// broken makes the waiter run a failing statement first, which
	// aborts the transaction it runs in unless it runs under a savepoint.
	broken bool
	calls  []uuid.UUID
	orgs   []uuid.UUID
	status []string
}

func (r *recordingWaiters) SyncFinished(ctx context.Context, orgID, jobID uuid.UUID) error {
	var status string
	if err := r.d.QueryRow(ctx, `SELECT status FROM jobs WHERE id = $1`, jobID).Scan(&status); err != nil {
		return err
	}
	r.calls, r.orgs, r.status = append(r.calls, jobID), append(r.orgs, orgID), append(r.status, status)
	if r.broken {
		if _, err := r.d.Exec(ctx, `SELECT 1/0`); err != nil {
			return err
		}
	}
	return r.err
}

// TestSyncFinishMovesItsWaiters (spec 2026-11-25 A, sync_first): a sync's
// terminal transition tells the waiters, inside its transaction (they see
// the terminal status), whatever the outcome; another kind does not. A
// waiter's failure does not hold the finish up (final review M5): it is
// logged, its writes are rolled back to a savepoint, and the sweep moves
// the requests later.
func TestSyncFinishMovesItsWaiters(t *testing.T) {
	f := newVarSourcesFixture(t, "jobs-sync-waiters-org")
	waiters := &recordingWaiters{d: f.d}
	f.queue.SetSyncWaiters(waiters)

	ok := f.runningJob(t, jobs.KindSyncRepo)
	stats, _ := workerStats(t, f.repoID, testReport())
	if err := f.queue.Finish(ctx, ok.ID, jobs.StatusSucceeded, stats, ""); err != nil {
		t.Fatal(err)
	}
	failed := f.runningJob(t, jobs.KindSyncRepo)
	if err := f.queue.Finish(ctx, failed.ID, jobs.StatusFailed, nil, "fetch failed"); err != nil {
		t.Fatal(err)
	}
	run := f.runningJob(t, jobs.KindConnectionTest)
	if err := f.queue.Finish(ctx, run.ID, jobs.StatusSucceeded, nil, ""); err != nil {
		t.Fatal(err)
	}
	if len(waiters.calls) != 2 || waiters.calls[0] != ok.ID || waiters.calls[1] != failed.ID ||
		waiters.orgs[0] != f.org || waiters.status[0] != "succeeded" || waiters.status[1] != "failed" {
		t.Fatalf("waiters told %v %v %v", waiters.calls, waiters.orgs, waiters.status)
	}

	for _, broken := range []bool{false, true} {
		waiters.err, waiters.broken = errors.New("boom"), broken
		done := f.runningJob(t, jobs.KindSyncRepo)
		if err := f.queue.Finish(ctx, done.ID, jobs.StatusSucceeded, nil, ""); err != nil {
			t.Fatalf("finish with failing waiters (broken statement %v) = %v, want it to finish", broken, err)
		}
		var status string
		if err := f.d.Pool().QueryRow(ctx, `SELECT status FROM jobs WHERE id = $1`, done.ID).Scan(&status); err != nil || status != "succeeded" {
			t.Fatalf("job after a failing waiter = %q %v", status, err)
		}
	}
}

// TestEnqueueSyncOnceWithReplaysItsGroupAndJoinsAQueuedSync (backlog
// 2.137): a settled repository sync webhook names its own run group. A
// second call with that group (a replay after lost bookkeeping) returns the
// sync it already started, whatever its status, and a call with another
// group joins the repository's queued sync instead of queuing another.
func TestEnqueueSyncOnceWithReplaysItsGroupAndJoinsAQueuedSync(t *testing.T) {
	org, repoID, trig := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	zs := &fakeZones{}
	zs.add(org, uuid.Must(uuid.NewV7()), hosts.DefaultZoneSlug)
	store := newFakeStore()
	svc := newService(store, &fakeQueue{}, &fakeTemplates{}, &fakeResolver{}, &fakeHosts{}, zs)
	req := func(group uuid.UUID) jobs.RunRequest {
		return jobs.RunRequest{Trigger: jobs.TriggerWebhook, TriggerRef: &trig, RunGroupID: group}
	}
	window := uuid.Must(uuid.NewV7())
	first, existing, err := svc.EnqueueSyncOnceWith(ctx, org, repoID, req(window))
	if err != nil || existing || first.RunGroupID != window || first.Trigger != jobs.TriggerWebhook || *first.TriggerRef != trig {
		t.Fatalf("first = %+v %v %v", first, existing, err)
	}
	store.mu.Lock()
	sj := store.jobs[first.ID]
	sj.j.Status = jobs.StatusRunning
	store.jobs[first.ID] = sj
	store.mu.Unlock()
	if again, existing, err := svc.EnqueueSyncOnceWith(ctx, org, repoID, req(window)); err != nil || !existing || again.ID != first.ID {
		t.Fatalf("replay = %+v %v %v, want %s", again, existing, err, first.ID)
	}
	queued, _, err := svc.EnqueueSync(ctx, org, repoID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if joined, existing, err := svc.EnqueueSyncOnceWith(ctx, org, repoID, req(uuid.Must(uuid.NewV7()))); err != nil || !existing || joined.ID != queued.ID {
		t.Fatalf("with a queued sync = %+v %v %v, want %s", joined, existing, err, queued.ID)
	}
	if _, _, err := svc.EnqueueSyncOnceWith(ctx, org, repoID, jobs.RunRequest{Trigger: jobs.TriggerWebhook, TriggerRef: &trig,
		Overrides: &jobs.RunOverrides{ExtraVars: map[string]any{"a": 1}}}); !errors.Is(err, jobs.ErrInvalidInput) {
		t.Fatalf("a sync takes no overrides: %v", err)
	}
}
