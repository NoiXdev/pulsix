package chain_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/audit"
	"github.com/noixdev/pulsix/internal/chain"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/jobs"
)

// fakeStarterStore keeps due rows in memory and applies outcomes the way
// the SQL batch does: only to rows that are still due.
type fakeStarterStore struct {
	mu         sync.Mutex
	rows       map[uuid.UUID]*chain.FollowUp
	sweeps     int
	failWrites int // the next writes fail this many times
}

func (s *fakeStarterStore) SweepStale(context.Context, time.Time, int) (int, error) {
	s.sweeps++
	return 0, nil
}

func (s *fakeStarterStore) LockDue(_ context.Context, now time.Time, limit int) (chain.DueBatch, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var due []chain.FollowUp
	for _, r := range s.rows {
		if r.State == chain.StateDue && r.NextAttemptAt != nil && !r.NextAttemptAt.After(now) && len(due) < limit {
			due = append(due, *r)
		}
	}
	slices.SortFunc(due, func(a, b chain.FollowUp) int { return a.Position - b.Position })
	return &fakeBatch{store: s, rows: due}, nil
}

type fakeBatch struct {
	store *fakeStarterStore
	rows  []chain.FollowUp
}

func (b *fakeBatch) Rows() []chain.FollowUp { return b.rows }

func (b *fakeBatch) Write(ctx context.Context, f chain.FollowUp, o chain.Outcome, notify func(context.Context, pgx.Tx) error) error {
	b.store.mu.Lock()
	if b.store.failWrites > 0 {
		b.store.failWrites--
		b.store.mu.Unlock()
		return errors.New("write-back lost")
	}
	r := b.store.rows[f.ID]
	if r == nil || r.State != chain.StateDue {
		b.store.mu.Unlock()
		return fmt.Errorf("follow-up %s is not due", f.ID)
	}
	if o.CountAttempt {
		r.Attempts++
	}
	r.Reason = o.Reason
	switch o.State {
	case chain.StateStarted:
		r.State, r.ChildGroupID = chain.StateStarted, &o.ChildGroupID
	case chain.StateSkipped:
		r.State = chain.StateSkipped
	default:
		next := o.NextAttemptAt
		r.NextAttemptAt = &next
	}
	b.store.mu.Unlock()
	if notify != nil {
		_ = notify(ctx, nil)
	}
	return nil
}

func (b *fakeBatch) Commit(context.Context) error   { return nil }
func (b *fakeBatch) Rollback(context.Context) error { return nil }

type fakeRuns struct {
	mu      sync.Mutex
	reqs    []jobs.RunRequest
	scoped  []bool
	active  int // HasActiveRun answers true this many more times
	errs    []error
	panicOn bool
	// groups models the idempotency keys of successful enqueues.
	groups map[string]uuid.UUID
}

func (r *fakeRuns) EnqueueRunWith(ctx context.Context, org, tmpl uuid.UUID, req jobs.RunRequest) ([]jobs.Job, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.panicOn {
		panic("enqueue exploded")
	}
	got, ok := db.OrgScope(ctx)
	r.scoped = append(r.scoped, ok && got == org)
	r.reqs = append(r.reqs, req)
	if len(r.errs) > 0 {
		err := r.errs[0]
		r.errs = r.errs[1:]
		return nil, err
	}
	group, replay := r.groups[req.IdempotencyKey]
	if !replay {
		group = newID()
		if r.groups == nil {
			r.groups = map[string]uuid.UUID{}
		}
		r.groups[req.IdempotencyKey] = group
	}
	return []jobs.Job{{ID: newID(), OrgID: org, TemplateID: &tmpl, RunGroupID: group}}, nil
}

func (r *fakeRuns) StartedRunGroup(_ context.Context, _ uuid.UUID, key string) (*uuid.UUID, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if g, ok := r.groups[key]; ok {
		return &g, nil
	}
	return nil, nil
}

