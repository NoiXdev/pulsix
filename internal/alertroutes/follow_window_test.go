// The follower's fourth gate: the delivery window, the severities that
// cross it, and the rotation a route can draw its channel from (spec
// docs/superpowers/specs/2026-10-13-v0.4c-notification-schedules-
// design.md, decisions 2, 3, 4, 8 and 9). Split out of follow_test.go so
// that file stays about the tick's own transaction discipline; the
// fixture (followEnv, fixedClock, logBuffer) is shared and lives there.
//
// Every test here moves the clock deliberately. That is the whole point:
// three of the four gates depend only on which routes match, but the
// window depends on WHEN, and "when" is the one thing a frozen clock
// cannot express.
package alertroutes_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/alertroutes"
	"github.com/noixdev/pulsix/internal/alerts"
	"github.com/noixdev/pulsix/internal/oncall"
	"github.com/noixdev/pulsix/internal/rules"
)

// routeWithWindow creates one enabled route carrying a delivery window
// and, optionally, the severities that cross it when it is closed.
func (e *followEnv) routeWithWindow(scope alertroutes.Scope, scopeID *uuid.UUID, severities []rules.State,
	events []alerts.EventKind, channels []uuid.UUID, window *alertroutes.Window,
	breakthrough []rules.State) uuid.UUID {
	e.t.Helper()
	return e.createRoute(alertroutes.Input{
		Enabled: true, Scope: scope, ScopeID: scopeID,
		Severities: severities, Events: events, Channels: channels,
		Window: window, Breakthrough: breakthrough,
	})
}

// routeWithRotation creates one enabled route whose channel comes from a
// rotation. It still carries the fixture's own channel as its fixed list —
// Validate requires one, and more usefully it makes the rotation's answer
// provable: a test that sees a DIFFERENT channel delivered has watched the
// rotation override the list rather than merely agree with it.
func (e *followEnv) routeWithRotation(scope alertroutes.Scope, scopeID *uuid.UUID, severities []rules.State,
	events []alerts.EventKind, rotationID uuid.UUID, window *alertroutes.Window,
	breakthrough []rules.State) uuid.UUID {
	e.t.Helper()
	return e.createRoute(alertroutes.Input{
		Enabled: true, Scope: scope, ScopeID: scopeID,
		Severities: severities, Events: events, Channels: []uuid.UUID{e.channel},
		Window: window, Breakthrough: breakthrough, RotationID: &rotationID,
	})
}

// secondChannel adds another notification channel to the fixture's
// organization, for the tests that have to tell two of them apart.
func (e *followEnv) secondChannel() uuid.UUID {
	e.t.Helper()
	return seedChannel(e.t, e.d, e.org)
}

// rotation creates an on-call rotation cycling channels every periodDays,
// first handover at anchor.
func (e *followEnv) rotation(periodDays int, anchor time.Time, timezone string, channels []uuid.UUID) uuid.UUID {
	e.t.Helper()
	e.seq++
	r, err := e.rotations.Create(context.Background(), e.org, uuid.Must(uuid.NewV7()), oncall.Input{
		Name: "rotation-" + uuid.Must(uuid.NewV7()).String(), PeriodDays: periodDays,
		Anchor: anchor, Timezone: timezone, Channels: channels,
	}, e.clock.Now())
	if err != nil {
		e.t.Fatalf("create rotation: %v", err)
	}
	return r.ID
}

// breakWindowTimezone writes a timezone no zoneinfo knows straight onto
// the route's row, past Validate. That is not a contrived state: a hand
// edited row, a restore from an export, or a zone a future tzdata drops
// all reach it, and the database has no CHECK that could catch it.
func (e *followEnv) breakWindowTimezone(route uuid.UUID, timezone string) {
	e.t.Helper()
	if _, err := e.d.Pool().Exec(context.Background(),
		`UPDATE alert_routes SET window_timezone = $2 WHERE id = $1`, route, timezone); err != nil {
		e.t.Fatalf("break the window timezone: %v", err)
	}
}

