// SQLStore's tests exercise Execute and Sweep against a real database,
// the same way internal/rules' own store_sql_test.go and internal/items'
// own store_sql_test.go do: table CHECKs and the partial unique index
// (schema_test.go) prove the schema, these prove the store writes the
// columns and events the design's "Lifecycle" section describes.
//
// Every Execute test opens its own tx on the bare pool and passes it in
// directly — the shape the evaluator's own per-host claim transaction
// uses (internal/rules' EvalSQLStore.conn), not db.WithOrg, because
// OpenForHost and Execute take a caller-owned pgx.Tx precisely so they can
// run inside that transaction rather than their own. Every read a test
// makes while that transaction is still open goes through the same tx
// (gen.DBTX, which both pgx.Tx and the bare pool satisfy) rather than a
// second pool connection — READ COMMITTED means a second connection
// cannot see this transaction's own uncommitted writes.
package alerts_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/noixdev/pulsix/internal/alerts"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/rules"
)

// beginTx opens a plain, unscoped transaction on d's pool and registers
// its rollback — the same "no pulsix.org_id bound" shape the evaluator's
// own per-host claim transaction runs on (internal/rules.due_sql.go), which
// is why every statement these store methods issue names its own org_id
// rather than relying on RLS to supply one.
func beginTx(t *testing.T, d *db.DB) pgx.Tx {
	t.Helper()
	tx, err := d.Pool().Begin(context.Background())
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	return tx
}

// tuple is a plain tuple-scope Subject on host, the shape every Execute
// test below opens or advances.
func tuple(host uuid.UUID, checkKey, item, metric string) alerts.Subject {
	return alerts.Subject{HostID: host, Scope: alerts.ScopeTuple, CheckKey: checkKey, Item: item, Metric: metric}
}

// upsertCheckState writes (or overwrites) one check_states row — the row
// TestExecuteOpenSeedsWasFlappingFromCheckState's subquery reads flapping
// from, and the row TestSweepClosesAlertsWhoseTupleIsGone's absence is
// what the sweep's NOT EXISTS detects. It runs on the bare pool,
// autocommitted, so it must be called before any tx under test begins.
func upsertCheckState(t *testing.T, d *db.DB, org, host uuid.UUID, checkKey, item, metric string, flapping bool) {
	t.Helper()
	q := gen.New(d.Pool())
	now := time.Now().UTC().Truncate(time.Microsecond)
	err := q.UpsertCheckState(context.Background(), gen.UpsertCheckStateParams{
		OrgID: org, HostID: host, CheckKey: checkKey, Item: item, Metric: metric,
		State: "warn", Since: now, Flapping: flapping, EvaluatedAt: now,
	})
	if err != nil {
		t.Fatalf("upsert check state: %v", err)
	}
}

// alertEventKinds returns every alert_events row's kind for alertID, in
// (at) order, read through q — the test's own tx while one is still open,
// so an uncommitted write is still visible.
func alertEventKinds(t *testing.T, q gen.DBTX, alertID uuid.UUID) []string {
	t.Helper()
	rows, err := q.Query(context.Background(),
		`SELECT kind FROM alert_events WHERE alert_id = $1 ORDER BY at, kind`, alertID)
	if err != nil {
		t.Fatalf("query alert_events: %v", err)
	}
	defer rows.Close()
	var kinds []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			t.Fatalf("scan alert_events: %v", err)
		}
		kinds = append(kinds, k)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate alert_events: %v", err)
	}
	return kinds
}

// fullAlertRow is every column a test below needs beyond OpenForHost's own
// shape — closed alerts and ack fields in particular, since OpenForHost by
// design never returns a closed row.
type fullAlertRow struct {
	severity, peakSeverity         string
	closedAt                       *time.Time
	closedReason                   *string
	ackAt                          *time.Time
	ackSeverity                    *string
	wasFlapping, snoozeUntilChange bool
}

func alertRowByID(t *testing.T, q gen.DBTX, id uuid.UUID) fullAlertRow {
	t.Helper()
	var r fullAlertRow
	err := q.QueryRow(context.Background(), `
		SELECT severity, peak_severity, closed_at, closed_reason,
		       acknowledged_at, acknowledged_severity, was_flapping, snooze_until_change
		FROM alerts WHERE id = $1`, id).
		Scan(&r.severity, &r.peakSeverity, &r.closedAt, &r.closedReason,
			&r.ackAt, &r.ackSeverity, &r.wasFlapping, &r.snoozeUntilChange)
	if err != nil {
		t.Fatalf("read alert %s: %v", id, err)
	}
	return r
}

