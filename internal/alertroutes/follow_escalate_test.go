// The follower's second pass (v0.4d, spec docs/superpowers/specs/
// 2026-10-16-v0.4d-escalation-chains-design.md): escalatePass itself
// (follow_escalate.go), and claimBases, the one addition to the forward
// pass that makes a chain possible at all (follow.go). Every case drives
// the fixture's own injected clock; none sleeps.
//
// Not t.Parallel(): these share one database container with every other
// test in this package, and a sibling's teardown can truncate mid-test —
// the same reason follow_test.go and follow_window_test.go stay serial
// too. Only the pure, no-database tests in this package run in parallel.
package alertroutes_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/alertroutes"
	"github.com/noixdev/pulsix/internal/alerts"
	"github.com/noixdev/pulsix/internal/rules"
)

// TestTheFirstDeliveryRecordsTheBase is Step 1 of the task: without a
// position-0 row, ChainsNeedingAttention can never return a chain, so
// nothing else in this file can work until this one does.
func TestTheFirstDeliveryRecordsTheBase(t *testing.T) {
	env := newFollowEnv(t)
	alert := env.openCritAlert()
	route := env.routeWithSteps([]alertroutes.Step{
		{Position: 1, Delay: 5 * time.Minute, ChannelIDs: []uuid.UUID{env.channel}},
	})

	if err := env.follower.Tick(context.Background(), env.org); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	base, ok := env.ledgerRow(alert, route, 0)
	if !ok {
		t.Fatal("the first delivery recorded no base; no chain can ever start")
	}
	t0 := env.clock.Now()
	if !base.Equal(t0) {
		t.Fatalf("base = %s, want %s", base, t0)
	}

	// A second delivery on the same pair must not move it.
	env.clock.advance(time.Minute)
	env.raiseSeverity(alert)
	if err := env.follower.Tick(context.Background(), env.org); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	again, _ := env.ledgerRow(alert, route, 0)
	if !again.Equal(t0) {
		t.Fatalf("base moved to %s on a later delivery", again)
	}
}

// TestAMutedAlertPausesAndResumesWithoutABurst is spec decision 5, and the
// reason delays are relative: a chain that pauses and resumes must give
// the next rung its whole delay rather than firing the moment the mute
// lifts.
func TestAMutedAlertPausesAndResumesWithoutABurst(t *testing.T) {
	env := newFollowEnv(t)
	alert := env.openCritAlert()
	route := env.routeWithSteps([]alertroutes.Step{
		{Position: 1, Delay: 5 * time.Minute, ChannelIDs: []uuid.UUID{env.channel}},
		{Position: 2, Delay: 5 * time.Minute, ChannelIDs: []uuid.UUID{env.channel}},
	})
	if err := env.follower.Tick(context.Background(), env.org); err != nil { // base at t0
		t.Fatalf("Tick: %v", err)
	}

	// Snoozed one minute in, for ten minutes — well short of either rung's
	// own 5-minute delay measured from the base, so if the chain anchored
	// on the stale base instead of the mute it would already have fired
	// by the time the snooze even ends.
	env.clock.advance(time.Minute)
	env.snooze(alert, env.clock.Now().Add(10*time.Minute))
	// One minute after the snooze ends: short of rung 1's own full delay
	// measured from THAT moment (which the snooze also already outlasted
	// the base-relative due time by), so still not due either way.
	env.clock.advance(11 * time.Minute)
	if err := env.follower.Tick(context.Background(), env.org); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if n := env.deliveriesTo(route, 1); n != 0 {
		t.Fatalf("rung 1 fired %d time(s) the instant the snooze lapsed; it should wait out its delay", n)
	}
	// Five minutes after the snooze ended, not five minutes after the base.
	env.clock.advance(4 * time.Minute)
	if err := env.follower.Tick(context.Background(), env.org); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if n := env.deliveriesTo(route, 1); n != 1 {
		t.Fatalf("rung 1 fired %d time(s), want 1", n)
	}
	if n := env.deliveriesTo(route, 2); n != 0 {
		t.Fatalf("rung 2 fired %d time(s) in the same tick; a ladder climbs one rung at a time", n)
	}
}