// logs is everything the follower has said so far.
func (e *followEnv) logs() string { return e.log.String() }

// businessHours is the window every test below reuses: deliver from 09:00
// for nine hours, so 03:00 is unambiguously outside it and 09:30 inside.
func businessHours() *alertroutes.Window {
	return &alertroutes.Window{Cron: "0 9 * * *", Duration: 9 * time.Hour, Timezone: "UTC"}
}

func TestEventOutsideTheWindowIsDeferred(t *testing.T) {
	env := newFollowEnv(t)
	// Business hours, and the clock says 03:00.
	env.routeWithWindow(alertroutes.ScopeOrg, nil,
		[]rules.State{rules.StateCrit}, []alerts.EventKind{alerts.EventOpened},
		[]uuid.UUID{env.channel}, businessHours(), nil)
	env.clock.set(time.Date(2026, 1, 5, 3, 0, 0, 0, time.UTC))
	env.openCritAlert()

	if err := env.follower.Tick(context.Background(), env.org); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if len(env.enq.calls) != 0 {
		t.Fatalf("an alert outside the window was delivered: %d calls", len(env.enq.calls))
	}
	if got := env.ledgerState(); got != alertroutes.StateDeferred {
		t.Errorf("ledger state = %q, want %q", got, alertroutes.StateDeferred)
	}

	// 09:00: the window opens and the held alert goes out.
	env.clock.set(time.Date(2026, 1, 5, 9, 30, 0, 0, time.UTC))
	if err := env.follower.Tick(context.Background(), env.org); err != nil {
		t.Fatalf("Tick after the window opened: %v", err)
	}
	if len(env.enq.calls) != 1 {
		t.Errorf("Enqueue called %d times once the window opened, want 1", len(env.enq.calls))
	}
	if got := env.ledgerState(); got != alertroutes.StateSent {
		t.Errorf("ledger state = %q once delivered, want %q", got, alertroutes.StateSent)
	}
}

func TestBreakthroughSeverityIgnoresTheWindow(t *testing.T) {
	env := newFollowEnv(t)
	env.routeWithWindow(alertroutes.ScopeOrg, nil,
		[]rules.State{rules.StateCrit}, []alerts.EventKind{alerts.EventOpened},
		[]uuid.UUID{env.channel}, businessHours(), []rules.State{rules.StateCrit})
	env.clock.set(time.Date(2026, 1, 5, 3, 0, 0, 0, time.UTC))
	env.openCritAlert()

	if err := env.follower.Tick(context.Background(), env.org); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if len(env.enq.calls) != 1 {
		t.Errorf("a breakthrough severity at 03:00 produced %d messages, want 1", len(env.enq.calls))
	}
}

func TestMaintenanceSilencesEvenABreakthroughSeverity(t *testing.T) {
	// Decision 4, and the boundary that keeps a maintenance window a
	// promise rather than a request.
	env := newFollowEnv(t)
	env.routeWithWindow(alertroutes.ScopeOrg, nil,
		[]rules.State{rules.StateCrit}, []alerts.EventKind{alerts.EventOpened},
		[]uuid.UUID{env.channel}, businessHours(), []rules.State{rules.StateCrit})
	env.clock.set(time.Date(2026, 1, 5, 3, 0, 0, 0, time.UTC))
	env.maintenanceWindow(env.host, env.clock.Now().Add(-time.Hour), env.clock.Now().Add(time.Hour))
	env.openCritAlert()

	if err := env.follower.Tick(context.Background(), env.org); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if len(env.enq.calls) != 0 {
		t.Fatalf("maintenance did not silence a breakthrough severity: %d calls", len(env.enq.calls))
	}
	if got := env.ledgerState(); got != alertroutes.StateSuppressed {
		t.Errorf("ledger state = %q, want %q — suppression outranks the window",
			got, alertroutes.StateSuppressed)
	}
}

