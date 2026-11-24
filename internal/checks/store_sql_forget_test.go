// SQLStore's forget and expiry (backlog 3.12b) against the real database:
// what a forget deletes and what it leaves, how it serialises with the rule
// evaluator and with ingest (Review Focus 1, and the controller's ruling
// that the forget takes the evaluator's own per-host lock), the tuple cap
// after a forget (Review Focus 4) and the expiry selection's boundaries
// (Review Focus 2 and 3).
//
// The races are made deterministic without sleeping: one side holds its
// transaction open, and the test polls pg_blocking_pids until the other
// side is seen waiting on it.
package checks_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/agents"
	"github.com/noixdev/pulsix/internal/alerts"
	"github.com/noixdev/pulsix/internal/checks"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/items"
	"github.com/noixdev/pulsix/internal/rules"
)

// putCurrent writes one check_current row for (key, item, metric) at at,
// straight into the table, and marks the host's evaluation cursor the way
// every ingest does — a host with current values always has one.
func putCurrent(t *testing.T, d *db.DB, org, host uuid.UUID, key, item, metric string, at time.Time) {
	t.Helper()
	q := gen.New(d.Pool())
	if err := q.UpsertCheckCurrent(ctx, gen.UpsertCheckCurrentParams{
		OrgID: org, HostID: host, CheckKey: key, Item: item, Metric: metric, Value: f64(1), At: at,
	}); err != nil {
		t.Fatal(err)
	}
	if err := q.MarkHostDirty(ctx, gen.MarkHostDirtyParams{OrgID: org, HostID: host}); err != nil {
		t.Fatal(err)
	}
}

