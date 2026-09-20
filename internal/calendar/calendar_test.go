package calendar_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/calendar"
	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/maintenance"
	"github.com/noixdev/pulsix/internal/schedules"
)

var ctx = context.Background()

type fakeSchedules struct {
	list    []schedules.Schedule
	runs    []schedules.Run
	listErr error
	runsErr error
}

func (f fakeSchedules) List(context.Context, uuid.UUID, *uuid.UUID) ([]schedules.Schedule, map[uuid.UUID]schedules.Run, error) {
	return f.list, nil, f.listErr
}

func (f fakeSchedules) RunsBetween(_ context.Context, _ uuid.UUID, from, to time.Time) ([]schedules.Run, error) {
	var out []schedules.Run
	for _, r := range f.runs {
		if !r.DueAt.Before(from) && r.DueAt.Before(to) {
			out = append(out, r)
		}
	}
	return out, f.runsErr
}

type fakeGroups struct {
	sums map[uuid.UUID]jobs.GroupSummary
	err  error
}

func (f fakeGroups) GroupSummaries(context.Context, uuid.UUID, []uuid.UUID) (map[uuid.UUID]jobs.GroupSummary, error) {
	return f.sums, f.err
}

type fakeWindows struct {
	occ []maintenance.Occurrence
	err error
}

func (f fakeWindows) OccurrencesBetween(context.Context, uuid.UUID, time.Time, time.Time) ([]maintenance.Occurrence, error) {
	return f.occ, f.err
}

func TestRangeCombinesPastPlannedAndMaintenance(t *testing.T) {
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC) // Wednesday noon
	from, to := time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC), time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	nightly := schedules.Schedule{ID: uuid.Must(uuid.NewV7()), Name: "Nightly", TemplateID: uuid.Must(uuid.NewV7()), Cron: "0 3 * * *", Timezone: "UTC"}
	paused := schedules.Schedule{ID: uuid.Must(uuid.NewV7()), Name: "Paused", Cron: "0 * * * *", Timezone: "UTC", Paused: true}
	group := uuid.Must(uuid.NewV7())
	firstJob := uuid.Must(uuid.NewV7())
	src := fakeSchedules{
		list: []schedules.Schedule{nightly, paused},
		runs: []schedules.Run{
			{ScheduleID: nightly.ID, DueAt: time.Date(2026, 9, 22, 3, 0, 0, 0, time.UTC), Outcome: schedules.OutcomeTriggered, JobGroupID: &group},
			{ScheduleID: nightly.ID, DueAt: time.Date(2026, 9, 23, 3, 0, 0, 0, time.UTC), Outcome: schedules.OutcomeSkippedMaintenance, Reason: "in a window"},
			{ScheduleID: uuid.Must(uuid.NewV7()), DueAt: time.Date(2026, 9, 23, 4, 0, 0, 0, time.UTC), Outcome: schedules.OutcomeTriggered}, // deleted schedule
		},
	}
	window := maintenance.Occurrence{Name: "db upgrade", Start: time.Date(2026, 9, 23, 2, 0, 0, 0, time.UTC), End: time.Date(2026, 9, 23, 4, 0, 0, 0, time.UTC)}
	svc := calendar.NewService(src, fakeGroups{sums: map[uuid.UUID]jobs.GroupSummary{group: {GroupID: group, FirstJobID: firstJob, Status: jobs.StatusFailed}}},
		fakeWindows{occ: []maintenance.Occurrence{window}}, func() time.Time { return now })
	v, err := svc.Range(ctx, uuid.Must(uuid.NewV7()), from, to)
	if err != nil {
		t.Fatal(err)
	}
	// 2 past decisions + planned 24th, 25th, 26th and 27th at 03:00.
	if len(v.Entries) != 6 || v.Truncated || len(v.Maintenance) != 1 {
		t.Fatalf("view = %+v", v)
	}
	past := v.Entries[0]
	if past.State != "triggered" || past.JobID == nil || *past.JobID != firstJob || past.JobStatus != "failed" || past.ScheduleName != "Nightly" {
		t.Fatalf("past = %+v", past)
	}
	if v.Entries[1].State != "skipped_maintenance" || v.Entries[1].Reason != "in a window" {
		t.Fatalf("skipped = %+v", v.Entries[1])
	}
	if p := v.Entries[2]; p.State != calendar.StatePlanned || !p.At.Equal(time.Date(2026, 9, 24, 3, 0, 0, 0, time.UTC)) || p.TemplateID != nightly.TemplateID {
		t.Fatalf("planned = %+v", p)
	}
}