// TestAStepReachingNobodyStillFires is Review Focus 2: a rung whose
// channels are gone must not wedge the ladder behind it. v0.4b already
// decided a route that reaches nobody survives and says so; a rung does
// the same.
func TestAStepReachingNobodyStillFires(t *testing.T) {
	env := newFollowEnv(t)
	alert := env.openCritAlert()
	ch := env.secondChannel()
	route := env.routeWithSteps([]alertroutes.Step{
		{Position: 1, Delay: 5 * time.Minute, ChannelIDs: []uuid.UUID{ch}},
		{Position: 2, Delay: 5 * time.Minute, ChannelIDs: []uuid.UUID{env.secondChannel()}},
	})
	if err := env.follower.Tick(context.Background(), env.org); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	env.deleteChannel(ch) // rung 1 can now reach nobody

	env.clock.advance(5 * time.Minute)
	if err := env.follower.Tick(context.Background(), env.org); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if _, ok := env.ledgerRow(alert, route, 1); !ok {
		t.Fatal("rung 1 reached nobody and was not recorded; the ladder is stuck on it for ever")
	}

	env.clock.advance(5 * time.Minute)
	if err := env.follower.Tick(context.Background(), env.org); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if n := env.deliveriesTo(route, 2); n != 1 {
		t.Fatalf("rung 2 fired %d time(s); an unreachable rung must not block the one behind it", n)
	}
}

// TestEscalationFiresOneRungAtATime is the acceptance test's own first
// sentence: a crit that nobody acknowledges reaches step 1 after its
// delay, step 2 after its own, each one once.
func TestEscalationFiresOneRungAtATime(t *testing.T) {
	env := newFollowEnv(t)
	env.openCritAlert()
	route := env.routeWithSteps([]alertroutes.Step{
		{Position: 1, Delay: 5 * time.Minute, ChannelIDs: []uuid.UUID{env.channel}},
		{Position: 2, Delay: 5 * time.Minute, ChannelIDs: []uuid.UUID{env.channel}},
	})
	if err := env.follower.Tick(context.Background(), env.org); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	env.clock.advance(5 * time.Minute)
	if err := env.follower.Tick(context.Background(), env.org); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if n := env.deliveriesTo(route, 1); n != 1 {
		t.Fatalf("rung 1 fired %d time(s), want 1", n)
	}
	if n := env.deliveriesTo(route, 2); n != 0 {
		t.Fatalf("rung 2 fired %d time(s) before its own delay elapsed, want 0", n)
	}

	env.clock.advance(5 * time.Minute)
	if err := env.follower.Tick(context.Background(), env.org); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if n := env.deliveriesTo(route, 2); n != 1 {
		t.Fatalf("rung 2 fired %d time(s), want 1", n)
	}

	// Ticking again must not re-fire either rung: ClaimStep's own
	// ON CONFLICT DO NOTHING is the idempotency, proven end to end.
	env.clock.advance(time.Hour)
	if err := env.follower.Tick(context.Background(), env.org); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if n := env.deliveriesTo(route, 1); n != 1 {
		t.Errorf("rung 1 fired %d time(s) after a later tick, want still 1", n)
	}
	if n := env.deliveriesTo(route, 2); n != 1 {
		t.Errorf("rung 2 fired %d time(s) after a later tick, want still 1", n)
	}
}