// setAcknowledged stamps id as acknowledged at severity sev — the fixture
// TestExecuteWritesAckVoided and TestReopenedSubjectStartsUnacknowledged
// both need, built directly in SQL since acknowledging is Task 6's
// Service, not this store. It runs through q so it lands in the same
// transaction as the Execute calls the test is building on top of it.
func setAcknowledged(t *testing.T, q gen.DBTX, id uuid.UUID, sev string) {
	t.Helper()
	_, err := q.Exec(context.Background(), `
		UPDATE alerts SET acknowledged_at = now(), acknowledged_severity = $2 WHERE id = $1`, id, sev)
	if err != nil {
		t.Fatalf("acknowledge %s: %v", id, err)
	}
}

func TestExecuteOpensAndWritesAnOpenedEvent(t *testing.T) {
	d := testdb.Open(t)
	org, host := seedHost(t, d)
	st := alerts.NewSQLStore(d)
	sub := tuple(host, "disk", "/", "used_percent")
	openedAt := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	tx := beginTx(t, d)
	err := st.Execute(context.Background(), tx, org, []alerts.Action{
		{Kind: alerts.ActionOpen, Subject: sub, At: openedAt, Severity: rules.StateWarn},
	})
	if err != nil {
		t.Fatalf("execute open: %v", err)
	}

	open, err := st.OpenForHost(context.Background(), tx, org, host)
	if err != nil {
		t.Fatalf("open for host: %v", err)
	}
	if len(open) != 1 {
		t.Fatalf("got %d open alerts, want 1: %+v", len(open), open)
	}
	a := open[0]
	if a.Subject != sub {
		t.Fatalf("subject = %+v, want %+v", a.Subject, sub)
	}
	if !a.OpenedAt.Equal(openedAt) {
		t.Fatalf("opened_at = %v, want %v (the action's own At, not now())", a.OpenedAt, openedAt)
	}
	if a.Severity != rules.StateWarn || a.Peak != rules.StateWarn {
		t.Fatalf("severity/peak = %q/%q, want warn/warn", a.Severity, a.Peak)
	}
	if a.AckAt != nil || a.ClosedAt != nil {
		t.Fatalf("a freshly opened alert carries ack/close state: %+v", a)
	}

	kinds := alertEventKinds(t, tx, a.ID)
	if len(kinds) != 1 || kinds[0] != "opened" {
		t.Fatalf("alert_events = %v, want exactly one 'opened'", kinds)
	}
}

// TestExecuteOpenSeedsWasFlappingFromCheckState pins the brief's own
// ruling: Action carries no Flapping field (widening it would touch four
// tasks), so an ActionOpen's was_flapping is seeded by the INSERT's own
// subquery reading check_states.flapping for the same subject, inside the
// same transaction the evaluator already wrote that row in.
func TestExecuteOpenSeedsWasFlappingFromCheckState(t *testing.T) {
	d := testdb.Open(t)
	org, host := seedHost(t, d)
	st := alerts.NewSQLStore(d)

	flapping := tuple(host, "disk", "/", "used_percent")
	calm := tuple(host, "cpu", "", "used_percent")
	upsertCheckState(t, d, org, host, flapping.CheckKey, flapping.Item, flapping.Metric, true)
	upsertCheckState(t, d, org, host, calm.CheckKey, calm.Item, calm.Metric, false)

	tx := beginTx(t, d)
	err := st.Execute(context.Background(), tx, org, []alerts.Action{
		{Kind: alerts.ActionOpen, Subject: flapping, At: time.Now(), Severity: rules.StateWarn},
		{Kind: alerts.ActionOpen, Subject: calm, At: time.Now(), Severity: rules.StateWarn},
	})
	if err != nil {
		t.Fatalf("execute open: %v", err)
	}

	open, err := st.OpenForHost(context.Background(), tx, org, host)
	if err != nil {
		t.Fatalf("open for host: %v", err)
	}
	got := map[alerts.Subject]bool{}
	for _, a := range open {
		got[a.Subject] = a.WasFlapping
	}
	if !got[flapping] {
		t.Fatalf("the tuple whose check_states row was flapping opened with was_flapping = false")
	}
	if got[calm] {
		t.Fatalf("the tuple whose check_states row was not flapping opened with was_flapping = true")
	}
}

// TestExecuteOpenHostScopeNeverFlaps pins the other half: a host-scope
// subject carries no check_key/item/metric that could ever match a
// check_states row, so it must always open with was_flapping = false,
// never an accidental match on some other tuple's empty-string columns.
func TestExecuteOpenHostScopeNeverFlaps(t *testing.T) {
	d := testdb.Open(t)
	org, host := seedHost(t, d)
	st := alerts.NewSQLStore(d)
	sub := alerts.Subject{HostID: host, Scope: alerts.ScopeHost}

	// The adversarial fixture: check_states carries no CHECK forbidding
	// empty check_key/item/metric (unlike alerts' own CHECK on scope, see
	// migration 00049), so a row shaped exactly like a host-scope
	// Subject's own empty columns is insertable. Without InsertAlert's own
	// scope = 'tuple' guard this row — not merely "no row at all" — is
	// what a host-scope open would match, and would open already
	// flapping. A fixture with zero check_states rows cannot tell "no
	// match because excluded by scope" apart from "no match because
	// nothing happened to be there".
	upsertCheckState(t, d, org, host, "", "", "", true)

	tx := beginTx(t, d)
	err := st.Execute(context.Background(), tx, org, []alerts.Action{
		{Kind: alerts.ActionOpen, Subject: sub, At: time.Now(), Severity: rules.StateUnknown},
	})
	if err != nil {
		t.Fatalf("execute open: %v", err)
	}
	open, err := st.OpenForHost(context.Background(), tx, org, host)
	if err != nil {
		t.Fatalf("open for host: %v", err)
	}
	if len(open) != 1 || open[0].WasFlapping {
		t.Fatalf("host-scope open = %+v, want exactly one alert with was_flapping = false", open)
	}
}

