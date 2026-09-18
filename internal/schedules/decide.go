package schedules

import (
	"context"
	"fmt"
	"runtime/debug"
	"time"

	"github.com/noixdev/pulsix/internal/audit"
	"github.com/noixdev/pulsix/internal/cronexpr"
	"github.com/noixdev/pulsix/internal/db"
)

// maxCountedOccurrences caps the missed-occurrence count of one decision.
const maxCountedOccurrences = 10000

// Decision is what the ticker writes back for one due schedule.
type Decision struct {
	Run       Run
	NextRunAt *time.Time
}

// Decide applies spec §2 steps 2–5 to one due schedule: the late /
// catch-up rule, the overlap guard, the enqueue, and the next occurrence
// computed from now (a long downtime never replays a backlog).
//
// A panic anywhere below — a stored override that no longer reveals, a
// third-party enqueuer's own bug, cronexpr on some edge case, an empty
// jobs slice if that contract ever changes, the auditor itself — is
// recovered into a generic failed Decision instead of crashing the
// ticker's goroutine (fix round 1, Important #1). The recover is
// registered before cronexpr.Parse/Next run (fix round 2 closed a gap: a
// panic there used to run before any recover was registered at all, so it
// escaped Decide entirely and left the row due forever, re-panicking
// every tick). next is only used once nextComputed confirms it was
// actually assigned — a panic inside Next itself leaves it at its zero
// value, and NextRunAt must be nil then, the same as any other schedule
// Decide cannot safely advance past.
func (s *Service) Decide(ctx context.Context, sch Schedule, now time.Time, grace time.Duration) (result Decision) {
	run := Run{ID: s.newID(), OrgID: sch.OrgID, ScheduleID: sch.ID, DecidedAt: now}
	if sch.NextRunAt != nil {
		run.DueAt = *sch.NextRunAt
	} else {
		run.DueAt = now
	}

	var next time.Time
	var nextComputed bool
	defer func() {
		if r := recover(); r != nil {
			s.log.ErrorContext(ctx, "schedule decide panicked; recording a failed run", "schedule", sch.ID, "org", sch.OrgID, "panic", r, "stack", string(debug.Stack()))
			result = Decision{Run: run}
			result.Run.Outcome, result.Run.Reason = OutcomeFailed, "the scheduled run failed unexpectedly (see the manager log)"
			if nextComputed {
				result.NextRunAt = &next
			}
			// auditSafely, not auditRun: if the panic being recovered
			// here came from inside auditRun's own normal-path call
			// below, a persistent fault there would panic again on this
			// call too, and a panic raised by a deferred function is not
			// caught by that function's own recover — it would escape
			// Decide entirely (fix round 2, Minor).
			s.auditSafely(ctx, sch, result.Run)
		}
	}()

	expr, err := cronexpr.Parse(sch.Cron)
	loc, lerr := time.LoadLocation(sch.Timezone)
	if err != nil || lerr != nil {
		s.log.ErrorContext(ctx, "schedule has an unusable timetable; stopping it", "schedule", sch.ID, "org", sch.OrgID)
		run.Outcome, run.Reason = OutcomeFailed, "the schedule's timetable is invalid; edit and save it"
		s.auditRun(ctx, sch, run)
		return Decision{Run: run}
	}
	next = expr.Next(now, loc)
	nextComputed = true
	decision := Decision{Run: run, NextRunAt: &next}

	reason := ""
	if now.Sub(run.DueAt) > grace {
		count, latest := expr.Between(run.DueAt, now, loc, maxCountedOccurrences)
		if count == 0 {
			count, latest = 1, run.DueAt
		}
		switch {
		case now.Sub(latest) <= grace:
			if count > 1 {
				reason = fmt.Sprintf("%d earlier occurrence(s) missed", count-1)
			}
		case sch.Guards.CatchUp.Enabled && now.Sub(latest) <= sch.Guards.CatchUp.window():
			reason = fmt.Sprintf("catch-up of %d missed occurrence(s)", count)
		default:
			decision.Run.Outcome = OutcomeMissed
			decision.Run.Reason = fmt.Sprintf("%d occurrence(s) missed since %s", count, run.DueAt.UTC().Format(time.RFC3339))
			return decision
		}
	}

	if sch.Guards.Overlap != OverlapQueue {
		active, err := s.runs.HasActiveRun(db.WithOrgScope(ctx, sch.OrgID), sch.OrgID, sch.TemplateID)
		if err != nil {
			s.log.ErrorContext(ctx, "schedule overlap check failed", "schedule", sch.ID, "err", err)
			decision.Run.Outcome, decision.Run.Reason = OutcomeFailed, "could not check for an active run (see the manager log)"
			s.auditRun(ctx, sch, decision.Run)
			return decision
		}
		if active {
			decision.Run.Outcome, decision.Run.Reason = OutcomeSkippedOverlap, "a run of this template is still active"
			return decision
		}
	}

	created, err := s.enqueue(ctx, sch, nil)
	if err != nil {
		s.log.ErrorContext(ctx, "scheduled run could not be enqueued", "schedule", sch.ID, "org", sch.OrgID, "err", err)
		decision.Run.Outcome, decision.Run.Reason = OutcomeFailed, failureReason(err)
	} else {
		decision.Run.Outcome, decision.Run.Reason = OutcomeTriggered, reason
		decision.Run.JobGroupID = &created[0].RunGroupID
	}
	s.auditRun(ctx, sch, decision.Run)
	return decision
}

// auditSafely calls auditRun but never lets it panic a second time. It
// exists for Decide's own recover handler: a panic that originated inside
// auditRun itself (the normal-path call after a successful enqueue, or
// the invalid-timetable call above) would otherwise panic again here if
// the underlying fault is persistent, and a panic raised by a deferred
// function is not caught by that function's own recover — it would
// escape Decide entirely, be caught only by processRow's outer recover
// (fix round 1, Important #1), and leave the row without any run written
// back, re-enqueuing on every tick while the auditor keeps panicking (fix
// round 2, Minor).
func (s *Service) auditSafely(ctx context.Context, sch Schedule, run Run) {
	defer func() {
		if r := recover(); r != nil {
			s.log.ErrorContext(ctx, "schedule audit panicked while recording a recovered run", "schedule", sch.ID, "org", sch.OrgID, "panic", r, "stack", string(debug.Stack()))
		}
	}()
	s.auditRun(ctx, sch, run)
}

// auditRun records a triggered or failed scheduled run with actor
// schedule:<id>; the creator and template ride in meta (spec §2 "Actor").
// An audit failure is logged, never fatal: the run already happened.
func (s *Service) auditRun(ctx context.Context, sch Schedule, run Run) {
	meta := map[string]any{
		"schedule_id": sch.ID.String(), "template_id": sch.TemplateID.String(), "outcome": string(run.Outcome),
	}
	if sch.CreatedBy != nil {
		meta["created_by"] = sch.CreatedBy.String()
	}
	if run.JobGroupID != nil {
		meta["job_group_id"] = run.JobGroupID.String()
	}
	orgID, actor := sch.OrgID, sch.ID
	if err := s.audit.Record(db.WithOrgScope(ctx, orgID), audit.Event{
		OrgID: &orgID, ActorKind: audit.ActorKindSchedule, ActorID: &actor,
		Action: "schedule.run", ObjectType: "job_template", ObjectID: sch.TemplateID.String(),
		Summary: fmt.Sprintf("schedule %q %s", sch.Name, run.Outcome), Meta: meta,
	}); err != nil {
		s.log.ErrorContext(ctx, "audit scheduled run", "schedule", sch.ID, "err", err)
	}
}
