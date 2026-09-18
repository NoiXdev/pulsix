package schedules_test

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/audit"
	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/schedules"
)

const grace = 30 * time.Second // 2 × a 15 s tick

func due(t *testing.T, w *world, spec string, dueAt time.Time, g schedules.Guards) schedules.Schedule {
	t.Helper()
	in := validInput(w.templateID)
	in.Name = "sched " + uuid.NewString()[:8]
	in.Cron, in.Timezone, in.Guards = spec, "UTC", g
	s, err := w.svc.Create(ctx, w.org, w.actor, in)
	if err != nil {
		t.Fatal(err)
	}
	s.NextRunAt = &dueAt
	return s
}

func TestDecide(t *testing.T) {
	base := time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)
	skip := schedules.Guards{Overlap: schedules.OverlapSkip, CatchUp: schedules.CatchUp{Window: "1h"}}
	queue := schedules.Guards{Overlap: schedules.OverlapQueue, CatchUp: schedules.CatchUp{Window: "1h"}}
	catchUp := schedules.Guards{Overlap: schedules.OverlapSkip, CatchUp: schedules.CatchUp{Enabled: true, Window: "2h"}}
	cases := []struct {
		name       string
		spec       string
		dueAt      time.Time
		now        time.Time
		guards     schedules.Guards
		active     bool
		enqueueErr error
		outcome    schedules.Outcome
		reason     string // substring
		next       string
		enqueued   bool
	}{
		{name: "on time", spec: "0 * * * *", dueAt: base, now: base.Add(5 * time.Second), guards: skip,
			outcome: schedules.OutcomeTriggered, next: "2026-09-18T11:00:00Z", enqueued: true},
		{name: "overlap skip", spec: "0 * * * *", dueAt: base, now: base.Add(time.Second), guards: skip, active: true,
			outcome: schedules.OutcomeSkippedOverlap, reason: "still active", next: "2026-09-18T11:00:00Z"},
		{name: "overlap queue runs anyway", spec: "0 * * * *", dueAt: base, now: base.Add(time.Second), guards: queue, active: true,
			outcome: schedules.OutcomeTriggered, next: "2026-09-18T11:00:00Z", enqueued: true},
		{name: "late without catch-up is missed", spec: "0 * * * *", dueAt: base, now: base.Add(10 * time.Minute), guards: skip,
			outcome: schedules.OutcomeMissed, reason: "1 occurrence(s) missed", next: "2026-09-18T11:00:00Z"},
		{name: "several missed collapse into one row", spec: "0 * * * *", dueAt: base, now: base.Add(5*time.Hour + 10*time.Minute), guards: skip,
			outcome: schedules.OutcomeMissed, reason: "6 occurrence(s) missed", next: "2026-09-18T16:00:00Z"},
		{name: "catch-up inside the window runs once", spec: "0 * * * *", dueAt: base, now: base.Add(3*time.Hour + 10*time.Minute), guards: catchUp,
			outcome: schedules.OutcomeTriggered, reason: "catch-up of 4 missed occurrence(s)", next: "2026-09-18T14:00:00Z", enqueued: true},
		{name: "catch-up outside the window is missed", spec: "0 3 * * *", dueAt: base.Add(-7 * time.Hour), now: base, guards: catchUp,
			outcome: schedules.OutcomeMissed, next: "2026-09-19T03:00:00Z"},
		{name: "a frequent schedule whose latest occurrence is on time runs normally", spec: "* * * * *", dueAt: base, now: base.Add(10*time.Minute + 5*time.Second), guards: skip,
			outcome: schedules.OutcomeTriggered, reason: "10 earlier occurrence(s) missed", next: "2026-09-18T10:11:00Z", enqueued: true},
		{name: "enqueue failure is recorded generically", spec: "0 * * * *", dueAt: base, now: base, guards: skip, enqueueErr: jobs.ErrEmptyScope,
			outcome: schedules.OutcomeFailed, reason: "resolves to no host", next: "2026-09-18T11:00:00Z"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t)
			s := due(t, w, c.spec, c.dueAt, c.guards)
			w.runs.active, w.runs.err = c.active, c.enqueueErr
			d := w.svc.Decide(ctx, s, c.now, grace)
			if d.Run.Outcome != c.outcome || !strings.Contains(d.Run.Reason, c.reason) {
				t.Fatalf("run = %+v, want %s containing %q", d.Run, c.outcome, c.reason)
			}
			if d.NextRunAt == nil || d.NextRunAt.UTC().Format(time.RFC3339) != c.next {
				t.Fatalf("next = %v, want %s", d.NextRunAt, c.next)
			}
			if !d.Run.DueAt.Equal(c.dueAt) || !d.Run.DecidedAt.Equal(c.now) || d.Run.ScheduleID != s.ID || d.Run.OrgID != w.org {
				t.Fatalf("run identity = %+v", d.Run)
			}
			if got := len(w.runs.requests) == 1; got != c.enqueued {
				t.Fatalf("enqueued = %v, want %v", got, c.enqueued)
			}
			if c.enqueued {
				req := w.runs.requests[0]
				if req.Trigger != jobs.TriggerSchedule || *req.TriggerRef != s.ID || req.Actor != nil {
					t.Fatalf("request = %+v", req)
				}
				if d.Run.JobGroupID == nil {
					t.Fatal("a triggered run records its job group")
				}
			}
		})
	}
}

