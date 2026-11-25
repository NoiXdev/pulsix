// Backlog 4.39 at the store: the read Reconcile adopts from
// (RecentlyClosedForHost) and the opening that writes an adoption.
package alerts_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/alerts"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/rules"
)

// openAndClose opens one alert on sub at `at` and closes it with reason
// at closedAt, inside tx, returning its id.
func openAndClose(t *testing.T, st *alerts.SQLStore, tx pgx.Tx, org uuid.UUID, sub alerts.Subject, severity rules.State, reason alerts.CloseReason, at, closedAt time.Time) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	if err := st.Execute(ctx, tx, org, []alerts.Action{{Kind: alerts.ActionOpen, Subject: sub, At: at, Severity: severity}}); err != nil {
		t.Fatalf("open %+v: %v", sub, err)
	}
	open, err := st.OpenForHost(ctx, tx, org, sub.HostID)
	if err != nil {
		t.Fatalf("open for host: %v", err)
	}
	for _, a := range open {
		if a.Subject == sub {
			if err := st.Execute(ctx, tx, org, []alerts.Action{{Kind: alerts.ActionClose, AlertID: a.ID, At: closedAt, Reason: reason}}); err != nil {
				t.Fatalf("close %s: %v", a.ID, err)
			}
			return a.ID
		}
	}
	t.Fatalf("no open alert on %+v after opening it", sub)
	return uuid.Nil
}

// TestRecentlyClosedForHostReturnsTheLatestCloseOfEachSubject: per tuple
// subject the latest close at or after since, whatever its reason; older
// closes, host-scope alerts, open alerts and other hosts' alerts are left
// out.
func TestRecentlyClosedForHostReturnsTheLatestCloseOfEachSubject(t *testing.T) {
	d := testdb.Open(t)
	org, host := seedHost(t, d)
	other := seedAdditionalHost(t, d, org)
	st := alerts.NewSQLStore(d)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	since := now.Add(-time.Hour)

	tx := beginTx(t, d)
	reopened := tuple(host, "disk", "/", "used_pct")
	openAndClose(t, st, tx, org, reopened, rules.StateCrit, alerts.Stale, now.Add(-2*time.Hour), now.Add(-50*time.Minute))
	latest := openAndClose(t, st, tx, org, reopened, rules.StateCrit, alerts.Resolved, now.Add(-40*time.Minute), now.Add(-10*time.Minute))
	staleOne := openAndClose(t, st, tx, org, tuple(host, "cpu", "", "used_pct"), rules.StateWarn, alerts.Stale, now.Add(-2*time.Hour), now.Add(-30*time.Minute))
	atTheBound := openAndClose(t, st, tx, org, tuple(host, "mem", "", "used_pct"), rules.StateWarn, alerts.Stale, now.Add(-2*time.Hour), since)
	openAndClose(t, st, tx, org, tuple(host, "load", "", "load1"), rules.StateWarn, alerts.Stale, now.Add(-3*time.Hour), since.Add(-time.Microsecond))
	openAndClose(t, st, tx, org, alerts.Subject{HostID: host, Scope: alerts.ScopeHost}, rules.StateUnknown, alerts.Stale, now.Add(-time.Hour), now.Add(-5*time.Minute))
	openAndClose(t, st, tx, org, tuple(other, "disk", "/", "used_pct"), rules.StateCrit, alerts.Stale, now.Add(-time.Hour), now.Add(-5*time.Minute))
	if err := st.Execute(ctx, tx, org, []alerts.Action{{Kind: alerts.ActionOpen, Subject: tuple(host, "swap", "", "used_pct"), At: now, Severity: rules.StateWarn}}); err != nil {
		t.Fatalf("open swap: %v", err)
	}

	got, err := st.RecentlyClosedForHost(ctx, tx, org, host, since)
	if err != nil {
		t.Fatalf("recently closed for host: %v", err)
	}
	want := map[uuid.UUID]alerts.CloseReason{latest: alerts.Resolved, staleOne: alerts.Stale, atTheBound: alerts.Stale}
	if len(got) != len(want) {
		t.Fatalf("got %d episodes, want %d: %+v", len(got), len(want), got)
	}
	for _, a := range got {
		reason, ok := want[a.ID]
		if !ok {
			t.Fatalf("unexpected episode %+v", a)
		}
		if a.ClosedReason != reason || a.ClosedAt == nil {
			t.Fatalf("episode %s closed %v reason %q, want reason %q", a.ID, a.ClosedAt, a.ClosedReason, reason)
		}
	}
}

