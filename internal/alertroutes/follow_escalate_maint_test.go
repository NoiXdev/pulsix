// Backlog 4.41: the escalation pass's maintenance anchor (spec docs/
// superpowers/specs/2026-10-16-v0.4d-escalation-chains-design.md,
// decision 5's third bullet) asks whether any maintenance or silence
// occurrence covered the chain at any instant since its own anchor, not
// whether one covered two sampled instants. A window that opens and closes
// strictly between two ticks used to be invisible, and the next rung fired
// from an anchor that was too old.
//
// Not t.Parallel(), for the reason follow_escalate_test.go gives.
package alertroutes_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/alertroutes"
)

// escalationTimeline is a fixture whose clock is pinned to a whole minute,
// so a recurring window's cron can name an exact stretch of it.
var escalationTimeline = time.Date(2026, 1, 5, 10, 0, 0, 0, time.UTC)

// tickAt moves the clock to at and runs one tick.
func (e *followEnv) tickAt(at time.Time) {
	e.t.Helper()
	e.clock.set(at)
	if err := e.follower.Tick(context.Background(), e.org); err != nil {
		e.t.Fatalf("Tick at %s: %v", at.Format(time.TimeOnly), err)
	}
}

// recurringMaintenanceWindow puts the fixture's host into a recurring
// maintenance window: cron in UTC, each stretch lasting duration, created
// at the fixture clock's now.
func (e *followEnv) recurringMaintenanceWindow(cron string, duration time.Duration) uuid.UUID {
	e.t.Helper()
	e.seq++
	id := uuid.Must(uuid.NewV7())
	if _, err := e.d.Pool().Exec(context.Background(), `
		INSERT INTO maintenance_windows (id, org_id, name, target_kind, host_id, cron, duration_seconds, timezone, reason, created_at, updated_at)
		VALUES ($1, $2, $3, 'host', $4, $5, $6, 'UTC', 'maintenance', $7, $7)`,
		id, e.org, fmt.Sprintf("window-%d", e.seq), e.host, cron, int(duration/time.Second), e.clock.Now()); err != nil {
		e.t.Fatalf("create recurring maintenance window: %v", err)
	}
	return id
}

// TestAWindowBetweenTwoTicksDelaysTheNextRung covers a window that opens
// and closes strictly between two consecutive ticks, and between the
// chain's anchor and the moment its next rung would naively come due, so
// no tick ever sees it active and neither of the old sampled instants
// lands inside it. Decision 5 still says the rung gets its full delay from
// the window's end: due at end+5m, not base+5m.
func TestAWindowBetweenTwoTicksDelaysTheNextRung(t *testing.T) {
	for _, tc := range []struct {
		name   string
		window func(env *followEnv, t0 time.Time)
	}{
		{"one-off", func(env *followEnv, t0 time.Time) {
			env.maintenanceWindow(env.host, t0.Add(2*time.Minute), t0.Add(3*time.Minute))
		}},
		{"recurring", func(env *followEnv, _ time.Time) {
			env.recurringMaintenanceWindow("2 10 * * *", time.Minute) // [10:02, 10:03) daily
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newFollowEnv(t)
			t0 := escalationTimeline
			env.clock.set(t0)
			env.openCritAlert()
			route := env.routeWithSteps([]alertroutes.Step{
				{Position: 1, Delay: 5 * time.Minute, ChannelIDs: []uuid.UUID{env.channel}},
			})
			env.tickAt(t0) // base at t0
			env.tickAt(t0.Add(time.Minute))
			tc.window(env, t0) // covers [t0+2m, t0+3m)
			env.tickAt(t0.Add(4 * time.Minute))

			// base+5m: the old anchor's due time. The window ended at
			// t0+3m, so the rung is not due until t0+8m.
			for _, at := range []time.Duration{5 * time.Minute, 7 * time.Minute} {
				env.tickAt(t0.Add(at))
				if n := env.deliveriesTo(route, 1); n != 0 {
					t.Fatalf("rung 1 fired %d time(s) at t0+%s; the window ending at t0+3m gives it until t0+8m", n, at)
				}
			}
			env.tickAt(t0.Add(8 * time.Minute))
			if n := env.deliveriesTo(route, 1); n != 1 {
				t.Fatalf("rung 1 fired %d time(s) at t0+8m, want 1", n)
			}
		})
	}
}