func TestDecideAuditsTriggeredAndFailedRuns(t *testing.T) {
	base := time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)
	w := newWorld(t)
	s := due(t, w, "0 * * * *", base, schedules.Guards{})
	w.svc.Decide(ctx, s, base, grace)
	if len(w.audit.events) != 1 {
		t.Fatalf("events = %+v", w.audit.events)
	}
	e := w.audit.events[0]
	if e.ActorKind != audit.ActorKindSchedule || *e.ActorID != s.ID || e.Action != "schedule.run" ||
		e.ObjectType != "job_template" || e.ObjectID != w.templateID.String() || *e.OrgID != w.org {
		t.Fatalf("event = %+v", e)
	}
	if e.Meta["created_by"] != w.actor.String() || e.Meta["outcome"] != "triggered" {
		t.Fatalf("meta = %+v", e.Meta)
	}
	// A skipped run is recorded in schedule_runs only.
	w.runs.active = true
	s2 := due(t, w, "*/5 * * * *", base, schedules.Guards{})
	w.svc.Decide(ctx, s2, base, grace)
	if len(w.audit.events) != 1 {
		t.Fatalf("a skip must not be audited: %+v", w.audit.events)
	}
}

func TestDecideStopsAScheduleWhoseExpressionNoLongerParses(t *testing.T) {
	base := time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)
	w := newWorld(t)
	s := due(t, w, "0 * * * *", base, schedules.Guards{})
	s.Cron = "garbage"
	d := w.svc.Decide(ctx, s, base, grace)
	if d.Run.Outcome != schedules.OutcomeFailed || d.NextRunAt != nil {
		t.Fatalf("decision = %+v", d)
	}
	if len(w.audit.events) != 1 {
		t.Fatalf("an invalid timetable must be audited too, like any other failed run: %+v", w.audit.events)
	}
}

func TestDecideAuditsAnOverlapCheckFailure(t *testing.T) {
	base := time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)
	w := newWorld(t)
	w.runs.activeErr = errors.New("db down")
	s := due(t, w, "0 * * * *", base, schedules.Guards{Overlap: schedules.OverlapSkip})
	d := w.svc.Decide(ctx, s, base, grace)
	if d.Run.Outcome != schedules.OutcomeFailed || !strings.Contains(d.Run.Reason, "could not check for an active run") {
		t.Fatalf("decision = %+v", d)
	}
	if d.NextRunAt == nil {
		t.Fatal("the schedule must still advance past the failed check")
	}
	if len(w.audit.events) != 1 {
		t.Fatalf("events = %+v", w.audit.events)
	}
}