// putRuleState writes one check_states row and one check_state_changes
// row for the tuple, through the evaluator's own statements, on q.
func putRuleState(t *testing.T, q *gen.Queries, org, host uuid.UUID, key, item, metric string) {
	t.Helper()
	now := time.Now().UTC()
	if err := q.UpsertCheckState(ctx, gen.UpsertCheckStateParams{
		OrgID: org, HostID: host, CheckKey: key, Item: item, Metric: metric,
		State: "warn", Since: now, EvaluatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := q.InsertCheckStateChange(ctx, gen.InsertCheckStateChangeParams{
		At: now, OrgID: org, HostID: host, CheckKey: key, Item: item, Metric: metric,
		FromState: "ok", ToState: "warn",
	}); err != nil {
		t.Fatal(err)
	}
}

// itemRows counts host's rows of (key, item) in table.
func itemRows(t *testing.T, d *db.DB, table string, host uuid.UUID, key, item string) int {
	t.Helper()
	var n int
	if err := d.Pool().QueryRow(ctx, `SELECT count(*) FROM `+pgx.Identifier{table}.Sanitize()+
		` WHERE host_id = $1 AND check_key = $2 AND item = $3`, host, key, item).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// ruleRows is an item's check_states plus check_state_changes rows.
func ruleRows(t *testing.T, d *db.DB, host uuid.UUID, key, item string) int {
	t.Helper()
	return itemRows(t, d, "check_states", host, key, item) + itemRows(t, d, "check_state_changes", host, key, item)
}

// openAlert opens one warn alert on the tuple through the alerts store, on
// tx, and returns its id.
func openAlert(t *testing.T, d *db.DB, tx pgx.Tx, org, host uuid.UUID, key, item, metric string) uuid.UUID {
	t.Helper()
	st := alerts.NewSQLStore(d)
	sub := alerts.Subject{HostID: host, Scope: alerts.ScopeTuple, CheckKey: key, Item: item, Metric: metric}
	if err := st.Execute(ctx, tx, org, []alerts.Action{
		{Kind: alerts.ActionOpen, Subject: sub, At: time.Now(), Severity: rules.StateWarn},
	}); err != nil {
		t.Fatal(err)
	}
	open, err := st.OpenForHost(ctx, tx, org, host)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range open {
		if a.Subject == sub {
			return a.ID
		}
	}
	t.Fatalf("no open alert on %+v", sub)
	return uuid.Nil
}

// openAlertCommitted is openAlert in a transaction of its own, committed.
func openAlertCommitted(t *testing.T, d *db.DB, org, host uuid.UUID, key, item, metric string) uuid.UUID {
	t.Helper()
	tx, err := d.Pool().Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op once committed
	id := openAlert(t, d, tx, org, host, key, item, metric)
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return id
}

// alertClose reads one alert's closed reason ("" while open) and how many
// 'closed' events it has.
func alertClose(t *testing.T, d *db.DB, id uuid.UUID) (reason string, closedEvents int) {
	t.Helper()
	var r *string
	if err := d.Pool().QueryRow(ctx, `SELECT closed_reason FROM alerts WHERE id = $1`, id).Scan(&r); err != nil {
		t.Fatal(err)
	}
	if err := d.Pool().QueryRow(ctx,
		`SELECT count(*) FROM alert_events WHERE alert_id = $1 AND kind = 'closed'`, id).Scan(&closedEvents); err != nil {
		t.Fatal(err)
	}
	if r != nil {
		reason = *r
	}
	return reason, closedEvents
}

// newForgetService is the service under test: the real store, the real
// alerts service as its alert closer, and a fixed clock.
func newForgetService(d *db.DB, now time.Time) *checks.Service {
	return checks.NewService(checks.NewSQLStore(d), fixedClock(now),
		checks.WithAlertCloser(alerts.NewService(alerts.NewSQLStore(d), func() time.Time { return now })))
}

// beginHolder opens a transaction on the bare pool, as the evaluator and
// the alert sweep do, and returns it with its backend pid.
func beginHolder(t *testing.T, d *db.DB) (pgx.Tx, int32) {
	t.Helper()
	tx, err := d.Pool().Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	var pid int32
	if err := tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	return tx, pid
}

// waitBlockedBy polls until some backend is waiting on a lock pid holds —
// the deterministic "the other side has reached the lock" signal. It
// fails the test after a generous bound rather than hanging.
func waitBlockedBy(t *testing.T, d *db.DB, pid int32) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		var n int
		if err := d.Pool().QueryRow(ctx,
			`SELECT count(*) FROM pg_stat_activity WHERE $1 = ANY(pg_blocking_pids(pid))`, pid).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n > 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("nothing came to wait on backend %d", pid)
}

// firstSeen reads the item's check_items row; ok is false when it has none.
func firstSeen(t *testing.T, d *db.DB, host uuid.UUID, key, item string) (time.Time, bool) {
	t.Helper()
	var at time.Time
	err := d.Pool().QueryRow(ctx,
		`SELECT first_seen FROM check_items WHERE host_id = $1 AND check_key = $2 AND item = $3`, host, key, item).Scan(&at)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, false
	}
	if err != nil {
		t.Fatal(err)
	}
	return at, true
}

// noChecks is a HostChecks with nothing resolved: enough for the
// evaluator's Load to read a host's tuples and run its discovery.
type noChecks struct{}

func (noChecks) ContextForHost(context.Context, uuid.UUID, uuid.UUID) (rules.CheckContext, error) {
	return rules.CheckContext{}, nil
}

// loadHost runs the evaluator's own Load for host, which is where items
// are discovered into check_items.
func loadHost(t *testing.T, d *db.DB, org, host uuid.UUID) {
	t.Helper()
	if _, err := rules.NewEvalSQLStore(d, noChecks{}, items.NewSQLStore(d), rules.AgentOnlineWindow{Floor: agents.OnlineWindowFloor, HeartbeatsPerWindow: agents.HeartbeatsPerOnlineWindow}, nil).
		Load(ctx, rules.Due{OrgID: org, HostID: host}, time.Now()); err != nil {
		t.Fatalf("evaluator load: %v", err)
	}
}

// asyncForget runs ForgetItem on its own goroutine.
func asyncForget(svc *checks.Service, org, host uuid.UUID, key, item string) <-chan error {
	done := make(chan error, 1)
	go func() { done <- svc.ForgetItem(context.Background(), org, host, key, item) }()
	return done
}

// TestForgetItemRemovesOneItemOnly: every metric of the item loses its
// current value, rule state and state-change history; its open alert is
// closed as forgotten; its samples stay. A sibling item of the same check,
// another check's whole-host item, and the same item on another host are
// all untouched.
func TestForgetItemRemovesOneItemOnly(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "checks-forget-one")
	host := seedHost(t, d, org, "forget-host")
	other := seedHost(t, d, org, "forget-other-host")
	now := time.Now().UTC().Truncate(time.Second)
	svc := newForgetService(d, now)
	scoped := db.WithOrgScope(ctx, org)

	if _, err := svc.RecordObservations(scoped, org, []checks.Observation{
		{HostID: host, CheckKey: "disk", Item: "/mnt/old", Metric: "used_pct", At: now, Value: f64(91)},
		{HostID: host, CheckKey: "disk", Item: "/mnt/old", Metric: "free", At: now, Value: f64(5)},
		{HostID: host, CheckKey: "disk", Item: "/", Metric: "used_pct", At: now, Value: f64(40)},
		{HostID: host, CheckKey: "cpu", Item: "", Metric: "usage", At: now, Value: f64(3)},
		{HostID: other, CheckKey: "disk", Item: "/mnt/old", Metric: "used_pct", At: now, Value: f64(1)},
	}); err != nil {
		t.Fatal(err)
	}
	q := gen.New(d.Pool())
	for _, tu := range [][3]string{{"disk", "/mnt/old", "used_pct"}, {"disk", "/mnt/old", "free"}, {"disk", "/", "used_pct"}, {"cpu", "", "usage"}} {
		putRuleState(t, q, org, host, tu[0], tu[1], tu[2])
	}
	putRuleState(t, q, org, other, "disk", "/mnt/old", "used_pct")
	loadHost(t, d, org, host)
	loadHost(t, d, org, other)
	for _, it := range []struct {
		host uuid.UUID
		item string
	}{{host, "/mnt/old"}, {host, "/"}, {other, "/mnt/old"}} {
		if _, ok := firstSeen(t, d, it.host, "disk", it.item); !ok {
			t.Fatalf("setup: %q was not discovered", it.item)
		}
	}
	forgotten := openAlertCommitted(t, d, org, host, "disk", "/mnt/old", "used_pct")
	sibling := openAlertCommitted(t, d, org, host, "disk", "/", "used_pct")
	dirtyBefore, _ := cursorDirtyAt(t, d, host)

	if err := svc.ForgetItem(scoped, org, host, "disk", "/mnt/old"); err != nil {
		t.Fatalf("forget item: %v", err)
	}

	if n := itemRows(t, d, "check_current", host, "disk", "/mnt/old"); n != 0 {
		t.Errorf("forgotten item keeps %d current rows", n)
	}
	if n := ruleRows(t, d, host, "disk", "/mnt/old"); n != 0 {
		t.Errorf("forgotten item keeps %d rule rows (states and changes)", n)
	}
	if n := itemRows(t, d, "check_samples", host, "disk", "/mnt/old"); n != 2 {
		t.Errorf("forgotten item has %d samples, want both kept", n)
	}
	for _, it := range [][2]string{{"disk", "/"}, {"cpu", ""}} {
		if n := itemRows(t, d, "check_current", host, it[0], it[1]); n != 1 {
			t.Errorf("sibling %v has %d current rows, want 1", it, n)
		}
		if n := ruleRows(t, d, host, it[0], it[1]); n != 2 {
			t.Errorf("sibling %v has %d rule rows, want 2", it, n)
		}
	}
	if n := itemRows(t, d, "check_current", other, "disk", "/mnt/old"); n != 1 {
		t.Errorf("same item on another host has %d current rows, want 1", n)
	}
	if n := ruleRows(t, d, other, "disk", "/mnt/old"); n != 2 {
		t.Errorf("same item on another host has %d rule rows, want 2", n)
	}
	if _, ok := firstSeen(t, d, host, "disk", "/mnt/old"); ok {
		t.Errorf("forgotten item keeps its check_items row")
	}
	if _, ok := firstSeen(t, d, host, "disk", "/"); !ok {
		t.Errorf("sibling item lost its check_items row")
	}
	if _, ok := firstSeen(t, d, other, "disk", "/mnt/old"); !ok {
		t.Errorf("same item on another host lost its check_items row")
	}
	if reason, events := alertClose(t, d, forgotten); reason != string(alerts.Forgotten) || events != 1 {
		t.Errorf("forgotten item's alert: reason %q with %d closed events, want forgotten with 1", reason, events)
	}
	if reason, _ := alertClose(t, d, sibling); reason != "" {
		t.Errorf("sibling's alert closed as %q, want it open", reason)
	}
	if after, _ := cursorDirtyAt(t, d, host); !after.After(dirtyBefore) {
		t.Errorf("dirty_at = %v, want it moved past %v so the roll-up is re-evaluated", after, dirtyBefore)
	}
}