func TestRangeIncludesAnOccurrenceExactlyAtFrom(t *testing.T) {
	now := time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)
	from := time.Date(2026, 9, 22, 3, 0, 0, 0, time.UTC)
	sch := schedules.Schedule{ID: uuid.Must(uuid.NewV7()), Name: "Nightly", Cron: "0 3 * * *", Timezone: "UTC"}
	svc := calendar.NewService(fakeSchedules{list: []schedules.Schedule{sch}}, fakeGroups{}, fakeWindows{}, func() time.Time { return now })
	v, err := svc.Range(ctx, uuid.Must(uuid.NewV7()), from, from.Add(24*time.Hour))
	if err != nil || len(v.Entries) != 1 || !v.Entries[0].At.Equal(from) {
		t.Fatalf("entries = %+v, %v", v.Entries, err)
	}
}

func TestRangeTruncatesFrequentSchedules(t *testing.T) {
	now := time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)
	every := schedules.Schedule{ID: uuid.Must(uuid.NewV7()), Name: "Every minute", Cron: "* * * * *", Timezone: "UTC"}
	broken := schedules.Schedule{ID: uuid.Must(uuid.NewV7()), Name: "Broken", Cron: "nope", Timezone: "UTC"}
	badZone := schedules.Schedule{ID: uuid.Must(uuid.NewV7()), Name: "Bad zone", Cron: "@daily", Timezone: "Mars/Olympus"}
	svc := calendar.NewService(fakeSchedules{list: []schedules.Schedule{every, broken, badZone}}, fakeGroups{}, fakeWindows{}, func() time.Time { return now })
	v, err := svc.Range(ctx, uuid.Must(uuid.NewV7()), now, now.Add(7*24*time.Hour))
	if err != nil || len(v.Entries) != 500 || !v.Truncated {
		t.Fatalf("entries = %d, truncated %v, %v", len(v.Entries), v.Truncated, err)
	}
	many := make([]schedules.Schedule, 12)
	for i := range many {
		many[i] = schedules.Schedule{ID: uuid.Must(uuid.NewV7()), Name: "m", Cron: "* * * * *", Timezone: "UTC"}
	}
	v, err = calendar.NewService(fakeSchedules{list: many}, fakeGroups{}, fakeWindows{}, func() time.Time { return now }).Range(ctx, uuid.Must(uuid.NewV7()), now, now.Add(7*24*time.Hour))
	if err != nil || len(v.Entries) != 5000 || !v.Truncated {
		t.Fatalf("entries = %d, truncated %v, %v", len(v.Entries), v.Truncated, err)
	}
}

func TestRangeValidationAndErrors(t *testing.T) {
	now := time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)
	ok := calendar.NewService(fakeSchedules{}, fakeGroups{}, fakeWindows{}, nil)
	for _, r := range [][2]time.Time{{now, now}, {now, now.Add(-time.Hour)}, {now, now.Add(43 * 24 * time.Hour)}} {
		if _, err := ok.Range(ctx, uuid.Must(uuid.NewV7()), r[0], r[1]); !errors.Is(err, calendar.ErrInvalidRange) {
			t.Errorf("range %v = %v", r, err)
		}
	}
	boom := errors.New("db down")
	for name, svc := range map[string]*calendar.Service{
		"list":    calendar.NewService(fakeSchedules{listErr: boom}, fakeGroups{}, fakeWindows{}, nil),
		"runs":    calendar.NewService(fakeSchedules{runsErr: boom}, fakeGroups{}, fakeWindows{}, nil),
		"groups":  calendar.NewService(fakeSchedules{}, fakeGroups{err: boom}, fakeWindows{}, nil),
		"windows": calendar.NewService(fakeSchedules{}, fakeGroups{}, fakeWindows{err: boom}, nil),
	} {
		if _, err := svc.Range(ctx, uuid.Must(uuid.NewV7()), now, now.Add(time.Hour)); !errors.Is(err, boom) {
			t.Errorf("%s failure = %v", name, err)
		}
	}
}

