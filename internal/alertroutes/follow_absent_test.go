package alertroutes_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/alertroutes"
)

// openAbsentWarning opens a client's absent warning on the fixture's host
// — the host-scope alert at severity warn (backlog 3.57) — with its
// 'opened' event, and gives the host an agent last seen `ago` before the
// fixture's clock. It reports the alert and that last contact.
func (e *followEnv) openAbsentWarning(ago time.Duration) (uuid.UUID, time.Time) {
	e.t.Helper()
	ctx := context.Background()
	var seen time.Time
	if err := e.d.Pool().QueryRow(ctx, `
		INSERT INTO host_agents (host_id, org_id, agent_name, cert_serial, last_seen)
		VALUES ($1, $2, 'agent', $3, $4) RETURNING last_seen`,
		e.host, e.org, uuid.NewString(), e.clock.Now().Add(-ago)).Scan(&seen); err != nil {
		e.t.Fatalf("insert agent: %v", err)
	}
	var alertID uuid.UUID
	if err := e.d.Pool().QueryRow(ctx, `
		INSERT INTO alerts (org_id, host_id, scope, check_key, item, metric, opened_at, severity, peak_severity, severity_changed_at)
		VALUES ($1, $2, 'host', '', '', '', $3, 'warn', 'warn', $3)
		RETURNING id`, e.org, e.host, e.clock.Now()).Scan(&alertID); err != nil {
		e.t.Fatalf("open absent warning: %v", err)
	}
	if _, err := e.d.Pool().Exec(ctx, `
		INSERT INTO alert_events (org_id, alert_id, at, kind, payload)
		VALUES ($1, $2, $3, 'opened', '{}'::jsonb)`, e.org, alertID, e.clock.Now()); err != nil {
		e.t.Fatalf("open absent warning event: %v", err)
	}
	return alertID, seen
}

// TestAbsentWarningMessagesSayWhenTheClientWasLastSeen (backlog 3.57):
// the forward pass and an escalation rung both carry the agent's last
// contact on the absent warning's item, read when the message is built,
// so every channel can say "not seen since …".
func TestAbsentWarningMessagesSayWhenTheClientWasLastSeen(t *testing.T) {
	env := newFollowEnv(t)
	alert, seen := env.openAbsentWarning(15 * 24 * time.Hour)
	env.routeWithSteps([]alertroutes.Step{
		{Position: 1, Delay: 5 * time.Minute, ChannelIDs: []uuid.UUID{env.channel}},
	})

	if err := env.follower.Tick(context.Background(), env.org); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	env.clock.advance(5 * time.Minute)
	if err := env.follower.Tick(context.Background(), env.org); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if len(env.enq.events) != 2 {
		t.Fatalf("enqueued %d messages, want the opened one and the escalation", len(env.enq.events))
	}
	for i, wantEvent := range []string{"opened", "escalated"} {
		items := env.enq.events[i].Alerts.Items
		if len(items) != 1 || items[0].AlertID != alert || items[0].Event != wantEvent {
			t.Fatalf("message %d items = %+v, want the warning's %s", i, items, wantEvent)
		}
		if got := items[0].NotSeenSince; got == nil || !got.Equal(seen) {
			t.Fatalf("message %d (%s) not seen since = %v, want %v", i, wantEvent, got, seen)
		}
	}
}

// TestAbsentWarningIsSuppressedLikeAnyAlert: a maintenance window over the
// client holds the warning exactly as it holds any other alert — nothing
// about the host-scope subject bypasses the mute.
func TestAbsentWarningIsSuppressedLikeAnyAlert(t *testing.T) {
	env := newFollowEnv(t)
	env.openAbsentWarning(15 * 24 * time.Hour)
	env.routeWithSteps(nil)
	now := env.clock.Now()
	env.maintenanceWindow(env.host, now.Add(-time.Hour), now.Add(time.Hour))

	if err := env.follower.Tick(context.Background(), env.org); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if n := env.enq.count(); n != 0 {
		t.Fatalf("enqueued %d messages for a warning in maintenance, want none", n)
	}
	if got := env.ledgerState(); got != alertroutes.StateSuppressed {
		t.Fatalf("ledger = %q, want suppressed", got)
	}
}
