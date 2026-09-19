package schedules_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/jobs"
	"github.com/noixdev/pulsix/internal/schedules"
)

func TestMaintenanceGuard(t *testing.T) {
	base := time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)
	hA, hB := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	guards := func(m schedules.Maintenance) schedules.Guards {
		return schedules.Guards{Overlap: schedules.OverlapSkip, CatchUp: schedules.CatchUp{Window: "1h"}, Maintenance: m}
	}
	cases := []struct {
		name     string
		mode     schedules.Maintenance
		inWindow []uuid.UUID
		checkErr error
		outcome  schedules.Outcome
		reason   string
		exclude  []uuid.UUID
		enqueued bool
	}{
		{name: "ignore runs inside a window", mode: schedules.MaintenanceIgnore, inWindow: []uuid.UUID{hA, hB},
			outcome: schedules.OutcomeTriggered, enqueued: true},
		{name: "skip without a window runs", mode: schedules.MaintenanceSkip,
			outcome: schedules.OutcomeTriggered, enqueued: true},
		{name: "skip with one host in a window", mode: schedules.MaintenanceSkip, inWindow: []uuid.UUID{hA},
			outcome: schedules.OutcomeSkippedMaintenance, reason: "1 targeted host(s) in a maintenance window"},
		{name: "exclude_hosts leaves the host out", mode: schedules.MaintenanceExcludeHosts, inWindow: []uuid.UUID{hA},
			outcome: schedules.OutcomeTriggered, reason: "1 host(s) left out for maintenance", exclude: []uuid.UUID{hA}, enqueued: true},
		{name: "exclude_hosts with every host in a window skips", mode: schedules.MaintenanceExcludeHosts, inWindow: []uuid.UUID{hA, hB},
			outcome: schedules.OutcomeSkippedMaintenance, reason: "every targeted host"},
		{name: "a failing check fails closed", mode: schedules.MaintenanceSkip, checkErr: errors.New("db down"),
			outcome: schedules.OutcomeFailed, reason: "could not check maintenance windows"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t)
			m := &fakeMaintenance{inWindow: map[uuid.UUID]bool{}, err: c.checkErr}
			for _, h := range c.inWindow {
				m.inWindow[h] = true
			}
			w.svc.SetMaintenance(m)
			w.runs.targets = []uuid.UUID{hA, hB}
			s := due(t, w, "0 * * * *", base, guards(c.mode))
			d := w.svc.Decide(ctx, s, base.Add(time.Second), grace)
			if d.Run.Outcome != c.outcome || !strings.Contains(d.Run.Reason, c.reason) {
				t.Fatalf("run = %+v, want %s containing %q", d.Run, c.outcome, c.reason)
			}
			if got := len(w.runs.requests) == 1; got != c.enqueued {
				t.Fatalf("enqueued = %v, want %v", got, c.enqueued)
			}
			if c.enqueued && len(c.exclude) > 0 {
				if ex := w.runs.requests[0].ExcludeHosts; len(ex) != 1 || ex[0] != c.exclude[0] {
					t.Fatalf("exclude = %v, want %v", ex, c.exclude)
				}
			}
			if d.NextRunAt == nil {
				t.Fatal("a guarded decision must still advance the schedule")
			}
		})
	}
}

func TestMaintenanceGuardWithoutACheckerFailsClosed(t *testing.T) {
	w := newWorld(t)
	s := due(t, w, "0 * * * *", w.now, schedules.Guards{Overlap: schedules.OverlapSkip, CatchUp: schedules.CatchUp{Window: "1h"}, Maintenance: schedules.MaintenanceSkip})
	if d := w.svc.Decide(ctx, s, w.now, grace); d.Run.Outcome != schedules.OutcomeFailed || len(w.runs.requests) != 0 {
		t.Fatalf("decision = %+v, want failed and no run", d.Run)
	}
}

func TestMaintenanceGuardRaceWithTheEnqueueSkips(t *testing.T) {
	w := newWorld(t)
	w.svc.SetMaintenance(&fakeMaintenance{inWindow: map[uuid.UUID]bool{}})
	w.runs.err = jobs.ErrAllHostsExcluded
	s := due(t, w, "0 * * * *", w.now, schedules.Guards{Overlap: schedules.OverlapSkip, CatchUp: schedules.CatchUp{Window: "1h"}, Maintenance: schedules.MaintenanceExcludeHosts})
	if d := w.svc.Decide(ctx, s, w.now, grace); d.Run.Outcome != schedules.OutcomeSkippedMaintenance {
		t.Fatalf("decision = %+v, want skipped_maintenance", d.Run)
	}
}

func TestMaintenanceGuardTargetsFailureFailsTheRun(t *testing.T) {
	w := newWorld(t)
	w.svc.SetMaintenance(&fakeMaintenance{})
	w.runs.targetsErr = jobs.ErrNotFound
	s := due(t, w, "0 * * * *", w.now, schedules.Guards{Overlap: schedules.OverlapSkip, CatchUp: schedules.CatchUp{Window: "1h"}, Maintenance: schedules.MaintenanceSkip})
	if d := w.svc.Decide(ctx, s, w.now, grace); d.Run.Outcome != schedules.OutcomeFailed {
		t.Fatalf("decision = %+v, want failed", d.Run)
	}
}

func TestMaintenanceGuardValidation(t *testing.T) {
	w := newWorld(t)
	in := validInput(w.templateID)
	s, err := w.svc.Create(ctx, w.org, w.actor, in)
	if err != nil || s.Guards.Maintenance != schedules.MaintenanceIgnore {
		t.Fatalf("default guard = %q, %v", s.Guards.Maintenance, err)
	}
	in.Name, in.Guards.Maintenance = "other", "sometimes"
	if _, err := w.svc.Create(ctx, w.org, w.actor, in); !errors.Is(err, schedules.ErrInvalidInput) {
		t.Fatalf("unknown guard = %v", err)
	}
}