// TestForgottenItemIsDiscoveredAgain: an item that reports again after a
// forget is a new item — once the host is evaluated, check_items has it
// with a new first_seen, not the one from before the forget.
func TestForgottenItemIsDiscoveredAgain(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "checks-forget-rediscover")
	host := seedHost(t, d, org, "forget-rediscover-host")
	now := time.Now().UTC().Truncate(time.Second)
	svc := newForgetService(d, now)
	scoped := db.WithOrgScope(ctx, org)
	obs := []checks.Observation{{HostID: host, CheckKey: "docker", Item: "build-9", Metric: "running", At: now, Value: f64(1)}}

	if _, err := svc.RecordObservations(scoped, org, obs); err != nil {
		t.Fatal(err)
	}
	loadHost(t, d, org, host)
	before, ok := firstSeen(t, d, host, "docker", "build-9")
	if !ok {
		t.Fatal("setup: the item was not discovered")
	}
	if err := svc.ForgetItem(scoped, org, host, "docker", "build-9"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RecordObservations(scoped, org, obs); err != nil {
		t.Fatal(err)
	}
	loadHost(t, d, org, host)
	after, ok := firstSeen(t, d, host, "docker", "build-9")
	if !ok {
		t.Fatal("the item that reported again was not discovered again")
	}
	if !after.After(before) {
		t.Fatalf("first_seen = %v after the reappearance, want later than the forgotten item's %v", after, before)
	}
}

