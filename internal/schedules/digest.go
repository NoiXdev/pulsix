package schedules

import (
	"cmp"
	"context"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/notify"
)

// digestSlowest is how many of the week's slowest runs a digest lists.
const digestSlowest = 5

// maxDigestSchedules caps the summary's schedule list at
// notify.MaxDigestSchedules. It aliases the notify constant because the
// cap and the body bound (maxBodyBytes) protect the same message: every
// entry costs roughly 110 bytes of JSON, so past about 600 schedules a
// default webhook body crosses maxBodyBytes and the delivery is judged
// permanent — a large organization's digest would never arrive (backlog
// 2.84).
const maxDigestSchedules = notify.MaxDigestSchedules

// Digest summarises [from, to) for the weekly digest (spec §7): per
// schedule the runs it started, its failures (runs that failed or were
// cancelled, and occurrences that could not start) and its skipped or
// missed occurrences; plus the slowest finished runs. It implements
// notify.DigestBuilder. Links are the outbox's business: a run is named by
// its first job.
//
// The schedule list is capped (backlog 2.84): schedules with runs in the
// period come first — most runs first, then by name — and the rest follow
// by name, truncated to the cap. The summary records the true count in
// ScheduleCountTotal and sets Truncated, so both renderers can say "+N
// schedules without activity"; a capped digest renders instead of failing
// the delivery for ever. maxBodyBytes stays the backstop.
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
	slices.SortStableFunc(out.Slowest, func(a, b notify.DigestRun) int { return cmp.Compare(b.DurationSeconds, a.DurationSeconds) })
	if len(out.Slowest) > digestSlowest {
		out.Slowest = out.Slowest[:digestSlowest]
	}
	capDigestSchedules(&out)
	return out, nil
}

// capDigestSchedules applies the digest list cap to a fully populated
// summary: schedules with runs first (most runs first, then name), the
// idle ones after (name), truncated to maxDigestSchedules. Truncating
// only at the end — after the counts are filled — is what keeps the
// kept entries' numbers right, and the ordering is what makes the
// renderers' note true: what falls off the end is the week's least
// active schedules, ordinarily those without runs in the period.
func capDigestSchedules(out *notify.DigestSummary) {
	if len(out.Schedules) <= maxDigestSchedules {
		return
	}
	slices.SortStableFunc(out.Schedules, func(a, b notify.DigestSchedule) int {
		if c := cmp.Compare(b.Runs, a.Runs); c != 0 {
			return c
		}
		return cmp.Compare(a.Name, b.Name)
	})
	out.ScheduleCountTotal = len(out.Schedules)
	out.Schedules = out.Schedules[:maxDigestSchedules]
	out.Truncated = true
}