// TestExecuteOpenWritesTheAdoption: an ActionOpen carrying an adoption
// starts acknowledged and snoozed in its own INSERT, names the episode it
// continues, and its opened event says what was carried over.
func TestExecuteOpenWritesTheAdoption(t *testing.T) {
	d := testdb.Open(t)
	org, host := seedHost(t, d)
	user := seedUser(t, d)
	st := alerts.NewSQLStore(d)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	sub := tuple(host, "disk", "/", "used_pct")

	tx := beginTx(t, d)
	prev := openAndClose(t, st, tx, org, sub, rules.StateCrit, alerts.Stale, now.Add(-time.Hour), now.Add(-10*time.Minute))
	ackAt, until := now.Add(-30*time.Minute), now.Add(time.Hour)
	ad := &alerts.Adoption{
		From: prev, AckAt: &ackAt, AckBy: &user, AckSeverity: rules.StateCrit, AckNote: "on it",
		SnoozedUntil: &until,
	}
	if err := st.Execute(ctx, tx, org, []alerts.Action{{Kind: alerts.ActionOpen, Subject: sub, At: now, Severity: rules.StateCrit, Adopt: ad}}); err != nil {
		t.Fatalf("execute adopting open: %v", err)
	}

	open, err := st.OpenForHost(ctx, tx, org, host)
	if err != nil || len(open) != 1 {
		t.Fatalf("open for host = %+v, %v; want one alert", open, err)
	}
	a := open[0]
	if a.ContinuesAlertID == nil || *a.ContinuesAlertID != prev {
		t.Fatalf("continues_alert_id = %v, want %s", a.ContinuesAlertID, prev)
	}
	if a.AckAt == nil || !a.AckAt.Equal(ackAt) || a.AckBy == nil || *a.AckBy != user || a.AckSeverity != rules.StateCrit || a.AckNote != "on it" {
		t.Fatalf("acknowledgement = %v/%v/%q/%q, want %v/%s/crit/on it", a.AckAt, a.AckBy, a.AckSeverity, a.AckNote, ackAt, user)
	}
	if a.SnoozedUntil == nil || !a.SnoozedUntil.Equal(until) || a.SnoozeUntilChange {
		t.Fatalf("snooze = %v/%v, want until %v", a.SnoozedUntil, a.SnoozeUntilChange, until)
	}
	if a.Peak != rules.StateCrit {
		t.Fatalf("peak = %q, want the opening's own crit", a.Peak)
	}

	var payload []byte
	var actor *uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT payload, actor_id FROM alert_events WHERE alert_id = $1 AND kind = 'opened'`, a.ID).Scan(&payload, &actor); err != nil {
		t.Fatalf("read opened event: %v", err)
	}
	var p alerts.OpenedPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		t.Fatalf("decode opened payload %s: %v", payload, err)
	}
	if p != (alerts.OpenedPayload{ContinuesAlertID: prev, AckCarried: true, SnoozeCarried: true}) {
		t.Fatalf("opened payload = %+v, want continues %s with ack and snooze carried", p, prev)
	}
	if actor != nil {
		t.Fatalf("opened event actor = %v, want none: nobody acted", actor)
	}
	if kinds := alertEventKinds(t, tx, a.ID); len(kinds) != 1 {
		t.Fatalf("alert_events = %v, want only 'opened' (no acknowledged/snoozed event for a carried state)", kinds)
	}
}

// TestExecuteOpenWithoutAdoptionKeepsTheDefaultPayload: a plain opening's
// event payload stays '{}' and the row continues nothing.
func TestExecuteOpenWithoutAdoptionKeepsTheDefaultPayload(t *testing.T) {
	d := testdb.Open(t)
	org, host := seedHost(t, d)
	st := alerts.NewSQLStore(d)
	ctx := context.Background()

	tx := beginTx(t, d)
	if err := st.Execute(ctx, tx, org, []alerts.Action{{Kind: alerts.ActionOpen, Subject: tuple(host, "disk", "/", "used_pct"), At: time.Now(), Severity: rules.StateWarn}}); err != nil {
		t.Fatalf("execute open: %v", err)
	}
	open, err := st.OpenForHost(ctx, tx, org, host)
	if err != nil || len(open) != 1 {
		t.Fatalf("open for host = %+v, %v; want one alert", open, err)
	}
	if open[0].ContinuesAlertID != nil {
		t.Fatalf("continues_alert_id = %v on a plain opening", open[0].ContinuesAlertID)
	}
	var payload string
	if err := tx.QueryRow(ctx, `SELECT payload::text FROM alert_events WHERE alert_id = $1`, open[0].ID).Scan(&payload); err != nil {
		t.Fatalf("read opened event: %v", err)
	}
	if payload != "{}" {
		t.Fatalf("opened payload = %s, want {}", payload)
	}
}

// TestAContinuationCannotCrossOrganizations: continues_alert_id is a
// composite reference (org_id, continues_alert_id) → alerts (org_id, id),
// so an alert cannot continue another organization's episode, and
// deleting the continued episode nulls only continues_alert_id.
func TestAContinuationCannotCrossOrganizations(t *testing.T) {
	d := testdb.Open(t)
	orgA, hostA := seedHost(t, d)
	orgB, hostB := seedHost(t, d)
	st := alerts.NewSQLStore(d)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)

	tx := beginTx(t, d)
	prevA := openAndClose(t, st, tx, orgA, tuple(hostA, "disk", "/", "used_pct"), rules.StateCrit, alerts.Stale, now.Add(-time.Hour), now.Add(-time.Minute))
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}

	tx = beginTx(t, d)
	err := st.Execute(ctx, tx, orgB, []alerts.Action{{
		Kind: alerts.ActionOpen, Subject: tuple(hostB, "disk", "/", "used_pct"), At: now, Severity: rules.StateCrit,
		Adopt: &alerts.Adoption{From: prevA, SnoozeUntilChange: true},
	}})
	if !db.IsForeignKeyViolation(err) {
		t.Fatalf("continuing another organization's episode: err = %v, want a foreign key violation", err)
	}
	_ = tx.Rollback(ctx)

	tx = beginTx(t, d)
	sub := tuple(hostA, "disk", "/", "used_pct")
	if err := st.Execute(ctx, tx, orgA, []alerts.Action{{
		Kind: alerts.ActionOpen, Subject: sub, At: now, Severity: rules.StateCrit,
		Adopt: &alerts.Adoption{From: prevA, SnoozeUntilChange: true},
	}}); err != nil {
		t.Fatalf("continue own episode: %v", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM alerts WHERE id = $1`, prevA); err != nil {
		t.Fatalf("delete the continued episode: %v", err)
	}
	open, err := st.OpenForHost(ctx, tx, orgA, hostA)
	if err != nil || len(open) != 1 {
		t.Fatalf("open for host = %+v, %v; want one alert", open, err)
	}
	if open[0].ContinuesAlertID != nil || open[0].OrgID != orgA {
		t.Fatalf("after deleting the continued episode: continues %v, org %s; want nil and %s", open[0].ContinuesAlertID, open[0].OrgID, orgA)
	}
}