// TestForgetItemNotFound: an item with no current row, an item of another
// organization, and a second forget of the same item are all ErrNotFound,
// and a not-found forget changes nothing — not even the host's dirty mark.
func TestForgetItemNotFound(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "checks-forget-missing")
	orgB := seedOrg(t, d, "checks-forget-missing-b")
	host := seedHost(t, d, org, "forget-missing-host")
	now := time.Now().UTC().Truncate(time.Second)
	svc := newForgetService(d, now)
	putCurrent(t, d, org, host, "disk", "/", "used_pct", now)
	dirtyBefore, _ := cursorDirtyAt(t, d, host)

	if err := svc.ForgetItem(db.WithOrgScope(ctx, org), org, host, "disk", "/nope"); !errors.Is(err, checks.ErrNotFound) {
		t.Fatalf("unknown item = %v, want ErrNotFound", err)
	}
	if err := svc.ForgetItem(db.WithOrgScope(ctx, orgB), orgB, host, "disk", "/"); !errors.Is(err, checks.ErrNotFound) {
		t.Fatalf("another organization's item = %v, want ErrNotFound", err)
	}
	if n := itemRows(t, d, "check_current", host, "disk", "/"); n != 1 {
		t.Fatalf("a not-found forget removed rows: %d left", n)
	}
	if after, _ := cursorDirtyAt(t, d, host); !after.Equal(dirtyBefore) {
		t.Fatalf("a not-found forget moved dirty_at from %v to %v", dirtyBefore, after)
	}
	if err := svc.ForgetItem(db.WithOrgScope(ctx, org), org, host, "disk", "/"); err != nil {
		t.Fatal(err)
	}
	if err := svc.ForgetItem(db.WithOrgScope(ctx, org), org, host, "disk", "/"); !errors.Is(err, checks.ErrNotFound) {
		t.Fatalf("second forget = %v, want ErrNotFound", err)
	}
}

// TestForgetWaitsForARunningEvaluation is the controller's ruling: the
// forget takes the evaluator's per-host lock (LockOneEvalCursor's row
// lock on check_eval_cursor). An evaluation that holds it and writes the
// item's state, its history and an alert is waited for; the forget then
// removes all of it and closes the alert exactly once. Without the lock
// the forget would finish first and the evaluation would commit a state
// and an open alert for an item that is gone.
func TestForgetWaitsForARunningEvaluation(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "checks-forget-eval")
	host := seedHost(t, d, org, "forget-eval-host")
	now := time.Now().UTC().Truncate(time.Second)
	svc := newForgetService(d, now)
	putCurrent(t, d, org, host, "docker", "build-7", "running", now)

	eval, pid := beginHolder(t, d)
	if _, err := gen.New(eval).LockOneEvalCursor(ctx, host); err != nil {
		t.Fatalf("the evaluator's claim: %v", err)
	}

	done := asyncForget(svc, org, host, "docker", "build-7")
	waitBlockedBy(t, d, pid)

	putRuleState(t, gen.New(eval), org, host, "docker", "build-7", "running")
	alertID := openAlert(t, d, eval, org, host, "docker", "build-7", "running")
	if err := eval.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("forget: %v", err)
	}

	if n := ruleRows(t, d, host, "docker", "build-7"); n != 0 {
		t.Errorf("the evaluation's rule rows survived the forget: %d", n)
	}
	if n := itemRows(t, d, "check_current", host, "docker", "build-7"); n != 0 {
		t.Errorf("current rows survived: %d", n)
	}
	if reason, events := alertClose(t, d, alertID); reason != string(alerts.Forgotten) || events != 1 {
		t.Errorf("the evaluation's alert: reason %q with %d closed events, want forgotten with exactly 1", reason, events)
	}
}