// TestExecuteRaisesPeakButNeverLowersIt is the brief's own test: raise to
// crit, then fall to warn. peak_severity must stick at crit, and exactly
// one severity_raised event must exist — the fall writes none.
func TestExecuteRaisesPeakButNeverLowersIt(t *testing.T) {
	d := testdb.Open(t)
	org, host := seedHost(t, d)
	st := alerts.NewSQLStore(d)
	sub := tuple(host, "disk", "/", "used_percent")

	tx := beginTx(t, d)
	ctx := context.Background()
	if err := st.Execute(ctx, tx, org, []alerts.Action{
		{Kind: alerts.ActionOpen, Subject: sub, At: time.Now(), Severity: rules.StateWarn},
	}); err != nil {
		t.Fatalf("execute open: %v", err)
	}
	open, err := st.OpenForHost(ctx, tx, org, host)
	if err != nil || len(open) != 1 {
		t.Fatalf("open for host: %v (%d rows)", err, len(open))
	}
	id := open[0].ID

	if err := st.Execute(ctx, tx, org, []alerts.Action{
		{Kind: alerts.ActionSeverity, AlertID: id, At: time.Now(), Severity: rules.StateCrit},
	}); err != nil {
		t.Fatalf("execute raise to crit: %v", err)
	}
	if err := st.Execute(ctx, tx, org, []alerts.Action{
		{Kind: alerts.ActionSeverity, AlertID: id, At: time.Now(), Severity: rules.StateWarn},
	}); err != nil {
		t.Fatalf("execute fall to warn: %v", err)
	}

	row := alertRowByID(t, tx, id)
	if row.severity != "warn" {
		t.Fatalf("severity = %q, want warn (the current reading)", row.severity)
	}
	if row.peakSeverity != "crit" {
		t.Fatalf("peak_severity = %q, want crit (a peak must only ever rise)", row.peakSeverity)
	}

	kinds := alertEventKinds(t, tx, id)
	count := 0
	for _, k := range kinds {
		if k == "severity_raised" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("severity_raised events = %d, want exactly 1 (the fall must write none): %v", count, kinds)
	}
}

// TestPeakSeverityRankMatchesRulesStateRank is Review Focus 2: the peak's
// rank is computed twice, once in rules.StateRank (Go) and once in the
// RaiseAlertSeverity CASE (SQL), and the two must agree for every pair of
// the three severities an alert can actually carry. Crit outranking warn
// alone cannot show a warn/unknown transposition — crit is highest under
// either ordering — so this drives every ordered pair, which is exactly
// what would catch the SQL CASE ranking unknown above warn (or any other
// swap) that TestExecuteRaisesPeakButNeverLowersIt's crit/warn pair alone
// would pass right through.
func TestPeakSeverityRankMatchesRulesStateRank(t *testing.T) {
	severities := []rules.State{rules.StateWarn, rules.StateCrit, rules.StateUnknown}
	d := testdb.Open(t)
	st := alerts.NewSQLStore(d)
	ctx := context.Background()

	for _, openAt := range severities {
		for _, raiseTo := range severities {
			t.Run(string(openAt)+"_then_"+string(raiseTo), func(t *testing.T) {
				org, host := seedHost(t, d)
				sub := tuple(host, "disk", "/", "used_percent")
				tx := beginTx(t, d)

				if err := st.Execute(ctx, tx, org, []alerts.Action{
					{Kind: alerts.ActionOpen, Subject: sub, At: time.Now(), Severity: openAt},
				}); err != nil {
					t.Fatalf("execute open: %v", err)
				}
				open, err := st.OpenForHost(ctx, tx, org, host)
				if err != nil || len(open) != 1 {
					t.Fatalf("open for host: %v (%d rows)", err, len(open))
				}
				id := open[0].ID

				if err := st.Execute(ctx, tx, org, []alerts.Action{
					{Kind: alerts.ActionSeverity, AlertID: id, At: time.Now(), Severity: raiseTo},
				}); err != nil {
					t.Fatalf("execute severity change: %v", err)
				}

				row := alertRowByID(t, tx, id)
				want := rules.Worse(openAt, raiseTo)
				if rules.State(row.peakSeverity) != want {
					t.Fatalf("opened at %q, raised to %q: peak_severity = %q, want %q (rules.StateRank order)",
						openAt, raiseTo, row.peakSeverity, want)
				}
			})
		}
	}
}

