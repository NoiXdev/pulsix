package schedules_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/notify"
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
	// Below the cap nothing is truncated (backlog 2.84).
	if sum.Truncated || sum.ScheduleCountTotal != 0 {
		t.Fatalf("an org under the cap must not be marked truncated: %+v", sum)
	}
}

// TestDigestCapsTheScheduleList is backlog 2.84: an organization with more
// schedules than notify.MaxDigestSchedules gets a capped list — schedules
// with runs in the period first (most runs first, then by name), then the
// rest by name — and a Truncated + ScheduleCountTotal that let the
// renderers say what was left out. The full list at ~110 bytes of JSON per
// schedule would push a large org's webhook body past maxBodyBytes, whose
// errBodyTooLarge the sender judges permanent: the digest would never
// arrive.
func TestDigestCapsTheScheduleList(t *testing.T) {
	w := newWorld(t)
	const total = notify.MaxDigestSchedules + 3
	from := time.Date(2026, 9, 21, 8, 0, 0, 0, time.UTC)
	to := from.AddDate(0, 0, 7)
	w.runs.summaries = map[uuid.UUID]jobs.GroupSummary{}

	// Two busy schedules (runs recorded in the week), the rest idle. The
	// busy one named "busy-b" has more runs than "busy-a", so the capped
	// head must order busy-b before busy-a — runs first, runs desc.
	ids := map[string]uuid.UUID{}
	for i := 0; i < total; i++ {
		in := validInput(w.templateID)
		in.Name = fmt.Sprintf("idle-%03d", i)
		if i == total-1 {
			in.Name = "busy-a"
		}
		if i == total-2 {
			in.Name = "busy-b"
		}
		s, err := w.svc.Create(ctx, w.org, w.actor, in)
		if err != nil {
			t.Fatal(err)
		}
		ids[s.Name] = s.ID
	}
	record := func(name string, n int) {
		t.Helper()
		for i := 0; i < n; i++ {
			due := from.Add(time.Duration(i) * time.Hour)
			if err := w.store.RecordRun(ctx, schedules.Run{ID: uuid.Must(uuid.NewV7()), OrgID: w.org, ScheduleID: ids[name], DueAt: due, DecidedAt: due, Outcome: schedules.OutcomeTriggered}); err != nil {
				t.Fatal(err)
			}
		}
	}
	record("busy-a", 3)
	record("busy-b", 5)

	sum, err := w.svc.Digest(ctx, w.org, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if len(sum.Schedules) != notify.MaxDigestSchedules {
		t.Fatalf("capped list = %d entries, want %d", len(sum.Schedules), notify.MaxDigestSchedules)
	}
	if !sum.Truncated || sum.ScheduleCountTotal != total {
		t.Fatalf("summary = truncated %v, total %d; want true, %d", sum.Truncated, sum.ScheduleCountTotal, total)
	}
	// Runs first, most runs first, then name: busy-b (5) beats busy-a (3),
	// which beats every idle schedule ("idle-000" … comes after by name).
	if sum.Schedules[0].ID != ids["busy-b"] || sum.Schedules[1].ID != ids["busy-a"] {
		t.Fatalf("the schedules with runs must lead, most runs first: %q then %q", sum.Schedules[0].Name, sum.Schedules[1].Name)
	}
	if sum.Schedules[2].Name != "idle-000" || sum.Schedules[notify.MaxDigestSchedules-1].Name != "idle-197" {
		t.Fatalf("the rest must follow by name: first %q, last %q", sum.Schedules[2].Name, sum.Schedules[notify.MaxDigestSchedules-1].Name)
	}
	// Per-schedule counts must still be filled for the entries kept.
	if sum.Schedules[0].Runs != 5 || sum.Schedules[1].Runs != 3 {
		t.Fatalf("kept entries lost their run counts: %+v, %+v", sum.Schedules[0], sum.Schedules[1])
	}
}