// TestEvaluationSkipsAHostBeingForgotten: the other order. While the
// forget holds the host's lock, the evaluator's own claim statement finds
// the host taken and skips it — it never evaluates in the middle of a
// forget — and the host is left dirty, so it is evaluated after.
func TestEvaluationSkipsAHostBeingForgotten(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "checks-forget-eval-skip")
	host := seedHost(t, d, org, "forget-eval-skip-host")
	now := time.Now().UTC().Truncate(time.Second)
	putCurrent(t, d, org, host, "docker", "build-8", "running", now)
	st := checks.NewSQLStore(d)

	var claimErr error
	forgot, err := st.ForgetItem(ctx, checks.ItemRef{OrgID: org, HostID: host, CheckKey: "docker", Item: "build-8"},
		checks.ForgetSteps{Within: func(pgx.Tx) error {
			_, claimErr = gen.New(d.Pool()).LockOneEvalCursor(ctx, host)
			return nil
		}})
	if err != nil || !forgot {
		t.Fatalf("forget = %v, %v", forgot, err)
	}
	if !errors.Is(claimErr, pgx.ErrNoRows) {
		t.Fatalf("the evaluator's claim during a forget = %v, want no row (skipped)", claimErr)
	}
	if _, err := gen.New(d.Pool()).LockOneEvalCursor(ctx, host); err != nil {
		t.Fatalf("after the forget the host must still be due for evaluation: %v", err)
	}
}

// TestIngestCommittedDuringTheForgetsWaitLandsBefore is Review Focus 1, one
// way round: an ingest transaction that has written a reading for the item
// and marked the host (so it holds the cursor) commits while the forget
// waits for it. The forget then removes that reading too.
func TestIngestCommittedDuringTheForgetsWaitLandsBefore(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "checks-forget-ingest-before")
	host := seedHost(t, d, org, "forget-ingest-before-host")
	now := time.Now().UTC().Truncate(time.Second)
	svc := newForgetService(d, now)
	putCurrent(t, d, org, host, "docker", "web", "running", now.Add(-time.Minute))

	ingest, pid := beginHolder(t, d)
	q := gen.New(ingest)
	if _, err := q.UpdateCheckCurrent(ctx, gen.UpdateCheckCurrentParams{
		OrgID: org, HostID: host, CheckKey: "docker", Item: "web", Metric: "running", Value: f64(1), At: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := q.UpsertCheckCurrent(ctx, gen.UpsertCheckCurrentParams{
		OrgID: org, HostID: host, CheckKey: "docker", Item: "web", Metric: "restarts", Value: f64(0), At: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := q.MarkHostDirty(ctx, gen.MarkHostDirtyParams{OrgID: org, HostID: host}); err != nil {
		t.Fatal(err)
	}

	done := asyncForget(svc, org, host, "docker", "web")
	waitBlockedBy(t, d, pid)
	if err := ingest.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("forget: %v", err)
	}
	if n := itemRows(t, d, "check_current", host, "docker", "web"); n != 0 {
		t.Fatalf("%d current rows survived; a reading committed before the forget's deletes must be removed", n)
	}
}

// TestIngestRacingTheForgetLandsAfter is Review Focus 1, the other way
// round — and the deadlock the lock order has to avoid. The ingest has
// updated one of the item's rows (holding its row lock) but has not yet
// reached MarkHostDirty when the forget takes the cursor. The ingest then
// waits for the forget on the cursor; were the forget to wait for the
// ingest's row in turn, Postgres would kill one of them. Instead the
// forget leaves that row to the ingest, which commits after it: the item
// comes back with exactly that reading, and no rule state is left over
// from before.
func TestIngestRacingTheForgetLandsAfter(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "checks-forget-ingest-after")
	host := seedHost(t, d, org, "forget-ingest-after-host")
	now := time.Now().UTC().Truncate(time.Second)
	putCurrent(t, d, org, host, "docker", "web", "running", now.Add(-time.Minute))
	putCurrent(t, d, org, host, "docker", "web", "restarts", now.Add(-time.Minute))
	putRuleState(t, gen.New(d.Pool()), org, host, "docker", "web", "running")
	st := checks.NewSQLStore(d)

	ingest, pid := beginHolder(t, d)
	q := gen.New(ingest)
	if _, err := q.UpdateCheckCurrent(ctx, gen.UpdateCheckCurrentParams{
		OrgID: org, HostID: host, CheckKey: "docker", Item: "web", Metric: "running", Value: f64(1), At: now,
	}); err != nil {
		t.Fatal(err)
	}
	ingestDone := make(chan error, 1)

	// Bounded: a forget that waited for the ingest's row instead of
	// leaving it would wait for ever here (the ingest is parked until the
	// forget's Within step), so the bound turns that into a failure.
	bounded, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	forgot, err := st.ForgetItem(bounded, checks.ItemRef{OrgID: org, HostID: host, CheckKey: "docker", Item: "web"},
		checks.ForgetSteps{Within: func(pgx.Tx) error {
			// The forget holds the cursor now; the ingest reaches its
			// MarkHostDirty and has to wait for the forget's commit.
			go func() {
				if err := q.MarkHostDirty(ctx, gen.MarkHostDirtyParams{OrgID: org, HostID: host}); err != nil {
					ingestDone <- err
					return
				}
				ingestDone <- ingest.Commit(ctx)
			}()
			waitBlockedByAny(t, d, pid)
			return nil
		}})
	if err != nil || !forgot {
		t.Fatalf("forget = %v, %v", forgot, err)
	}
	if err := <-ingestDone; err != nil {
		t.Fatalf("the ingest racing the forget failed: %v", err)
	}

	if n := itemRows(t, d, "check_current", host, "docker", "web"); n != 1 {
		t.Fatalf("item has %d current rows, want exactly the reading that landed after the forget", n)
	}
	if n := ruleRows(t, d, host, "docker", "web"); n != 0 {
		t.Fatalf("item has %d rule rows from before the forget, want none", n)
	}
	if _, err := gen.New(d.Pool()).LockOneEvalCursor(ctx, host); err != nil {
		t.Fatalf("the re-created item must leave its host due for evaluation: %v", err)
	}
}

// waitBlockedByAny polls until the ingest backend pid is itself waiting on
// a lock — here, on the cursor the forget holds.
func waitBlockedByAny(t *testing.T, d *db.DB, pid int32) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		var n int
		if err := d.Pool().QueryRow(ctx,
			`SELECT cardinality(pg_blocking_pids($1))`, pid).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n > 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("backend %d never came to wait on a lock", pid)
}