func (r *fakeRuns) HasActiveRun(context.Context, uuid.UUID, uuid.UUID) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.active > 0 {
		r.active--
		return true, nil
	}
	return false, nil
}

type fakeNames struct{ names map[uuid.UUID]string }

func (n fakeNames) TemplateName(_ context.Context, _, id uuid.UUID) (string, error) {
	name, ok := n.names[id]
	if !ok {
		return "", jobs.ErrNotFound
	}
	return name, nil
}

type fakeMaint struct {
	in    map[uuid.UUID]bool
	calls int
}

func (m *fakeMaint) HostsInMaintenance(_ context.Context, _ uuid.UUID, hostIDs []uuid.UUID, _ time.Time) (map[uuid.UUID]bool, error) {
	m.calls++
	out := map[uuid.UUID]bool{}
	for _, h := range hostIDs {
		if m.in[h] {
			out[h] = true
		}
	}
	return out, nil
}

type fakeGuards struct {
	mode  string
	found bool
	err   error
	calls int
}

func (g *fakeGuards) MaintenanceMode(context.Context, uuid.UUID, uuid.UUID) (string, bool, error) {
	g.calls++
	return g.mode, g.found, g.err
}

type fakeAuditor struct {
	mu     sync.Mutex
	events []audit.Event
}

func (a *fakeAuditor) Record(_ context.Context, e audit.Event) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = append(a.events, e)
	return nil
}

func (a *fakeAuditor) all() []audit.Event {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.events)
}

type fakeNotifier struct{ notices []chain.SkipNotice }

func (n *fakeNotifier) FollowUpSkipped(_ context.Context, _ pgx.Tx, s chain.SkipNotice) error {
	n.notices = append(n.notices, s)
	return nil
}

const testTick = 5 * time.Second

var quietLog = slog.New(slog.DiscardHandler)

type starterFixture struct {
	t                    *testing.T
	org, parent, tmpl    uuid.UUID
	parentTmpl, schedule uuid.UUID
	h1, h2               uuid.UUID
	now                  time.Time
	store                *fakeStarterStore
	runs                 *fakeRuns
	maint                *fakeMaint
	guards               *fakeGuards
	audit                *fakeAuditor
	notes                *fakeNotifier
	starter              *chain.Starter
}

func newStarterFixture(t *testing.T) *starterFixture {
	t.Helper()
	f := &starterFixture{
		t: t, org: newID(), parent: newID(), tmpl: newID(), parentTmpl: newID(), schedule: newID(), h1: newID(), h2: newID(),
		now:   time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC),
		store: &fakeStarterStore{rows: map[uuid.UUID]*chain.FollowUp{}},
		runs:  &fakeRuns{}, maint: &fakeMaint{in: map[uuid.UUID]bool{}}, guards: &fakeGuards{},
		audit: &fakeAuditor{}, notes: &fakeNotifier{},
	}
	names := fakeNames{names: map[uuid.UUID]string{f.tmpl: "listmonk", f.parentTmpl: "cloudpanel"}}
	f.starter = chain.NewStarter(f.store, f.runs, names, f.maint, f.guards, f.audit, func() time.Time { return f.now }, quietLog, chain.WithTick(testTick))
	f.starter.SetNotifier(f.notes)
	return f
}

// row adds one due follow-up of f.parent for f.tmpl on h1 and h2, started
// by hand.
func (f *starterFixture) row(mut ...func(*chain.FollowUp)) *chain.FollowUp {
	user := newID()
	next := f.now
	r := &chain.FollowUp{
		ID: newID(), OrgID: f.org, ParentGroupID: f.parent, TemplateID: f.tmpl, State: chain.StateDue,
		HostIDs: []uuid.UUID{f.h1, f.h2}, RootTrigger: "manual", Depth: 1, Path: []uuid.UUID{f.parentTmpl},
		ConfiguredBy: &user, NextAttemptAt: &next, Position: len(f.store.rows),
	}
	for _, m := range mut {
		m(r)
	}
	f.store.rows[r.ID] = r
	return r
}

