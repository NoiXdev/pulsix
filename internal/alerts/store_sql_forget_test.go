// CloseForgotten's own tests (backlog 3.12b): forgetting an item closes
// every open alert on it with closed_reason 'forgotten' and the same
// 'closed' event every other close writes — the event the notification
// follower reads, which is what makes routes treat a forget like a
// resolve. Like the Execute tests in store_sql_test.go, each runs on a
// caller-owned transaction, the shape the forget itself uses: the
// alert closes commit or roll back with the item's deletes.
package alerts_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/alerts"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/rules"
)

// openAlertID opens one alert on sub through Execute, inside tx, and
// returns its id.
func openAlertID(t *testing.T, st *alerts.SQLStore, tx pgx.Tx, org uuid.UUID, sub alerts.Subject) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	if err := st.Execute(ctx, tx, org, []alerts.Action{
		{Kind: alerts.ActionOpen, Subject: sub, At: time.Now(), Severity: rules.StateWarn},
	}); err != nil {
		t.Fatalf("execute open %+v: %v", sub, err)
	}
	open, err := st.OpenForHost(ctx, tx, org, sub.HostID)
	if err != nil {
		t.Fatalf("open for host: %v", err)
	}
	for _, a := range open {
		if a.Subject == sub {
			return a.ID
		}
	}
	t.Fatalf("no open alert on %+v after opening it", sub)
	return uuid.Nil
}

// closedEvents counts alertID's 'closed' events, read through q.
func closedEvents(t *testing.T, q gen.DBTX, alertID uuid.UUID) int {
	t.Helper()
	n := 0
	for _, k := range alertEventKinds(t, q, alertID) {
		if k == "closed" {
			n++
		}
	}
	return n
}

func TestCloseForgottenClosesEveryOpenAlertOfTheItem(t *testing.T) {
	d := testdb.Open(t)
	org, host := seedHost(t, d)
	st := alerts.NewSQLStore(d)
	ctx := context.Background()
	now := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)

	tx := beginTx(t, d)
	acked := openAlertID(t, st, tx, org, tuple(host, "disk", "/", "used_percent"))
	snoozed := openAlertID(t, st, tx, org, tuple(host, "disk", "/", "free_bytes"))
	plain := openAlertID(t, st, tx, org, tuple(host, "disk", "/", "inodes_percent"))
	otherItem := openAlertID(t, st, tx, org, tuple(host, "disk", "/home", "used_percent"))
	otherCheck := openAlertID(t, st, tx, org, tuple(host, "cpu", "", "used_percent"))
	hostScope := openAlertID(t, st, tx, org, alerts.Subject{HostID: host, Scope: alerts.ScopeHost})
	setAcknowledged(t, tx, acked, "warn")
	if _, err := tx.Exec(ctx, `UPDATE alerts SET snoozed_until = $2 WHERE id = $1`, snoozed, now.Add(time.Hour)); err != nil {
		t.Fatalf("snooze: %v", err)
	}

	n, err := st.CloseForgotten(ctx, tx, org, host, "disk", "/", now)
	if err != nil {
		t.Fatalf("close forgotten: %v", err)
	}
	if n != 3 {
		t.Fatalf("closed %d alerts, want 3 (open, acknowledged and snoozed alike)", n)
	}

	for name, id := range map[string]uuid.UUID{"acknowledged": acked, "snoozed": snoozed, "plain": plain} {
		row := alertRowByID(t, tx, id)
		if row.closedAt == nil || !row.closedAt.Equal(now) {
			t.Errorf("%s alert closed_at = %v, want %v", name, row.closedAt, now)
		}
		if row.closedReason == nil || *row.closedReason != string(alerts.Forgotten) {
			t.Errorf("%s alert closed_reason = %v, want forgotten", name, row.closedReason)
		}
		if got := closedEvents(t, tx, id); got != 1 {
			t.Errorf("%s alert has %d closed events, want exactly 1", name, got)
		}
	}
	for name, id := range map[string]uuid.UUID{"other item": otherItem, "other check": otherCheck, "host scope": hostScope} {
		if row := alertRowByID(t, tx, id); row.closedAt != nil {
			t.Errorf("%s alert was closed (%v), want it left open", name, row.closedReason)
		}
		if got := closedEvents(t, tx, id); got != 0 {
			t.Errorf("%s alert has %d closed events, want none", name, got)
		}
	}
}

