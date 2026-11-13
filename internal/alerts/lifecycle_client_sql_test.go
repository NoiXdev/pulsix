package alerts_test

import (
	"context"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/alerts"
	"github.com/noixdev/pulsix/internal/rules"
)

// TestAbsentClientLifecycle (backlog 3.57) drives the host-scope subject
// through the real ticker, store and alerts table: a server whose agent
// has been gone for fifteen days goes unknown; switched to a client it is
// frozen, so the unknown alert closes stale and — in the same evaluation,
// on the same subject, without tripping the one-open-per-subject index —
// the absent warning opens. Later ticks do not open it again, the frozen
// tuple alert is left alone, and the warning closes resolved once the
// agent is back.
func TestAbsentClientLifecycle(t *testing.T) {
	r := newLifecycleRig(t)
	ctx := context.Background()
	if _, err := r.d.Pool().Exec(ctx, `
		INSERT INTO host_agents (host_id, org_id, agent_name, cert_serial, last_seen)
		VALUES ($1, $2, 'agent', 'serial-absent', now() - interval '15 days')`, r.host, r.org); err != nil {
		t.Fatalf("insert agent: %v", err)
	}
	tuple := r.openEpisode(t)

	// A server: its stale tuple turns the roll-up unknown and the host
	// alert opens at once.
	r.advance(4 * time.Minute)
	r.makeDue(t)
	r.tick(t)
	unknown := r.openHostAlert(t)
	if unknown.Severity != rules.StateUnknown {
		t.Fatalf("server host alert severity = %q, want unknown", unknown.Severity)
	}

	// Server to client while the agent is off.
	if _, err := r.d.Pool().Exec(ctx, `UPDATE hosts SET kind = 'client' WHERE id = $1`, r.host); err != nil {
		t.Fatalf("set kind: %v", err)
	}
	r.makeDue(t)
	r.tick(t)
	if closed, reason := r.closure(t, unknown.ID); !closed || reason != string(alerts.Stale) {
		t.Fatalf("unknown host alert closed=%v reason=%q, want closed stale", closed, reason)
	}
	warning := r.openHostAlert(t)
	if warning.Severity != rules.StateWarn {
		t.Fatalf("absent warning severity = %q, want warn", warning.Severity)
	}
	if closed, _ := r.closure(t, tuple.ID); closed {
		t.Fatal("the frozen client's tuple alert was closed")
	}

	// The next ticks keep the one warning.
	for range 2 {
		r.advance(time.Minute)
		r.makeDue(t)
		r.tick(t)
		if again := r.openHostAlert(t); again.ID != warning.ID {
			t.Fatalf("a later tick replaced the warning %s with %s", warning.ID, again.ID)
		}
	}

	// The agent is back and reports: judged again, the warning resolves.
	if _, err := r.d.Pool().Exec(ctx,
		`UPDATE host_agents SET last_seen = now(), online_since = now() WHERE host_id = $1`, r.host); err != nil {
		t.Fatalf("agent back: %v", err)
	}
	r.report(t, 96)
	r.tick(t)
	if closed, reason := r.closure(t, warning.ID); !closed || reason != string(alerts.Resolved) {
		t.Fatalf("absent warning closed=%v reason=%q, want closed resolved", closed, reason)
	}
	if closed, _ := r.closure(t, tuple.ID); closed {
		t.Fatal("the tuple alert closed although its disk still reads crit")
	}
}

// openHostAlert is the one host-scope alert open on the rig's host, or a
// failure when there is none or more than one.
func (r *lifecycleRig) openHostAlert(t *testing.T) alerts.Alert {
	t.Helper()
	tx := beginTx(t, r.d)
	defer func() { _ = tx.Rollback(context.Background()) }()
	open, err := alerts.NewSQLStore(r.d).OpenForHost(context.Background(), tx, r.org, r.host)
	if err != nil {
		t.Fatalf("open for host: %v", err)
	}
	var found []alerts.Alert
	for _, a := range open {
		if a.Subject.Scope == alerts.ScopeHost {
			found = append(found, a)
		}
	}
	if len(found) != 1 {
		t.Fatalf("open host alerts = %+v, want exactly one", found)
	}
	return found[0]
}

// TestAClientsOwnUnknownAlertSurvivesAShutdown (backlog 3.57, final
// review): a client whose roll-up went unknown while it was on — a real
// problem, older than its agent's last contact — keeps that host alert
// through a power-off and the boot after it. No close and no reopen, so
// no notification pair, and the acknowledgement it carries stays.
func TestAClientsOwnUnknownAlertSurvivesAShutdown(t *testing.T) {
	r := newLifecycleRig(t)
	ctx := context.Background()
	// The rig's clock runs an hour behind the database's, so every moment
	// the evaluation stamps (the roll-up's Since) lies before the agent's
	// last contact, which the database stamps.
	r.now = r.now.Add(-time.Hour)
	if _, err := r.d.Pool().Exec(ctx, `UPDATE hosts SET kind = 'client' WHERE id = $1`, r.host); err != nil {
		t.Fatalf("set kind: %v", err)
	}
	// Online, with no known online start: staleness is today's rule.
	if _, err := r.d.Pool().Exec(ctx, `
		INSERT INTO host_agents (host_id, org_id, agent_name, cert_serial, last_seen)
		VALUES ($1, $2, 'agent', 'serial-own-unknown', now())`, r.host, r.org); err != nil {
		t.Fatalf("insert agent: %v", err)
	}
	r.report(t, 96)
	r.openEpisode(t)

	// The disk check stops reporting while the client is on: unknown,
	// and once the grace has passed the host alert opens.
	r.advance(4 * time.Minute)
	r.makeDue(t)
	r.tick(t)
	r.advance(2 * time.Minute)
	r.makeDue(t)
	r.tick(t)
	unknown := r.openHostAlert(t)
	if unknown.Severity != rules.StateUnknown {
		t.Fatalf("host alert severity = %q, want unknown", unknown.Severity)
	}
	if err := alerts.NewSQLStore(r.d).Acknowledge(ctx, r.org, unknown.ID, seedUser(t, r.d), "on it", r.now); err != nil {
		t.Fatalf("acknowledge: %v", err)
	}

	checkKept := func(when string) {
		t.Helper()
		got := r.openHostAlert(t)
		if got.ID != unknown.ID || got.AckAt == nil {
			t.Fatalf("%s: host alert = %+v, want %s still open and acknowledged", when, got, unknown.ID)
		}
		var events int
		if err := r.d.Pool().QueryRow(ctx,
			`SELECT count(*) FROM alert_events WHERE alert_id = $1`, unknown.ID).Scan(&events); err != nil {
			t.Fatalf("count events: %v", err)
		}
		if events != 2 {
			t.Fatalf("%s: %d events on the host alert, want only opened and acknowledged", when, events)
		}
	}

	// Power-off: the agent's last contact is newer than the unknown.
	if _, err := r.d.Pool().Exec(ctx,
		`UPDATE host_agents SET last_seen = now() - interval '5 minutes' WHERE host_id = $1`, r.host); err != nil {
		t.Fatalf("agent off: %v", err)
	}
	r.advance(time.Minute)
	r.makeDue(t)
	r.tick(t)
	checkKept("while off")

	// Boot: online again, still unknown.
	if _, err := r.d.Pool().Exec(ctx, `UPDATE host_agents SET last_seen = now() WHERE host_id = $1`, r.host); err != nil {
		t.Fatalf("agent back: %v", err)
	}
	r.advance(time.Minute)
	r.makeDue(t)
	r.tick(t)
	checkKept("after the boot")
}