func (f *starterFixture) fromSchedule(r *chain.FollowUp) {
	r.RootTrigger = "schedule"
	r.RootRef = &f.schedule
}

func (f *starterFixture) tick() {
	f.t.Helper()
	if err := f.starter.Tick(ctx); err != nil {
		f.t.Fatal(err)
	}
}

func (f *starterFixture) wantSkipped(r *chain.FollowUp, reason string) {
	f.t.Helper()
	if r.State != chain.StateSkipped || r.Reason != reason {
		f.t.Fatalf("row = %s %q, want skipped %q", r.State, r.Reason, reason)
	}
	ev := f.audit.all()
	if len(ev) != 1 || ev[0].Meta["outcome"] != "skipped" || ev[0].Meta["reason"] != reason {
		f.t.Fatalf("audit = %+v, want one skipped event", ev)
	}
}

func (f *starterFixture) wantNotice(r *chain.FollowUp, category, reason string) {
	f.t.Helper()
	want := chain.SkipNotice{
		OrgID: f.org, ScheduleID: f.schedule, RowID: r.ID, TemplateID: f.tmpl, ParentTemplateID: r.Path[len(r.Path)-1],
		Category: category, Reason: reason, DecidedAt: f.now,
	}
	if len(f.notes.notices) != 1 || f.notes.notices[0] != want {
		f.t.Fatalf("notices = %+v, want %+v", f.notes.notices, want)
	}
}

func TestStarterStarts(t *testing.T) {
	f := newStarterFixture(t)
	r := f.row()
	f.tick()
	if len(f.runs.reqs) != 1 || !f.runs.scoped[0] {
		t.Fatalf("enqueues = %d, scoped = %v", len(f.runs.reqs), f.runs.scoped)
	}
	req := f.runs.reqs[0]
	if req.Trigger != jobs.TriggerChain || req.TriggerRef == nil || *req.TriggerRef != f.parent || req.Actor != nil ||
		!slices.Equal(req.OnlyHosts, r.HostIDs) || req.ExcludeHosts != nil || req.IdempotencyKey != "chain:"+r.ID.String() ||
		req.Overrides != nil || req.FollowUps != nil {
		t.Fatalf("request = %+v", req)
	}
	c := req.Chain
	if c == nil || c.RootTrigger != "manual" || c.RootRef != nil || c.Depth != 1 || !slices.Equal(c.Path, r.Path) || c.ConfiguredBy != r.ConfiguredBy {
		t.Fatalf("chain = %+v", c)
	}
	if r.State != chain.StateStarted || r.ChildGroupID == nil || r.Reason != "" {
		t.Fatalf("row = %+v", r)
	}
	ev := f.audit.all()
	if len(ev) != 1 {
		t.Fatalf("audit = %+v", ev)
	}
	e := ev[0]
	if e.OrgID == nil || *e.OrgID != f.org || e.ActorKind != audit.ActorKindChain || e.ActorID == nil || *e.ActorID != f.parent ||
		e.Action != "chain.run" || e.ObjectType != "job_template" || e.ObjectID != f.tmpl.String() ||
		e.Summary != fmt.Sprintf("started follow-up listmonk of run %s", f.parent) {
		t.Fatalf("event = %+v", e)
	}
	wantMeta := map[string]any{
		"parent_group_id": f.parent.String(), "child_group_id": r.ChildGroupID.String(), "root_trigger": "manual",
		"depth": 1, "outcome": "started", "reason": "", "configured_by": r.ConfiguredBy.String(),
	}
	if fmt.Sprint(e.Meta) != fmt.Sprint(wantMeta) {
		t.Fatalf("meta = %v, want %v", e.Meta, wantMeta)
	}
	if len(f.notes.notices) != 0 || f.store.sweeps != 1 {
		t.Fatalf("notices = %+v, sweeps = %d", f.notes.notices, f.store.sweeps)
	}
}