// TestCloseForgottenLeavesClosedAlertsAlone: an episode that already
// ended keeps its own reason and its one closed event — forgetting the
// item afterwards is not a second ending.
func TestCloseForgottenLeavesClosedAlertsAlone(t *testing.T) {
	d := testdb.Open(t)
	org, host := seedHost(t, d)
	st := alerts.NewSQLStore(d)
	ctx := context.Background()

	tx := beginTx(t, d)
	id := openAlertID(t, st, tx, org, tuple(host, "disk", "/", "used_percent"))
	if err := st.Execute(ctx, tx, org, []alerts.Action{
		{Kind: alerts.ActionClose, AlertID: id, At: time.Now(), Reason: alerts.Resolved},
	}); err != nil {
		t.Fatalf("execute close: %v", err)
	}

	n, err := st.CloseForgotten(ctx, tx, org, host, "disk", "/", time.Now())
	if err != nil {
		t.Fatalf("close forgotten: %v", err)
	}
	if n != 0 {
		t.Fatalf("closed %d alerts, want 0", n)
	}
	if row := alertRowByID(t, tx, id); row.closedReason == nil || *row.closedReason != "resolved" {
		t.Fatalf("closed_reason = %v, want resolved (unchanged)", row.closedReason)
	}
	if got := closedEvents(t, tx, id); got != 1 {
		t.Fatalf("closed events = %d, want exactly 1", got)
	}
}

// TestCloseForgottenWholeHostItem: the whole-host item "" is an item like
// any other — it closes only the check's alerts with item "", never the
// same check's named items, and never a host-scope alert, whose own
// check_key and item are empty too.
func TestCloseForgottenWholeHostItem(t *testing.T) {
	d := testdb.Open(t)
	org, host := seedHost(t, d)
	st := alerts.NewSQLStore(d)
	ctx := context.Background()

	tx := beginTx(t, d)
	whole := openAlertID(t, st, tx, org, tuple(host, "cpu", "", "used_percent"))
	named := openAlertID(t, st, tx, org, tuple(host, "cpu", "core0", "used_percent"))
	hostScope := openAlertID(t, st, tx, org, alerts.Subject{HostID: host, Scope: alerts.ScopeHost})

	n, err := st.CloseForgotten(ctx, tx, org, host, "cpu", "", time.Now())
	if err != nil {
		t.Fatalf("close forgotten: %v", err)
	}
	if n != 1 {
		t.Fatalf("closed %d alerts, want 1", n)
	}
	if row := alertRowByID(t, tx, whole); row.closedReason == nil || *row.closedReason != "forgotten" {
		t.Fatalf("whole-host item alert closed_reason = %v, want forgotten", row.closedReason)
	}
	if row := alertRowByID(t, tx, named); row.closedAt != nil {
		t.Fatal("a named item of the same check was closed")
	}

	// Even asked for the empty check key, a host-scope alert is not an
	// item's alert.
	n, err = st.CloseForgotten(ctx, tx, org, host, "", "", time.Now())
	if err != nil {
		t.Fatalf("close forgotten with an empty check key: %v", err)
	}
	if n != 0 {
		t.Fatalf("closed %d alerts for an empty check key, want 0", n)
	}
	if row := alertRowByID(t, tx, hostScope); row.closedAt != nil {
		t.Fatal("the host-scope alert was closed")
	}
}

