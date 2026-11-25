package runrequests_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/runrequests"
)

// syncJob inserts a sync_repo job of f's organization in status and
// returns its id.
func (f sqlFixture) syncJob(t *testing.T, status jobs.Status, errText string) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	zone := uuid.Must(uuid.NewV7())
	if _, err := f.d.Pool().Exec(ctx, `INSERT INTO zones (id, org_id, slug, name) VALUES ($1, $2, $3, 'z')`, zone, f.org, "z-"+zone.String()[24:]); err != nil {
		t.Fatal(err)
	}
	id := uuid.Must(uuid.NewV7())
	if _, err := f.d.Pool().Exec(ctx, `INSERT INTO jobs (id, org_id, zone_id, kind, run_group_id, status, error, payload)
		VALUES ($1, $2, $3, 'sync_repo', $4, $5, $6, '{}')`, id, f.org, zone, uuid.Must(uuid.NewV7()), string(status), errText); err != nil {
		t.Fatal(err)
	}
	return id
}

func (f sqlFixture) setJob(t *testing.T, id uuid.UUID, status jobs.Status, errText string) {
	t.Helper()
	if _, err := f.d.Pool().Exec(context.Background(), `UPDATE jobs SET status = $2, error = $3 WHERE id = $1`, id, string(status), errText); err != nil {
		t.Fatal(err)
	}
}

func (f sqlFixture) waiting(job uuid.UUID, token *uuid.UUID, created time.Time) runrequests.Request {
	return runrequests.Request{
		ID: uuid.Must(uuid.NewV7()), OrgID: f.org, Kind: runrequests.KindRun, TemplateID: f.template, TemplateRevision: 1,
		Reason: "sync first", RequestedByUser: f.user, RequestedByToken: token, Status: runrequests.StatusWaitingForSync,
		SyncJobID: &job, ExpiresInMinutes: 15, CreatedAt: created,
	}
}

// TestSQLStoreWaitingForSync: a waiting request is stored without an
// expiry, counts against its token's cap but not in the approvers' badge,
// is listed with its sync job's state, and ends pending (its expiry
// starting then) or failed (with the reason) exactly once.
func TestSQLStoreWaitingForSync(t *testing.T) {
	d := testdb.Open(t)
	f := newSQLFixture(t, d, "runreq-wait")
	store := runrequests.NewSQLStore(d)
	now := time.Now().UTC().Truncate(time.Microsecond)
	tok := uuid.Must(uuid.NewV7())
	job := f.syncJob(t, jobs.StatusQueued, "")

	w := f.waiting(job, &tok, now)
	if err := store.InsertCapped(f.ctx, w, runrequests.MaxPendingPerToken, now); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(f.ctx, f.org, w.ID)
	if err != nil || got.Status != runrequests.StatusWaitingForSync || !got.ExpiresAt.IsZero() || got.SyncJobID == nil ||
		*got.SyncJobID != job || got.ExpiresInMinutes != 15 || got.SyncError != "" {
		t.Fatalf("waiting = %+v %v", got, err)
	}
	if n, err := store.CountPending(f.ctx, f.org, &tok, now); err != nil || n != 1 {
		t.Fatalf("the token's count = %d %v, want 1 (a waiting request counts against the cap)", n, err)
	}
	if n, err := store.CountPending(f.ctx, f.org, nil, now); err != nil || n != 0 {
		t.Fatalf("the badge = %d %v, want 0 (nobody can approve a waiting request)", n, err)
	}
	st := runrequests.StatusWaitingForSync
	if rows, err := store.List(f.ctx, f.org, &st, now); err != nil || len(rows) != 1 || rows[0].ID != w.ID {
		t.Fatalf("listed as waiting = %+v %v", rows, err)
	}

	list, err := store.ListWaitingFor(f.ctx, job)
	if err != nil || len(list) != 1 || list[0].ID != w.ID || list[0].OrgID != f.org || list[0].SyncStatus != jobs.StatusQueued ||
		list[0].ExpiresInMinutes != 15 || !list[0].CreatedAt.Equal(now) || list[0].SyncJobID != job {
		t.Fatalf("waiting on the sync = %+v %v", list, err)
	}
	if other, err := store.ListWaitingFor(f.ctx, uuid.Must(uuid.NewV7())); err != nil || len(other) != 0 {
		t.Fatalf("waiting on another sync = %+v %v", other, err)
	}
	f.setJob(t, job, jobs.StatusSucceeded, "")
	if list, err = store.ListWaitingFor(f.ctx, job); err != nil || len(list) != 1 || list[0].SyncStatus != jobs.StatusSucceeded {
		t.Fatalf("after the sync = %+v %v", list, err)
	}

	expires := now.Add(15 * time.Minute)
	if ok, err := store.EndWait(f.ctx, f.org, w.ID, runrequests.StatusPending, expires, ""); err != nil || !ok {
		t.Fatalf("end the wait = %v %v", ok, err)
	}
	if ok, err := store.EndWait(f.ctx, f.org, w.ID, runrequests.StatusFailed, now, "late"); err != nil || ok {
		t.Fatalf("end it twice = %v %v, want false", ok, err)
	}
	if got, err = store.Get(f.ctx, f.org, w.ID); err != nil || got.Status != runrequests.StatusPending || !got.ExpiresAt.Equal(expires) || got.SyncError != "" {
		t.Fatalf("pending = %+v %v", got, err)
	}
	if list, err = store.ListWaitingFor(f.ctx, job); err != nil || len(list) != 0 {
		t.Fatalf("still waiting = %+v %v", list, err)
	}

	failedJob := f.syncJob(t, jobs.StatusFailed, "fetch failed")
	fw := f.waiting(failedJob, nil, now)
	if err := store.Insert(f.ctx, fw); err != nil {
		t.Fatal(err)
	}
	if ok, err := store.EndWait(f.ctx, f.org, fw.ID, runrequests.StatusFailed, now, "fetch failed"); err != nil || !ok {
		t.Fatalf("fail = %v %v", ok, err)
	}
	if got, err = store.Get(f.ctx, f.org, fw.ID); err != nil || got.Status != runrequests.StatusFailed || !got.ExpiresAt.Equal(now) || got.SyncError != "fetch failed" {
		t.Fatalf("failed = %+v %v", got, err)
	}
}