// TestRangeTruncatesMaintenance covers the maxMaintenance cap (review fix
// round 1, Minor 1): maintenance.Service.OccurrencesBetween only bounds
// each window's own contribution (500), so an org with many windows could
// otherwise make the calendar's maintenance list unbounded even though
// Range's own schedule-entry caps (maxPerSchedule, maxEntries) do not
// apply to it.
func TestRangeTruncatesMaintenance(t *testing.T) {
	now := time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)
	occ := make([]maintenance.Occurrence, 5001)
	for i := range occ {
		occ[i] = maintenance.Occurrence{Name: "w", Start: now, End: now.Add(time.Hour)}
	}
	svc := calendar.NewService(fakeSchedules{}, fakeGroups{}, fakeWindows{occ: occ}, func() time.Time { return now })
	v, err := svc.Range(ctx, uuid.Must(uuid.NewV7()), now, now.Add(24*time.Hour))
	if err != nil || len(v.Maintenance) != 5000 || !v.Truncated {
		t.Fatalf("maintenance = %d, truncated %v, %v", len(v.Maintenance), v.Truncated, err)
	}
}

// TestRangeAcceptsSixCalendarWeeksAcrossADSTTransition is the final
// review's Critical #1. The SPA's month view asks for the six week rows it
// draws: 42 *calendar* days, built with setDate. Their absolute length is
// not fixed — a span containing an autumn fall-back transition is 1009 h,
// one containing a spring-forward 1007 h — so an absolute 42x24 h bound
// refused exactly the months a DST-zone operator needs (Europe/Berlin and
// America/New_York, October 2026; America/New_York, November 2026), and
// the SPA drew the resulting 400 as a silently empty grid.
//
// The boundary is pinned from both sides: 42 calendar days across a
// fall-back is accepted, one nanosecond more than MaxRange is not.
func TestRangeAcceptsSixCalendarWeeksAcrossADSTTransition(t *testing.T) {
	now := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	svc := calendar.NewService(fakeSchedules{}, fakeGroups{}, fakeWindows{}, func() time.Time { return now })
	for _, zone := range []string{"Europe/Berlin", "America/New_York"} {
		loc, err := time.LoadLocation(zone)
		if err != nil {
			t.Fatal(err)
		}
		for _, first := range []time.Time{
			time.Date(2026, 9, 28, 0, 0, 0, 0, loc),  // the October grid
			time.Date(2026, 10, 26, 0, 0, 0, 0, loc), // the November grid
		} {
			last := first.AddDate(0, 0, 42) // what addDays(from, 42) builds
			if _, err := svc.Range(ctx, uuid.Must(uuid.NewV7()), first, last); err != nil {
				t.Errorf("%s %s: 42 calendar days (%s) = %v", zone, first.Format("Jan 2006"), last.Sub(first), err)
			}
		}
	}
	if _, err := svc.Range(ctx, uuid.Must(uuid.NewV7()), now, now.Add(calendar.MaxRange)); err != nil {
		t.Errorf("exactly MaxRange = %v", err)
	}
	if _, err := svc.Range(ctx, uuid.Must(uuid.NewV7()), now, now.Add(calendar.MaxRange+1)); !errors.Is(err, calendar.ErrInvalidRange) {
		t.Errorf("one nanosecond past MaxRange = %v", err)
	}
}