// TestExecuteWritesAckVoided is the v0.4a brief's own test, updated for
// v0.4d decision 2 (backlog 4.36): VoidsAck produces the event and leaves
// acknowledged_at in place — the ack happened, it just stopped covering
// the now-higher severity — but acknowledged_severity itself no longer
// survives the void (TestExecuteClearsAckSeverityWhenVoided below drives
// this further, into a severity that falls back to the voided value).
func TestExecuteWritesAckVoided(t *testing.T) {
	d := testdb.Open(t)
	org, host := seedHost(t, d)
	st := alerts.NewSQLStore(d)
	sub := tuple(host, "mem", "", "used_percent")
	ctx := context.Background()

	tx := beginTx(t, d)
	if err := st.Execute(ctx, tx, org, []alerts.Action{
		{Kind: alerts.ActionOpen, Subject: sub, At: time.Now(), Severity: rules.StateWarn},
	}); err != nil {
		t.Fatalf("execute open: %v", err)
	}
	open, err := st.OpenForHost(ctx, tx, org, host)
	if err != nil || len(open) != 1 {
		t.Fatalf("open for host: %v (%d rows)", err, len(open))
	}
	id := open[0].ID
	setAcknowledged(t, tx, id, "warn")

	if err := st.Execute(ctx, tx, org, []alerts.Action{
		{Kind: alerts.ActionSeverity, AlertID: id, At: time.Now(), Severity: rules.StateCrit, VoidsAck: true},
	}); err != nil {
		t.Fatalf("execute severity + void ack: %v", err)
	}

	row := alertRowByID(t, tx, id)
	if row.ackAt == nil {
		t.Fatalf("acknowledged_at = nil after a void, want the original acknowledgement left in place")
	}
	if row.ackSeverity != nil {
		t.Fatalf("acknowledged_severity = %q after a void, want NULL (backlog 4.36: it no longer survives the void)", *row.ackSeverity)
	}

	kinds := alertEventKinds(t, tx, id)
	count := 0
	for _, k := range kinds {
		if k == "ack_voided" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("ack_voided events = %d, want exactly 1: %v", count, kinds)
	}
}

// TestExecuteClearsAckSeverityWhenVoided is backlog 4.36, settled by v0.4d
// decision 2: acknowledged_severity is cleared in the same UPDATE that
// writes the ack_voided event (RaiseAlertSeverity's own fourth SET
// target), not left standing the way TestExecuteWritesAckVoided above
// still finds it right after the void. Without the clear, a severity that
// later falls back to (or below) the voided ack's own acknowledged_severity
// makes the live comparison `severity <= acknowledged_severity` read
// acknowledged again, while the alert's own timeline still shows the
// ack_voided event — the row and the history disagreeing is exactly what
// this test drives into existence and then rules out.
func TestExecuteClearsAckSeverityWhenVoided(t *testing.T) {
	d := testdb.Open(t)
	org, host := seedHost(t, d)
	st := alerts.NewSQLStore(d)
	sub := tuple(host, "mem", "", "used_percent")
	ctx := context.Background()

	tx := beginTx(t, d)
	if err := st.Execute(ctx, tx, org, []alerts.Action{
		{Kind: alerts.ActionOpen, Subject: sub, At: time.Now(), Severity: rules.StateWarn},
	}); err != nil {
		t.Fatalf("execute open: %v", err)
	}
	open, err := st.OpenForHost(ctx, tx, org, host)
	if err != nil || len(open) != 1 {
		t.Fatalf("open for host: %v (%d rows)", err, len(open))
	}
	id := open[0].ID
	setAcknowledged(t, tx, id, "warn")

	// Raise to crit — the ack (given at warn) no longer covers, so this
	// voids it, same as TestExecuteWritesAckVoided.
	if err := st.Execute(ctx, tx, org, []alerts.Action{
		{Kind: alerts.ActionSeverity, AlertID: id, At: time.Now(), Severity: rules.StateCrit, VoidsAck: true},
	}); err != nil {
		t.Fatalf("execute severity + void ack: %v", err)
	}

	// Then fall back to warn — exactly the severity the old
	// acknowledged_severity used to equal. A severity fall never writes an
	// event (TestExecuteRaisesPeakButNeverLowersIt) and never voids an ack
	// (Reconcile's own advance() only voids on a rise), so this action
	// carries VoidsAck: false.
	if err := st.Execute(ctx, tx, org, []alerts.Action{
		{Kind: alerts.ActionSeverity, AlertID: id, At: time.Now(), Severity: rules.StateWarn, VoidsAck: false},
	}); err != nil {
		t.Fatalf("execute fall back to warn: %v", err)
	}

	row := alertRowByID(t, tx, id)
	if row.ackSeverity != nil {
		t.Fatalf("acknowledged_severity = %v after a voided ack's severity fell back to its own old value, want NULL (backlog 4.36)", *row.ackSeverity)
	}
	if row.ackAt == nil {
		t.Fatalf("acknowledged_at was cleared too; only acknowledged_severity should be — the acknowledgement itself still happened")
	}

	kinds := alertEventKinds(t, tx, id)
	count := 0
	for _, k := range kinds {
		if k == "ack_voided" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("ack_voided events = %d, want exactly 1 (the fall back writes none): %v", count, kinds)
	}
}