func TestStarterTooDeep(t *testing.T) {
	f := newStarterFixture(t)
	r := f.row(func(r *chain.FollowUp) { r.Depth = 10; f.fromSchedule(r) })
	f.guards.found = true
	f.tick()
	f.wantSkipped(r, "the chain is too long")
	f.wantNotice(r, "failure", "the chain is too long")
	if len(f.runs.reqs) != 0 {
		t.Fatal("a too-deep follow-up was enqueued")
	}
}

func TestStarterCycleOnPath(t *testing.T) {
	f := newStarterFixture(t)
	r := f.row(func(r *chain.FollowUp) { r.Path = []uuid.UUID{f.tmpl, f.parentTmpl}; r.Depth = 2 })
	f.tick()
	f.wantSkipped(r, "the template is already in this chain")
	if len(f.runs.reqs) != 0 || len(f.notes.notices) != 0 {
		t.Fatalf("enqueues = %d, notices for a manual chain = %+v", len(f.runs.reqs), f.notes.notices)
	}
}

func TestStarterDeletedTemplate(t *testing.T) {
	t.Run("before the enqueue", func(t *testing.T) {
		f := newStarterFixture(t)
		r := f.row(func(r *chain.FollowUp) { r.TemplateID = newID(); f.fromSchedule(r) })
		f.tmpl = r.TemplateID
		f.guards.found = true
		f.tick()
		f.wantSkipped(r, "the template was deleted")
		f.wantNotice(r, "failure", "the template was deleted")
		if len(f.runs.reqs) != 0 {
			t.Fatal("a deleted template was enqueued")
		}
	})
	t.Run("during the enqueue", func(t *testing.T) {
		f := newStarterFixture(t)
		r := f.row()
		f.runs.errs = []error{fmt.Errorf("template: %w", jobs.ErrNotFound)}
		f.tick()
		f.wantSkipped(r, "the template was deleted")
	})
	t.Run("a deleted root schedule notifies nothing", func(t *testing.T) {
		f := newStarterFixture(t)
		r := f.row(func(r *chain.FollowUp) { r.TemplateID = newID(); f.fromSchedule(r) })
		f.tick()
		f.wantSkipped(r, "the template was deleted")
		if len(f.notes.notices) != 0 {
			t.Fatalf("notices = %+v", f.notes.notices)
		}
	})
}

func TestStarterWaitsForActiveRunWithoutCountingAttempts(t *testing.T) {
	f := newStarterFixture(t)
	r := f.row()
	f.runs.active = 6
	for i := range 6 {
		f.tick()
		if r.State != chain.StateDue || r.Reason != "waiting for the active run of listmonk" || r.Attempts != 0 ||
			!r.NextAttemptAt.Equal(f.now.Add(testTick)) {
			t.Fatalf("tick %d: row = %+v", i, r)
		}
		if len(f.audit.all()) != 0 || len(f.runs.reqs) != 0 {
			t.Fatalf("tick %d: waiting audited or enqueued", i)
		}
		f.now = f.now.Add(testTick)
	}
	f.tick()
	if r.State != chain.StateStarted || r.Attempts != 0 || len(f.runs.reqs) != 1 || len(f.audit.all()) != 1 {
		t.Fatalf("after the run ended: row = %+v, enqueues = %d", r, len(f.runs.reqs))
	}
}

func TestStarterMaintenanceSkip(t *testing.T) {
	f := newStarterFixture(t)
	r := f.row(f.fromSchedule)
	f.guards.mode, f.guards.found = "skip", true
	f.maint.in[f.h2] = true
	f.tick()
	f.wantSkipped(r, "1 host(s) in a maintenance window")
	f.wantNotice(r, "skipped", "1 host(s) in a maintenance window")
	if len(f.runs.reqs) != 0 {
		t.Fatal("enqueued during a window")
	}
}