// TestAcknowledgingBetweenRungsStopsTheChain is decision 4's first row:
// acknowledged is final, not a pause.
func TestAcknowledgingBetweenRungsStopsTheChain(t *testing.T) {
	env := newFollowEnv(t)
	alert := env.openCritAlert()
	route := env.routeWithSteps([]alertroutes.Step{
		{Position: 1, Delay: 5 * time.Minute, ChannelIDs: []uuid.UUID{env.channel}},
		{Position: 2, Delay: 5 * time.Minute, ChannelIDs: []uuid.UUID{env.channel}},
	})
	if err := env.follower.Tick(context.Background(), env.org); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	env.clock.advance(5 * time.Minute)
	if err := env.follower.Tick(context.Background(), env.org); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if n := env.deliveriesTo(route, 1); n != 1 {
		t.Fatalf("rung 1 fired %d time(s), want 1", n)
	}

	env.acknowledge(alert)
	env.clock.advance(5 * time.Minute)
	if err := env.follower.Tick(context.Background(), env.org); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if n := env.deliveriesTo(route, 2); n != 0 {
		t.Fatalf("rung 2 fired %d time(s) after an acknowledgement, want 0", n)
	}

	// Tick again, well past rung 2's delay, to prove this is stopped for
	// good rather than merely not yet due.
	env.clock.advance(time.Hour)
	if err := env.follower.Tick(context.Background(), env.org); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if n := env.deliveriesTo(route, 2); n != 0 {
		t.Errorf("rung 2 fired %d time(s) long after the acknowledgement, want 0", n)
	}
}

// TestAClosedAlertNeverEscalates is Review Focus 4 and decision 4's second
// row. It ticks once more after the alert closed and the follower has
// re-read its working set, not only immediately: the point is that the
// chain is silent for good, not merely not-yet-due.
func TestAClosedAlertNeverEscalates(t *testing.T) {
	env := newFollowEnv(t)
	alert := env.openCritAlert()
	route := env.routeWithSteps([]alertroutes.Step{
		{Position: 1, Delay: 5 * time.Minute, ChannelIDs: []uuid.UUID{env.channel}},
	})
	if err := env.follower.Tick(context.Background(), env.org); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	env.closeAlert(alert)
	env.clock.advance(5 * time.Minute)
	if err := env.follower.Tick(context.Background(), env.org); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if n := env.deliveriesTo(route, 1); n != 0 {
		t.Fatalf("rung 1 fired %d time(s) on a closed alert, want 0", n)
	}

	// Once more, well past the delay, with the follower's working set
	// re-read from scratch: a closed alert is excluded by
	// ChainsNeedingAttentionBase's own `closed_at IS NULL` filter, so it
	// cannot reappear no matter how long afterward this runs.
	env.clock.advance(time.Hour)
	if err := env.follower.Tick(context.Background(), env.org); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if n := env.deliveriesTo(route, 1); n != 0 {
		t.Errorf("rung 1 fired %d time(s) on a closed alert after a later tick, want 0", n)
	}
}

// TestAnExpiringSnoozeResumesTheChain is the case that writes no event: a
// bounded snooze that simply runs out, rather than being cleared by a
// person (spec decision 5's own closing note — Unsnooze writes 'unsnoozed',
// but an expiry writes nothing at all).
func TestAnExpiringSnoozeResumesTheChain(t *testing.T) {
	env := newFollowEnv(t)
	alert := env.openCritAlert()
	route := env.routeWithSteps([]alertroutes.Step{
		{Position: 1, Delay: 5 * time.Minute, ChannelIDs: []uuid.UUID{env.channel}},
	})
	if err := env.follower.Tick(context.Background(), env.org); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	env.clock.advance(time.Minute)
	env.snooze(alert, env.clock.Now().Add(10*time.Minute))
	// One minute after the snooze runs out on its own — short of rung 1's
	// own full delay measured from that moment.
	env.clock.advance(11 * time.Minute)
	if err := env.follower.Tick(context.Background(), env.org); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if n := env.deliveriesTo(route, 1); n != 0 {
		t.Fatalf("rung 1 fired %d time(s) the instant the snooze expired; it should wait out its delay", n)
	}

	env.clock.advance(4 * time.Minute) // five minutes after the snooze ended
	if err := env.follower.Tick(context.Background(), env.org); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if n := env.deliveriesTo(route, 1); n != 1 {
		t.Fatalf("rung 1 fired %d time(s) once the chain resumed, want 1", n)
	}
}

