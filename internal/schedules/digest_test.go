package schedules_test

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/schedules"
)

func TestDigestSummarisesTheWeek(t *testing.T) {
	w := newWorld(t)
	a, err := w.svc.Create(ctx, w.org, w.actor, validInput(w.templateID))
	if err != nil {
		t.Fatal(err)
	}
	in := validInput(w.templateID)
	in.Name = "Quiet"
	quiet, err := w.svc.Create(ctx, w.org, w.actor, in)
	if err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, 9, 21, 8, 0, 0, 0, time.UTC)
	to := from.AddDate(0, 0, 7)
	record := func(outcome schedules.Outcome, due time.Time, group *uuid.UUID) {
		t.Helper()
		if err := w.store.RecordRun(ctx, schedules.Run{ID: uuid.Must(uuid.NewV7()), OrgID: w.org, ScheduleID: a.ID, DueAt: due, DecidedAt: due, Outcome: outcome, JobGroupID: group}); err != nil {
			t.Fatal(err)
		}
	}
	w.runs.summaries = map[uuid.UUID]jobs.GroupSummary{}
	var slow uuid.UUID
	for i, d := range []time.Duration{2 * time.Minute, 9 * time.Minute, 4 * time.Minute, 1 * time.Minute, 7 * time.Minute, 3 * time.Minute} {
		g := uuid.Must(uuid.NewV7())
		start := from.Add(time.Duration(i) * time.Hour)
		end := start.Add(d)
		status := jobs.StatusSucceeded
		if i == 1 {
			status, slow = jobs.StatusFailed, g
		}
		w.runs.summaries[g] = jobs.GroupSummary{GroupID: g, FirstJobID: g, Status: status, StartedAt: &start, FinishedAt: &end}
		record(schedules.OutcomeTriggered, start, &g)
	}
	record(schedules.OutcomeSkippedMaintenance, from.Add(10*time.Hour), nil)
	record(schedules.OutcomeMissed, from.Add(11*time.Hour), nil)
	record(schedules.OutcomeFailed, from.Add(12*time.Hour), nil)
	record(schedules.OutcomeTriggered, to.Add(time.Hour), nil) // outside the week

	sum, err := w.svc.Digest(ctx, w.org, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if len(sum.Schedules) != 2 || !sum.From.Equal(from) || !sum.To.Equal(to) {
		t.Fatalf("summary = %+v", sum)
	}
	var got, idle = sum.Schedules[0], sum.Schedules[1]
	if got.ID != a.ID {
		got, idle = idle, got
	}
	if got.Runs != 6 || got.Failures != 2 || got.Skipped != 2 {
		t.Fatalf("schedule = %+v, want 6 runs, 2 failures (a failed run, a failed start), 2 skipped", got)
	}
	if idle.ID != quiet.ID || idle.Runs != 0 {
		t.Fatalf("idle schedule = %+v", idle)
	}
	if len(sum.Slowest) != 5 || sum.Slowest[0].RunID != slow || sum.Slowest[0].DurationSeconds != 540 || sum.Slowest[4].DurationSeconds != 120 {
		t.Fatalf("slowest = %+v", sum.Slowest)
	}
}