// TestForgetFreesTheTupleCap is Review Focus 4: a host at
// MaxTuplesPerHost refuses new tuples; forgetting one item drops its tuple
// count, and the next new tuple is accepted again through the ingest's own
// cap path.
func TestForgetFreesTheTupleCap(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "checks-forget-cap")
	host := seedHost(t, d, org, "forget-cap-host")
	seedTuples(t, d, org, host, checks.MaxTuplesPerHost)
	if err := gen.New(d.Pool()).MarkHostDirty(ctx, gen.MarkHostDirtyParams{OrgID: org, HostID: host}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	svc := newForgetService(d, now)
	scoped := db.WithOrgScope(ctx, org)
	fresh := []checks.Observation{{HostID: host, CheckKey: "docker", Item: "new", Metric: "running", At: now, Value: f64(1)}}

	if res, err := svc.RecordObservations(scoped, org, fresh); err != nil || res.OverCap != 1 {
		t.Fatalf("at the cap: %+v, %v, want the new tuple over the cap", res, err)
	}
	if err := svc.ForgetItem(scoped, org, host, "seed", "item-1"); err != nil {
		t.Fatal(err)
	}
	if got := tupleCount(t, d, host); got != checks.MaxTuplesPerHost-1 {
		t.Fatalf("after the forget the host holds %d tuples, want %d", got, checks.MaxTuplesPerHost-1)
	}
	if res, err := svc.RecordObservations(scoped, org, fresh); err != nil || res.Accepted != 1 || res.OverCap != 0 {
		t.Fatalf("after the forget: %+v, %v, want the new tuple accepted", res, err)
	}
}

// expiredSet runs the store's selection and returns its items as a set.
func expiredSet(t *testing.T, d *db.DB, cutoff time.Time, period time.Duration, limit int) map[checks.ItemRef]bool {
	t.Helper()
	refs, err := checks.NewSQLStore(d).ExpiredItems(ctx, cutoff, period, limit)
	if err != nil {
		t.Fatal(err)
	}
	set := map[checks.ItemRef]bool{}
	for _, r := range refs {
		set[r] = true
	}
	return set
}

