package schedules

import (
	"context"
	"fmt"
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
func (s *Service) Decide(ctx context.Context, sch Schedule, now time.Time, grace time.Duration) Decision {
	run := Run{ID: s.newID(), OrgID: sch.OrgID, ScheduleID: sch.ID, DecidedAt: now}
	if sch.NextRunAt != nil {
		run.DueAt = *sch.NextRunAt
	} else {
		run.DueAt = now
	}
	expr, err := cronexpr.Parse(sch.Cron)
	loc, lerr := time.LoadLocation(sch.Timezone)
	if err != nil || lerr != nil {
		s.log.ErrorContext(ctx, "schedule has an unusable timetable; stopping it", "schedule", sch.ID, "org", sch.OrgID)
		run.Outcome, run.Reason = OutcomeFailed, "the schedule's timetable is invalid; edit and save it"
		return Decision{Run: run}
	}
	next := expr.Next(now, loc)
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