// TestExecuteSnoozeUntilChangeOnlyClearsWhenAsked pins both halves of
// RaiseAlertSeverity's own third SET target: ClearsSnoozeUntilChange must
// turn the flag off, AND a severity action that does not ask for it must
// leave an existing one standing — driving only the first case would let
// the ELSE branch of that CASE collapse to an unconditional "false" (every
// severity move silently dropping an until-change snooze) without
// failing anything.
func TestExecuteSnoozeUntilChangeOnlyClearsWhenAsked(t *testing.T) {
	cases := []struct {
		name    string
		clears  bool
		wantSet bool
	}{
		{name: "clears when asked", clears: true, wantSet: false},
		{name: "left standing otherwise", clears: false, wantSet: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := testdb.Open(t)
			org, host := seedHost(t, d)
			st := alerts.NewSQLStore(d)
			sub := tuple(host, "mem", "", "used_percent")
			ctx := context.Background()

			tx := beginTx(t, d)
			if err := st.Execute(ctx, tx, org, []alerts.Action{
				{Kind: alerts.ActionOpen, Subject: sub, At: time.Now(), Severity: rules.StateWarn},
			}); err != nil {
				t.Fatalf("execute open: %v", err)
			}
			open, _ := st.OpenForHost(ctx, tx, org, host)
			id := open[0].ID
			if _, err := tx.Exec(ctx, `UPDATE alerts SET snooze_until_change = true WHERE id = $1`, id); err != nil {
				t.Fatalf("seed snooze_until_change: %v", err)
			}

			if err := st.Execute(ctx, tx, org, []alerts.Action{
				{Kind: alerts.ActionSeverity, AlertID: id, At: time.Now(), Severity: rules.StateCrit, ClearsSnoozeUntilChange: tc.clears},
			}); err != nil {
				t.Fatalf("execute severity change: %v", err)
			}
			if row := alertRowByID(t, tx, id); row.snoozeUntilChange != tc.wantSet {
				t.Fatalf("snooze_until_change = %v, want %v (ClearsSnoozeUntilChange=%v)",
					row.snoozeUntilChange, tc.wantSet, tc.clears)
			}
		})
	}
}

// TestExecuteFlappedLatchesWithNoEvent: ActionFlapped sets was_flapping
// but writes nothing to alert_events — there is no "flapped" kind in the
// CHECK migration 00049 puts on alert_events.kind, so this is the one
// action kind Execute must apply silently.
func TestExecuteFlappedLatchesWithNoEvent(t *testing.T) {
	d := testdb.Open(t)
	org, host := seedHost(t, d)
	st := alerts.NewSQLStore(d)
	sub := tuple(host, "disk", "/", "used_percent")
	ctx := context.Background()

	tx := beginTx(t, d)
	if err := st.Execute(ctx, tx, org, []alerts.Action{
		{Kind: alerts.ActionOpen, Subject: sub, At: time.Now(), Severity: rules.StateWarn},
	}); err != nil {
		t.Fatalf("execute open: %v", err)
	}
	open, _ := st.OpenForHost(ctx, tx, org, host)
	id := open[0].ID

	if err := st.Execute(ctx, tx, org, []alerts.Action{
		{Kind: alerts.ActionFlapped, AlertID: id, At: time.Now()},
	}); err != nil {
		t.Fatalf("execute flapped: %v", err)
	}

	row := alertRowByID(t, tx, id)
	if !row.wasFlapping {
		t.Fatalf("was_flapping not set after ActionFlapped")
	}
	kinds := alertEventKinds(t, tx, id)
	for _, k := range kinds {
		if k != "opened" {
			t.Fatalf("alert_events = %v, want only the opening event (flapping writes none)", kinds)
		}
	}
}