// TestCloseForgottenIsOrgScoped: the caller's transaction carries no
// pulsix.org_id (like the evaluator's), so the statement's own org_id
// predicate is the only tenant guard — host ids are unique, so without it
// org B could close org A's alerts by naming A's host.
func TestCloseForgottenIsOrgScoped(t *testing.T) {
	d := testdb.Open(t)
	orgA, hostA := seedHost(t, d)
	orgB, _ := seedHost(t, d)
	st := alerts.NewSQLStore(d)
	ctx := context.Background()

	tx := beginTx(t, d)
	id := openAlertID(t, st, tx, orgA, tuple(hostA, "disk", "/", "used_percent"))

	n, err := st.CloseForgotten(ctx, tx, orgB, hostA, "disk", "/", time.Now())
	if err != nil {
		t.Fatalf("close forgotten as org B: %v", err)
	}
	if n != 0 {
		t.Fatalf("org B closed %d of org A's alerts, want 0", n)
	}
	if row := alertRowByID(t, tx, id); row.closedAt != nil {
		t.Fatal("org A's alert was closed by org B")
	}
	if got := closedEvents(t, tx, id); got != 0 {
		t.Fatalf("org A's alert gained %d closed events, want none", got)
	}
}

// TestForgottenSubjectReopensAsAFreshEpisode is Review Focus 5's last
// clause: an item that reports again after being forgotten opens a NEW
// alert, unacknowledged, rather than reopening the forgotten one.
func TestForgottenSubjectReopensAsAFreshEpisode(t *testing.T) {
	d := testdb.Open(t)
	org, host := seedHost(t, d)
	st := alerts.NewSQLStore(d)
	ctx := context.Background()
	sub := tuple(host, "disk", "/", "used_percent")

	tx := beginTx(t, d)
	old := openAlertID(t, st, tx, org, sub)
	setAcknowledged(t, tx, old, "warn")
	if _, err := st.CloseForgotten(ctx, tx, org, host, "disk", "/", time.Now()); err != nil {
		t.Fatalf("close forgotten: %v", err)
	}

	fresh := openAlertID(t, st, tx, org, sub)
	if fresh == old {
		t.Fatal("the re-appearing item reopened the forgotten alert instead of opening a new one")
	}
	if row := alertRowByID(t, tx, fresh); row.closedAt != nil || row.ackAt != nil {
		t.Fatalf("fresh alert = %+v, want open and unacknowledged", row)
	}
	if row := alertRowByID(t, tx, old); row.closedReason == nil || *row.closedReason != "forgotten" {
		t.Fatalf("forgotten alert closed_reason = %v after the reopen, want forgotten (unchanged)", row.closedReason)
	}
}

// TestCloseForgottenRacingAConcurrentClose: two transactions end the
// same episode at once — the evaluator resolving it, a forget closing it.
// Whichever commits first wins and the other must not close it a second
// time: a second 'closed' event would notify the alert's routes twice.
// The statement's own closed_at IS NULL is re-checked by Postgres after
// the row lock is released (READ COMMITTED), which is what this pins.
func TestCloseForgottenRacingAConcurrentClose(t *testing.T) {
	d := testdb.Open(t)
	org, host := seedHost(t, d)
	st := alerts.NewSQLStore(d)
	ctx := context.Background()
	insertOpenAlert(t, d, org, host, "disk", "/", "used_percent")
	var id uuid.UUID
	if err := d.Pool().QueryRow(ctx, `SELECT id FROM alerts WHERE org_id = $1 AND host_id = $2`, org, host).Scan(&id); err != nil {
		t.Fatalf("read alert id: %v", err)
	}

	evaluator := beginTx(t, d)
	if err := st.Execute(ctx, evaluator, org, []alerts.Action{
		{Kind: alerts.ActionClose, AlertID: id, At: time.Now(), Reason: alerts.Resolved},
	}); err != nil {
		t.Fatalf("evaluator close: %v", err)
	}

	type result struct {
		n   int
		err error
	}
	done := make(chan result, 1)
	go func() {
		forget, err := d.Pool().Begin(ctx)
		if err != nil {
			done <- result{err: err}
			return
		}
		defer forget.Rollback(context.Background()) //nolint:errcheck // no-op once committed
		n, err := st.CloseForgotten(ctx, forget, org, host, "disk", "/", time.Now())
		if err == nil {
			err = forget.Commit(ctx)
		}
		done <- result{n: n, err: err}
	}()

	// The forget blocks on the evaluator's row lock; give it time to get
	// there before releasing it, so the test exercises the re-check rather
	// than a forget that simply ran second.
	select {
	case r := <-done:
		t.Fatalf("forget finished while the evaluator still held the row: n=%d err=%v", r.n, r.err)
	case <-time.After(300 * time.Millisecond):
	}
	if err := evaluator.Commit(ctx); err != nil {
		t.Fatalf("commit evaluator: %v", err)
	}
	r := <-done
	if r.err != nil {
		t.Fatalf("forget: %v", r.err)
	}
	if r.n != 0 {
		t.Fatalf("forget closed %d alerts, want 0 — the evaluator already closed it", r.n)
	}
	if row := alertRowByID(t, d.Pool(), id); row.closedReason == nil || *row.closedReason != "resolved" {
		t.Fatalf("closed_reason = %v, want resolved (the first close wins)", row.closedReason)
	}
	if got := closedEvents(t, d.Pool(), id); got != 1 {
		t.Fatalf("closed events = %d, want exactly 1", got)
	}
}