// TestExpiredItemsBoundaries is the selection's rule at its edges, one host
// per case so the hosts' newest readings do not mix: exactly at the period
// is not due and a second past it is; a host whose newest reading is
// exactly the item's newest plus the period is not due and a second later
// is; a host offline for longer than the period keeps everything (Review
// Focus 2); an item with one fresh metric is kept whole (Review Focus 3);
// and the whole-host item "" expires like any other.
func TestExpiredItemsBoundaries(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "checks-expiry-edges")
	now := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	period := 168 * time.Hour
	cutoff := now.Add(-period)
	sec := time.Second

	type reading struct {
		item, metric string
		at           time.Time
	}
	cases := []struct {
		name     string
		readings []reading
		due      map[string]bool // item -> due
	}{
		{"exactly at the period", []reading{{"c", "m", cutoff}, {"live", "m", now}}, map[string]bool{"c": false, "live": false}},
		{"a second past the period", []reading{{"c", "m", cutoff.Add(-sec)}, {"live", "m", now}}, map[string]bool{"c": true}},
		// The host's newest reading lies an hour in the future, so the
		// second half holds either way and only the first half decides.
		{"first half alone, exactly at the period", []reading{{"c", "m", cutoff}, {"live", "m", now.Add(time.Hour)}}, map[string]bool{"c": false}},
		{"first half alone, a second past it", []reading{{"c", "m", cutoff.Add(-sec)}, {"live", "m", now.Add(time.Hour)}}, map[string]bool{"c": true}},
		{"host newest exactly item + period", []reading{{"c", "m", cutoff.Add(-time.Hour)}, {"live", "m", cutoff.Add(-time.Hour).Add(period)}}, map[string]bool{"c": false}},
		{"host newest a second later", []reading{{"c", "m", cutoff.Add(-time.Hour)}, {"live", "m", cutoff.Add(-time.Hour).Add(period + sec)}}, map[string]bool{"c": true}},
		// Every reading stopped together, 40 days ago: the host went offline,
		// nothing on it disappeared.
		{"offline host", []reading{{"c", "m", now.Add(-40 * 24 * time.Hour)}, {"live", "m", now.Add(-40*24*time.Hour + time.Hour)}}, map[string]bool{"c": false, "live": false}},
		{"one fresh metric keeps the item", []reading{{"c", "old", now.Add(-30 * 24 * time.Hour)}, {"c", "fresh", now}}, map[string]bool{"c": false}},
		{"whole-host item", []reading{{"", "m", cutoff.Add(-sec)}, {"live", "m", now}}, map[string]bool{"": true, "live": false}},
	}
	hosts := make([]uuid.UUID, len(cases))
	for i, tc := range cases {
		hosts[i] = seedHost(t, d, org, "expiry-"+uuid.NewString()[:8])
		for _, r := range tc.readings {
			putCurrent(t, d, org, hosts[i], "docker", r.item, r.metric, r.at)
		}
	}

	got := expiredSet(t, d, cutoff, period, 100)
	for i, tc := range cases {
		for item, want := range tc.due {
			ref := checks.ItemRef{OrgID: org, HostID: hosts[i], CheckKey: "docker", Item: item}
			if got[ref] != want {
				t.Errorf("%s: item %q due = %v, want %v", tc.name, item, got[ref], want)
			}
		}
	}
}

// TestExpiredItemsHonoursTheLimitOldestFirst: three due items, a limit of
// two — the two longest gone come back.
func TestExpiredItemsHonoursTheLimitOldestFirst(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "checks-expiry-limit")
	host := seedHost(t, d, org, "expiry-limit-host")
	now := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	period := 168 * time.Hour
	putCurrent(t, d, org, host, "docker", "live", "m", now)
	for i, age := range []time.Duration{10, 30, 20} {
		putCurrent(t, d, org, host, "docker", []string{"ten", "thirty", "twenty"}[i], "m", now.Add(-age*24*time.Hour))
	}

	got := expiredSet(t, d, now.Add(-period), period, 2)
	want := map[checks.ItemRef]bool{
		{OrgID: org, HostID: host, CheckKey: "docker", Item: "thirty"}: true,
		{OrgID: org, HostID: host, CheckKey: "docker", Item: "twenty"}: true,
	}
	if len(got) != 2 || !got[checks.ItemRef{OrgID: org, HostID: host, CheckKey: "docker", Item: "thirty"}] ||
		!got[checks.ItemRef{OrgID: org, HostID: host, CheckKey: "docker", Item: "twenty"}] {
		t.Fatalf("selection = %v, want %v", got, want)
	}
}