// TestExecuteCloseWritesClosedEvent covers ActionClose: the row closes
// with the action's own reason and timestamp, and exactly one 'closed'
// event lands regardless of reason (resolved here; the sweep covers stale).
func TestExecuteCloseWritesClosedEvent(t *testing.T) {
	d := testdb.Open(t)
	org, host := seedHost(t, d)
	st := alerts.NewSQLStore(d)
	sub := tuple(host, "disk", "/", "used_percent")
	ctx := context.Background()
	closedAt := time.Date(2026, 1, 1, 13, 0, 0, 0, time.UTC)

	tx := beginTx(t, d)
	if err := st.Execute(ctx, tx, org, []alerts.Action{
		{Kind: alerts.ActionOpen, Subject: sub, At: time.Now(), Severity: rules.StateWarn},
	}); err != nil {
		t.Fatalf("execute open: %v", err)
	}
	open, _ := st.OpenForHost(ctx, tx, org, host)
	id := open[0].ID

	if err := st.Execute(ctx, tx, org, []alerts.Action{
		{Kind: alerts.ActionClose, AlertID: id, At: closedAt, Reason: alerts.Resolved},
	}); err != nil {
		t.Fatalf("execute close: %v", err)
	}

	row := alertRowByID(t, tx, id)
	if row.closedAt == nil || !row.closedAt.Equal(closedAt) {
		t.Fatalf("closed_at = %v, want %v", row.closedAt, closedAt)
	}
	if row.closedReason == nil || *row.closedReason != "resolved" {
		t.Fatalf("closed_reason = %v, want resolved", row.closedReason)
	}

	still, err := st.OpenForHost(ctx, tx, org, host)
	if err != nil {
		t.Fatalf("open for host after close: %v", err)
	}
	if len(still) != 0 {
		t.Fatalf("OpenForHost still returns the closed alert: %+v", still)
	}

	kinds := alertEventKinds(t, tx, id)
	count := 0
	for _, k := range kinds {
		if k == "closed" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("closed events = %d, want exactly 1: %v", count, kinds)
	}
}

// TestReopenedSubjectStartsUnacknowledged is Review Focus 3: a subject
// that goes bad, is acknowledged, resolves, and goes bad again must
// produce a NEW row that starts unacknowledged. The partial unique index
// makes reopening possible at all; a careless upsert that updated the
// closed row in place instead of inserting a fresh one would carry the
// old acknowledgement forward and silence the new incident.
func TestReopenedSubjectStartsUnacknowledged(t *testing.T) {
	d := testdb.Open(t)
	org, host := seedHost(t, d)
	st := alerts.NewSQLStore(d)
	sub := tuple(host, "disk", "/", "used_percent")
	ctx := context.Background()

	tx := beginTx(t, d)
	if err := st.Execute(ctx, tx, org, []alerts.Action{
		{Kind: alerts.ActionOpen, Subject: sub, At: time.Now(), Severity: rules.StateWarn},
	}); err != nil {
		t.Fatalf("execute first open: %v", err)
	}
	first, _ := st.OpenForHost(ctx, tx, org, host)
	firstID := first[0].ID
	setAcknowledged(t, tx, firstID, "warn")

	if err := st.Execute(ctx, tx, org, []alerts.Action{
		{Kind: alerts.ActionClose, AlertID: firstID, At: time.Now(), Reason: alerts.Resolved},
	}); err != nil {
		t.Fatalf("execute close: %v", err)
	}

	if err := st.Execute(ctx, tx, org, []alerts.Action{
		{Kind: alerts.ActionOpen, Subject: sub, At: time.Now(), Severity: rules.StateWarn},
	}); err != nil {
		t.Fatalf("execute reopen: %v", err)
	}
	second, err := st.OpenForHost(ctx, tx, org, host)
	if err != nil {
		t.Fatalf("open for host after reopen: %v", err)
	}
	if len(second) != 1 {
		t.Fatalf("got %d open alerts after reopening, want 1: %+v", len(second), second)
	}
	if second[0].ID == firstID {
		t.Fatalf("the reopened alert reused the closed row's id")
	}
	if second[0].AckAt != nil {
		t.Fatalf("the reopened alert carried the closed episode's acknowledgement forward: %+v", second[0])
	}
}

func TestSweepClosesAlertsWhoseTupleIsGone(t *testing.T) {
	d := testdb.Open(t)
	org, host := seedHost(t, d)
	insertOpenAlert(t, d, org, host, "disk", "/", "used_percent")
	// No check_states row for this subject exists at all — exactly the
	// condition SweepStaleAlerts' NOT EXISTS is written to detect, and
	// what rules.Result.Dropped leaves behind when an operator ignores an
	// item. An earlier version of this comment also claimed it was "the
	// same end state 'the last check was removed' produces"; v0.4a's final
	// review found that false — removing a check leaves the row standing —
	// and the real cause is now driven end to end in lifecycle_sql_test.go
	// rather than assumed here.

	n, err := alerts.NewSQLStore(d).Sweep(context.Background(), time.Now())
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if n != 1 {
		t.Fatalf("sweep closed %d alerts, want 1", n)
	}

	var id uuid.UUID
	var closedReason *string
	err = d.Pool().QueryRow(context.Background(),
		`SELECT id, closed_reason FROM alerts WHERE org_id = $1 AND host_id = $2`, org, host).Scan(&id, &closedReason)
	if err != nil {
		t.Fatalf("read swept alert: %v", err)
	}
	if closedReason == nil || *closedReason != "stale" {
		t.Fatalf("closed_reason = %v, want stale", closedReason)
	}

	// The post-review ruling: the sweep writes a 'closed' event in the same
	// statement, not only the column — without it a swept alert would read
	// on Task 10's own timeline as an episode that opened and never ended.
	kinds := alertEventKinds(t, d.Pool(), id)
	count := 0
	for _, k := range kinds {
		if k == "closed" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("closed events after sweep = %d, want exactly 1: %v", count, kinds)
	}
}

