// Package alerts_test exercises migration 00049 directly, through raw SQL
// against the bare pool — there is no Go package here yet (Task 2 adds the
// domain types and store), so these tests are the only thing proving the
// schema itself is shaped the way every later task's SQL assumes.
package alerts_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
)

// seedHost creates a bare organization, zone and host — the minimal
// fixture alerts' composite FK (org_id, host_id) REFERENCES hosts (org_id,
// id) needs, the same shape every other domain's own seedHost builds (see
// e.g. internal/rules/store_sql_test.go), collapsed into one call since
// every test below needs exactly one org and one host, never more.
func seedHost(t *testing.T, d *db.DB) (org, host uuid.UUID) {
	t.Helper()
	q := gen.New(d.Pool())
	slug := "alerts-" + uuid.Must(uuid.NewV7()).String()
	o, err := q.CreateOrganization(context.Background(), gen.CreateOrganizationParams{
		ID: uuid.Must(uuid.NewV7()), Slug: slug, Name: slug,
	})
	if err != nil {
		t.Fatal(err)
	}
	z, err := q.CreateZone(context.Background(), gen.CreateZoneParams{
		ID: uuid.Must(uuid.NewV7()), OrgID: o.ID, Slug: "z1", Name: "z1",
	})
	if err != nil {
		t.Fatal(err)
	}
	h, err := q.CreateHost(context.Background(), gen.CreateHostParams{
		ID: uuid.Must(uuid.NewV7()), OrgID: o.ID, ZoneID: z.ID, Name: "h1", Facts: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	return o.ID, h.ID
}

// alertRow is every column the tests below need to vary, one field per
// group the design's "Data model" section lists (subject, course,
// acknowledgement); everything not set here takes a valid default so a
// test only has to name the columns its own CHECK is about.
type alertRow struct {
	scope, checkKey, item, metric string
	closedAt                      *time.Time
	closedReason                  *string
	ackAt                         *time.Time
	ackSeverity                   *string
}

// insertAlertRow is the one raw INSERT every test below drives, letting
// each test assert on the resulting error (or lack of one) rather than on
// how the row was built.
func insertAlertRow(t *testing.T, d *db.DB, org, host uuid.UUID, r alertRow) error {
	t.Helper()
	_, err := d.Pool().Exec(context.Background(), `
		INSERT INTO alerts (
			org_id, host_id, scope, check_key, item, metric,
			opened_at, severity, peak_severity,
			closed_at, closed_reason, acknowledged_at, acknowledged_severity
		) VALUES ($1, $2, $3, $4, $5, $6, now(), 'warn', 'warn', $7, $8, $9, $10)`,
		org, host, r.scope, r.checkKey, r.item, r.metric,
		r.closedAt, r.closedReason, r.ackAt, r.ackSeverity)
	return err
}

// tryInsertOpenAlert and insertOpenAlert are the brief's own helpers: a
// plain open, tuple-scoped alert on (checkKey, item, metric), the shape
// every open-alert test below needs.
func tryInsertOpenAlert(t *testing.T, d *db.DB, org, host uuid.UUID, checkKey, item, metric string) error {
	t.Helper()
	return insertAlertRow(t, d, org, host, alertRow{scope: "tuple", checkKey: checkKey, item: item, metric: metric})
}

func insertOpenAlert(t *testing.T, d *db.DB, org, host uuid.UUID, checkKey, item, metric string) {
	t.Helper()
	if err := tryInsertOpenAlert(t, d, org, host, checkKey, item, metric); err != nil {
		t.Fatalf("insert open alert: %v", err)
	}
}

// closeAlert closes the one open alert matching the given subject, the way
// a later task's store will (UPDATE ... WHERE closed_at IS NULL), so that
// TestOnlyOneOpenAlertPerSubject can prove a closed subject is reopenable.
func closeAlert(t *testing.T, d *db.DB, org, host uuid.UUID, checkKey, item, metric string) {
	t.Helper()
	_, err := d.Pool().Exec(context.Background(), `
		UPDATE alerts SET closed_at = now(), closed_reason = 'resolved'
		WHERE org_id = $1 AND host_id = $2 AND check_key = $3 AND item = $4 AND metric = $5
			AND closed_at IS NULL`,
		org, host, checkKey, item, metric)
	if err != nil {
		t.Fatalf("close alert: %v", err)
	}
}

// TestOnlyOneOpenAlertPerSubject proves alerts_one_open_per_subject both
// ways: a second open alert on the same subject is refused, and a closed
// one does not block reopening — the index is a partial one, not a plain
// unique constraint, and a test that only checked the first half would
// pass even if the WHERE clause were missing.
func TestOnlyOneOpenAlertPerSubject(t *testing.T) {
	d := testdb.Open(t)
	org, host := seedHost(t, d)
	insertOpenAlert(t, d, org, host, "disk", "/", "used_percent")
	err := tryInsertOpenAlert(t, d, org, host, "disk", "/", "used_percent")
	if err == nil {
		t.Fatal("a second open alert on one subject was accepted; the partial unique index is missing")
	}
	// The same subject may have any number of *closed* alerts.
	closeAlert(t, d, org, host, "disk", "/", "used_percent")
	if err := tryInsertOpenAlert(t, d, org, host, "disk", "/", "used_percent"); err != nil {
		t.Fatalf("reopening a resolved subject was refused: %v", err)
	}
}

// TestHostScopeAlertHasEmptySubjectColumns pins the subject CHECK both
// ways: a 'host' scope row must carry none of check_key/item/metric, and a
// 'tuple' scope row must carry both check_key and metric.
func TestHostScopeAlertHasEmptySubjectColumns(t *testing.T) {
	d := testdb.Open(t)
	org, host := seedHost(t, d)

	if err := insertAlertRow(t, d, org, host, alertRow{scope: "host", checkKey: "disk"}); err == nil {
		t.Fatal("a scope='host' alert with a non-empty check_key was accepted")
	}
	if err := insertAlertRow(t, d, org, host, alertRow{scope: "tuple", checkKey: "disk", item: "/"}); err == nil {
		t.Fatal("a scope='tuple' alert with an empty metric was accepted")
	}

	// The valid shape of each scope must still be accepted.
	if err := insertAlertRow(t, d, org, host, alertRow{scope: "host"}); err != nil {
		t.Fatalf("a valid host-scope alert was refused: %v", err)
	}
	if err := insertAlertRow(t, d, org, host, alertRow{scope: "tuple", checkKey: "cpu", metric: "used_percent"}); err != nil {
		t.Fatalf("a valid tuple-scope alert was refused: %v", err)
	}
}

// TestClosedAtAndReasonAreSetTogether pins the pair CHECK in both wrong
// directions, plus the valid shape that closing an alert actually
// produces — the same three-shape treatment
// TestAcknowledgedAtAndSeverityAreSetTogether gives its own pair below.
func TestClosedAtAndReasonAreSetTogether(t *testing.T) {
	d := testdb.Open(t)
	org, host := seedHost(t, d)
	now := time.Now().UTC().Truncate(time.Microsecond)
	resolved := "resolved"

	if err := insertAlertRow(t, d, org, host, alertRow{
		scope: "tuple", checkKey: "disk", metric: "used_percent", closedAt: &now,
	}); err == nil {
		t.Fatal("closed_at set without closed_reason was accepted")
	}
	if err := insertAlertRow(t, d, org, host, alertRow{
		scope: "tuple", checkKey: "disk", metric: "used_percent", closedReason: &resolved,
	}); err == nil {
		t.Fatal("closed_reason set without closed_at was accepted")
	}
	if err := insertAlertRow(t, d, org, host, alertRow{
		scope: "tuple", checkKey: "disk", metric: "used_percent", closedAt: &now, closedReason: &resolved,
	}); err != nil {
		t.Fatalf("closed_at and closed_reason set together was refused: %v", err)
	}
}

// TestAcknowledgedAtAndSeverityAreSetTogether pins the acknowledgement
// pair CHECK the same way TestClosedAtAndReasonAreSetTogether pins the
// closing pair.
func TestAcknowledgedAtAndSeverityAreSetTogether(t *testing.T) {
	d := testdb.Open(t)
	org, host := seedHost(t, d)
	now := time.Now().UTC().Truncate(time.Microsecond)
	warn := "warn"

	if err := insertAlertRow(t, d, org, host, alertRow{
		scope: "tuple", checkKey: "disk", metric: "used_percent", ackAt: &now,
	}); err == nil {
		t.Fatal("acknowledged_at set without acknowledged_severity was accepted")
	}
	if err := insertAlertRow(t, d, org, host, alertRow{
		scope: "tuple", checkKey: "disk", metric: "used_percent", ackSeverity: &warn,
	}); err == nil {
		t.Fatal("acknowledged_severity set without acknowledged_at was accepted")
	}
	if err := insertAlertRow(t, d, org, host, alertRow{
		scope: "tuple", checkKey: "disk", metric: "used_percent", ackAt: &now, ackSeverity: &warn,
	}); err != nil {
		t.Fatalf("acknowledged_at and acknowledged_severity set together was refused: %v", err)
	}
}

// seedAlert inserts one open, tuple-scoped alert and returns its id, for
// TestAlertsRLS to hang an alert_events row off.
func seedAlert(t *testing.T, d *db.DB, org, host uuid.UUID) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	err := d.Pool().QueryRow(context.Background(), `
		INSERT INTO alerts (org_id, host_id, scope, check_key, item, metric, opened_at, severity, peak_severity)
		VALUES ($1, $2, 'tuple', 'cpu', '', 'used_percent', now(), 'warn', 'warn')
		RETURNING id`, org, host).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// seedAlertEvent inserts one 'opened' event for alertID, carrying its own
// org_id exactly as the migration requires.
func seedAlertEvent(t *testing.T, d *db.DB, org, alertID uuid.UUID) {
	t.Helper()
	_, err := d.Pool().Exec(context.Background(), `
		INSERT INTO alert_events (org_id, alert_id, at, kind, payload)
		VALUES ($1, $2, now(), 'opened', '{}'::jsonb)`, org, alertID)
	if err != nil {
		t.Fatal(err)
	}
}

// TestAlertsRLS mirrors rules.TestStateStoreRLS: it exercises the
// org_isolation policy directly, via a raw query over d.WithOrg's
// transaction, for both tables this migration adds. testdb.Open never sets
// pulsix.org_id on its own connections, so a plain query over the bare
// pool only ever exercises org_isolation_bypass — never org_isolation's
// own USING clause, which is what this test would fail to catch if it
// were ever replaced with USING (true). alert_events gets its own
// assertion, not a ride on alerts' own, because it is the table the task
// brief calls out as needing its own org_id precisely so RLS does not have
// to reach through alert_id.
func TestAlertsRLS(t *testing.T) {
	d := testdb.Open(t)
	orgA, hostA := seedHost(t, d)
	orgB, hostB := seedHost(t, d)

	alertA := seedAlert(t, d, orgA, hostA)
	alertB := seedAlert(t, d, orgB, hostB)
	seedAlertEvent(t, d, orgA, alertA)
	seedAlertEvent(t, d, orgB, alertB)

	assertOnlyOrgAVisible := func(t *testing.T, table string) {
		t.Helper()
		var seen []uuid.UUID
		err := d.WithOrg(context.Background(), orgA, func(tx pgx.Tx) error {
			rows, err := tx.Query(context.Background(), "SELECT org_id FROM "+table)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var id uuid.UUID
				if err := rows.Scan(&id); err != nil {
					return err
				}
				seen = append(seen, id)
			}
			return rows.Err()
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(seen) != 1 || seen[0] != orgA {
			t.Fatalf("RLS leak on %s: saw %v, want only [%s]", table, seen, orgA)
		}
	}

	t.Run("alerts", func(t *testing.T) { assertOnlyOrgAVisible(t, "alerts") })
	t.Run("alert_events", func(t *testing.T) { assertOnlyOrgAVisible(t, "alert_events") })
}
