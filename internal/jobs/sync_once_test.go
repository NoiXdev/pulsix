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
	d      *db.DB
	err    error
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
	return r.err
}

// TestSyncFinishMovesItsWaiters (spec 2026-11-25 A, sync_first): a sync's
// terminal transition tells the waiters, inside its transaction (they see
// the terminal status), whatever the outcome; another kind does not. A
// waiter's failure rolls the finish back, so the two never diverge.
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

	boom := errors.New("boom")
	waiters.err = boom
	stuck := f.runningJob(t, jobs.KindSyncRepo)
	if err := f.queue.Finish(ctx, stuck.ID, jobs.StatusSucceeded, nil, ""); !errors.Is(err, boom) {
		t.Fatalf("finish with failing waiters = %v, want boom", err)
	}
	var status string
	if err := f.d.Pool().QueryRow(ctx, `SELECT status FROM jobs WHERE id = $1`, stuck.ID).Scan(&status); err != nil || status != "running" {
		t.Fatalf("job after the rolled back finish = %q %v", status, err)
	}
}