// TestAWindowBetweenRungsDelaysTheSecondRung is the same gap one rung up:
// the anchor is the previous rung's own firing, and a window that came and
// went between two ticks after it moves the next rung's due time.
func TestAWindowBetweenRungsDelaysTheSecondRung(t *testing.T) {
	env := newFollowEnv(t)
	t0 := escalationTimeline
	env.clock.set(t0)
	env.openCritAlert()
	route := env.routeWithSteps([]alertroutes.Step{
		{Position: 1, Delay: 5 * time.Minute, ChannelIDs: []uuid.UUID{env.channel}},
		{Position: 2, Delay: 5 * time.Minute, ChannelIDs: []uuid.UUID{env.channel}},
	})
	env.tickAt(t0)
	env.tickAt(t0.Add(5 * time.Minute)) // rung 1 fires at t0+5m
	if n := env.deliveriesTo(route, 1); n != 1 {
		t.Fatalf("rung 1 fired %d time(s), want 1", n)
	}
	env.maintenanceWindow(env.host, t0.Add(6*time.Minute), t0.Add(7*time.Minute))
	env.tickAt(t0.Add(8 * time.Minute))

	env.tickAt(t0.Add(10 * time.Minute)) // rung 1 + 5m: too early
	if n := env.deliveriesTo(route, 2); n != 0 {
		t.Fatalf("rung 2 fired %d time(s) at t0+10m; the window ending at t0+7m gives it until t0+12m", n)
	}
	env.tickAt(t0.Add(12 * time.Minute))
	if n := env.deliveriesTo(route, 2); n != 1 {
		t.Fatalf("rung 2 fired %d time(s) at t0+12m, want 1", n)
	}
}

// TestAWindowOutsideTheChainsIntervalChangesNothing is the other
// direction: an occurrence that ended before the chain's anchor, one that
// has not started yet, and a recurring window whose stretches all fall
// outside [anchor, now] leave the rung due exactly at base+5m.
func TestAWindowOutsideTheChainsIntervalChangesNothing(t *testing.T) {
	env := newFollowEnv(t)
	t0 := escalationTimeline
	env.clock.set(t0)
	env.maintenanceWindow(env.host, t0.Add(-20*time.Minute), t0.Add(-10*time.Minute)) // before the anchor
	env.maintenanceWindow(env.host, t0.Add(30*time.Minute), t0.Add(40*time.Minute))   // not yet
	env.recurringMaintenanceWindow("50 9,11 * * *", time.Minute)                      // 09:50 and 11:50
	env.openCritAlert()
	route := env.routeWithSteps([]alertroutes.Step{
		{Position: 1, Delay: 5 * time.Minute, ChannelIDs: []uuid.UUID{env.channel}},
	})
	env.tickAt(t0)
	env.tickAt(t0.Add(4 * time.Minute))
	if n := env.deliveriesTo(route, 1); n != 0 {
		t.Fatalf("rung 1 fired %d time(s) before its delay", n)
	}
	env.tickAt(t0.Add(5 * time.Minute))
	if n := env.deliveriesTo(route, 1); n != 1 {
		t.Fatalf("rung 1 fired %d time(s) at base+5m, want 1 — no window covered the chain", n)
	}
}

// TestTheRangeLowerBoundStillSeesAWindowEndingAfterIt pins the fold's
// lower bound, now - delay (review Minor 2). At t0+6m the range is
// [t0+1m, t0+6m], later than the anchor t0, and a window ending at t0+2m
// inside it must still hold the rung until t0+7m; a bound any tighter
// would fire it here. At t0+7m, end+5m exactly, it fires.
func TestTheRangeLowerBoundStillSeesAWindowEndingAfterIt(t *testing.T) {
	env := newFollowEnv(t)
	t0 := escalationTimeline
	env.clock.set(t0)
	env.openCritAlert()
	route := env.routeWithSteps([]alertroutes.Step{
		{Position: 1, Delay: 5 * time.Minute, ChannelIDs: []uuid.UUID{env.channel}},
	})
	env.tickAt(t0)
	env.maintenanceWindow(env.host, t0.Add(30*time.Second), t0.Add(2*time.Minute)) // straddles t0+1m
	env.tickAt(t0.Add(6 * time.Minute))
	if n := env.deliveriesTo(route, 1); n != 0 {
		t.Fatalf("rung 1 fired %d time(s) at t0+6m; the window ending at t0+2m holds it until t0+7m", n)
	}
	env.tickAt(t0.Add(7 * time.Minute)) // end+5m exactly
	if n := env.deliveriesTo(route, 1); n != 1 {
		t.Fatalf("rung 1 fired %d time(s) at end+5m, want 1", n)
	}
}

