package schedules_test

import (
	"strings"
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
}
