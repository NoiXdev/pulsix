// Backlog 4.39 at the follower: an alert that re-opened within an hour of
// a stale close and adopted its predecessor's acknowledgement or snooze.
// The opening's row starts acknowledged or snoozed (internal/alerts'
// SQLStore.open), so the follower's existing gates decide it with no case
// of their own: an adopted acknowledgement still sends the opening (decide
// has no acknowledgement gate) and stops the chain (dueStep's acknowledged
// check), an adopted snooze holds the opening (decide's mute gate).
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

// staleEpisode opens a crit alert, lets prepare acknowledge or snooze it,
// and closes it stale straight through SQL (no 'closed' event: nothing
// here is about the close's own notification).
func (e *followEnv) staleEpisode(prepare func(uuid.UUID)) uuid.UUID {
	e.t.Helper()
	id := e.openCritAlert()
	prepare(id)
	if _, err := e.d.Pool().Exec(context.Background(),
		`UPDATE alerts SET closed_at = $2, closed_reason = 'stale' WHERE id = $1`, id, e.clock.Now()); err != nil {
		e.t.Fatalf("close stale: %v", err)
	}
	return id
}

// reopenAdopting opens a new crit alert on its own subject through the real
// alerts.SQLStore, carrying ad, and returns its id.
func (e *followEnv) reopenAdopting(ad alerts.Adoption) uuid.UUID {
	e.t.Helper()
	ctx := context.Background()
	sub := alerts.Subject{HostID: e.host, Scope: alerts.ScopeTuple, CheckKey: "cpu", Metric: "adopted"}
	tx, err := e.d.Begin(ctx)
	if err != nil {
		e.t.Fatalf("begin reopen: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := e.alertStore.Execute(ctx, tx, e.org, []alerts.Action{
		{Kind: alerts.ActionOpen, Subject: sub, At: e.clock.Now(), Severity: rules.StateCrit, Adopt: &ad},
	}); err != nil {
		e.t.Fatalf("reopen adopting: %v", err)
	}
	var id uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT id FROM alerts WHERE host_id = $1 AND metric = 'adopted' AND closed_at IS NULL`, e.host).Scan(&id); err != nil {
		e.t.Fatalf("read reopened alert: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		e.t.Fatalf("commit reopen: %v", err)
	}
	return id
}

// openedLedger is the ledger state of alert's 'opened' event, "" before
// the follower decided it.
func (e *followEnv) openedLedger(alert uuid.UUID) alertroutes.LedgerState {
	e.t.Helper()
	var state string
	err := e.d.Pool().QueryRow(context.Background(), `
		SELECT COALESCE((SELECT s.state FROM alert_notify_state s WHERE s.alert_event_id = ae.id), '')
		FROM alert_events ae WHERE ae.alert_id = $1 AND ae.kind = 'opened'`, alert).Scan(&state)
	if err != nil {
		e.t.Fatalf("read opened ledger: %v", err)
	}
	return alertroutes.LedgerState(state)
}

func (e *followEnv) tick() {
	e.t.Helper()
	if err := e.follower.Tick(context.Background(), e.org); err != nil {
		e.t.Fatalf("Tick: %v", err)
	}
}

// TestAnAdoptedAcknowledgementNotifiesTheOpeningButNeverEscalates: the
// problem is back, so the opening is sent; it is acknowledged, so the
// chain stops exactly as for an alert acknowledged by hand
// (TestAcknowledgingBetweenRungsStopsTheChain).
func TestAnAdoptedAcknowledgementNotifiesTheOpeningButNeverEscalates(t *testing.T) {
	env := newFollowEnv(t)
	prev := env.staleEpisode(env.acknowledge)
	route := env.routeWithSteps([]alertroutes.Step{
		{Position: 1, Delay: 5 * time.Minute, ChannelIDs: []uuid.UUID{env.channel}},
	})
	env.tick() // the previous episode's own opening

	ackAt := env.clock.Now()
	by := env.actor()
	alert := env.reopenAdopting(alerts.Adoption{From: prev, AckAt: &ackAt, AckBy: &by, AckSeverity: rules.StateCrit})
	env.clock.advance(time.Minute)
	env.tick()
	if st := env.openedLedger(alert); st != alertroutes.StateSent {
		t.Fatalf("opening of an alert with an adopted acknowledgement = %q, want sent", st)
	}
	if _, ok := env.ledgerRow(alert, route, 0); !ok {
		t.Fatal("the delivered opening claimed no chain base")
	}

	env.clock.advance(5 * time.Minute)
	env.tick()
	env.clock.advance(time.Hour)
	env.tick()
	if n := env.deliveriesTo(route, 1); n != 0 {
		t.Fatalf("rung 1 fired %d time(s) on an alert with an adopted acknowledgement, want 0", n)
	}
}

// TestAnAdoptedSnoozeHoldsTheOpening: muted exactly as any snoozed alert
// is, until the snooze runs out.
func TestAnAdoptedSnoozeHoldsTheOpening(t *testing.T) {
	env := newFollowEnv(t)
	until := env.clock.Now().Add(30 * time.Minute)
	prev := env.staleEpisode(func(id uuid.UUID) { env.snooze(id, until) })
	env.routeWithSteps(nil)
	env.tick()

	alert := env.reopenAdopting(alerts.Adoption{From: prev, SnoozedUntil: &until})
	env.clock.advance(time.Minute)
	env.tick()
	if st := env.openedLedger(alert); st != alertroutes.StateSuppressed {
		t.Fatalf("opening of an alert with an adopted snooze = %q, want suppressed", st)
	}

	env.clock.advance(30 * time.Minute)
	env.tick()
	if st := env.openedLedger(alert); st != alertroutes.StateSent {
		t.Fatalf("opening after the adopted snooze ran out = %q, want sent", st)
	}
}
