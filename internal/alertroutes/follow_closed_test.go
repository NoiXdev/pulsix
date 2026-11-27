// The resolve message's audience (backlog 2.144): a closed event reaches
// every route that already queued a message about its alert, even when
// the alert eased to a severity outside that route's set before it closed.
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

// tickOnce runs one follower tick and fails the test on an error.
func (e *followEnv) tickOnce() {
	e.t.Helper()
	if err := e.follower.Tick(context.Background(), e.org); err != nil {
		e.t.Fatalf("Tick: %v", err)
	}
}

// closedItems counts the closed items about alert across every message
// the enqueuer took.
func (e *followEnv) closedItems(alert uuid.UUID) int {
	e.t.Helper()
	e.enq.mu.Lock()
	defer e.enq.mu.Unlock()
	n := 0
	for _, ev := range e.enq.events {
		for _, it := range ev.Alerts.Items {
			if it.AlertID == alert && it.Event == string(alerts.EventClosed) {
				n++
			}
		}
	}
	return n
}

// TestClosedReachesTheRouteThatPagedAfterTheSeverityEased is the
// production shape of the TestAlertResolveNotificationEndToEnd flake: a
// crit-only route pages on "opened", the alert falls to warn (the CPU
// reading dropped under the crit threshold but stayed over the seeded warn
// one; a falling severity writes no event), then the condition clears and
// the alert closes. The closed event used to be matched on the alert's
// live severity — warn — against a crit-only route, recorded 'unrouted',
// and the person who was paged was never told it resolved.
func TestClosedReachesTheRouteThatPagedAfterTheSeverityEased(t *testing.T) {
	env := newFollowEnv(t)
	env.route(alertroutes.ScopeOrg, nil, []rules.State{rules.StateCrit},
		[]alerts.EventKind{alerts.EventOpened, alerts.EventClosed}, []uuid.UUID{env.channel})
	alert := env.openCritAlert()
	env.tickOnce()
	if got := env.enq.count(); got != 1 {
		t.Fatalf("messages after the alert opened = %d, want 1", got)
	}

	env.setSeverity(alert, rules.StateWarn)
	env.closeAlert(alert)
	env.tickOnce()

	if got := env.enq.count(); got != 2 {
		t.Fatalf("messages after the alert closed = %d, want 2 — the resolve message of its own", got)
	}
	if got := env.closedItems(alert); got != 1 {
		t.Errorf("closed items about the alert = %d, want 1", got)
	}
}

// TestClosedReachesTheRouteThatToldAfterTheSeverityRose is the mirror
// image: a warn-only route is told the alert opened at warn, the alert
// then rises to crit (a severity outside that route's set) and closes
// there. The route's subscriber heard "opened" and is owed the resolve.
func TestClosedReachesTheRouteThatToldAfterTheSeverityRose(t *testing.T) {
	env := newFollowEnv(t)
	env.route(alertroutes.ScopeOrg, nil, []rules.State{rules.StateWarn},
		[]alerts.EventKind{alerts.EventOpened, alerts.EventClosed}, []uuid.UUID{env.channel})
	alert := env.openAlert(rules.StateWarn)
	env.tickOnce()

	env.setSeverity(alert, rules.StateCrit)
	env.closeAlert(alert)
	env.tickOnce()

	if got := env.closedItems(alert); got != 1 {
		t.Errorf("closed items about the alert = %d, want 1", got)
	}
}

// TestClosedSkipsARouteThatNeverTold keeps the widening to the audience of
// the page: a crit-only route that never heard of a warn alert is not told
// it closed, and the event is recorded 'unrouted' exactly as before.
func TestClosedSkipsARouteThatNeverTold(t *testing.T) {
	env := newFollowEnv(t)
	env.route(alertroutes.ScopeOrg, nil, []rules.State{rules.StateCrit},
		[]alerts.EventKind{alerts.EventOpened, alerts.EventClosed}, []uuid.UUID{env.channel})
	alert := env.openAlert(rules.StateWarn)
	env.tickOnce()
	env.closeAlert(alert)
	env.tickOnce()

	if got := env.enq.count(); got != 0 {
		t.Errorf("messages = %d, want 0 — the route never told anybody about this alert", got)
	}
}