// TestSweepLeavesAlertsAloneWhenTheirTupleStillReports is the mirror of
// TestSweepClosesAlertsWhoseTupleIsGone: an open alert whose check_states
// row is still present must not be touched by the sweep at all — the
// NOT EXISTS must be the only thing deciding, not merely "closed_at IS
// NULL AND scope = 'tuple'" on their own.
func TestSweepLeavesAlertsAloneWhenTheirTupleStillReports(t *testing.T) {
	d := testdb.Open(t)
	org, host := seedHost(t, d)
	insertOpenAlert(t, d, org, host, "disk", "/", "used_percent")
	upsertCheckState(t, d, org, host, "disk", "/", "used_percent", false)

	n, err := alerts.NewSQLStore(d).Sweep(context.Background(), time.Now())
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if n != 0 {
		t.Fatalf("sweep closed %d alerts, want 0 (the tuple still reports)", n)
	}
}

// TestSweepLeavesHostScopeAlertsAlone: a scope='host' alert has no
// check_key/item/metric by construction (migration 00049's own CHECK), so
// it can never match a check_states row either way — the sweep excludes
// scope='host' explicitly rather than relying on that NOT EXISTS always
// holding true for it, and this is what proves the exclusion is real.
func TestSweepLeavesHostScopeAlertsAlone(t *testing.T) {
	d := testdb.Open(t)
	org, host := seedHost(t, d)
	st := alerts.NewSQLStore(d)
	ctx := context.Background()

	tx := beginTx(t, d)
	if err := st.Execute(ctx, tx, org, []alerts.Action{
		{Kind: alerts.ActionOpen, Subject: alerts.Subject{HostID: host, Scope: alerts.ScopeHost}, At: time.Now(), Severity: rules.StateUnknown},
	}); err != nil {
		t.Fatalf("execute open: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}

	n, err := st.Sweep(ctx, time.Now())
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if n != 0 {
		t.Fatalf("sweep closed %d host-scope alerts, want 0", n)
	}

	readTx := beginTx(t, d)
	open, err := st.OpenForHost(ctx, readTx, org, host)
	if err != nil {
		t.Fatalf("open for host: %v", err)
	}
	if len(open) != 1 {
		t.Fatalf("got %d open alerts after sweep, want the host-scope alert to survive: %+v", len(open), open)
	}
}

// TestOrgIDGuardsEveryAlertStatement is the fix for a gap review found:
// every statement in queries/alerts.sql names org_id precisely because the
// evaluator's own transaction is unscoped (00049's org_isolation_bypass
// policy applies whenever pulsix.org_id is unset, which this tx never
// sets), yet alerts.id and hosts.id are both globally unique UUIDs — so
// every test above, each running under a single organization, would stay
// green even if "AND org_id = @org_id" were quietly deleted from
// ListOpenAlertsForHost, GetAlertSeverity, RaiseAlertSeverity,
// SetAlertFlapping or CloseAlert. That guard is the only tenant isolation
// on this path at all, which is why CLAUDE.md puts tenancy at >= 90%
// coverage.
//
// Each subtest below drives one statement directly against org B's
// credentials but org A's own alert id/host id, and asserts org A's row
// is untouched — RaiseAlertSeverity, SetAlertFlapping and CloseAlert are
// :exec statements that silently affect zero rows rather than erroring
// when the WHERE matches nothing, so "no error" alone would prove
// nothing; reading the row back after each call is what actually pins the
// guard. GetAlertSeverity and ListOpenAlertsForHost are read statements,
// so their own guard is pinned by what they return instead.
func TestOrgIDGuardsEveryAlertStatement(t *testing.T) {
	d := testdb.Open(t)
	orgA, hostA := seedHost(t, d)
	orgB, _ := seedHost(t, d)
	st := alerts.NewSQLStore(d)
	ctx := context.Background()

	tx := beginTx(t, d)
	if err := st.Execute(ctx, tx, orgA, []alerts.Action{
		{Kind: alerts.ActionOpen, Subject: tuple(hostA, "disk", "/", "used_percent"), At: time.Now(), Severity: rules.StateWarn},
	}); err != nil {
		t.Fatalf("execute open: %v", err)
	}
	open, err := st.OpenForHost(ctx, tx, orgA, hostA)
	if err != nil || len(open) != 1 {
		t.Fatalf("open for host: %v (%d rows)", err, len(open))
	}
	alertA := open[0].ID
	q := gen.New(tx)

	t.Run("OpenForHost", func(t *testing.T) {
		got, err := st.OpenForHost(ctx, tx, orgB, hostA)
		if err != nil {
			t.Fatalf("open for host as orgB: %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("OpenForHost(orgB, hostA) = %+v, want none of org A's alerts", got)
		}
	})

	t.Run("GetAlertSeverity", func(t *testing.T) {
		if _, err := q.GetAlertSeverity(ctx, gen.GetAlertSeverityParams{ID: alertA, OrgID: orgB}); err == nil {
			t.Fatalf("GetAlertSeverity(alertA, orgB) returned no error, want org A's row to be invisible to org B")
		}
	})

	t.Run("RaiseAlertSeverity", func(t *testing.T) {
		if err := q.RaiseAlertSeverity(ctx, gen.RaiseAlertSeverityParams{
			Severity: "crit", SeverityRank: 3, ClearsSnooze: false, ID: alertA, OrgID: orgB,
		}); err != nil {
			t.Fatalf("RaiseAlertSeverity(alertA, orgB): %v", err)
		}
		if row := alertRowByID(t, tx, alertA); row.severity != "warn" {
			t.Fatalf("severity = %q after a wrong-org raise, want warn (unchanged)", row.severity)
		}
	})

	t.Run("SetAlertFlapping", func(t *testing.T) {
		if err := q.SetAlertFlapping(ctx, gen.SetAlertFlappingParams{ID: alertA, OrgID: orgB}); err != nil {
			t.Fatalf("SetAlertFlapping(alertA, orgB): %v", err)
		}
		if row := alertRowByID(t, tx, alertA); row.wasFlapping {
			t.Fatalf("was_flapping = true after a wrong-org flap, want unchanged")
		}
	})

	t.Run("CloseAlert", func(t *testing.T) {
		reason := "resolved"
		if err := q.CloseAlert(ctx, gen.CloseAlertParams{
			ClosedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true}, Reason: &reason, ID: alertA, OrgID: orgB,
		}); err != nil {
			t.Fatalf("CloseAlert(alertA, orgB): %v", err)
		}
		if row := alertRowByID(t, tx, alertA); row.closedAt != nil {
			t.Fatalf("closed_at = %v after a wrong-org close, want nil (unchanged)", row.closedAt)
		}
	})
}

// TestOperatorWritesRunUnderRowLevelSecurity is v0.4a's final review,
// Important: Acknowledge, Unacknowledge, Snooze and Unsnooze used to begin
// their transaction on the bare pool, so the only thing keeping an
// organization's acknowledgement inside its own tenant was the hand-written
// org_id predicate — one of the two layers, on the four endpoints that
// mutate operator-visible state. They now begin through db.DB.Begin, which
// binds the request's own scope.
//
// The test distinguishes the two states rather than merely re-proving the
// predicate: it calls the store with a context scoped to org B while
// passing org A's id, the combination a bug or a future refactor could
// produce. Under RLS the row is not visible and the update matches nothing;
// without it, the org_id predicate alone would happily find org A's row and
// acknowledge it.
func TestOperatorWritesRunUnderRowLevelSecurity(t *testing.T) {
	d := testdb.Open(t)
	orgA, hostA := seedHost(t, d)
	orgB, _ := seedHost(t, d)
	st := alerts.NewSQLStore(d)
	ctx := context.Background()
	insertOpenAlert(t, d, orgA, hostA, "disk", "/", "used_percent")

	var id uuid.UUID
	if err := d.Pool().QueryRow(ctx,
		`SELECT id FROM alerts WHERE org_id = $1 AND host_id = $2`, orgA, hostA).Scan(&id); err != nil {
		t.Fatalf("read seeded alert: %v", err)
	}
	actor := seedUser(t, d)

	// Scoped to the alert's own organization: the ordinary request, which
	// must still work now that the transaction is bound at all.
	if err := st.Acknowledge(db.WithOrgScope(ctx, orgA), orgA, id, actor, "mine", time.Now()); err != nil {
		t.Fatalf("acknowledge as the owning organization: %v", err)
	}
	if row := alertRowByID(t, d.Pool(), id); row.ackAt == nil {
		t.Fatal("the owning organization's acknowledgement did not land")
	}

	// Scoped to another organization: RLS hides the row, so the UPDATE
	// matches nothing and the store says so.
	if err := st.Snooze(db.WithOrgScope(ctx, orgB), orgA, id, actor, nil, true, time.Now()); err == nil {
		t.Fatal("snoozing org A's alert from org B's scope succeeded; the transaction is not bound to the request's organization")
	}
	if row := alertRowByID(t, d.Pool(), id); row.snoozeUntilChange {
		t.Fatal("org B's snooze landed on org A's alert")
	}
}