func TestStarterMaintenanceExclude(t *testing.T) {
	f := newStarterFixture(t)
	r := f.row(f.fromSchedule)
	f.guards.mode, f.guards.found = "exclude_hosts", true
	f.maint.in[f.h2] = true
	f.tick()
	if len(f.runs.reqs) != 1 || !slices.Equal(f.runs.reqs[0].ExcludeHosts, []uuid.UUID{f.h2}) ||
		!slices.Equal(f.runs.reqs[0].OnlyHosts, []uuid.UUID{f.h1, f.h2}) {
		t.Fatalf("requests = %+v", f.runs.reqs)
	}
	if c := f.runs.reqs[0].Chain; c.RootTrigger != "schedule" || c.RootRef == nil || *c.RootRef != f.schedule {
		t.Fatalf("chain = %+v", c)
	}
	if r.State != chain.StateStarted || r.Reason != "1 host(s) left out for maintenance" {
		t.Fatalf("row = %+v", r)
	}
	if ev := f.audit.all(); len(ev) != 1 || ev[0].Meta["root_ref"] != f.schedule.String() || ev[0].Meta["reason"] != r.Reason {
		t.Fatalf("audit = %+v", ev)
	}
	if len(f.notes.notices) != 0 {
		t.Fatalf("a started follow-up notified a skip: %+v", f.notes.notices)
	}
}

func TestStarterMaintenanceAllExcluded(t *testing.T) {
	f := newStarterFixture(t)
	r := f.row(f.fromSchedule)
	f.guards.mode, f.guards.found = "exclude_hosts", true
	f.maint.in[f.h1], f.maint.in[f.h2] = true, true
	f.tick()
	f.wantSkipped(r, "every host is in a maintenance window")
	f.wantNotice(r, "skipped", "every host is in a maintenance window")
}

func TestStarterMaintenanceInvalidGuard(t *testing.T) {
	f := newStarterFixture(t)
	r := f.row(f.fromSchedule)
	f.guards.mode, f.guards.found = "sometimes", true
	f.tick()
	f.wantSkipped(r, "could not start: the schedule's maintenance guard is invalid")
	f.wantNotice(r, "failure", "could not start: the schedule's maintenance guard is invalid")
}

func TestStarterMaintenanceIgnoredForManualRoot(t *testing.T) {
	f := newStarterFixture(t)
	r := f.row()
	f.guards.mode, f.guards.found = "skip", true
	f.maint.in[f.h1] = true
	f.tick()
	if r.State != chain.StateStarted || f.guards.calls != 0 || f.maint.calls != 0 || f.runs.reqs[0].ExcludeHosts != nil {
		t.Fatalf("row = %+v, guard calls = %d, maintenance calls = %d", r, f.guards.calls, f.maint.calls)
	}
}

func TestStarterDeletedScheduleIgnoresWindows(t *testing.T) {
	f := newStarterFixture(t)
	r := f.row(f.fromSchedule)
	f.guards.mode, f.guards.found = "skip", false
	f.maint.in[f.h1] = true
	f.tick()
	if r.State != chain.StateStarted || f.maint.calls != 0 || f.runs.reqs[0].ExcludeHosts != nil {
		t.Fatalf("row = %+v, maintenance calls = %d", r, f.maint.calls)
	}
}

func TestStarterNoHostsInScope(t *testing.T) {
	for _, err := range []error{jobs.ErrEmptyScope, jobs.ErrHostNotInScope} {
		f := newStarterFixture(t)
		r := f.row(f.fromSchedule)
		f.guards.found = true
		f.runs.errs = []error{err}
		f.tick()
		f.wantSkipped(r, "none of the hosts is in this template's scope")
		if len(f.notes.notices) != 0 {
			t.Fatalf("%v: notices = %+v", err, f.notes.notices)
		}
	}
}

