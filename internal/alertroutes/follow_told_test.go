// The told audience beyond 2.144's resolve: an acknowledgement reaches
// every route that already queued a message about its alert, even when the
// alert eased to a severity outside that route's set before somebody took
// it (backlog 2.145; snoozes keep matching on the live severity), and a
// resolve or an acknowledgement also reaches the channels of every
// escalation step that fired for such a route (backlog 2.146).
package alertroutes_test

import (
	"context"
	"slices"
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

// itemsTo counts the items of kind about alert in the messages enqueued
// for channel.
func (e *followEnv) itemsTo(channel, alert uuid.UUID, kind alerts.EventKind) int {
	e.t.Helper()
	e.enq.mu.Lock()
	defer e.enq.mu.Unlock()
	n := 0
	for i, c := range e.enq.calls {
		if !slices.Contains(c.channels, channel) {
			continue
		}
		for _, it := range e.enq.events[i].Alerts.Items {
			if it.AlertID == alert && it.Event == string(kind) {
				n++
			}
		}
	}
	return n
}

// climbToStepOne opens a crit alert on a crit-only route whose ladder
// names step1 and step2 five minutes apart, and ticks until step 1 has
// fired.
func (e *followEnv) climbToStepOne(step1, step2 alertroutes.Step) (alert, route uuid.UUID) {
	e.t.Helper()
	step1.Position, step1.Delay = 1, 5*time.Minute
	step2.Position, step2.Delay = 2, 5*time.Minute
	route = e.routeWithStepsForSeverities([]rules.State{rules.StateCrit}, []alertroutes.Step{step1, step2})
	alert = e.openCritAlert()
	e.tickOnce()
	e.clock.advance(5 * time.Minute)
	e.tickOnce()
	if n := e.deliveriesTo(route, 1); n != 1 {
		e.t.Fatalf("rung 1 fired %d time(s), want 1", n)
	}
	return alert, route
}

// TestClosedReachesTheEscalationStepsThatFired: nobody answered the page,
// so step 1 woke a second person; the alert then eased and resolved. The
// resolve reaches the route's own channel and step 1's, and not step 2's,
// which never fired — the second person is not left working an incident
// that is over.
func TestClosedReachesTheEscalationStepsThatFired(t *testing.T) {
	env := newFollowEnv(t)
	step1, step2 := env.secondChannel(), env.secondChannel()
	alert, _ := env.climbToStepOne(alertroutes.Step{ChannelIDs: []uuid.UUID{step1}},
		alertroutes.Step{ChannelIDs: []uuid.UUID{step2}})

	env.setSeverity(alert, rules.StateWarn)
	env.closeAlert(alert)
	env.tickOnce()

	for _, c := range []struct {
		name    string
		channel uuid.UUID
		want    int
	}{{"the route's own channel", env.channel, 1}, {"step 1's channel", step1, 1}, {"step 2's channel", step2, 0}} {
		if got := env.itemsTo(c.channel, alert, alerts.EventClosed); got != c.want {
			t.Errorf("closed items to %s = %d, want %d", c.name, got, c.want)
		}
	}
}

// TestAcknowledgementReachesTheEscalationStepsThatFired: the person step 1
// woke hears that somebody has taken the alert; step 2 never fired (the
// acknowledgement stops the chain) and hears nothing.
func TestAcknowledgementReachesTheEscalationStepsThatFired(t *testing.T) {
	env := newFollowEnv(t)
	step1, step2 := env.secondChannel(), env.secondChannel()
	alert, _ := env.climbToStepOne(alertroutes.Step{ChannelIDs: []uuid.UUID{step1}},
		alertroutes.Step{ChannelIDs: []uuid.UUID{step2}})

	env.acknowledge(alert)
	env.tickOnce()

	if got := env.itemsTo(step1, alert, alerts.EventAcknowledged); got != 1 {
		t.Errorf("acknowledged items to step 1's channel = %d, want 1", got)
	}
	if got := env.itemsTo(step2, alert, alerts.EventAcknowledged); got != 0 {
		t.Errorf("acknowledged items to step 2's channel = %d, want 0", got)
	}
	if got := env.itemsTo(env.channel, alert, alerts.EventAcknowledged); got != 1 {
		t.Errorf("acknowledged items to the route's own channel = %d, want 1", got)
	}
}

// TestAStepChannelThatIsAlsoTheRoutesHearsOnce: a ladder that repeats the
// route's own channel (page again, louder) must not tell it the alert
// resolved twice.
func TestAStepChannelThatIsAlsoTheRoutesHearsOnce(t *testing.T) {
	env := newFollowEnv(t)
	alert, _ := env.climbToStepOne(alertroutes.Step{ChannelIDs: []uuid.UUID{env.channel}},
		alertroutes.Step{ChannelIDs: []uuid.UUID{env.secondChannel()}})

	env.closeAlert(alert)
	env.tickOnce()

	if got := env.closedItems(alert); got != 1 {
		t.Errorf("closed items about the alert = %d, want 1", got)
	}
}

// TestARotationStepIsResolvedAtTheTicksInstant: step 1 names a rotation
// and woke whoever was on call then; by the time the alert resolves the
// rotation has handed over. The resolve goes to whoever is on call now
// (decision 8, as for the route's own rotation).
func TestARotationStepIsResolvedAtTheTicksInstant(t *testing.T) {
	env := newFollowEnv(t)
	first, second := env.secondChannel(), env.secondChannel()
	rot := env.rotation(1, env.clock.Now().Add(-time.Hour), "UTC", []uuid.UUID{first, second})
	alert, _ := env.climbToStepOne(alertroutes.Step{RotationID: &rot},
		alertroutes.Step{ChannelIDs: []uuid.UUID{env.secondChannel()}})
	if got := env.itemsTo(first, alert, alerts.EventKind("escalated")); got != 1 {
		t.Fatalf("escalated items to the first on call = %d, want 1", got)
	}

	env.acknowledge(alert) // stop the chain before the day passes
	env.clock.advance(24 * time.Hour)
	env.closeAlert(alert)
	env.tickOnce()

	if got := env.itemsTo(second, alert, alerts.EventClosed); got != 1 {
		t.Errorf("closed items to the one on call now = %d, want 1", got)
	}
	if got := env.itemsTo(first, alert, alerts.EventClosed); got != 0 {
		t.Errorf("closed items to the one on call when the step fired = %d, want 0", got)
	}
}

// replaceSteps rewrites route's ladder through the store, as an edit in
// the route dialog does (ReplaceSteps deletes the old rungs and inserts
// the new ones at the positions given).
func (e *followEnv) replaceSteps(route uuid.UUID, steps []alertroutes.Step) {
	e.t.Helper()
	ctx := context.Background()
	tx, err := e.d.Begin(ctx)
	if err != nil {
		e.t.Fatalf("begin replace steps: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := e.store.ReplaceSteps(ctx, tx, e.org, route, steps); err != nil {
		e.t.Fatalf("replace steps: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		e.t.Fatalf("commit replace steps: %v", err)
	}
}

// TestAStepCutFromTheLadderMidIncidentHearsNothing pins ruling R3 of the
// 2.145/2.146 plan: a fired step is looked up by position in the ladder as
// it stands when the event goes out. Both rungs fired; then the ladder is
// cut to one step and the alert resolves.
//
//   - Step 2 dropped: position 2 no longer exists, so its former channel
//     hears nothing, and step 1's channel still hears the resolve.
//   - Step 1 dropped and the old step 2 renumbered to position 1: position
//     1 fired, so the channel now at position 1 (the old step 2's) hears
//     the resolve and the old step 1's does not — R3's recorded cost.
func TestAStepCutFromTheLadderMidIncidentHearsNothing(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		keep                 func(step1, step2 uuid.UUID) uuid.UUID
		wantStep1, wantStep2 int
	}{
		{name: "step 2 dropped", keep: func(s1, _ uuid.UUID) uuid.UUID { return s1 }, wantStep1: 1, wantStep2: 0},
		{name: "step 1 dropped, step 2 renumbered", keep: func(_, s2 uuid.UUID) uuid.UUID { return s2 }, wantStep1: 0, wantStep2: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := newFollowEnv(t)
			step1, step2 := env.secondChannel(), env.secondChannel()
			alert, route := env.climbToStepOne(alertroutes.Step{ChannelIDs: []uuid.UUID{step1}},
				alertroutes.Step{ChannelIDs: []uuid.UUID{step2}})
			env.clock.advance(5 * time.Minute)
			env.tickOnce()
			if n := env.deliveriesTo(route, 2); n != 1 {
				t.Fatalf("rung 2 fired %d time(s), want 1", n)
			}

			env.replaceSteps(route, []alertroutes.Step{
				{Position: 1, Delay: 5 * time.Minute, ChannelIDs: []uuid.UUID{tc.keep(step1, step2)}},
			})
			env.closeAlert(alert)
			env.tickOnce()

			if got := env.itemsTo(step1, alert, alerts.EventClosed); got != tc.wantStep1 {
				t.Errorf("closed items to the old step 1's channel = %d, want %d", got, tc.wantStep1)
			}
			if got := env.itemsTo(step2, alert, alerts.EventClosed); got != tc.wantStep2 {
				t.Errorf("closed items to the old step 2's channel = %d, want %d", got, tc.wantStep2)
			}
			if got := env.itemsTo(env.channel, alert, alerts.EventClosed); got != 1 {
				t.Errorf("closed items to the route's own channel = %d, want 1", got)
			}
		})
	}
}