// TestTheLatestOfSeveralWindowsWins: two windows in the gap, the later-
// ending one created first (review Minor 3). The rung is due at the latest
// end plus its delay, t0+9m, not at the earlier one's t0+7m.
func TestTheLatestOfSeveralWindowsWins(t *testing.T) {
	env := newFollowEnv(t)
	t0 := escalationTimeline
	env.clock.set(t0)
	env.openCritAlert()
	route := env.routeWithSteps([]alertroutes.Step{
		{Position: 1, Delay: 5 * time.Minute, ChannelIDs: []uuid.UUID{env.channel}},
	})
	env.tickAt(t0)
	env.maintenanceWindow(env.host, t0.Add(3*time.Minute), t0.Add(4*time.Minute))
	env.maintenanceWindow(env.host, t0.Add(1*time.Minute), t0.Add(2*time.Minute))
	for _, at := range []time.Duration{5 * time.Minute, 8 * time.Minute} {
		env.tickAt(t0.Add(at))
		if n := env.deliveriesTo(route, 1); n != 0 {
			t.Fatalf("rung 1 fired %d time(s) at t0+%s; the later window ending at t0+4m holds it until t0+9m", n, at)
		}
	}
	env.tickAt(t0.Add(9 * time.Minute))
	if n := env.deliveriesTo(route, 1); n != 1 {
		t.Fatalf("rung 1 fired %d time(s) at t0+9m, want 1", n)
	}
}

// TestAWindowEndingExactlyAtATickHoldsTheRung is the one boundary where
// the mute gate and the fold use different predicates (review Minor 4): a
// window is half-open, so at its end instant it no longer mutes, but that
// end is the anchor and the rung waits a full delay from it.
func TestAWindowEndingExactlyAtATickHoldsTheRung(t *testing.T) {
	env := newFollowEnv(t)
	t0 := escalationTimeline
	env.clock.set(t0)
	env.openCritAlert()
	route := env.routeWithSteps([]alertroutes.Step{
		{Position: 1, Delay: 5 * time.Minute, ChannelIDs: []uuid.UUID{env.channel}},
	})
	env.tickAt(t0)
	env.maintenanceWindow(env.host, t0.Add(3*time.Minute), t0.Add(6*time.Minute))
	for _, at := range []time.Duration{5 * time.Minute, 6 * time.Minute, 10 * time.Minute} {
		env.tickAt(t0.Add(at))
		if n := env.deliveriesTo(route, 1); n != 0 {
			t.Fatalf("rung 1 fired %d time(s) at t0+%s; the window ending at t0+6m holds it until t0+11m", n, at)
		}
	}
	env.tickAt(t0.Add(11 * time.Minute))
	if n := env.deliveriesTo(route, 1); n != 1 {
		t.Fatalf("rung 1 fired %d time(s) at t0+11m, want 1", n)
	}
}

// TestAWindowEnteredAfterTheFactHoldsNothing is the final review's
// Important 1: a stretch that was already over when its window was
// created never muted anything, so it must not restart the rung's delay —
// neither a one-off window entered with past times (recording a reboot
// after it happened) nor a new recurring window whose stretch today has
// already passed. Both are created at t0+4m; the rung fires at base+5m.
func TestAWindowEnteredAfterTheFactHoldsNothing(t *testing.T) {
	for _, tc := range []struct {
		name   string
		window func(env *followEnv, t0 time.Time)
	}{
		{"back-dated one-off", func(env *followEnv, t0 time.Time) {
			env.maintenanceWindow(env.host, t0.Add(time.Minute), t0.Add(2*time.Minute))
		}},
		{"new recurring, today's stretch over", func(env *followEnv, _ time.Time) {
			env.recurringMaintenanceWindow("2 10 * * *", time.Minute) // [10:02, 10:03) daily
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newFollowEnv(t)
			t0 := escalationTimeline
			env.clock.set(t0)
			env.openCritAlert()
			route := env.routeWithSteps([]alertroutes.Step{
				{Position: 1, Delay: 5 * time.Minute, ChannelIDs: []uuid.UUID{env.channel}},
			})
			env.tickAt(t0)
			env.clock.set(t0.Add(4 * time.Minute))
			tc.window(env, t0) // created at t0+4m, after the stretch ended
			env.tickAt(t0.Add(5 * time.Minute))
			if n := env.deliveriesTo(route, 1); n != 1 {
				t.Fatalf("rung 1 fired %d time(s) at base+5m, want 1 — the window was created after its stretch ended", n)
			}
		})
	}
}
