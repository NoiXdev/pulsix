package chain_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/chain"
	"github.com/noixdev/pulsix/internal/inventory/render"
	"github.com/noixdev/pulsix/internal/jobs"
)

// oneZone resolves every scope to the env's zone with the given hosts.
type oneZone struct {
	zone  uuid.UUID
	hosts []uuid.UUID
}

func (z oneZone) Resolve(context.Context, uuid.UUID, render.Scope, string) ([]jobs.ZoneTargets, error) {
	return []jobs.ZoneTargets{{ZoneID: z.zone, HostIDs: z.hosts}}, nil
}

// envTemplates looks templates up by id with an empty scope.
type envTemplates struct{ org uuid.UUID }

func (t envTemplates) TemplateLookup(_ context.Context, org, id uuid.UUID) (jobs.TemplateRow, error) {
	if org != t.org {
		return jobs.TemplateRow{}, jobs.ErrNotFound
	}
	return jobs.TemplateRow{ID: id, OrgID: org, Scope: json.RawMessage(`{}`)}, nil
}

// heldRuns counts enqueues and holds each one until another tick has
// finished, so both starters are inside their batches at the same time.
type heldRuns struct {
	chain.Runs
	finished *atomic.Int32
	calls    atomic.Int32
	expired  atomic.Bool
}