// TestABreakthroughSeverityStartsTheClockAtOpen is spec decision 3: a
// breakthrough severity crosses a shut window on delivery, so its base is
// the moment it actually went out, not the window's next opening.
func TestABreakthroughSeverityStartsTheClockAtOpen(t *testing.T) {
	env := newFollowEnv(t)
	route := env.createRoute(alertroutes.Input{
		Enabled: true, Scope: alertroutes.ScopeOrg,
		Severities:   []rules.State{rules.StateCrit},
		Events:       []alerts.EventKind{alerts.EventOpened},
		Channels:     []uuid.UUID{env.channel},
		Window:       businessHours(),
		Breakthrough: []rules.State{rules.StateCrit},
		Steps: []alertroutes.Step{
			{Position: 1, Delay: 5 * time.Minute, ChannelIDs: []uuid.UUID{env.channel}},
		},
	})
	env.clock.set(time.Date(2026, 1, 5, 3, 0, 0, 0, time.UTC)) // outside business hours
	alert := env.openCritAlert()

	if err := env.follower.Tick(context.Background(), env.org); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	t0 := env.clock.Now()
	base, ok := env.ledgerRow(alert, route, 0)
	if !ok {
		t.Fatal("a breakthrough severity did not record a base at all")
	}
	if !base.Equal(t0) {
		t.Fatalf("base = %s, want %s — the window must not have delayed it", base, t0)
	}

	env.clock.advance(5 * time.Minute) // still 03:05, still outside the window
	if err := env.follower.Tick(context.Background(), env.org); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if n := env.deliveriesTo(route, 1); n != 1 {
		t.Fatalf("rung 1 fired %d time(s) 5 minutes after the breakthrough base, want 1", n)
	}
}

// TestAShutWindowStartsTheClockWhenItOpens is decision 3's other half: a
// non-breakthrough severity waits for the window, so its base is the
// opening, not the moment it was raised.
func TestAShutWindowStartsTheClockWhenItOpens(t *testing.T) {
	env := newFollowEnv(t)
	route := env.createRoute(alertroutes.Input{
		Enabled: true, Scope: alertroutes.ScopeOrg,
		Severities: []rules.State{rules.StateCrit},
		Events:     []alerts.EventKind{alerts.EventOpened},
		Channels:   []uuid.UUID{env.channel},
		Window:     businessHours(),
		Steps: []alertroutes.Step{
			{Position: 1, Delay: 5 * time.Minute, ChannelIDs: []uuid.UUID{env.channel}},
		},
	})
	env.clock.set(time.Date(2026, 1, 5, 3, 0, 0, 0, time.UTC)) // outside business hours
	alert := env.openCritAlert()

	if err := env.follower.Tick(context.Background(), env.org); err != nil {
		t.Fatalf("Tick while shut: %v", err)
	}
	if _, ok := env.ledgerRow(alert, route, 0); ok {
		t.Fatal("a shut window's event recorded a base before ever delivering")
	}

	env.clock.set(time.Date(2026, 1, 5, 9, 30, 0, 0, time.UTC)) // the window opens
	if err := env.follower.Tick(context.Background(), env.org); err != nil {
		t.Fatalf("Tick once the window opened: %v", err)
	}
	t0 := env.clock.Now()
	base, ok := env.ledgerRow(alert, route, 0)
	if !ok {
		t.Fatal("the window opened and delivered, but recorded no base")
	}
	if !base.Equal(t0) {
		t.Fatalf("base = %s, want %s — the opening, not the raise", base, t0)
	}

	env.clock.advance(5 * time.Minute)
	if err := env.follower.Tick(context.Background(), env.org); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if n := env.deliveriesTo(route, 1); n != 1 {
		t.Fatalf("rung 1 fired %d time(s) 5 minutes after the window opened, want 1", n)
	}
}