func TestStarterRetriesThenGivesUp(t *testing.T) {
	f := newStarterFixture(t)
	r := f.row(f.fromSchedule)
	f.guards.found = true
	boom := errors.New("database is tired")
	f.runs.errs = []error{boom, boom, boom, boom, boom}
	for attempt, backoff := range []time.Duration{10 * time.Second, 20 * time.Second, 40 * time.Second, 80 * time.Second} {
		f.tick()
		if r.State != chain.StateDue || r.Attempts != attempt+1 || r.Reason != "could not start: database is tired" ||
			!r.NextAttemptAt.Equal(f.now.Add(backoff)) {
			t.Fatalf("attempt %d: row = %+v, want next at now+%s", attempt+1, r, backoff)
		}
		f.now = f.now.Add(backoff - time.Second)
		f.tick() // not due yet
		if len(f.runs.reqs) != attempt+1 {
			t.Fatalf("attempt %d: retried before the backoff ran out", attempt+1)
		}
		f.now = f.now.Add(time.Second)
	}
	if len(f.audit.all()) != 0 {
		t.Fatal("a retry was audited")
	}
	f.tick()
	if r.Attempts != 5 {
		t.Fatalf("attempts = %d, want 5", r.Attempts)
	}
	f.wantSkipped(r, "could not start: database is tired")
	f.wantNotice(r, "failure", "could not start: database is tired")
}

func TestStarterBackoffIsCapped(t *testing.T) {
	f := newStarterFixture(t)
	r := f.row(func(r *chain.FollowUp) { r.Attempts = 3 })
	f.starter = chain.NewStarter(f.store, f.runs, fakeNames{names: map[uuid.UUID]string{f.tmpl: "listmonk"}}, f.maint, f.guards, f.audit,
		func() time.Time { return f.now }, quietLog, chain.WithTick(time.Minute))
	f.runs.errs = []error{errors.New("boom")}
	f.tick()
	if !r.NextAttemptAt.Equal(f.now.Add(5 * time.Minute)) {
		t.Fatalf("next attempt at %s, want now+5m", r.NextAttemptAt)
	}
}

func TestStarterRecoversAPanicAndLeavesTheRowDue(t *testing.T) {
	f := newStarterFixture(t)
	r := f.row()
	f.runs.panicOn = true
	f.tick()
	if r.State != chain.StateDue || r.Attempts != 0 || len(f.audit.all()) != 0 {
		t.Fatalf("row = %+v", r)
	}
	f.runs.panicOn = false
	f.tick()
	if r.State != chain.StateStarted {
		t.Fatalf("next tick: row = %+v", r)
	}
}

// Row hosts {h1, h2}, the template's scope only {h2}, h2 in a window: the
// exclusion empties the scope, which is a maintenance skip.
func TestStarterMaintenanceExcludeEmptiesTheScope(t *testing.T) {
	f := newStarterFixture(t)
	r := f.row(f.fromSchedule)
	f.guards.mode, f.guards.found = "exclude_hosts", true
	f.maint.in[f.h2] = true
	f.runs.errs = []error{fmt.Errorf("enqueue: %w", jobs.ErrAllHostsExcluded)}
	f.tick()
	if len(f.runs.reqs) != 1 || !slices.Equal(f.runs.reqs[0].ExcludeHosts, []uuid.UUID{f.h2}) {
		t.Fatalf("requests = %+v", f.runs.reqs)
	}
	f.wantSkipped(r, "every host is in a maintenance window")
	f.wantNotice(r, "skipped", "every host is in a maintenance window")
}

func TestStarterSkipsARowWithoutHosts(t *testing.T) {
	f := newStarterFixture(t)
	r := f.row(func(r *chain.FollowUp) { r.HostIDs = []uuid.UUID{}; f.fromSchedule(r) })
	f.guards.found = true
	f.tick()
	f.wantSkipped(r, "the run played on no host")
	if len(f.runs.reqs) != 0 || len(f.notes.notices) != 0 {
		t.Fatalf("enqueues = %d, notices = %+v", len(f.runs.reqs), f.notes.notices)
	}
}