// TestForgetExpiredAcrossOrganizations: the hourly expiry runs with no
// organization bound, finds and forgets due items in every organization,
// and counts them per organization; a fresh item is not touched.
func TestForgetExpiredAcrossOrganizations(t *testing.T) {
	d := testdb.Open(t)
	orgA := seedOrg(t, d, "checks-expiry-a")
	orgB := seedOrg(t, d, "checks-expiry-b")
	hostA := seedHost(t, d, orgA, "expiry-a-host")
	hostB := seedHost(t, d, orgB, "expiry-b-host")
	now := time.Now().UTC().Truncate(time.Second)
	period := 2 * time.Hour
	gone := now.Add(-3 * time.Hour)
	putCurrent(t, d, orgA, hostA, "docker", "a1", "running", gone)
	putCurrent(t, d, orgA, hostA, "docker", "a2", "running", gone)
	putCurrent(t, d, orgA, hostA, "cpu", "", "usage", now)
	putCurrent(t, d, orgB, hostB, "docker", "b1", "running", gone)
	putCurrent(t, d, orgB, hostB, "cpu", "", "usage", now)
	alertID := openAlertCommitted(t, d, orgB, hostB, "docker", "b1", "running")

	counts, err := newForgetService(d, now).ForgetExpired(context.Background(), period, 100)
	if err != nil {
		t.Fatalf("forget expired: %v", err)
	}
	if counts[orgA] != 2 || counts[orgB] != 1 || len(counts) != 2 {
		t.Fatalf("counts = %v, want 2 in A and 1 in B", counts)
	}
	for _, c := range []struct {
		host      uuid.UUID
		key, item string
		want      int
	}{{hostA, "docker", "a1", 0}, {hostA, "docker", "a2", 0}, {hostB, "docker", "b1", 0}, {hostA, "cpu", "", 1}, {hostB, "cpu", "", 1}} {
		if n := itemRows(t, d, "check_current", c.host, c.key, c.item); n != c.want {
			t.Errorf("%s/%q has %d current rows, want %d", c.key, c.item, n, c.want)
		}
	}
	if reason, events := alertClose(t, d, alertID); reason != string(alerts.Forgotten) || events != 1 {
		t.Errorf("expired item's alert: %q with %d closed events, want forgotten with 1", reason, events)
	}
}

// TestForgetItemProceedSeesTheNewestReadingsAndCanDecline: the expiry's
// re-check runs inside the forget transaction with the item's and the
// host's newest readings, and a decline leaves everything as it was.
func TestForgetItemProceedSeesTheNewestReadingsAndCanDecline(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "checks-forget-proceed")
	host := seedHost(t, d, org, "forget-proceed-host")
	now := time.Now().UTC().Truncate(time.Second)
	old := now.Add(-time.Hour)
	putCurrent(t, d, org, host, "docker", "web", "running", old)
	putCurrent(t, d, org, host, "docker", "web", "restarts", old.Add(-time.Minute))
	putCurrent(t, d, org, host, "cpu", "", "usage", now)
	dirtyBefore, _ := cursorDirtyAt(t, d, host)

	var seen checks.ItemNewest
	within := false
	forgot, err := checks.NewSQLStore(d).ForgetItem(ctx, checks.ItemRef{OrgID: org, HostID: host, CheckKey: "docker", Item: "web"},
		checks.ForgetSteps{
			Proceed: func(n checks.ItemNewest) bool { seen = n; return false },
			Within:  func(pgx.Tx) error { within = true; return nil },
		})
	if err != nil || forgot {
		t.Fatalf("declined forget = %v, %v, want false, nil", forgot, err)
	}
	if seen.Rows != 2 || !seen.Item.Equal(old) || !seen.Host.Equal(now) {
		t.Fatalf("Proceed saw %+v, want 2 rows, item newest %v, host newest %v", seen, old, now)
	}
	if within {
		t.Fatal("Within ran for a declined forget")
	}
	if n := itemRows(t, d, "check_current", host, "docker", "web"); n != 2 {
		t.Fatalf("a declined forget removed rows: %d left", n)
	}
	if after, _ := cursorDirtyAt(t, d, host); !after.Equal(dirtyBefore) {
		t.Fatalf("a declined forget moved dirty_at")
	}
}

// TestForgetItemRollsBackWhenWithinFails: the alert close failing undoes
// every delete — the item's deletes and its alert close are one
// transaction.
func TestForgetItemRollsBackWhenWithinFails(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "checks-forget-rollback")
	host := seedHost(t, d, org, "forget-rollback-host")
	now := time.Now().UTC().Truncate(time.Second)
	putCurrent(t, d, org, host, "docker", "web", "running", now)
	putRuleState(t, gen.New(d.Pool()), org, host, "docker", "web", "running")
	boom := errors.New("boom")

	_, err := checks.NewSQLStore(d).ForgetItem(ctx, checks.ItemRef{OrgID: org, HostID: host, CheckKey: "docker", Item: "web"},
		checks.ForgetSteps{Within: func(pgx.Tx) error { return boom }})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want it to wrap the Within failure", err)
	}
	if n := itemRows(t, d, "check_current", host, "docker", "web"); n != 1 {
		t.Fatalf("current rows after a failed forget = %d, want 1", n)
	}
	if n := ruleRows(t, d, host, "docker", "web"); n != 2 {
		t.Fatalf("rule rows after a failed forget = %d, want 2", n)
	}
}
