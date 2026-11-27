// The told audience beyond the resolve (backlog 2.145): an acknowledgement
// reaches every route that already queued a message about its alert, even
// when the alert eased to a severity outside that route's set before
// somebody took it. Snoozes keep matching on the live severity.
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

// itemsOf counts the items of kind about alert across every message the
// enqueuer took.
func (e *followEnv) itemsOf(alert uuid.UUID, kind alerts.EventKind) int {
	e.t.Helper()
	e.enq.mu.Lock()
	defer e.enq.mu.Unlock()
	n := 0
	for _, ev := range e.enq.events {
		for _, it := range ev.Alerts.Items {
			if it.AlertID == alert && it.Event == string(kind) {
				n++
			}
		}
	}
	return n
}

// eventLedger reads the ledger state of alert's single event of kind, ""
// when it has none.
func (e *followEnv) eventLedger(alert uuid.UUID, kind alerts.EventKind) alertroutes.LedgerState {
	e.t.Helper()
	var state string
	err := e.d.Pool().QueryRow(context.Background(), `
		SELECT coalesce((SELECT s.state FROM alert_notify_state s WHERE s.alert_event_id = ae.id), '')
		FROM alert_events ae WHERE ae.alert_id = $1 AND ae.kind = $2`, alert, string(kind)).Scan(&state)
	if err != nil {
		e.t.Fatalf("read %s event ledger: %v", kind, err)
	}
	return alertroutes.LedgerState(state)
}

// TestAcknowledgementReachesTheRouteThatPagedAfterTheSeverityEased: a
// crit-only route pages on "opened", the alert eases to warn and somebody
// acknowledges it. Matched on the live warn, the acknowledgement found no
// route and the second person paged kept working an incident someone
// already had.
func TestAcknowledgementReachesTheRouteThatPagedAfterTheSeverityEased(t *testing.T) {
	env := newFollowEnv(t)
	env.route(alertroutes.ScopeOrg, nil, []rules.State{rules.StateCrit},
		[]alerts.EventKind{alerts.EventOpened, alerts.EventAcknowledged}, []uuid.UUID{env.channel})
	alert := env.openCritAlert()
	env.tickOnce()

	env.setSeverity(alert, rules.StateWarn)
	env.acknowledge(alert)
	env.tickOnce()

	if got := env.itemsOf(alert, alerts.EventAcknowledged); got != 1 {
		t.Errorf("acknowledged items about the alert = %d, want 1", got)
	}
}

// TestSnoozeStillNeedsTheLiveSeverity keeps snoozes where the owner left
// them (2026-11-27): a snooze of an alert that eased out of a route's set
// is matched on the live severity and reaches nobody there.
func TestSnoozeStillNeedsTheLiveSeverity(t *testing.T) {
	env := newFollowEnv(t)
	env.route(alertroutes.ScopeOrg, nil, []rules.State{rules.StateCrit},
		[]alerts.EventKind{alerts.EventOpened, alerts.EventSnoozed}, []uuid.UUID{env.channel})
	alert := env.openCritAlert()
	env.tickOnce()

	env.setSeverity(alert, rules.StateWarn)
	env.snooze(alert, env.clock.Now().Add(time.Hour))
	env.tickOnce()

	if got := env.itemsOf(alert, alerts.EventSnoozed); got != 0 {
		t.Errorf("snoozed items about the alert = %d, want 0", got)
	}
	if got := env.eventLedger(alert, alerts.EventSnoozed); got != alertroutes.StateUnrouted {
		t.Errorf("snoozed event ledger = %q, want %q", got, alertroutes.StateUnrouted)
	}
}

// TestAcknowledgementBreaksThroughWhereThePageDid: the night-shift route
// (crit only, business hours, crit breaking through) pages at 03:00, the
// alert eases to warn and is acknowledged at 03:10. The acknowledgement
// goes out at once, as the page did, instead of waiting for the window.
func TestAcknowledgementBreaksThroughWhereThePageDid(t *testing.T) {
	env := newFollowEnv(t)
	env.clock.set(time.Date(2026, 1, 5, 3, 0, 0, 0, time.UTC))
	env.routeWithWindow(alertroutes.ScopeOrg, nil, []rules.State{rules.StateCrit},
		[]alerts.EventKind{alerts.EventOpened, alerts.EventAcknowledged}, []uuid.UUID{env.channel},
		businessHours(), []rules.State{rules.StateCrit})
	alert := env.openCritAlert()
	env.tickOnce()

	env.clock.advance(5 * time.Minute)
	env.setSeverity(alert, rules.StateWarn)
	env.clock.advance(5 * time.Minute)
	env.acknowledge(alert)
	env.tickOnce()

	if got := env.itemsOf(alert, alerts.EventAcknowledged); got != 1 {
		t.Errorf("acknowledged items about the alert = %d, want 1", got)
	}
	if got := env.eventLedger(alert, alerts.EventAcknowledged); got != alertroutes.StateSent {
		t.Errorf("acknowledged event ledger = %q, want %q", got, alertroutes.StateSent)
	}
}