// TestSuppressionOutranksAClosedWindow is the other half of the ordering:
// not a breakthrough severity this time, just an ordinary alert that trips
// BOTH gates at once. The ledger has one row per event and therefore one
// reason, and that reason must be the maintenance window — an operator
// reading 'deferred' would go looking at the time of day and never learn
// the host was down for maintenance at all.
func TestSuppressionOutranksAClosedWindow(t *testing.T) {
	env := newFollowEnv(t)
	env.routeWithWindow(alertroutes.ScopeOrg, nil,
		[]rules.State{rules.StateCrit}, []alerts.EventKind{alerts.EventOpened},
		[]uuid.UUID{env.channel}, businessHours(), nil)
	env.clock.set(time.Date(2026, 1, 5, 3, 0, 0, 0, time.UTC))
	env.maintenanceWindow(env.host, env.clock.Now().Add(-time.Hour), env.clock.Now().Add(time.Hour))
	env.openCritAlert()

	if err := env.follower.Tick(context.Background(), env.org); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if got := env.ledgerState(); got != alertroutes.StateSuppressed {
		t.Errorf("ledger state = %q, want %q", got, alertroutes.StateSuppressed)
	}
}

// TestADeferredEventBecomesSuppressedWhenMaintenanceBegins is the spec's
// own "an event may move from deferred to suppressed between ticks, and
// that needs no special case". It is worth a test because the catch-up
// pass's cheapest possible shape — "already decided, nothing to write" —
// would silently leave the stale reason behind.
func TestADeferredEventBecomesSuppressedWhenMaintenanceBegins(t *testing.T) {
	env := newFollowEnv(t)
	env.routeWithWindow(alertroutes.ScopeOrg, nil,
		[]rules.State{rules.StateCrit}, []alerts.EventKind{alerts.EventOpened},
		[]uuid.UUID{env.channel}, businessHours(), nil)
	env.clock.set(time.Date(2026, 1, 5, 3, 0, 0, 0, time.UTC))
	env.openCritAlert()

	if err := env.follower.Tick(context.Background(), env.org); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if got := env.ledgerState(); got != alertroutes.StateDeferred {
		t.Fatalf("ledger state = %q before maintenance, want %q", got, alertroutes.StateDeferred)
	}

	env.maintenanceWindow(env.host, env.clock.Now().Add(-time.Minute), env.clock.Now().Add(time.Hour))
	if err := env.follower.Tick(context.Background(), env.org); err != nil {
		t.Fatalf("Tick once maintenance began: %v", err)
	}
	if got := env.ledgerState(); got != alertroutes.StateSuppressed {
		t.Errorf("ledger state = %q once maintenance began, want %q", got, alertroutes.StateSuppressed)
	}
}

// TestAnOpenRouteDeliversWhileAClosedOneWaitsForNobody pins the one case
// a single ledger row per event cannot express both halves of, so the
// choice has to be made deliberately: routes add up, and two routes with
// COMPLEMENTARY windows are the design's own answer to "only on weekdays"
// (spec, "What this is not"). Holding the event because the other route's
// window is shut would deliver a Saturday alert to the weekday route on
// Monday — a stale page, hours late, that nobody can act on. So a route
// whose window is open delivers now and the event is 'sent'; a route whose
// window is shut hears nothing about THIS event, which is precisely what
// its window said.
func TestAnOpenRouteDeliversWhileAClosedOneWaitsForNobody(t *testing.T) {
	env := newFollowEnv(t)
	night := env.secondChannel()
	// Business hours for the fixture's channel...
	env.routeWithWindow(alertroutes.ScopeOrg, nil,
		[]rules.State{rules.StateCrit}, []alerts.EventKind{alerts.EventOpened},
		[]uuid.UUID{env.channel}, businessHours(), nil)
	// ...and the complementary night shift for the other.
	env.routeWithWindow(alertroutes.ScopeOrg, nil,
		[]rules.State{rules.StateCrit}, []alerts.EventKind{alerts.EventOpened},
		[]uuid.UUID{night},
		&alertroutes.Window{Cron: "0 18 * * *", Duration: 15 * time.Hour, Timezone: "UTC"}, nil)
	env.clock.set(time.Date(2026, 1, 5, 3, 0, 0, 0, time.UTC))
	env.openCritAlert()

	if err := env.follower.Tick(context.Background(), env.org); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if len(env.enq.calls) != 1 {
		t.Fatalf("Enqueue called %d times, want 1 — the night shift was awake", len(env.enq.calls))
	}
	if got := env.enq.calls[0].channels; len(got) != 1 || got[0] != night {
		t.Errorf("delivered to %v, want only the channel whose window was open", got)
	}
	if got := env.ledgerState(); got != alertroutes.StateSent {
		t.Errorf("ledger state = %q, want %q — somebody was told", got, alertroutes.StateSent)
	}
}

