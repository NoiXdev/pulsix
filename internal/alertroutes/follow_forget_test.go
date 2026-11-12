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

// forget closes every open alert of the fixture host's whole-host cpu
// item — the subject openAlert gives every alert — through the real
// alerts.Service, on a transaction of its own, the way the forget does.
func (e *followEnv) forget() int {
	e.t.Helper()
	ctx := context.Background()
	tx, err := e.d.Pool().Begin(ctx)
	if err != nil {
		e.t.Fatalf("begin forget: %v", err)
	}
	defer tx.Rollback(context.Background()) //nolint:errcheck // no-op once committed
	n, err := alerts.NewService(e.alertStore, e.clock.Now).CloseForgotten(ctx, tx, e.org, e.host, "cpu", "", e.clock.Now())
	if err != nil {
		e.t.Fatalf("close forgotten: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		e.t.Fatalf("commit forget: %v", err)
	}
	return n
}

// closedEventState is the ledger's decision on alert's 'closed' event, ""
// while undecided.
func (e *followEnv) closedEventState(alert uuid.UUID) alertroutes.LedgerState {
	e.t.Helper()
	var state string
	err := e.d.Pool().QueryRow(context.Background(), `
		SELECT coalesce(max(s.state), '') FROM alert_events ae
		LEFT JOIN alert_notify_state s ON s.alert_event_id = ae.id
		WHERE ae.alert_id = $1 AND ae.kind = 'closed'`, alert).Scan(&state)
	if err != nil {
		e.t.Fatalf("read closed event state: %v", err)
	}
	return alertroutes.LedgerState(state)
}

// TestForgottenAlertNotifiesLikeAResolve is Review Focus 5's routing half:
// a route listening for closed alerts hears a forgotten one exactly once,
// acknowledged or not — the forget writes the same 'closed' event a
// resolve does, so the follower needs no path of its own for it.
func TestForgottenAlertNotifiesLikeAResolve(t *testing.T) {
	env := newFollowEnv(t)
	env.route(alertroutes.ScopeOrg, nil, []rules.State{rules.StateCrit},
		[]alerts.EventKind{alerts.EventClosed}, []uuid.UUID{env.channel})
	alert := env.openCritAlert()
	env.acknowledge(alert)
	if err := env.follower.Tick(context.Background(), env.org); err != nil {
		t.Fatalf("Tick before the forget: %v", err)
	}
	if got := env.enq.count(); got != 0 {
		t.Fatalf("Enqueue called %d times before the forget, want 0", got)
	}

	env.clock.advance(time.Minute)
	if n := env.forget(); n != 1 {
		t.Fatalf("forget closed %d alerts, want 1", n)
	}
	for range 2 {
		if err := env.follower.Tick(context.Background(), env.org); err != nil {
			t.Fatalf("Tick after the forget: %v", err)
		}
	}
	if got := env.enq.count(); got != 1 {
		t.Fatalf("Enqueue called %d times after the forget, want exactly 1", got)
	}
	if got := env.closedEventState(alert); got != alertroutes.StateSent {
		t.Fatalf("closed event ledger state = %q, want %q", got, alertroutes.StateSent)
	}
}

// TestForgottenSnoozedAlertIsHeldAndRetiredLikeAResolvedOne: a snoozed
// alert's closing is suppressed and then retired as expired — whichever
// way it closed. The forget must not take a path that delivers it.
func TestForgottenSnoozedAlertIsHeldAndRetiredLikeAResolvedOne(t *testing.T) {
	outcome := func(t *testing.T, closeIt func(env *followEnv, alert uuid.UUID)) (alertroutes.LedgerState, int) {
		env := newFollowEnv(t)
		env.route(alertroutes.ScopeOrg, nil, []rules.State{rules.StateCrit},
			[]alerts.EventKind{alerts.EventClosed}, []uuid.UUID{env.channel})
		alert := env.openCritAlert()
		env.snooze(alert, env.clock.Now().Add(time.Hour))
		env.clock.advance(time.Minute)
		closeIt(env, alert)
		for range 2 {
			if err := env.follower.Tick(context.Background(), env.org); err != nil {
				t.Fatalf("Tick: %v", err)
			}
		}
		return env.closedEventState(alert), env.enq.count()
	}

	resolvedState, resolvedSent := outcome(t, func(env *followEnv, alert uuid.UUID) { env.closeAlert(alert) })
	forgottenState, forgottenSent := outcome(t, func(env *followEnv, _ uuid.UUID) { env.forget() })
	if forgottenState != resolvedState || forgottenSent != resolvedSent {
		t.Fatalf("forgotten: state %q, %d sent; resolved: state %q, %d sent — want the same",
			forgottenState, forgottenSent, resolvedState, resolvedSent)
	}
	if forgottenState != alertroutes.StateExpired {
		t.Fatalf("forgotten snoozed alert's closed event = %q, want %q", forgottenState, alertroutes.StateExpired)
	}
}