func (h *heldRuns) EnqueueRunWith(ctx context.Context, org, tmpl uuid.UUID, req jobs.RunRequest) ([]jobs.Job, error) {
	h.calls.Add(1)
	deadline := time.Now().Add(5 * time.Second)
	for h.finished.Load() == 0 {
		if time.Now().After(deadline) {
			h.expired.Store(true)
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	return h.Runs.EnqueueRunWith(ctx, org, tmpl, req)
}

func TestStarterTwoStartersStartOnce(t *testing.T) {
	e := newEnv(t)
	b := e.template("b")
	a := e.template("a", b)
	group, h1 := newID(), newID()
	e.plan(group, a, nil)
	e.job(group, "succeeded", []uuid.UUID{h1}, map[string][]uuid.UUID{"ok_hosts": {h1}})
	now := time.Now().Truncate(time.Microsecond)
	if err := e.store.ResolveGroup(e.scoped, e.org, group, jobs.StatusSucceeded, now); err != nil {
		t.Fatal(err)
	}

	svc := jobs.NewService(jobs.NewSQLStore(e.d), nil, envTemplates{org: e.org}, oneZone{zone: e.zone, hosts: []uuid.UUID{h1}}, nil, nil, nil, time.Now, rand.Reader)
	svc.SetFollowUpPlanner(e.store)
	var finished atomic.Int32
	runs := &heldRuns{Runs: svc, finished: &finished}
	aud := &fakeAuditor{}
	clock := func() time.Time { return now.Add(time.Second) }
	names := fakeNames{names: map[uuid.UUID]string{b: "b"}}

	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range 2 {
		st := chain.NewStarter(e.store, runs, names, nil, nil, aud, clock, quietLog)
		wg.Go(func() {
			<-start
			errs[i] = st.Tick(ctx)
			finished.Add(1)
		})
	}
	close(start)
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}

	if runs.expired.Load() {
		t.Fatal("the held enqueue timed out: the other tick never finished while the row was locked")
	}
	if n := runs.calls.Load(); n != 1 {
		t.Fatalf("enqueues = %d, want 1", n)
	}
	var groups, total int
	if err := e.d.Pool().QueryRow(ctx, `SELECT count(DISTINCT run_group_id), count(*) FROM jobs
		WHERE org_id = $1 AND trigger = 'chain' AND trigger_ref = $2 AND template_id = $3`, e.org, group, b).Scan(&groups, &total); err != nil {
		t.Fatal(err)
	}
	if groups != 1 || total != 1 {
		t.Fatalf("chained run groups = %d, jobs = %d, want 1 and 1", groups, total)
	}
	r := e.rows(group)[0]
	if r.State != chain.StateStarted || r.ChildGroupID == nil || r.Attempts != 0 {
		t.Fatalf("row = %+v", r)
	}
	var child uuid.UUID
	if err := e.d.Pool().QueryRow(ctx, `SELECT run_group_id FROM jobs WHERE trigger = 'chain' AND trigger_ref = $1`, group).Scan(&child); err != nil || child != *r.ChildGroupID {
		t.Fatalf("child group = %s, row says %s (%v)", child, r.ChildGroupID, err)
	}
	if ev := aud.all(); len(ev) != 1 || ev[0].Meta["outcome"] != "started" {
		t.Fatalf("audit = %+v, want one started event", ev)
	}
}

// failingRuns fails every enqueue of one template.
type failingRuns struct {
	chain.Runs
	fail uuid.UUID
}

func (f failingRuns) EnqueueRunWith(ctx context.Context, org, tmpl uuid.UUID, req jobs.RunRequest) ([]jobs.Job, error) {
	if tmpl == f.fail {
		return nil, errors.New("queue is down")
	}
	return f.Runs.EnqueueRunWith(ctx, org, tmpl, req)
}

func TestStarterRecordsEveryOutcomeInSQL(t *testing.T) {
	e := newEnv(t)
	b, c, d, x := e.template("b"), e.template("c"), e.template("d"), e.template("x")
	a := e.template("a", b, c, d, x)
	group, h1 := newID(), newID()
	e.plan(group, a, nil)
	e.job(group, "succeeded", []uuid.UUID{h1}, map[string][]uuid.UUID{"ok_hosts": {h1}})
	now := time.Now().Truncate(time.Microsecond)
	if err := e.store.ResolveGroup(e.scoped, e.org, group, jobs.StatusSucceeded, now); err != nil {
		t.Fatal(err)
	}
	// b starts; c waits for its own active run; d gives up on its fifth
	// attempt; x is deleted.
	e.job(newID(), "running", []uuid.UUID{h1}, nil)
	if _, err := e.d.Pool().Exec(ctx, `UPDATE jobs SET template_id = $1 WHERE org_id = $2 AND status = 'running'`, c, e.org); err != nil {
		t.Fatal(err)
	}
	if _, err := e.d.Pool().Exec(ctx, `UPDATE run_follow_ups SET attempts = 4 WHERE parent_group_id = $1 AND template_id = $2`, group, d); err != nil {
		t.Fatal(err)
	}
	svc := jobs.NewService(jobs.NewSQLStore(e.d), nil, envTemplates{org: e.org}, oneZone{zone: e.zone, hosts: []uuid.UUID{h1}}, nil, nil, nil, time.Now, rand.Reader)
	tick := now.Add(time.Second)
	st := chain.NewStarter(e.store, failingRuns{Runs: svc, fail: d}, fakeNames{names: map[uuid.UUID]string{b: "b", c: "c", d: "d"}}, nil, nil, &fakeAuditor{},
		func() time.Time { return tick }, quietLog)
	if err := st.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	rows := e.rows(group)
	if rows[0].State != chain.StateStarted || rows[0].ChildGroupID == nil || rows[0].ChildFirstJobID == nil {
		t.Fatalf("b = %+v", rows[0])
	}
	if rows[1].State != chain.StateDue || rows[1].Reason != "waiting for the active run of c" || rows[1].Attempts != 0 ||
		rows[1].NextAttemptAt == nil || !rows[1].NextAttemptAt.Equal(tick.Add(chain.DefaultTick)) {
		t.Fatalf("c = %+v", rows[1])
	}
	if rows[2].State != chain.StateSkipped || rows[2].Reason != "could not start: queue is down" || rows[2].Attempts != 5 {
		t.Fatalf("d = %+v", rows[2])
	}
	if rows[3].State != chain.StateSkipped || rows[3].Reason != "the template was deleted" || rows[3].Attempts != 0 {
		t.Fatalf("x = %+v", rows[3])
	}
}

// lossyStore fails the first row write of its batches.
type lossyStore struct {
	*chain.SQLStore
	fail atomic.Int32
}

func (l *lossyStore) LockDue(ctx context.Context, now time.Time, limit int) (chain.DueBatch, error) {
	b, err := l.SQLStore.LockDue(ctx, now, limit)
	if err != nil {
		return nil, err
	}
	return lossyBatch{DueBatch: b, store: l}, nil
}

type lossyBatch struct {
	chain.DueBatch
	store *lossyStore
}

func (b lossyBatch) Write(ctx context.Context, f chain.FollowUp, o chain.Outcome, notify func(context.Context, pgx.Tx) error) error {
	if b.store.fail.Add(-1) >= 0 {
		return errors.New("write-back lost")
	}
	return b.DueBatch.Write(ctx, f, o, notify)
}

func TestStarterLinksAStartWhoseWriteBackWasLost(t *testing.T) {
	e := newEnv(t)
	b := e.template("b")
	a := e.template("a", b)
	group, h1 := newID(), newID()
	e.plan(group, a, nil)
	e.job(group, "succeeded", []uuid.UUID{h1}, map[string][]uuid.UUID{"ok_hosts": {h1}})
	now := time.Now().Truncate(time.Microsecond)
	if err := e.store.ResolveGroup(e.scoped, e.org, group, jobs.StatusSucceeded, now); err != nil {
		t.Fatal(err)
	}
	svc := jobs.NewService(jobs.NewSQLStore(e.d), nil, envTemplates{org: e.org}, oneZone{zone: e.zone, hosts: []uuid.UUID{h1}}, nil, nil, nil, time.Now, rand.Reader)
	store := &lossyStore{SQLStore: e.store}
	store.fail.Store(1)
	clock := now.Add(time.Second)
	st := chain.NewStarter(store, svc, fakeNames{names: map[uuid.UUID]string{b: "b"}}, nil, nil, &fakeAuditor{}, func() time.Time { return clock }, quietLog)
	if err := st.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if r := e.rows(group)[0]; r.State != chain.StateDue {
		t.Fatalf("after the lost write: row = %+v", r)
	}
	clock = clock.Add(time.Second)
	if err := st.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	var child uuid.UUID
	var total int
	if err := e.d.Pool().QueryRow(ctx, `SELECT min(run_group_id::text)::uuid, count(*) FROM jobs WHERE trigger = 'chain' AND trigger_ref = $1`, group).Scan(&child, &total); err != nil {
		t.Fatal(err)
	}
	r := e.rows(group)[0]
	if total != 1 || r.State != chain.StateStarted || r.ChildGroupID == nil || *r.ChildGroupID != child || r.Reason != "" {
		t.Fatalf("jobs = %d, row = %+v, want started with %s", total, r, child)
	}
}

type panickyNotifier struct{ panics atomic.Bool }

func (p *panickyNotifier) FollowUpSkipped(context.Context, pgx.Tx, chain.SkipNotice) error {
	if p.panics.Load() {
		panic("notifier exploded")
	}
	return nil
}

func TestStarterNotifierPanicLeavesTheRowDue(t *testing.T) {
	e := newEnv(t)
	b := e.template("b")
	a := e.template("a", b)
	group, h1 := newID(), newID()
	e.plan(group, a, nil)
	e.job(group, "succeeded", []uuid.UUID{h1}, map[string][]uuid.UUID{"ok_hosts": {h1}})
	now := time.Now().Truncate(time.Microsecond)
	if err := e.store.ResolveGroup(e.scoped, e.org, group, jobs.StatusSucceeded, now); err != nil {
		t.Fatal(err)
	}
	if _, err := e.d.Pool().Exec(ctx, `UPDATE run_follow_ups SET root_trigger = 'schedule', root_ref = $1 WHERE parent_group_id = $2`, newID(), group); err != nil {
		t.Fatal(err)
	}
	aud := &fakeAuditor{}
	// b's name is unknown: the row is skipped as deleted, a failure notice.
	st := chain.NewStarter(e.store, &fakeRuns{}, fakeNames{}, nil, &fakeGuards{found: true}, aud, func() time.Time { return now.Add(time.Second) }, quietLog)
	n := &panickyNotifier{}
	n.panics.Store(true)
	st.SetNotifier(n)
	if err := st.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if r := e.rows(group)[0]; r.State != chain.StateDue || len(aud.all()) != 0 {
		t.Fatalf("after the panic: row = %+v, audit = %d", r, len(aud.all()))
	}
	n.panics.Store(false)
	if err := st.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if r := e.rows(group)[0]; r.State != chain.StateSkipped || r.Reason != "the template was deleted" || len(aud.all()) != 1 {
		t.Fatalf("next tick: row = %+v, audit = %d", r, len(aud.all()))
	}
}