func TestRotationIsResolvedAtDeliveryTimeNotEventTime(t *testing.T) {
	// Decision 8. The alert is raised while Anna is on call and caught up
	// after the handover to Bob. Bob can act; Anna has gone home.
	env := newFollowEnv(t)
	anna, bob := env.channel, env.secondChannel()
	anchor := time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC)
	rot := env.rotation(7, anchor, "UTC", []uuid.UUID{anna, bob})
	env.routeWithRotation(alertroutes.ScopeOrg, nil,
		[]rules.State{rules.StateCrit}, []alerts.EventKind{alerts.EventOpened},
		rot,
		&alertroutes.Window{Cron: "0 9 * * *", Duration: time.Hour, Timezone: "UTC"},
		nil)

	// Raised inside Anna's week but outside the delivery window: 14:00 on
	// the third day, four hours after the 09:00–10:00 window shut.
	env.clock.set(anchor.AddDate(0, 0, 3).Add(5 * time.Hour))
	env.openCritAlert()
	if err := env.follower.Tick(context.Background(), env.org); err != nil {
		t.Fatalf("Tick while held: %v", err)
	}
	if len(env.enq.calls) != 0 {
		t.Fatal("the alert should have been held outside the window")
	}

	// Delivered after the handover, inside the window.
	env.clock.set(anchor.AddDate(0, 0, 8).Add(30 * time.Minute))
	if err := env.follower.Tick(context.Background(), env.org); err != nil {
		t.Fatalf("Tick after the handover: %v", err)
	}
	if len(env.enq.calls) != 1 {
		t.Fatalf("Enqueue called %d times, want 1", len(env.enq.calls))
	}
	if got := env.enq.calls[0].channels; len(got) != 1 || got[0] != bob {
		t.Errorf("delivered to %v, want the channel on call at DELIVERY time", got)
	}
}

// TestARotationStandInTakesTheNightShift is the other half of the
// rotation gate: the follower must resolve through oncall.ChannelAt, not
// through the cycle alone, or every stand-in an operator enters is
// configured and ignored.
func TestARotationStandInTakesTheNightShift(t *testing.T) {
	env := newFollowEnv(t)
	anna, bob := env.channel, env.secondChannel()
	anchor := time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC)
	rot := env.rotation(7, anchor, "UTC", []uuid.UUID{anna, bob})
	env.routeWithRotation(alertroutes.ScopeOrg, nil,
		[]rules.State{rules.StateCrit}, []alerts.EventKind{alerts.EventOpened}, rot, nil, nil)

	at := anchor.AddDate(0, 0, 2)
	if _, err := env.rotations.CreateException(context.Background(), env.org, rot,
		uuid.Must(uuid.NewV7()), oncall.ExceptionInput{
			StartsAt: at.Add(-time.Hour), EndsAt: at.Add(time.Hour), ChannelID: bob, Note: "anna is ill",
		}, env.clock.Now()); err != nil {
		t.Fatalf("create exception: %v", err)
	}
	env.clock.set(at)
	env.openCritAlert()

	if err := env.follower.Tick(context.Background(), env.org); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if len(env.enq.calls) != 1 {
		t.Fatalf("Enqueue called %d times, want 1", len(env.enq.calls))
	}
	if got := env.enq.calls[0].channels; len(got) != 1 || got[0] != bob {
		t.Errorf("delivered to %v, want the stand-in", got)
	}
}

