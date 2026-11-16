package alertroutes_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/alertroutes"
)

// setDetail gives alert's row, and its events of kind (when kind is not
// ""), a detail — what the alerts executor writes on an opening or a
// severity change (backlog 3.13b).
func (e *followEnv) setDetail(alert uuid.UUID, kind, detail string) {
	e.t.Helper()
	ctx := context.Background()
	if _, err := e.d.Pool().Exec(ctx, `UPDATE alerts SET detail = $2 WHERE id = $1`, alert, detail); err != nil {
		e.t.Fatalf("set alert detail: %v", err)
	}
	if kind == "" {
		return
	}
	if _, err := e.d.Pool().Exec(ctx, `UPDATE alert_events SET detail = $3 WHERE alert_id = $1 AND kind = $2`,
		alert, kind, detail); err != nil {
		e.t.Fatalf("set event detail: %v", err)
	}
}

// TestMessagesSayWhyTheAlertFired (backlog 3.13b): an item carries the
// detail of the event it is about — the opened event's for the opening,
// none for the close — and an escalation, built from the alert row, the
// alert's latest detail; the escalated event it writes stores none.
func TestMessagesSayWhyTheAlertFired(t *testing.T) {
	env := newFollowEnv(t)
	alert := env.openCritAlert()
	env.setDetail(alert, "opened", "timeout")
	env.routeWithSteps([]alertroutes.Step{
		{Position: 1, Delay: 5 * time.Minute, ChannelIDs: []uuid.UUID{env.channel}},
	})

	if err := env.follower.Tick(context.Background(), env.org); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	// The detail moved on since the opening (a severity change captured a
	// new one); the escalation says the latest.
	env.setDetail(alert, "", "certificate")
	env.clock.advance(5 * time.Minute)
	if err := env.follower.Tick(context.Background(), env.org); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	env.closeAlert(alert)
	if err := env.follower.Tick(context.Background(), env.org); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	if len(env.enq.events) != 3 {
		t.Fatalf("enqueued %d messages, want the opening, the escalation and the close", len(env.enq.events))
	}
	for i, want := range []struct{ event, detail string }{
		{"opened", "timeout"},
		{"escalated", "certificate"},
		{"closed", ""},
	} {
		items := env.enq.events[i].Alerts.Items
		if len(items) != 1 || items[0].Event != want.event || items[0].Detail != want.detail {
			t.Errorf("message %d items = %+v, want %s with detail %q", i, items, want.event, want.detail)
		}
	}

	var stored *string
	if err := env.d.Pool().QueryRow(context.Background(),
		`SELECT detail FROM alert_events WHERE alert_id = $1 AND kind = 'escalated'`, alert).Scan(&stored); err != nil {
		t.Fatalf("read escalated event: %v", err)
	}
	if stored != nil {
		t.Errorf("escalated event stores detail %q, want none", *stored)
	}
}

// A held event delivered by the catch-up pass carries its detail too.
func TestACaughtUpMessageSaysWhyTheAlertFired(t *testing.T) {
	env := newFollowEnv(t)
	alert := env.openCritAlert()
	env.setDetail(alert, "opened", "refused")
	env.routeWithSteps(nil)
	now := env.clock.Now()
	window := env.maintenanceWindow(env.host, now.Add(-time.Hour), now.Add(time.Hour))

	if err := env.follower.Tick(context.Background(), env.org); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if n := env.enq.count(); n != 0 {
		t.Fatalf("enqueued %d messages in maintenance, want none", n)
	}
	env.endWindow(window)
	if err := env.follower.Tick(context.Background(), env.org); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if len(env.enq.events) != 1 || env.enq.events[0].Alerts.Items[0].Detail != "refused" {
		t.Fatalf("caught-up messages = %+v, want the opening with detail refused", env.enq.events)
	}
}