// panicOnceRuns panics on its first EnqueueRunWith call for a chosen
// template, then behaves like a normal enqueuer — the shape a broken
// third-party jobs contract or a bad template reveal could take.
type panicOnceRuns struct {
	mu       sync.Mutex
	poison   uuid.UUID
	panicked bool
	requests []jobs.RunRequest
}

func (f *panicOnceRuns) EnqueueRunWith(_ context.Context, orgID, templateID uuid.UUID, req jobs.RunRequest) ([]jobs.Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if templateID == f.poison && !f.panicked {
		f.panicked = true
		panic("boom: enqueue panicked")
	}
	f.requests = append(f.requests, req)
	return []jobs.Job{{ID: uuid.Must(uuid.NewV7()), OrgID: orgID, TemplateID: &templateID, RunGroupID: uuid.Must(uuid.NewV7()), Trigger: req.Trigger}}, nil
}

func (f *panicOnceRuns) HasActiveRun(context.Context, uuid.UUID, uuid.UUID) (bool, error) {
	return false, nil
}

// TestDecideRecoversAPanicAndAdvancesTheSchedule is fix round 1's Important
// #1: a panic anywhere inside deciding or enqueueing (a stored override
// that no longer reveals, a broken enqueuer, an unexpected empty jobs
// slice) must not crash the manager, and must not leave the poisoned
// occurrence due forever — that would crash-loop the manager on restart,
// since the transaction never committed.
func TestDecideRecoversAPanicAndAdvancesTheSchedule(t *testing.T) {
	base := time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)
	store := newFakeStore()
	tmpl := &fakeTemplates{}
	poisonTemplate := uuid.Must(uuid.NewV7())
	runs := &panicOnceRuns{poison: poisonTemplate}
	auditor := &fakeAudit{}
	org, actor := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	svc := schedules.NewService(store, tmpl, runs, auditor, func() time.Time { return base }, rand.Reader,
		slog.New(slog.NewTextHandler(io.Discard, nil)))

	in := validInput(poisonTemplate)
	s, err := svc.Create(ctx, org, actor, in)
	if err != nil {
		t.Fatal(err)
	}
	s.NextRunAt = &base

	d := svc.Decide(ctx, s, base, grace)
	if d.Run.Outcome != schedules.OutcomeFailed {
		t.Fatalf("decision after panic = %+v, want failed", d)
	}
	if d.NextRunAt == nil || !d.NextRunAt.After(base) {
		t.Fatalf("next run at = %v, must advance past the poisoned occurrence", d.NextRunAt)
	}

	// A second decide, once the schedule is due again, is unaffected —
	// the ticker keeps going instead of crash-looping on the same row.
	s.NextRunAt = d.NextRunAt
	d2 := svc.Decide(ctx, s, *d.NextRunAt, grace)
	if d2.Run.Outcome != schedules.OutcomeTriggered {
		t.Fatalf("decision after recovery = %+v, want triggered", d2)
	}
}

// TestTickerSurvivesAPanickingScheduleAndProcessesTheRest is the
// batch-level half of fix round 1's Important #1: one schedule's enqueue
// panicking must not stop the other due schedules in the same batch from
// firing, and Tick must return normally instead of the panic escaping the
// whole ticker loop.
func TestTickerSurvivesAPanickingScheduleAndProcessesTheRest(t *testing.T) {
	base := time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)
	store := newFakeStore()
	tmpl := &fakeTemplates{}
	poisonTemplate, healthyTemplate := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	runs := &panicOnceRuns{poison: poisonTemplate}
	auditor := &fakeAudit{}
	org, actor := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	svc := schedules.NewService(store, tmpl, runs, auditor, func() time.Time { return base }, rand.Reader,
		slog.New(slog.NewTextHandler(io.Discard, nil)))

	mk := func(templateID uuid.UUID) schedules.Schedule {
		in := validInput(templateID)
		in.Name = "sched " + uuid.NewString()[:8]
		s, err := svc.Create(ctx, org, actor, in)
		if err != nil {
			t.Fatal(err)
		}
		s.NextRunAt = &base
		return s
	}
	poison, healthy := mk(poisonTemplate), mk(healthyTemplate)

	fd := &fakeDue{due: []schedules.Schedule{poison, healthy}}
	tk := schedules.NewTicker(svc, fd, 15*time.Second, func() time.Time { return base }, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	n, err := tk.Tick(ctx)
	if err != nil || n != 2 {
		t.Fatalf("Tick = %d, %v", n, err)
	}
	if len(fd.decisions) != 2 {
		t.Fatalf("decisions = %+v", fd.decisions)
	}
	if fd.decisions[0].Run.Outcome != schedules.OutcomeFailed {
		t.Fatalf("poisoned schedule's decision = %+v, want failed", fd.decisions[0])
	}
	if fd.decisions[1].Run.Outcome != schedules.OutcomeTriggered {
		t.Fatalf("healthy schedule's decision = %+v, want triggered", fd.decisions[1])
	}
	if len(runs.requests) != 1 {
		t.Fatalf("enqueued = %d, want 1 (only the healthy schedule)", len(runs.requests))
	}
}