// TestARotationWithNoChannelIsUnrouted is the spec's "nothing here goes
// quietly inactive": a rotation that can name nobody records 'unrouted',
// the same state a channel-less route already produces, so v0.4b's
// "reaches nobody" indicator lights up instead of the event sitting in
// the catch-up pass forever waiting for a window that is not the problem.
func TestARotationWithNoChannelIsUnrouted(t *testing.T) {
	env := newFollowEnv(t)
	anchor := time.Date(2026, 1, 5, 9, 0, 0, 0, time.UTC)
	rot := env.rotation(7, anchor, "UTC", []uuid.UUID{env.channel})
	env.routeWithRotation(alertroutes.ScopeOrg, nil,
		[]rules.State{rules.StateCrit}, []alerts.EventKind{alerts.EventOpened}, rot, nil, nil)
	// Emptying the cycle is what a rotation whose channels were all
	// deleted leaves behind; the route's rotation_id survives it.
	if _, err := env.d.Pool().Exec(context.Background(),
		`DELETE FROM oncall_rotation_channels WHERE rotation_id = $1`, rot); err != nil {
		t.Fatalf("empty the rotation: %v", err)
	}
	env.clock.set(anchor)
	env.openCritAlert()

	if err := env.follower.Tick(context.Background(), env.org); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if len(env.enq.calls) != 0 {
		t.Fatalf("Enqueue called %d times, want 0", len(env.enq.calls))
	}
	if got := env.ledgerState(); got != alertroutes.StateUnrouted {
		t.Errorf("ledger state = %q, want %q", got, alertroutes.StateUnrouted)
	}
	if !strings.Contains(env.logs(), "rotation") {
		t.Error("a rotation that names nobody must be logged, or nobody learns it is broken")
	}
}

func TestAnUnreadableWindowDelivers(t *testing.T) {
	// Decision 9, and the only test that pins it. Both failure directions
	// are wrong and only one is dangerous: a broken window that delivers
	// is loud, so somebody notices and fixes it; one that holds is silent
	// and is discovered at the next outage nobody heard about.
	//
	// The row is written straight to the database, bypassing Validate —
	// which is exactly how this can arise in production: a timezone the
	// system no longer knows, or a hand-edited row.
	env := newFollowEnv(t)
	route := env.routeWithWindow(alertroutes.ScopeOrg, nil,
		[]rules.State{rules.StateCrit}, []alerts.EventKind{alerts.EventOpened},
		[]uuid.UUID{env.channel},
		&alertroutes.Window{Cron: "0 9 * * *", Duration: time.Hour, Timezone: "UTC"},
		nil)
	env.breakWindowTimezone(route, "Mars/Olympus")
	// 03:00 — far outside the window, so a working window would hold.
	env.clock.set(time.Date(2026, 1, 5, 3, 0, 0, 0, time.UTC))
	env.openCritAlert()

	if err := env.follower.Tick(context.Background(), env.org); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if len(env.enq.calls) != 1 {
		t.Fatalf("an unreadable window produced %d messages, want 1 — it must deliver, not hold",
			len(env.enq.calls))
	}
	if got := env.ledgerState(); got != alertroutes.StateSent {
		t.Errorf("ledger state = %q, want %q", got, alertroutes.StateSent)
	}
	if !strings.Contains(env.logs(), "window") {
		t.Error("an unreadable window must be logged, or nobody learns it is broken")
	}
}
