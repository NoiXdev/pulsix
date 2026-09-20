package schedules

import (
	"context"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/notify"
)

// digestSlowest is how many of the week's slowest runs a digest lists.
const digestSlowest = 5

// Digest summarises [from, to) for the weekly digest (spec §7): per
// schedule the runs it started, its failures (runs that failed or were
// cancelled, and occurrences that could not start) and its skipped or
// missed occurrences; plus the slowest finished runs. It implements
// notify.DigestBuilder. Links are the outbox's business: a run is named by
// its first job.
func (s *Service) Digest(ctx context.Context, orgID uuid.UUID, from, to time.Time) (notify.DigestSummary, error) {
	list, err := s.store.List(ctx, orgID, nil)
	if err != nil {
		return notify.DigestSummary{}, err
	}
	runs, err := s.store.RunsBetween(ctx, orgID, from, to)
	if err != nil {
		return notify.DigestSummary{}, err
	}
	var groups []uuid.UUID
	for _, r := range runs {
		if r.JobGroupID != nil {
			groups = append(groups, *r.JobGroupID)
		}
	}
	summaries, err := s.runs.GroupSummaries(ctx, orgID, groups)
	if err != nil {
		return notify.DigestSummary{}, err
	}
	per := map[uuid.UUID]*notify.DigestSchedule{}
	names := map[uuid.UUID]string{}
	out := notify.DigestSummary{From: from, To: to, Schedules: make([]notify.DigestSchedule, len(list)), Slowest: []notify.DigestRun{}}
	for i, sch := range list {
		out.Schedules[i] = notify.DigestSchedule{ID: sch.ID, Name: sch.Name}
		per[sch.ID], names[sch.ID] = &out.Schedules[i], sch.Name
	}
	for _, r := range runs {
		ds := per[r.ScheduleID]
		if ds == nil {
			continue
		}
		switch r.Outcome {
		case OutcomeTriggered:
			ds.Runs++
			if r.JobGroupID == nil {
				continue
			}
			g, ok := summaries[*r.JobGroupID]
			if !ok {
				continue
			}
			if g.Status == jobs.StatusFailed || g.Status == jobs.StatusCancelled {
				ds.Failures++
			}
			if d := g.Duration(); d > 0 {
				out.Slowest = append(out.Slowest, notify.DigestRun{
					Schedule: names[r.ScheduleID], RunID: g.FirstJobID, StartedAt: *g.StartedAt, DurationSeconds: int64(d.Round(time.Second) / time.Second),
				})
			}
		case OutcomeFailed:
			ds.Failures++
		case OutcomeSkippedOverlap, OutcomeSkippedMaintenance, OutcomeMissed:
			ds.Skipped++
		}
	}
	slices.SortFunc(out.Slowest, func(a, b notify.DigestRun) int { return int(b.DurationSeconds - a.DurationSeconds) })
	if len(out.Slowest) > digestSlowest {
		out.Slowest = out.Slowest[:digestSlowest]
	}
	return out, nil
}