// TestCloseForgottenMetricClosesOnlyThatMetric (backlog 3.88): forgetting
// one metric of an item closes its open alert, and only its — the item's
// other metrics, the same metric of another item, another check's alert
// with the same metric name, another organization's and a host-scope
// alert all stay open. The close is the same 'forgotten' one with one
// 'closed' event.
func TestCloseForgottenMetricClosesOnlyThatMetric(t *testing.T) {
	d := testdb.Open(t)
	org, host := seedHost(t, d)
	orgB, _ := seedHost(t, d)
	st := alerts.NewSQLStore(d)
	ctx := context.Background()
	now := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)

	tx := beginTx(t, d)
	target := openAlertID(t, st, tx, org, tuple(host, "http", "", "http.dns_ms"))
	sibling := openAlertID(t, st, tx, org, tuple(host, "http", "", "http.total_ms"))
	otherItem := openAlertID(t, st, tx, org, tuple(host, "http", "api", "http.dns_ms"))
	otherCheck := openAlertID(t, st, tx, org, tuple(host, "web", "", "http.dns_ms"))
	hostScope := openAlertID(t, st, tx, org, alerts.Subject{HostID: host, Scope: alerts.ScopeHost})

	if n, err := st.CloseForgottenMetric(ctx, tx, orgB, host, "http", "", "http.dns_ms", now); err != nil || n != 0 {
		t.Fatalf("close forgotten metric as another organization = %d, %v; want 0, nil", n, err)
	}
	n, err := st.CloseForgottenMetric(ctx, tx, org, host, "http", "", "http.dns_ms", now)
	if err != nil {
		t.Fatalf("close forgotten metric: %v", err)
	}
	if n != 1 {
		t.Fatalf("closed %d alerts, want 1", n)
	}
	row := alertRowByID(t, tx, target)
	if row.closedAt == nil || !row.closedAt.Equal(now) || row.closedReason == nil || *row.closedReason != string(alerts.Forgotten) {
		t.Fatalf("target alert closed_at/reason = %v/%v, want %v/forgotten", row.closedAt, row.closedReason, now)
	}
	if got := closedEvents(t, tx, target); got != 1 {
		t.Fatalf("target alert has %d closed events, want 1", got)
	}
	for name, id := range map[string]uuid.UUID{"sibling metric": sibling, "other item": otherItem, "other check": otherCheck, "host scope": hostScope} {
		if row := alertRowByID(t, tx, id); row.closedAt != nil {
			t.Errorf("%s alert was closed (%v), want it left open", name, row.closedReason)
		}
	}
	// A second close finds nothing open: no second event.
	if n, err := st.CloseForgottenMetric(ctx, tx, org, host, "http", "", "http.dns_ms", now); err != nil || n != 0 {
		t.Fatalf("second close = %d, %v; want 0, nil", n, err)
	}
	if got := closedEvents(t, tx, target); got != 1 {
		t.Fatalf("target alert has %d closed events after a second close, want 1", got)
	}
}