// closedLedger reads the ledger state of alert's closed event, "" when it
// has none.
func (e *followEnv) closedLedger(alert uuid.UUID) alertroutes.LedgerState {
	e.t.Helper()
	var state string
	err := e.d.Pool().QueryRow(context.Background(), `
		SELECT coalesce((SELECT s.state FROM alert_notify_state s WHERE s.alert_event_id = ae.id), '')
		FROM alert_events ae WHERE ae.alert_id = $1 AND ae.kind = 'closed'`, alert).Scan(&state)
	if err != nil {
		e.t.Fatalf("read closed event ledger: %v", err)
	}
	return alertroutes.LedgerState(state)
}

// TestClosedBreaksThroughWhereThePageDid is the night-shift route: crit
// only, business hours, crit breaking through. The alert opens at crit at
// 03:00 and pages through the shut window, eases to warn and closes at
// 03:30. The resolve must break through as the page did: judged at the
// live warn alone it was held ('deferred'), and a held event of a closed
// alert is never offered again, so it would expire unsent.
func TestClosedBreaksThroughWhereThePageDid(t *testing.T) {
	env := newFollowEnv(t)
	env.clock.set(time.Date(2026, 1, 5, 3, 0, 0, 0, time.UTC))
	env.routeWithWindow(alertroutes.ScopeOrg, nil, []rules.State{rules.StateCrit},
		[]alerts.EventKind{alerts.EventOpened, alerts.EventClosed}, []uuid.UUID{env.channel},
		businessHours(), []rules.State{rules.StateCrit})
	alert := env.openCritAlert()
	env.tickOnce()
	if got := env.enq.count(); got != 1 {
		t.Fatalf("messages after the 03:00 page = %d, want 1 — crit breaks through", got)
	}

	env.clock.advance(20 * time.Minute)
	env.setSeverity(alert, rules.StateWarn)
	env.clock.advance(10 * time.Minute)
	env.closeAlert(alert)
	env.tickOnce()

	if got := env.closedItems(alert); got != 1 {
		t.Errorf("closed items about the alert = %d, want 1", got)
	}
	if got := env.closedLedger(alert); got != alertroutes.StateSent {
		t.Errorf("closed event ledger = %q, want %q", got, alertroutes.StateSent)
	}
}

// TestClosedStaysHeldWhereThePageWasHeld keeps that rule to what the page
// could do: a warn-only episode on a route that lets only crit through its
// window is told at 09:30 inside the window and closes at 03:00 the next
// night. Its peak is warn, so the resolve does not break through: it is
// held ('deferred') like any other warn event at that hour.
func TestClosedStaysHeldWhereThePageWasHeld(t *testing.T) {
	env := newFollowEnv(t)
	env.clock.set(time.Date(2026, 1, 5, 9, 30, 0, 0, time.UTC))
	env.routeWithWindow(alertroutes.ScopeOrg, nil, []rules.State{rules.StateWarn},
		[]alerts.EventKind{alerts.EventOpened, alerts.EventClosed}, []uuid.UUID{env.channel},
		businessHours(), []rules.State{rules.StateCrit})
	alert := env.openAlert(rules.StateWarn)
	env.tickOnce()
	// Overnight: the alert is still warn, peak warn, and closes at 03:00
	// the next day with the window shut.
	env.clock.set(time.Date(2026, 1, 6, 3, 0, 0, 0, time.UTC))
	env.closeAlert(alert)
	env.tickOnce()

	if got := env.closedItems(alert); got != 0 {
		t.Errorf("closed items = %d, want 0 — a warn episode does not break through", got)
	}
	if got := env.closedLedger(alert); got != alertroutes.StateDeferred {
		t.Errorf("closed event ledger = %q, want %q", got, alertroutes.StateDeferred)
	}
}