// The enqueue succeeded but its write-back was lost: the next tick links
// the row to that run group instead of waiting on the row's own child run.
func TestStarterRecoversALostWriteBack(t *testing.T) {
	f := newStarterFixture(t)
	r := f.row()
	f.store.failWrites = 1
	f.tick()
	if r.State != chain.StateDue || len(f.runs.reqs) != 1 || len(f.audit.all()) != 0 {
		t.Fatalf("after the lost write: row = %+v, enqueues = %d", r, len(f.runs.reqs))
	}
	first := f.runs.groups["chain:"+r.ID.String()]
	f.runs.active = 100 // the child run is active now
	f.tick()
	if r.State != chain.StateStarted || r.ChildGroupID == nil || *r.ChildGroupID != first || r.Reason != "" || r.Attempts != 0 {
		t.Fatalf("row = %+v, want started with %s", r, first)
	}
	if len(f.runs.groups) != 1 || f.runs.reqs[len(f.runs.reqs)-1].IdempotencyKey != "chain:"+r.ID.String() {
		t.Fatalf("groups = %v", f.runs.groups)
	}
	if ev := f.audit.all(); len(ev) != 1 || ev[0].Meta["child_group_id"] != first.String() {
		t.Fatalf("audit = %+v", ev)
	}
}

// An earlier start whose write-back was lost, and the root schedule's guard
// now stops the follow-up: the row records the start that exists, with the
// guard's reason, and nothing is enqueued again.
func TestStarterReplayStopsForMaintenance(t *testing.T) {
	for _, tc := range []struct{ mode, reason string }{
		{"skip", "1 host(s) in a maintenance window"},
		{"exclude_hosts", "every host is in a maintenance window"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			f := newStarterFixture(t)
			r := f.row(f.fromSchedule)
			f.guards.mode, f.guards.found = tc.mode, true
			f.maint.in[f.h2] = true
			if tc.mode == "exclude_hosts" {
				f.maint.in[f.h1] = true
			}
			earlier := newID()
			f.runs.groups = map[string]uuid.UUID{"chain:" + r.ID.String(): earlier}
			f.tick()
			if len(f.runs.reqs) != 0 {
				t.Fatalf("re-enqueued in a window: %+v", f.runs.reqs)
			}
			if r.State != chain.StateStarted || r.ChildGroupID == nil || *r.ChildGroupID != earlier || r.Reason != tc.reason {
				t.Fatalf("row = %+v, want started with %s and %q", r, earlier, tc.reason)
			}
			if len(f.notes.notices) != 0 {
				t.Fatalf("a started follow-up notified a skip: %+v", f.notes.notices)
			}
			if ev := f.audit.all(); len(ev) != 1 || ev[0].Meta["outcome"] != "started" || ev[0].Meta["reason"] != tc.reason {
				t.Fatalf("audit = %+v", ev)
			}
		})
	}
}

func TestStarterReplayRetriesAMaintenanceError(t *testing.T) {
	f := newStarterFixture(t)
	r := f.row(f.fromSchedule)
	f.guards.err = errors.New("schedules are down")
	f.runs.groups = map[string]uuid.UUID{"chain:" + r.ID.String(): newID()}
	f.tick()
	if len(f.runs.reqs) != 0 || r.State != chain.StateDue || r.Attempts != 1 ||
		r.Reason != "could not start: read the schedule's maintenance guard: schedules are down" {
		t.Fatalf("row = %+v, enqueues = %d", r, len(f.runs.reqs))
	}
}

// Giving up on the replay still records the start that exists.
func TestStarterReplayGivesUpOnTheStartThatExists(t *testing.T) {
	f := newStarterFixture(t)
	r := f.row(func(r *chain.FollowUp) { f.fromSchedule(r); r.Attempts = 4 })
	f.guards.err = errors.New("schedules are down")
	earlier := newID()
	f.runs.groups = map[string]uuid.UUID{"chain:" + r.ID.String(): earlier}
	f.tick()
	if len(f.runs.reqs) != 0 || r.State != chain.StateStarted || r.ChildGroupID == nil || *r.ChildGroupID != earlier || r.Attempts != 4 {
		t.Fatalf("row = %+v, enqueues = %d", r, len(f.runs.reqs))
	}
}
