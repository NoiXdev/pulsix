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

	// Ticked WHILE still snoozed, past where the BASE-relative delay would
	// have fired rung 1 (base+5m) — review Critical 1: a test that only
	// ever ticks after a mute has already lifted never exercises the mute
	// gate itself, and passes even with alerts.Mute deleted from dueStep
	// entirely. This tick is the one that would catch that.
	env.clock.advance(5 * time.Minute)
	if err := env.follower.Tick(context.Background(), env.org); err != nil {
		t.Fatalf("Tick while still snoozed: %v", err)
	}
	if n := env.deliveriesTo(route, 1); n != 0 {
		t.Fatalf("rung 1 fired %d time(s) while still snoozed, want 0", n)
	}

	// One minute after the snooze ends: short of rung 1's own full delay
	// measured from THAT moment (which the snooze also already outlasted
	// the base-relative due time by), so still not due either way.
	env.clock.advance(6 * time.Minute)
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
	// Important 4 of the review, the reaches-nobody half: RecordEscalation
	// still writes the 'escalated' event even when nothing was sent
	// (fireStep's own doc comment, follow_escalate.go) — an empty channel
	// list in the payload, not a missing row, is what "fired, reached
	// nobody" has to look like on the timeline.
	if channels, ok := env.escalatedEvent(alert, 1); !ok {
		t.Error("rung 1 reached nobody but left no 'escalated' event on the timeline at all")
	} else if len(channels) != 0 {
		t.Errorf("escalated event for rung 1 names channels %v, want none — it reached nobody", channels)
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
	if n := env.deliveriesTo(route, 2); n != 0 {
		t.Fatalf("rung 2 fired %d time(s) before its own delay elapsed, want 0", n)
	}
	// Important 4 of the review: decision 7's own 'escalated' event, with
	// the step's position and the channel it actually reached in its
	// payload — nothing before this asserted the row exists at all, only
	// that a message went out and a ledger row was claimed.
	if channels, ok := env.escalatedEvent(alert, 1); !ok {
		t.Error("no 'escalated' alert_events row for rung 1 — the timeline has no record this rung fired")
	} else if len(channels) != 1 || channels[0] != env.channel {
		t.Errorf("escalated event for rung 1 names channels %v, want [%v]", channels, env.channel)
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

	// Ticked WHILE still snoozed (review Critical 1 — see
	// TestAMutedAlertPausesAndResumesWithoutABurst's own comment on why
	// this checkpoint has to exist, not only one after the mute lifts).
	env.clock.advance(5 * time.Minute)
	if err := env.follower.Tick(context.Background(), env.org); err != nil {
		t.Fatalf("Tick while still snoozed: %v", err)
	}
	if n := env.deliveriesTo(route, 1); n != 0 {
		t.Fatalf("rung 1 fired %d time(s) while still snoozed, want 0", n)
	}

	// One minute after the snooze runs out on its own — short of rung 1's
	// own full delay measured from that moment.
	env.clock.advance(6 * time.Minute)
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
	// The clock is pinned BEFORE the route is created, not after: the
	// route's own updated_at is now one of LastUrgentAt's anchors
	// (escalate_sql.go, migration 00058's review, Critical 3), and
	// creating it at the fixture's REAL starting instant — October, by
	// wall-clock, against this test's January timeline — would make that
	// anchor read as "in the future" relative to every `now` the test
	// drives afterward, which DueStep reads as never due.
	env.clock.set(time.Date(2026, 1, 5, 3, 0, 0, 0, time.UTC)) // outside business hours
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
	// Pinned before route creation — see
	// TestABreakthroughSeverityStartsTheClockAtOpen's own comment on why.
	env.clock.set(time.Date(2026, 1, 5, 3, 0, 0, 0, time.UTC)) // outside business hours
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

// TestAnOperatorSilenceHoldsTheChainThenResumesWithFullDelay is Critical 1
// of the review, the operator-silence third of alerts.Mute's reasons: a
// silence window is the identical maintenance.Occurrence machinery as a
// maintenance window with a different Reason (mute.go's own doc comment),
// which makes this the resume path's OWN regression test for "two of the
// three mute reasons share one fold, not one" (escalatePass's own comment,
// follow_escalate.go).
func TestAnOperatorSilenceHoldsTheChainThenResumesWithFullDelay(t *testing.T) {
	env := newFollowEnv(t)
	env.openCritAlert()
	route := env.routeWithSteps([]alertroutes.Step{
		{Position: 1, Delay: 5 * time.Minute, ChannelIDs: []uuid.UUID{env.channel}},
	})
	if err := env.follower.Tick(context.Background(), env.org); err != nil { // base at t0
		t.Fatalf("Tick: %v", err)
	}

	// Silenced one minute in, for ten minutes — short of rung 1's own
	// 5-minute delay measured from the base, so a stale anchor would
	// already have fired it by the time the window even ends.
	env.clock.advance(time.Minute)
	env.silenceWindow(env.host, env.clock.Now(), env.clock.Now().Add(10*time.Minute))

	// Ticked WHILE still silenced — the mute gate itself, not only its
	// aftermath (review Critical 1).
	env.clock.advance(5 * time.Minute)
	if err := env.follower.Tick(context.Background(), env.org); err != nil {
		t.Fatalf("Tick while still silenced: %v", err)
	}
	if n := env.deliveriesTo(route, 1); n != 0 {
		t.Fatalf("rung 1 fired %d time(s) while still silenced, want 0", n)
	}

	// One minute after the silence ends: short of rung 1's own full delay
	// measured from that moment.
	env.clock.advance(6 * time.Minute)
	if err := env.follower.Tick(context.Background(), env.org); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if n := env.deliveriesTo(route, 1); n != 0 {
		t.Fatalf("rung 1 fired %d time(s) the instant the silence lifted; it should wait out its delay", n)
	}

	env.clock.advance(4 * time.Minute) // five minutes after the silence ended
	if err := env.follower.Tick(context.Background(), env.org); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if n := env.deliveriesTo(route, 1); n != 1 {
		t.Fatalf("rung 1 fired %d time(s) once the chain resumed, want 1", n)
	}
}

// TestAMaintenanceWindowHoldsTheChainThenResumesWithFullDelay is Critical 1
// of the review, the maintenance third of alerts.Mute's reasons.
func TestAMaintenanceWindowHoldsTheChainThenResumesWithFullDelay(t *testing.T) {
	env := newFollowEnv(t)
	env.openCritAlert()
	route := env.routeWithSteps([]alertroutes.Step{
		{Position: 1, Delay: 5 * time.Minute, ChannelIDs: []uuid.UUID{env.channel}},
	})
	if err := env.follower.Tick(context.Background(), env.org); err != nil { // base at t0
		t.Fatalf("Tick: %v", err)
	}

	// Maintenance one minute in, for ten minutes — short of rung 1's own
	// 5-minute delay measured from the base.
	env.clock.advance(time.Minute)
	env.maintenanceWindow(env.host, env.clock.Now(), env.clock.Now().Add(10*time.Minute))

	// Ticked WHILE still in maintenance (review Critical 1).
	env.clock.advance(5 * time.Minute)
	if err := env.follower.Tick(context.Background(), env.org); err != nil {
		t.Fatalf("Tick during maintenance: %v", err)
	}
	if n := env.deliveriesTo(route, 1); n != 0 {
		t.Fatalf("rung 1 fired %d time(s) during maintenance, want 0", n)
	}

	// One minute after maintenance ends: short of rung 1's own full delay
	// measured from that moment.
	env.clock.advance(6 * time.Minute)
	if err := env.follower.Tick(context.Background(), env.org); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if n := env.deliveriesTo(route, 1); n != 0 {
		t.Fatalf("rung 1 fired %d time(s) the instant maintenance ended; it should wait out its delay", n)
	}

	env.clock.advance(4 * time.Minute) // five minutes after maintenance ended
	if err := env.follower.Tick(context.Background(), env.org); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if n := env.deliveriesTo(route, 1); n != 1 {
		t.Fatalf("rung 1 fired %d time(s) once the chain resumed, want 1", n)
	}
}

// TestASeverityReEnteringTheRouteSSetFiresPromptly is spec decision 5 as
// amended in review round 2: a severity excursion is NOT a mute. A mute
// means nobody was told anything; a severity leaving a route's set and
// coming back means the alert got worse and other routes may well have
// been shouting the whole time it was gone. So re-entry gets the next
// rung PROMPTLY — whatever the stale anchor already says is due — not a
// fresh full delay measured from the moment of re-entry. (The first
// version of this test asserted the opposite, deliberate-delay behaviour;
// review found that the only way to deliver it — folding
// severity_changed_at into LastUrgentAt — can stall a chain forever once
// severity wobbles INSIDE a route's own set, which is the far worse
// failure. See TestASeverityThatFallsButStaysInTheRouteSSetDoesNotPostponeTheNextRung
// for that regression test.)
func TestASeverityReEnteringTheRouteSSetFiresPromptly(t *testing.T) {
	env := newFollowEnv(t)
	alert := env.openAlert(rules.StateWarn)
	route := env.routeWithStepsForSeverities([]rules.State{rules.StateWarn}, []alertroutes.Step{
		{Position: 1, Delay: 5 * time.Minute, ChannelIDs: []uuid.UUID{env.channel}},
	})
	if err := env.follower.Tick(context.Background(), env.org); err != nil { // base at t0, warn matches
		t.Fatalf("Tick: %v", err)
	}

	// Rises out of the route's own severity set — paused, not stopped —
	// well past rung 1's own base-relative due time (t0+5m), so a resume
	// that still required a FRESH delay would read as 0 fired here too;
	// the point of this test is what happens the instant it re-enters.
	env.clock.advance(time.Minute)
	env.setSeverity(alert, rules.StateCrit)
	env.clock.advance(10 * time.Minute)
	if err := env.follower.Tick(context.Background(), env.org); err != nil {
		t.Fatalf("Tick while the severity was outside the route's set: %v", err)
	}
	if n := env.deliveriesTo(route, 1); n != 0 {
		t.Fatalf("rung 1 fired %d time(s) while severity was outside the route's set, want 0", n)
	}

	// Falls back into the route's set and is ticked almost immediately —
	// rung 1 must fire on THIS tick, not wait out a fresh delay from the
	// re-entry: the base (t0) is already more than 5 minutes behind by
	// now, so nothing further needs to elapse.
	env.clock.advance(time.Minute)
	env.setSeverity(alert, rules.StateWarn)
	env.clock.advance(time.Minute)
	if err := env.follower.Tick(context.Background(), env.org); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if n := env.deliveriesTo(route, 1); n != 1 {
		t.Fatalf("rung 1 fired %d time(s) on the tick right after re-entry, want 1 — re-entry should fire promptly, not wait out a fresh delay", n)
	}
}

// TestASeverityThatFallsButStaysInTheRouteSSetDoesNotPostponeTheNextRung is
// the regression test for review round 2's own finding: a severity that
// moves WITHOUT ever leaving the route's set (crit down to warn on a
// {warn, crit} route) must not push the next rung's due time out at all.
// Folding alerts.severity_changed_at into LastUrgentAt — the fix review
// round 1 asked for — did exactly that: the column moves on EVERY
// severity write, in or out of the set, so an alert that merely wobbles
// inside the set would restart its own ladder's delay on every wobble and
// could stall forever. This must fail against a build that still folds
// severity_changed_at into the anchor.
func TestASeverityThatFallsButStaysInTheRouteSSetDoesNotPostponeTheNextRung(t *testing.T) {
	env := newFollowEnv(t)
	alert := env.openAlert(rules.StateCrit)
	route := env.routeWithStepsForSeverities([]rules.State{rules.StateWarn, rules.StateCrit}, []alertroutes.Step{
		{Position: 1, Delay: 5 * time.Minute, ChannelIDs: []uuid.UUID{env.channel}},
	})
	if err := env.follower.Tick(context.Background(), env.org); err != nil { // base at t0, crit matches
		t.Fatalf("Tick: %v", err)
	}

	// Falls from crit to warn — still inside {warn, crit}, so the route
	// keeps matching throughout; nothing here should count as leaving or
	// re-entering anything.
	env.clock.advance(time.Minute)
	env.setSeverity(alert, rules.StateWarn)

	// Exactly rung 1's own delay after the BASE, not after the severity
	// move: if severity_changed_at were still folded into the anchor, this
	// tick would see rung 1 not yet due (t0+1m+5m = t0+6m > now), which is
	// the stall review found.
	env.clock.advance(4 * time.Minute) // now = t0+5m
	if err := env.follower.Tick(context.Background(), env.org); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if n := env.deliveriesTo(route, 1); n != 1 {
		t.Fatalf("rung 1 fired %d time(s) at t0+5m after a severity move inside the route's own set, want 1 — "+
			"a wobble inside the set must not postpone the next rung", n)
	}
}

// TestADisabledRouteResumesWithFullDelayWhenReEnabled is Critical 3 of the
// review. Decision 4 calls a disabled route a pause, but decision 5's
// anchor list never named it — a disabled route cannot appear in
// ChainsNeedingAttention at all (its own JOIN requires r.enabled), so
// re-enabling it is the only way this matters, and alert_routes.updated_at
// (which moves on exactly that, and on any other edit) is what
// escalate_sql.go now folds in to answer it.
func TestADisabledRouteResumesWithFullDelayWhenReEnabled(t *testing.T) {
	env := newFollowEnv(t)
	env.openCritAlert()
	route := env.routeWithSteps([]alertroutes.Step{
		{Position: 1, Delay: 5 * time.Minute, ChannelIDs: []uuid.UUID{env.channel}},
	})
	if err := env.follower.Tick(context.Background(), env.org); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	// Disabled well before rung 1's own delay would otherwise have
	// elapsed, and for long enough that the stale base alone would have
	// fired it many times over by the time it is re-enabled.
	env.clock.advance(time.Minute)
	env.setRouteEnabled(route, false)
	env.clock.advance(2 * time.Hour)
	if err := env.follower.Tick(context.Background(), env.org); err != nil {
		t.Fatalf("Tick while disabled: %v", err)
	}
	if n := env.deliveriesTo(route, 1); n != 0 {
		t.Fatalf("rung 1 fired %d time(s) while the route was disabled, want 0", n)
	}

	// Re-enabled. One minute later: short of rung 1's own full delay
	// measured from the re-enable.
	env.clock.advance(time.Minute)
	env.setRouteEnabled(route, true)
	env.clock.advance(time.Minute)
	if err := env.follower.Tick(context.Background(), env.org); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if n := env.deliveriesTo(route, 1); n != 0 {
		t.Fatalf("rung 1 fired %d time(s) the instant the route was re-enabled; it should wait out its delay", n)
	}

	env.clock.advance(4 * time.Minute) // five minutes after re-enabling
	if err := env.follower.Tick(context.Background(), env.org); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if n := env.deliveriesTo(route, 1); n != 1 {
		t.Fatalf("rung 1 fired %d time(s) once re-enabled, want 1", n)
	}
}

// TestARouteNarrowedToAnotherHostStopsEscalating is Important 5 of the
// review: ChainsNeedingAttentionBase's own structural query does not check
// a route's scope against the alert's host (its own doc comment says so,
// deliberately), so dueStep has to — a route edited to point somewhere
// else must pause a chain it is no longer scoped to reach, the same way a
// severity leaving the route's set already does.
func TestARouteNarrowedToAnotherHostStopsEscalating(t *testing.T) {
	env := newFollowEnv(t)
	alert := env.openCritAlert()
	route := env.createRoute(alertroutes.Input{
		Enabled: true, Scope: alertroutes.ScopeHost, ScopeID: &env.host,
		Severities: []rules.State{rules.StateCrit},
		Events:     []alerts.EventKind{alerts.EventOpened},
		Channels:   []uuid.UUID{env.channel},
		Steps:      []alertroutes.Step{{Position: 1, Delay: 5 * time.Minute, ChannelIDs: []uuid.UUID{env.channel}}},
	})
	if err := env.follower.Tick(context.Background(), env.org); err != nil { // base at t0, host scope matches
		t.Fatalf("Tick: %v", err)
	}
	if _, ok := env.ledgerRow(alert, route, 0); !ok {
		t.Fatal("no base recorded; the route's own host scope should have matched at open")
	}

	otherHost := env.secondHost()
	env.narrowRouteToHost(route, otherHost)

	// Well past rung 1's own delay — a stale anchor would have fired it
	// long ago.
	env.clock.advance(time.Hour)
	if err := env.follower.Tick(context.Background(), env.org); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if n := env.deliveriesTo(route, 1); n != 0 {
		t.Fatalf("rung 1 fired %d time(s) after the route was narrowed to a different host, want 0", n)
	}
}

// TestAnUnboundedSnoozeHoldsTheChainThenResumesWithFullDelay is Critical 1
// of the review, the snooze third of alerts.Mute's reasons — and the one
// shape of it that actually needs the mute GATE, not just an anchor: a
// bounded snooze's own snoozed_until column is folded into LastUrgentAt by
// ChainsNeedingAttentionBase regardless of alerts.Mute, so a test built on
// one alone cannot tell the gate apart from being deleted outright (which
// is exactly what review found). An unbounded snooze
// (snooze_until_change, no snoozed_until at all) has no such column to
// fall back on — alerts.Mute's own check in dueStep is the only thing
// holding the chain back while it is active.
func TestAnUnboundedSnoozeHoldsTheChainThenResumesWithFullDelay(t *testing.T) {
	env := newFollowEnv(t)
	alert := env.openCritAlert()
	route := env.routeWithSteps([]alertroutes.Step{
		{Position: 1, Delay: 5 * time.Minute, ChannelIDs: []uuid.UUID{env.channel}},
	})
	if err := env.follower.Tick(context.Background(), env.org); err != nil { // base at t0
		t.Fatalf("Tick: %v", err)
	}

	env.clock.advance(time.Minute)
	env.snoozeUntilChange(alert)

	// Ticked WHILE still snoozed, well past where the base-relative delay
	// would have fired rung 1.
	env.clock.advance(time.Hour)
	if err := env.follower.Tick(context.Background(), env.org); err != nil {
		t.Fatalf("Tick while unbounded-snoozed: %v", err)
	}
	if n := env.deliveriesTo(route, 1); n != 0 {
		t.Fatalf("rung 1 fired %d time(s) during an unbounded snooze, want 0", n)
	}

	env.clock.advance(time.Minute)
	env.unsnooze(alert)
	// One minute after the snooze was cleared: short of rung 1's own full
	// delay measured from that moment.
	env.clock.advance(time.Minute)
	if err := env.follower.Tick(context.Background(), env.org); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if n := env.deliveriesTo(route, 1); n != 0 {
		t.Fatalf("rung 1 fired %d time(s) the instant the snooze was cleared; it should wait out its delay", n)
	}

	env.clock.advance(4 * time.Minute) // five minutes after the snooze was cleared
	if err := env.follower.Tick(context.Background(), env.org); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if n := env.deliveriesTo(route, 1); n != 1 {
		t.Fatalf("rung 1 fired %d time(s) once the chain resumed, want 1", n)
	}
}