// alwaysPanicAudit panics on every Record call — a persistent bug in the
// audit sink, not a one-off glitch — used to reproduce fix round 2's
// Minor: the original panic comes from inside auditRun itself, on the
// normal (triggered) path after a successful enqueue, and Decide's own
// recover handler used to call auditRun a second time to record the
// failure. Because the underlying fault is persistent, that second call
// panics too, and a panic raised by a deferred function is not caught by
// that function's own recover.
type alwaysPanicAudit struct{}

func (alwaysPanicAudit) Record(context.Context, audit.Event) error {
	panic("boom: audit panicked")
}

// TestDecideRecoversAPanicFromTheAuditorItself is fix round 2's Minor: a
// panic originating inside auditRun's own call, on the normal (triggered)
// path after a successful enqueue, is caught by Decide's recover — which
// used to call auditRun a second time to record the failure. A panic
// raised by a deferred function is not caught by that function's own
// recover, so if the *same* call panics again, it used to escape Decide
// entirely, leaving the row's write-back to processRow's outer recover,
// which discards it — no run recorded, next_run_at never advanced, so the
// schedule re-enqueues on every following tick while the auditor keeps
// panicking.
func TestDecideRecoversAPanicFromTheAuditorItself(t *testing.T) {
	base := time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)
	store := newFakeStore()
	tmpl := &fakeTemplates{}
	runs := &fakeRuns{}
	auditor := alwaysPanicAudit{}
	org, actor, templateID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	svc := schedules.NewService(store, tmpl, runs, auditor, func() time.Time { return base }, rand.Reader,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	in := validInput(templateID)
	s, err := svc.Create(ctx, org, actor, in)
	if err != nil {
		t.Fatal(err)
	}
	s.NextRunAt = &base

	d := svc.Decide(ctx, s, base, grace)
	if d.Run.Outcome != schedules.OutcomeFailed {
		t.Fatalf("decision = %+v, want failed", d)
	}
	if d.NextRunAt == nil || !d.NextRunAt.After(base) {
		t.Fatalf("next run at = %v, must still advance past the poisoned occurrence", d.NextRunAt)
	}
	if len(runs.requests) != 1 {
		t.Fatalf("the enqueue itself must have already happened once before the audit panicked: %d", len(runs.requests))
	}
}

// TestDecideStopsAScheduleWithAnUnknownTimezone is Minor #3's remaining
// gap: the invalid-timetable branch is reached via lerr (LoadLocation),
// not just via an unparsable cron — the cron itself is valid here.
func TestDecideStopsAScheduleWithAnUnknownTimezone(t *testing.T) {
	base := time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)
	w := newWorld(t)
	s := due(t, w, "0 * * * *", base, schedules.Guards{})
	s.Timezone = "Mars/Olympus"
	d := w.svc.Decide(ctx, s, base, grace)
	if d.Run.Outcome != schedules.OutcomeFailed || d.NextRunAt != nil {
		t.Fatalf("decision = %+v", d)
	}
	if len(w.audit.events) != 1 {
		t.Fatalf("an invalid timezone must be audited too: %+v", w.audit.events)
	}
}