// TestSQLStoreWaitingSchema: only a waiting request may lack an expiry and
// it always names its sync; a rejection of a waiting request gives it
// the decision's time as its expiry; an approval does not move it.
func TestSQLStoreWaitingSchema(t *testing.T) {
	d := testdb.Open(t)
	f := newSQLFixture(t, d, "runreq-wait-schema")
	store := runrequests.NewSQLStore(d)
	now := time.Now().UTC().Truncate(time.Microsecond)
	job := f.syncJob(t, jobs.StatusRunning, "")

	noExpiry := f.waiting(job, nil, now)
	noExpiry.Status = runrequests.StatusPending
	if err := store.Insert(f.ctx, noExpiry); err == nil {
		t.Fatal("a pending request without an expiry was stored")
	}
	noSync := f.waiting(job, nil, now)
	noSync.SyncJobID = nil
	if err := store.Insert(f.ctx, noSync); err == nil {
		t.Fatal("a waiting request without its sync was stored")
	}

	w := f.waiting(job, nil, now)
	if err := store.Insert(f.ctx, w); err != nil {
		t.Fatal(err)
	}
	if ok, err := store.Decide(f.ctx, f.org, w.ID, runrequests.StatusApproved, f.user, now, "fp", ""); err != nil || ok {
		t.Fatalf("approve a waiting request = %v %v, want false", ok, err)
	}
	at := now.Add(time.Minute)
	if ok, err := store.Decide(f.ctx, f.org, w.ID, runrequests.StatusRejected, f.user, at, "", ""); err != nil || !ok {
		t.Fatalf("reject a waiting request = %v %v", ok, err)
	}
	if got, err := store.Get(f.ctx, f.org, w.ID); err != nil || got.Status != runrequests.StatusRejected || !got.ExpiresAt.Equal(at) {
		t.Fatalf("rejected = %+v %v", got, err)
	}
}

// TestSQLStoreSweepSeesEveryOrganization: the sweep lists without an
// organization scope and sees the waiting requests of every organization
// that are due: their sync ended or is gone, or they have waited since
// before staleBefore. A request whose sync still runs within the bound is
// not listed, however many there are.
func TestSQLStoreSweepSeesEveryOrganization(t *testing.T) {
	d := testdb.Open(t)
	a, b := newSQLFixture(t, d, "runreq-sweep-a"), newSQLFixture(t, d, "runreq-sweep-b")
	store := runrequests.NewSQLStore(d)
	now := time.Now().UTC().Truncate(time.Microsecond)
	staleBefore := now.Add(-runrequests.MaxSyncWait)
	running := a.syncJob(t, jobs.StatusRunning, "")
	young := a.waiting(running, nil, now)
	old := a.waiting(running, nil, staleBefore.Add(-time.Second))
	ended := a.waiting(a.syncJob(t, jobs.StatusCancelled, ""), nil, now)
	gone := b.waiting(uuid.Must(uuid.NewV7()), nil, now.Add(time.Second))
	for _, w := range []struct {
		f sqlFixture
		r runrequests.Request
	}{{a, young}, {a, old}, {a, ended}, {b, gone}} {
		if err := store.Insert(w.f.ctx, w.r); err != nil {
			t.Fatal(err)
		}
	}
	due, err := store.ListDueWaiting(context.Background(), staleBefore)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[uuid.UUID]runrequests.Waiting{}
	for _, w := range due {
		seen[w.ID] = w
	}
	if len(due) != 3 {
		t.Fatalf("due = %+v, want the old, the ended and the gone one", due)
	}
	if _, ok := seen[young.ID]; ok {
		t.Fatal("a request whose sync still runs within the bound was listed")
	}
	if seen[old.ID].SyncStatus != jobs.StatusRunning || seen[ended.ID].SyncStatus != jobs.StatusCancelled || seen[old.ID].OrgID != a.org {
		t.Fatalf("organization a = %+v %+v", seen[old.ID], seen[ended.ID])
	}
	if got, ok := seen[gone.ID]; !ok || got.SyncStatus != "" || got.OrgID != b.org {
		t.Fatalf("organization b = %+v %v", got, ok)
	}
	if scoped, err := store.ListDueWaiting(b.ctx, staleBefore); err != nil || len(scoped) != 1 || scoped[0].ID != gone.ID {
		t.Fatalf("scoped to b = %+v %v", scoped, err)
	}
}
