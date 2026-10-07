// Task 6's own store methods — the reads (GetAlert, ListAlerts),
// Acknowledge/Snooze and their reversals, and WindowsForHost's own
// recursive CTE — are proven against a real database here, separately
// from store_sql_test.go (Task 5's own file for OpenForHost/Execute/
// Sweep): service_test.go's fake Store exercises Service's own logic, but
// never runs a single one of these SQL statements, and group resolution
// in particular (the recursive CTE over inventory_groups/
// inventory_memberships) is exactly the kind of thing that compiles
// clean and still gets the join wrong.
package alerts_test

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/noixdev/pulsix/internal/alerts"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/maintenance"
	"github.com/noixdev/pulsix/internal/rules"
)

// seedAdditionalHost adds a second host to org's own zone-less setup —
// a fresh zone of its own, since seedHost's own zone id is not returned —
// for tests that need two hosts in the *same* organization (the FK on
// maintenance_windows.host_id ties it to (org_id, id), so a host from a
// different organization cannot sit in a window of this one at all; the
// case worth proving here is "same org, different host", not that).
func seedAdditionalHost(t *testing.T, d *db.DB, org uuid.UUID) uuid.UUID {
	t.Helper()
	q := gen.New(d.Pool())
	z, err := q.CreateZone(context.Background(), gen.CreateZoneParams{
		ID: uuid.Must(uuid.NewV7()), OrgID: org, Slug: "z-" + uuid.Must(uuid.NewV7()).String(), Name: "z2",
	})
	if err != nil {
		t.Fatalf("create zone: %v", err)
	}
	h, err := q.CreateHost(context.Background(), gen.CreateHostParams{
		ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: z.ID, Name: "h2", Facts: []byte(`{}`),
	})
	if err != nil {
		t.Fatalf("create host: %v", err)
	}
	return h.ID
}

// seedInventoryGroup creates a bare inventory group, optionally under
// parent — the ancestry WindowsForHost's own recursive CTE walks.
func seedInventoryGroup(t *testing.T, d *db.DB, org uuid.UUID, name string, parent *uuid.UUID) uuid.UUID {
	t.Helper()
	g, err := gen.New(d.Pool()).CreateInventoryGroup(context.Background(), gen.CreateInventoryGroupParams{
		ID: uuid.Must(uuid.NewV7()), OrgID: org, ParentID: db.NullUUID(parent), Name: name,
		Schema: []byte(`{"fields": []}`), Vars: []byte(`{}`),
	})
	if err != nil {
		t.Fatalf("create inventory group %s: %v", name, err)
	}
	return g.ID
}

// seedUser creates a bare user — acknowledged_by (alerts) and actor_id
// (alert_events) both carry a plain FK to users(id), so Acknowledge and
// Snooze/Unsnooze need a real row to point at, not just any uuid.
func seedUser(t *testing.T, d *db.DB) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	u, err := gen.New(d.Pool()).CreateUser(context.Background(), gen.CreateUserParams{
		ID: id, Email: id.String() + "@example.test", DisplayName: "actor",
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	return u.ID
}

// addHostToGroup makes host a direct member of group.
func addHostToGroup(t *testing.T, d *db.DB, org, group, host uuid.UUID) {
	t.Helper()
	_, err := gen.New(d.Pool()).UpsertInventoryMembership(context.Background(), gen.UpsertInventoryMembershipParams{
		GroupID: group, HostID: host, OrgID: org, Vars: []byte(`{}`),
	})
	if err != nil {
		t.Fatalf("add host %s to group %s: %v", host, group, err)
	}
}

// oneOffWindow creates a one-off maintenance window of reason targeting
// exactly one of host/group (whichever is non-nil) spanning [starts, ends).
// Its name is suffixed with a fresh uuid — UNIQUE(org_id, name) would
// otherwise collide the moment a test creates two windows of the same
// target kind.
func oneOffWindow(t *testing.T, d *db.DB, org uuid.UUID, host, group *uuid.UUID, reason maintenance.Reason, starts, ends time.Time) uuid.UUID {
	t.Helper()
	kind := "org"
	switch {
	case host != nil:
		kind = "host"
	case group != nil:
		kind = "inventory_group"
	}
	id, err := gen.New(d.Pool()).CreateMaintenanceWindow(context.Background(), gen.CreateMaintenanceWindowParams{
		ID: uuid.Must(uuid.NewV7()), OrgID: org, Name: "w-" + kind + "-" + uuid.Must(uuid.NewV7()).String(), TargetKind: kind,
		HostID: db.NullUUID(host), GroupID: db.NullUUID(group),
		StartsAt: pgtype.Timestamptz{Time: starts, Valid: true}, EndsAt: pgtype.Timestamptz{Time: ends, Valid: true},
		Note: "", Reason: string(reason),
	})
	if err != nil {
		t.Fatalf("create %s window: %v", kind, err)
	}
	return id
}

func TestGetAlertReturnsNotFoundForUnknownID(t *testing.T) {
	d := testdb.Open(t)
	org, _ := seedHost(t, d)
	st := alerts.NewSQLStore(d)

	_, err := st.GetAlert(context.Background(), org, uuid.Must(uuid.NewV7()))
	if err != alerts.ErrNotFound {
		t.Fatalf("GetAlert(unknown id) = %v, want ErrNotFound", err)
	}
}

func TestGetAlertReturnsTheRow(t *testing.T) {
	d := testdb.Open(t)
	org, host := seedHost(t, d)
	st := alerts.NewSQLStore(d)
	tx := beginTx(t, d)
	sub := tuple(host, "cpu", "", "used")
	if err := st.Execute(context.Background(), tx, org, []alerts.Action{
		{Kind: alerts.ActionOpen, Subject: sub, At: time.Now().UTC(), Severity: rules.StateWarn},
	}); err != nil {
		t.Fatalf("execute open: %v", err)
	}
	open, err := st.OpenForHost(context.Background(), tx, org, host)
	if err != nil || len(open) != 1 {
		t.Fatalf("open for host: %v / %d rows", err, len(open))
	}
	if err := tx.Commit(context.Background()); err != nil {
		t.Fatalf("commit: %v", err)
	}

	got, err := st.GetAlert(context.Background(), org, open[0].ID)
	if err != nil {
		t.Fatalf("GetAlert: %v", err)
	}
	if got.Subject != sub || got.Severity != rules.StateWarn {
		t.Fatalf("GetAlert = %+v, want subject %+v at warn", got, sub)
	}
}

func TestListAlertsFiltersBySeverityAndOpen(t *testing.T) {
	d := testdb.Open(t)
	org, host := seedHost(t, d)
	st := alerts.NewSQLStore(d)
	tx := beginTx(t, d)
	now := time.Now().UTC()
	if err := st.Execute(context.Background(), tx, org, []alerts.Action{
		{Kind: alerts.ActionOpen, Subject: tuple(host, "cpu", "", "used"), At: now, Severity: rules.StateWarn},
		{Kind: alerts.ActionOpen, Subject: tuple(host, "disk", "", "used"), At: now, Severity: rules.StateCrit},
	}); err != nil {
		t.Fatalf("execute opens: %v", err)
	}
	open, err := st.OpenForHost(context.Background(), tx, org, host)
	if err != nil || len(open) != 2 {
		t.Fatalf("open for host: %v / %d rows", err, len(open))
	}
	var critID uuid.UUID
	for _, a := range open {
		if a.Severity == rules.StateCrit {
			critID = a.ID
		}
	}
	if err := st.Execute(context.Background(), tx, org, []alerts.Action{
		{Kind: alerts.ActionClose, AlertID: critID, At: now, Reason: alerts.Resolved},
	}); err != nil {
		t.Fatalf("execute close: %v", err)
	}
	if err := tx.Commit(context.Background()); err != nil {
		t.Fatalf("commit: %v", err)
	}

	warnSev := rules.StateWarn
	rows, _, err := st.ListAlerts(context.Background(), org, alerts.Filter{Severity: warnSev}, "", 0)
	if err != nil {
		t.Fatalf("list by severity: %v", err)
	}
	if len(rows) != 1 || rows[0].Severity != rules.StateWarn {
		t.Fatalf("list(severity=warn) = %+v, want exactly the warn alert", rows)
	}

	isOpen := true
	rows, _, err = st.ListAlerts(context.Background(), org, alerts.Filter{Open: &isOpen}, "", 0)
	if err != nil {
		t.Fatalf("list by open: %v", err)
	}
	if len(rows) != 1 || rows[0].Severity != rules.StateWarn {
		t.Fatalf("list(open=true) = %+v, want exactly the still-open warn alert (crit was closed)", rows)
	}
}

// TestGetAlertAndListAlertsNameEachAlertsOwnHost is the test the coordinator
// asked for explicitly after review: a test asserting a merely non-empty
// HostName would still pass against a join that silently returned the
// WRONG host's name (e.g. a join missing its ON host_id condition
// entirely, which Postgres would happily turn into a cross join picking
// an arbitrary row) — here, two alerts on two different hosts in the same
// organization must each get their OWN host's name, never the other's and
// never both the same. seedHost's own host is named "h1"
// (schema_test.go); seedAdditionalHost's is "h2" — two different names in
// the same org is the discriminating case: a bug that joined on org_id
// alone, with host_id wrong or missing from the ON clause, would make
// every row in the organization resolve to whichever host happened to
// come back first, and this test would catch that by finding the two
// alerts' names swapped or identical instead of "h1"/"h2".
func TestGetAlertAndListAlertsNameEachAlertsOwnHost(t *testing.T) {
	d := testdb.Open(t)
	org, host1 := seedHost(t, d)
	host2 := seedAdditionalHost(t, d, org)
	st := alerts.NewSQLStore(d)
	tx := beginTx(t, d)
	now := time.Now().UTC()
	if err := st.Execute(context.Background(), tx, org, []alerts.Action{
		{Kind: alerts.ActionOpen, Subject: tuple(host1, "cpu", "", "used"), At: now, Severity: rules.StateWarn},
		{Kind: alerts.ActionOpen, Subject: tuple(host2, "disk", "", "used"), At: now, Severity: rules.StateCrit},
	}); err != nil {
		t.Fatalf("execute opens: %v", err)
	}
	if err := tx.Commit(context.Background()); err != nil {
		t.Fatalf("commit: %v", err)
	}

	rows, _, err := st.ListAlerts(context.Background(), org, alerts.Filter{}, "", 0)
	if err != nil {
		t.Fatalf("list alerts: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("list alerts = %+v, want exactly 2 rows", rows)
	}
	gotNames := map[uuid.UUID]string{}
	for _, r := range rows {
		gotNames[r.Subject.HostID] = r.HostName
	}
	if gotNames[host1] != "h1" {
		t.Fatalf("ListAlerts named host1 (%s) as %q, want \"h1\" — got the wrong host's name", host1, gotNames[host1])
	}
	if gotNames[host2] != "h2" {
		t.Fatalf("ListAlerts named host2 (%s) as %q, want \"h2\" — got the wrong host's name", host2, gotNames[host2])
	}

	// Get must agree with List for the same row, not just List on its own.
	for _, r := range rows {
		got, err := st.GetAlert(context.Background(), org, r.ID)
		if err != nil {
			t.Fatalf("GetAlert(%s): %v", r.ID, err)
		}
		if got.HostName != gotNames[r.Subject.HostID] {
			t.Fatalf("GetAlert(%s).HostName = %q, want %q (List's own answer for the same host)", r.ID, got.HostName, gotNames[r.Subject.HostID])
		}
	}
}

// TestListAlertsAcknowledgedFilterUsesCoverageNotMerePresence is review
// finding 2 (Important): the server used to filter `acknowledged` on
// `acknowledged_at IS NOT NULL` alone — "has anyone ever acknowledged
// this" — while the row the GUI renders labels acknowledgement by
// COVERAGE (design decision 2: a worsening severity voids an
// acknowledgement). An alert acknowledged at warn that has since risen to
// crit therefore used to render "Unacknowledged — overtaken" on screen
// while being excluded from a Filter{Acknowledged: false} query and
// included in Filter{Acknowledged: true} — the exact opposite of what an
// operator filtering for unhandled work needs to find. The fix makes the
// predicate itself a coverage comparison, the same severity-rank CASE
// RaiseAlertSeverity's own peak_severity column already uses.
func TestListAlertsAcknowledgedFilterUsesCoverageNotMerePresence(t *testing.T) {
	d := testdb.Open(t)
	org, host := seedHost(t, d)
	st := alerts.NewSQLStore(d)
	sub := tuple(host, "cpu", "", "used")
	now := time.Now().UTC()

	tx := beginTx(t, d)
	if err := st.Execute(context.Background(), tx, org, []alerts.Action{
		{Kind: alerts.ActionOpen, Subject: sub, At: now, Severity: rules.StateWarn},
	}); err != nil {
		t.Fatalf("execute open: %v", err)
	}
	open, err := st.OpenForHost(context.Background(), tx, org, host)
	if err != nil || len(open) != 1 {
		t.Fatalf("open for host: %v / %d rows", err, len(open))
	}
	id := open[0].ID
	if err := tx.Commit(context.Background()); err != nil {
		t.Fatalf("commit open: %v", err)
	}

	// Acknowledged while still at warn: acknowledged_severity = warn.
	actor := seedUser(t, d)
	if err := st.Acknowledge(context.Background(), org, id, actor, "", now); err != nil {
		t.Fatalf("Acknowledge: %v", err)
	}

	// Then the tuple worsens to crit — VoidsAck true, the same shape
	// Reconcile's own advance() produces — which leaves acknowledged_at
	// and acknowledged_severity on the row untouched (RaiseAlertSeverity
	// only ever moves severity/peak_severity/snooze_until_change) but
	// means this ack no longer covers.
	tx2 := beginTx(t, d)
	if err := st.Execute(context.Background(), tx2, org, []alerts.Action{
		{Kind: alerts.ActionSeverity, AlertID: id, At: now.Add(time.Minute), Severity: rules.StateCrit, VoidsAck: true},
	}); err != nil {
		t.Fatalf("execute raise to crit: %v", err)
	}
	if err := tx2.Commit(context.Background()); err != nil {
		t.Fatalf("commit raise: %v", err)
	}

	trueF := true
	rows, _, err := st.ListAlerts(context.Background(), org, alerts.Filter{Acknowledged: &trueF}, "", 0)
	if err != nil {
		t.Fatalf("list acknowledged=true: %v", err)
	}
	for _, r := range rows {
		if r.ID == id {
			t.Fatalf("list(acknowledged=true) includes an alert whose ack (warn) no longer covers its severity (crit) — the filter is still presence-based, not coverage-based")
		}
	}

	falseF := false
	rows, _, err = st.ListAlerts(context.Background(), org, alerts.Filter{Acknowledged: &falseF}, "", 0)
	if err != nil {
		t.Fatalf("list acknowledged=false: %v", err)
	}
	found := false
	for _, r := range rows {
		if r.ID == id {
			found = true
		}
	}
	if !found {
		t.Fatalf("list(acknowledged=false) does not include an alert whose ack (warn) no longer covers its severity (crit) — an operator filtering for unhandled work would miss exactly this alert")
	}
}

// TestListAlertsAndGetAlertAgreeOnAFullyPopulatedRow is the Minor review
// finding this milestone's own review made load-bearing: nothing pinned
// toAlertFromListRow's completeness against toAlertFromGetRow's. The two
// are hand-written, field-by-field conversions off two different
// sqlc-generated row types (store_sql.go's own doc comment on why they are
// not shared) — dropping one field from only one of the two converters
// compiles clean and returns a zero value nobody notices unless something
// asserts on that exact field. This alert is pushed through every column
// the table has (raised severity so peak != severity, acknowledged with a
// note, flapped, snoozed, then closed) and ListAlerts/GetAlert's own
// answers for the identical row are required to be byte-for-byte equal —
// an asymmetric drop in either converter fails this immediately, which is
// exactly finding 2's own risk: AckSeverity missing from the list mapping
// would make every acknowledged row on the list page render
// "Unacknowledged — overtaken" while Get kept reporting it correctly.
func TestListAlertsAndGetAlertAgreeOnAFullyPopulatedRow(t *testing.T) {
	d := testdb.Open(t)
	org, host := seedHost(t, d)
	st := alerts.NewSQLStore(d)
	sub := tuple(host, "cpu", "", "used")
	t0 := time.Now().UTC().Truncate(time.Microsecond)

	tx := beginTx(t, d)
	if err := st.Execute(context.Background(), tx, org, []alerts.Action{
		{Kind: alerts.ActionOpen, Subject: sub, At: t0, Severity: rules.StateWarn},
	}); err != nil {
		t.Fatalf("execute open: %v", err)
	}
	open, err := st.OpenForHost(context.Background(), tx, org, host)
	if err != nil || len(open) != 1 {
		t.Fatalf("open for host: %v / %d rows", err, len(open))
	}
	id := open[0].ID
	if err := tx.Commit(context.Background()); err != nil {
		t.Fatalf("commit open: %v", err)
	}

	actor := seedUser(t, d)
	if err := st.Acknowledge(context.Background(), org, id, actor, "watching closely", t0.Add(time.Minute)); err != nil {
		t.Fatalf("Acknowledge: %v", err)
	}

	tx2 := beginTx(t, d)
	if err := st.Execute(context.Background(), tx2, org, []alerts.Action{
		{Kind: alerts.ActionSeverity, AlertID: id, At: t0.Add(2 * time.Minute), Severity: rules.StateCrit, VoidsAck: true},
		{Kind: alerts.ActionFlapped, AlertID: id, At: t0.Add(2 * time.Minute)},
	}); err != nil {
		t.Fatalf("execute raise+flap: %v", err)
	}
	if err := tx2.Commit(context.Background()); err != nil {
		t.Fatalf("commit raise+flap: %v", err)
	}

	until := t0.Add(time.Hour)
	if err := st.Snooze(context.Background(), org, id, actor, &until, false, t0.Add(3*time.Minute)); err != nil {
		t.Fatalf("Snooze: %v", err)
	}

	tx3 := beginTx(t, d)
	if err := st.Execute(context.Background(), tx3, org, []alerts.Action{
		{Kind: alerts.ActionClose, AlertID: id, At: t0.Add(4 * time.Minute), Reason: alerts.Stale},
	}); err != nil {
		t.Fatalf("execute close: %v", err)
	}
	if err := tx3.Commit(context.Background()); err != nil {
		t.Fatalf("commit close: %v", err)
	}

	viaGet, err := st.GetAlert(context.Background(), org, id)
	if err != nil {
		t.Fatalf("GetAlert: %v", err)
	}
	rows, _, err := st.ListAlerts(context.Background(), org, alerts.Filter{}, "", 0)
	if err != nil {
		t.Fatalf("ListAlerts: %v", err)
	}
	var viaList alerts.AlertWithHost
	found := false
	for _, r := range rows {
		if r.ID == id {
			viaList, found = r, true
		}
	}
	if !found {
		t.Fatalf("ListAlerts did not return the alert at all: %+v", rows)
	}

	if !reflect.DeepEqual(normalizeAlertTimes(viaGet.Alert), normalizeAlertTimes(viaList.Alert)) {
		t.Fatalf("GetAlert and ListAlerts disagree on the same row:\n  GetAlert:   %+v\n  ListAlerts: %+v", viaGet.Alert, viaList.Alert)
	}
	if viaGet.HostName != viaList.HostName || viaGet.HostName == "" {
		t.Fatalf("HostName = %q (Get) / %q (List), want both equal and non-empty", viaGet.HostName, viaList.HostName)
	}

	// Belt and braces: pin the concrete values too, so a field dropped
	// identically from BOTH converters (which the Get/List comparison
	// above cannot see) still fails here.
	want := alerts.Alert{
		ID:      id,
		OrgID:   org,
		Subject: sub,

		OpenedAt: t0,
		Severity: rules.StateCrit,
		Peak:     rules.StateCrit,

		ClosedAt:     timePtr(t0.Add(4 * time.Minute)),
		ClosedReason: alerts.Stale,

		AckAt:       timePtr(t0.Add(time.Minute)),
		AckBy:       &actor,
		AckSeverity: rules.StateWarn,
		AckNote:     "watching closely",

		SnoozedUntil:      timePtr(until),
		SnoozeUntilChange: false,

		WasFlapping: true,
	}
	if !reflect.DeepEqual(normalizeAlertTimes(viaList.Alert), normalizeAlertTimes(want)) {
		t.Fatalf("ListAlerts row = %+v, want %+v", viaList.Alert, want)
	}
}

func timePtr(t time.Time) *time.Time { return &t }

// normalizeAlertTimes converts every time.Time/*time.Time field to UTC
// before a reflect.DeepEqual comparison — Postgres hands pgx's own driver
// back timestamps in the connection's local zone (CEST here, not UTC), and
// time.Time's struct representation (including its *Location pointer)
// differs between two values naming the identical instant in different
// zones, which would otherwise fail this test on a difference that is not
// a real one.
func normalizeAlertTimes(a alerts.Alert) alerts.Alert {
	a.OpenedAt = a.OpenedAt.UTC()
	if a.ClosedAt != nil {
		a.ClosedAt = timePtr(a.ClosedAt.UTC())
	}
	if a.AckAt != nil {
		a.AckAt = timePtr(a.AckAt.UTC())
	}
	if a.SnoozedUntil != nil {
		a.SnoozedUntil = timePtr(a.SnoozedUntil.UTC())
	}
	return a
}

func TestAcknowledgeWritesRowAndEvent(t *testing.T) {
	d := testdb.Open(t)
	org, host := seedHost(t, d)
	st := alerts.NewSQLStore(d)
	tx := beginTx(t, d)
	sub := tuple(host, "cpu", "", "used")
	if err := st.Execute(context.Background(), tx, org, []alerts.Action{
		{Kind: alerts.ActionOpen, Subject: sub, At: time.Now().UTC(), Severity: rules.StateWarn},
	}); err != nil {
		t.Fatalf("execute open: %v", err)
	}
	open, err := st.OpenForHost(context.Background(), tx, org, host)
	if err != nil || len(open) != 1 {
		t.Fatalf("open for host: %v / %d rows", err, len(open))
	}
	if err := tx.Commit(context.Background()); err != nil {
		t.Fatalf("commit: %v", err)
	}

	actor := seedUser(t, d)
	at := time.Now().UTC().Truncate(time.Microsecond)
	if err := st.Acknowledge(context.Background(), org, open[0].ID, actor, "looking", at); err != nil {
		t.Fatalf("Acknowledge: %v", err)
	}

	got, err := st.GetAlert(context.Background(), org, open[0].ID)
	if err != nil {
		t.Fatalf("GetAlert: %v", err)
	}
	if got.AckSeverity != rules.StateWarn || got.AckNote != "looking" || got.AckBy == nil || *got.AckBy != actor {
		t.Fatalf("acknowledged row = %+v, want severity warn, note %q, actor %s", got, "looking", actor)
	}

	events, err := st.Events(context.Background(), org, open[0].ID)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	var sawAcknowledged bool
	for _, e := range events {
		if e.Kind == "acknowledged" {
			sawAcknowledged = true
			if e.ActorID == nil || *e.ActorID != actor {
				t.Fatalf("acknowledged event actor = %v, want %s", e.ActorID, actor)
			}
		}
	}
	if !sawAcknowledged {
		t.Fatalf("events = %+v, want an 'acknowledged' kind among them", events)
	}
}

func TestAcknowledgeRejectsAnAlreadyClosedAlert(t *testing.T) {
	d := testdb.Open(t)
	org, host := seedHost(t, d)
	st := alerts.NewSQLStore(d)
	tx := beginTx(t, d)
	sub := tuple(host, "cpu", "", "used")
	now := time.Now().UTC()
	if err := st.Execute(context.Background(), tx, org, []alerts.Action{
		{Kind: alerts.ActionOpen, Subject: sub, At: now, Severity: rules.StateWarn},
	}); err != nil {
		t.Fatalf("execute open: %v", err)
	}
	open, err := st.OpenForHost(context.Background(), tx, org, host)
	if err != nil || len(open) != 1 {
		t.Fatalf("open for host: %v / %d rows", err, len(open))
	}
	if err := st.Execute(context.Background(), tx, org, []alerts.Action{
		{Kind: alerts.ActionClose, AlertID: open[0].ID, At: now, Reason: alerts.Resolved},
	}); err != nil {
		t.Fatalf("execute close: %v", err)
	}
	if err := tx.Commit(context.Background()); err != nil {
		t.Fatalf("commit: %v", err)
	}

	err = st.Acknowledge(context.Background(), org, open[0].ID, uuid.Must(uuid.NewV7()), "late", time.Now().UTC())
	// errors.Is, not just "err == nil" (review finding, Minor): this is
	// the only test covering AcknowledgeAlert's own closed_at IS NULL
	// guard, and asserting mere non-nilness would also pass on
	// ErrNotFound, a constraint violation, or a dropped connection.
	if !errors.Is(err, alerts.ErrInvalidInput) {
		t.Fatalf("Acknowledge on a closed alert: err = %v, want ErrInvalidInput", err)
	}
}

func TestSnoozeAndUnsnoozeWriteEvents(t *testing.T) {
	d := testdb.Open(t)
	org, host := seedHost(t, d)
	st := alerts.NewSQLStore(d)
	tx := beginTx(t, d)
	sub := tuple(host, "cpu", "", "used")
	if err := st.Execute(context.Background(), tx, org, []alerts.Action{
		{Kind: alerts.ActionOpen, Subject: sub, At: time.Now().UTC(), Severity: rules.StateWarn},
	}); err != nil {
		t.Fatalf("execute open: %v", err)
	}
	open, err := st.OpenForHost(context.Background(), tx, org, host)
	if err != nil || len(open) != 1 {
		t.Fatalf("open for host: %v / %d rows", err, len(open))
	}
	if err := tx.Commit(context.Background()); err != nil {
		t.Fatalf("commit: %v", err)
	}
	id := open[0].ID
	actor := seedUser(t, d)
	until := time.Now().UTC().Add(time.Hour).Truncate(time.Microsecond)

	if err := st.Snooze(context.Background(), org, id, actor, &until, false, time.Now().UTC()); err != nil {
		t.Fatalf("Snooze: %v", err)
	}
	got, err := st.GetAlert(context.Background(), org, id)
	if err != nil {
		t.Fatalf("GetAlert: %v", err)
	}
	if got.SnoozedUntil == nil || !got.SnoozedUntil.Equal(until) {
		t.Fatalf("SnoozedUntil = %v, want %v", got.SnoozedUntil, until)
	}

	if err := st.Unsnooze(context.Background(), org, id, actor, time.Now().UTC()); err != nil {
		t.Fatalf("Unsnooze: %v", err)
	}
	got, err = st.GetAlert(context.Background(), org, id)
	if err != nil {
		t.Fatalf("GetAlert: %v", err)
	}
	if got.SnoozedUntil != nil || got.SnoozeUntilChange {
		t.Fatalf("after Unsnooze: SnoozedUntil=%v SnoozeUntilChange=%v, want both cleared", got.SnoozedUntil, got.SnoozeUntilChange)
	}

	events, err := st.Events(context.Background(), org, id)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	var kinds []string
	for _, e := range events {
		kinds = append(kinds, e.Kind)
	}
	if !containsKind(kinds, "snoozed") || !containsKind(kinds, "unsnoozed") {
		t.Fatalf("events = %v, want both 'snoozed' and 'unsnoozed'", kinds)
	}
}

func containsKind(kinds []string, want string) bool {
	for _, k := range kinds {
		if k == want {
			return true
		}
	}
	return false
}

func TestUnacknowledgeWritesNoEvent(t *testing.T) {
	d := testdb.Open(t)
	org, host := seedHost(t, d)
	st := alerts.NewSQLStore(d)
	tx := beginTx(t, d)
	sub := tuple(host, "cpu", "", "used")
	if err := st.Execute(context.Background(), tx, org, []alerts.Action{
		{Kind: alerts.ActionOpen, Subject: sub, At: time.Now().UTC(), Severity: rules.StateWarn},
	}); err != nil {
		t.Fatalf("execute open: %v", err)
	}
	open, err := st.OpenForHost(context.Background(), tx, org, host)
	if err != nil || len(open) != 1 {
		t.Fatalf("open for host: %v / %d rows", err, len(open))
	}
	if err := tx.Commit(context.Background()); err != nil {
		t.Fatalf("commit: %v", err)
	}
	id, actor := open[0].ID, seedUser(t, d)
	if err := st.Acknowledge(context.Background(), org, id, actor, "", time.Now().UTC()); err != nil {
		t.Fatalf("Acknowledge: %v", err)
	}
	before, err := st.Events(context.Background(), org, id)
	if err != nil {
		t.Fatalf("Events (before): %v", err)
	}

	if err := st.Unacknowledge(context.Background(), org, id, actor, time.Now().UTC()); err != nil {
		t.Fatalf("Unacknowledge: %v", err)
	}
	got, err := st.GetAlert(context.Background(), org, id)
	if err != nil {
		t.Fatalf("GetAlert: %v", err)
	}
	if got.AckAt != nil || got.AckSeverity != "" {
		t.Fatalf("after Unacknowledge: AckAt=%v AckSeverity=%q, want both cleared", got.AckAt, got.AckSeverity)
	}

	after, err := st.Events(context.Background(), org, id)
	if err != nil {
		t.Fatalf("Events (after): %v", err)
	}
	if len(after) != len(before) {
		t.Fatalf("event count changed from %d to %d after Unacknowledge, want unchanged (no 'unacknowledged' kind exists)", len(before), len(after))
	}
}

// TestWindowsForHostResolvesOrgHostAndInventoryGroupTargets is
// WindowsForHost's own test: the recursive CTE must resolve a window
// targeting the organization, the host directly, and a group two levels
// above the host's own direct membership — and must not resolve a window
// on an unrelated host, or one that has already ended.
func TestWindowsForHostResolvesOrgHostAndInventoryGroupTargets(t *testing.T) {
	d := testdb.Open(t)
	org, host := seedHost(t, d)
	otherHost := seedAdditionalHost(t, d, org) // same org, not in the group — must not match
	st := alerts.NewSQLStore(d)
	now := time.Now().UTC().Truncate(time.Microsecond)

	root := seedInventoryGroup(t, d, org, "root", nil)
	leaf := seedInventoryGroup(t, d, org, "leaf", &root)
	addHostToGroup(t, d, org, leaf, host)

	orgWin := oneOffWindow(t, d, org, nil, nil, maintenance.ReasonMaintenance, now.Add(-time.Hour), now.Add(time.Hour))
	hostWin := oneOffWindow(t, d, org, &host, nil, maintenance.ReasonSilence, now.Add(-time.Hour), now.Add(time.Hour))
	ancestorGroupWin := oneOffWindow(t, d, org, nil, &root, maintenance.ReasonMaintenance, now.Add(-time.Hour), now.Add(time.Hour))
	_ = oneOffWindow(t, d, org, &otherHost, nil, maintenance.ReasonSilence, now.Add(-time.Hour), now.Add(time.Hour)) // unrelated host
	_ = oneOffWindow(t, d, org, &host, nil, maintenance.ReasonSilence, now.Add(-3*time.Hour), now.Add(-2*time.Hour)) // already ended

	got, err := st.WindowsForHost(context.Background(), org, host, now)
	if err != nil {
		t.Fatalf("WindowsForHost: %v", err)
	}
	ids := map[uuid.UUID]bool{}
	for _, occ := range got {
		ids[occ.WindowID] = true
	}
	if len(got) != 3 {
		t.Fatalf("WindowsForHost returned %d occurrences, want 3: %+v", len(got), got)
	}
	for _, want := range []uuid.UUID{orgWin, hostWin, ancestorGroupWin} {
		if !ids[want] {
			t.Errorf("missing expected window %s among %v", want, ids)
		}
	}
}

// recurringWindow creates a recurring maintenance window targeting host
// (or the organization, when host is nil) — WindowsForHost's own
// occurrenceAt (store_sql.go) is the only thing in this package that ever
// evaluates one, and until this test, nothing had.
func recurringWindow(t *testing.T, d *db.DB, org uuid.UUID, host *uuid.UUID, reason maintenance.Reason, cron, timezone string, duration time.Duration) uuid.UUID {
	t.Helper()
	kind := "org"
	if host != nil {
		kind = "host"
	}
	secs := int32(duration / time.Second) //nolint:gosec // test fixture, small durations only
	id, err := gen.New(d.Pool()).CreateMaintenanceWindow(context.Background(), gen.CreateMaintenanceWindowParams{
		ID: uuid.Must(uuid.NewV7()), OrgID: org, Name: "rw-" + kind + "-" + uuid.Must(uuid.NewV7()).String(), TargetKind: kind,
		HostID: db.NullUUID(host), Cron: &cron, DurationSeconds: &secs, Timezone: &timezone, Note: "", Reason: string(reason),
	})
	if err != nil {
		t.Fatalf("create recurring %s window: %v", kind, err)
	}
	return id
}

// TestWindowsForHostResolvesARecurringWindowMidStretch closes Finding 4:
// occurrenceAt's recurring branch (store_sql.go) — the most intricate new
// Go in the Task 6 diff — had no test of any kind before this one, fake
// or real. A daily window from 12:00 to 14:00 UTC, asked about at 13:00
// (mid-stretch) must report the *stretch's* own bounds, not the instant
// asked about; asked about at 15:00 (after it ended, before tomorrow's)
// must report nothing at all.
func TestWindowsForHostResolvesARecurringWindowMidStretch(t *testing.T) {
	d := testdb.Open(t)
	org, host := seedHost(t, d)
	st := alerts.NewSQLStore(d)
	windowID := recurringWindow(t, d, org, &host, maintenance.ReasonMaintenance, "0 12 * * *", "UTC", 2*time.Hour)

	midStretch := time.Date(2026, 1, 1, 13, 0, 0, 0, time.UTC)
	got, err := st.WindowsForHost(context.Background(), org, host, midStretch)
	if err != nil {
		t.Fatalf("WindowsForHost (mid-stretch): %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d occurrences, want 1: %+v", len(got), got)
	}
	wantStart := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	wantEnd := time.Date(2026, 1, 1, 14, 0, 0, 0, time.UTC)
	if got[0].WindowID != windowID || !got[0].Start.Equal(wantStart) || !got[0].End.Equal(wantEnd) {
		t.Fatalf("occurrence = %+v, want window %s spanning [%v, %v)", got[0], windowID, wantStart, wantEnd)
	}

	afterStretch := time.Date(2026, 1, 1, 15, 0, 0, 0, time.UTC)
	got, err = st.WindowsForHost(context.Background(), org, host, afterStretch)
	if err != nil {
		t.Fatalf("WindowsForHost (after the stretch): %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d occurrences after the stretch ended, want 0: %+v", len(got), got)
	}
}

// TestAcknowledgeCapturesSeverityAsOfTheWrite is Finding 1's own
// integration proof: AcknowledgeAlert no longer takes a severity
// parameter at all (it never reaches the Go layer), so the only way
// acknowledged_severity can end up correct is if the UPDATE's own
// `acknowledged_severity = severity` reads the row's current value. This
// raises the alert to crit and commits *before* ever calling Acknowledge
// — simulating "the evaluator moved it in the gap Service's own earlier
// GetAlert could not see" — and asserts the ack lands at crit, never at
// whatever a caller might have believed the severity to be.
func TestAcknowledgeCapturesSeverityAsOfTheWrite(t *testing.T) {
	d := testdb.Open(t)
	org, host := seedHost(t, d)
	st := alerts.NewSQLStore(d)
	tx := beginTx(t, d)
	sub := tuple(host, "cpu", "", "used")
	now := time.Now().UTC()
	if err := st.Execute(context.Background(), tx, org, []alerts.Action{
		{Kind: alerts.ActionOpen, Subject: sub, At: now, Severity: rules.StateWarn},
	}); err != nil {
		t.Fatalf("execute open: %v", err)
	}
	open, err := st.OpenForHost(context.Background(), tx, org, host)
	if err != nil || len(open) != 1 {
		t.Fatalf("open for host: %v / %d rows", err, len(open))
	}
	if err := st.Execute(context.Background(), tx, org, []alerts.Action{
		{Kind: alerts.ActionSeverity, AlertID: open[0].ID, At: now, Severity: rules.StateCrit},
	}); err != nil {
		t.Fatalf("execute raise to crit: %v", err)
	}
	if err := tx.Commit(context.Background()); err != nil {
		t.Fatalf("commit: %v", err)
	}

	actor := seedUser(t, d)
	if err := st.Acknowledge(context.Background(), org, open[0].ID, actor, "late", time.Now().UTC()); err != nil {
		t.Fatalf("Acknowledge: %v", err)
	}
	got, err := st.GetAlert(context.Background(), org, open[0].ID)
	if err != nil {
		t.Fatalf("GetAlert: %v", err)
	}
	if got.AckSeverity != rules.StateCrit {
		t.Fatalf("AckSeverity = %q, want %q — Acknowledge must capture the row's own current severity, not whatever a caller last saw", got.AckSeverity, rules.StateCrit)
	}
}

// TestSnoozeRejectsAnAlreadyClosedAlert is Minor A's own test: SnoozeAlert
// gained the same closed_at IS NULL guard AcknowledgeAlert already had,
// since without it a closed alert could be snoozed and List would then
// report it Muted.
func TestSnoozeRejectsAnAlreadyClosedAlert(t *testing.T) {
	d := testdb.Open(t)
	org, host := seedHost(t, d)
	st := alerts.NewSQLStore(d)
	tx := beginTx(t, d)
	sub := tuple(host, "cpu", "", "used")
	now := time.Now().UTC()
	if err := st.Execute(context.Background(), tx, org, []alerts.Action{
		{Kind: alerts.ActionOpen, Subject: sub, At: now, Severity: rules.StateWarn},
	}); err != nil {
		t.Fatalf("execute open: %v", err)
	}
	open, err := st.OpenForHost(context.Background(), tx, org, host)
	if err != nil || len(open) != 1 {
		t.Fatalf("open for host: %v / %d rows", err, len(open))
	}
	if err := st.Execute(context.Background(), tx, org, []alerts.Action{
		{Kind: alerts.ActionClose, AlertID: open[0].ID, At: now, Reason: alerts.Resolved},
	}); err != nil {
		t.Fatalf("execute close: %v", err)
	}
	if err := tx.Commit(context.Background()); err != nil {
		t.Fatalf("commit: %v", err)
	}

	until := time.Now().UTC().Add(time.Hour)
	err = st.Snooze(context.Background(), org, open[0].ID, uuid.Must(uuid.NewV7()), &until, false, time.Now().UTC())
	if err == nil {
		t.Fatal("Snooze on a closed alert returned no error, want an error (the row must not change)")
	}
	got, getErr := st.GetAlert(context.Background(), org, open[0].ID)
	if getErr != nil {
		t.Fatalf("GetAlert: %v", getErr)
	}
	if got.SnoozedUntil != nil {
		t.Fatalf("SnoozedUntil = %v after a rejected Snooze on a closed alert, want nil", got.SnoozedUntil)
	}
}

// TestListAlertsPaginates is v0.4a's final review, Important: the alerts
// table has no retention policy by design, the feature's own front page
// defaults to no state filter, and the query had no LIMIT — so every page
// load read every alert the organization had ever opened. It now pages on
// the (opened_at, id) keyset its ORDER BY already provided.
//
// The test walks the whole set one row at a time, which is what proves the
// cursor is a keyset and not an offset: every row must appear exactly once
// and in the server's own newest-first order, and the last page must hand
// back an empty cursor rather than looping.
func TestListAlertsPaginates(t *testing.T) {
	d := testdb.Open(t)
	org, host := seedHost(t, d)
	st := alerts.NewSQLStore(d)
	ctx := context.Background()

	base := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)
	const total = 5
	for i := range total {
		insertAlertAt(t, d, org, host, "disk", "/m"+strconv.Itoa(i), "used_pct", base.Add(time.Duration(i)*time.Minute))
	}

	var seen []uuid.UUID
	cursor := ""
	for page := 0; page <= total+1; page++ {
		rows, next, err := st.ListAlerts(ctx, org, alerts.Filter{}, cursor, 1)
		if err != nil {
			t.Fatalf("list page %d: %v", page, err)
		}
		if len(rows) == 0 {
			break
		}
		if len(rows) != 1 {
			t.Fatalf("page %d returned %d rows, want the limit of 1", page, len(rows))
		}
		seen = append(seen, rows[0].ID)
		if next == "" {
			break
		}
		cursor = next
	}
	if len(seen) != total {
		t.Fatalf("walking one row at a time saw %d alerts, want %d", len(seen), total)
	}
	unique := map[uuid.UUID]bool{}
	for _, id := range seen {
		if unique[id] {
			t.Fatalf("alert %s appeared on two pages", id)
		}
		unique[id] = true
	}

	// The same set unpaginated, to pin that the order the pages walked is
	// the server's own newest-first one and not an artefact of paging.
	all, next, err := st.ListAlerts(ctx, org, alerts.Filter{}, "", total)
	if err != nil {
		t.Fatalf("list unpaginated: %v", err)
	}
	if next != "" {
		t.Fatalf("next cursor = %q on a page that holds every alert, want empty", next)
	}
	for i, a := range all {
		if a.ID != seen[i] {
			t.Fatalf("row %d: paged walk saw %s, one page sees %s", i, seen[i], a.ID)
		}
	}
}

// TestListAlertsRejectsAMalformedCursor: a ?cursor= value EncodeCursor did
// not produce is the caller's mistake, and the store says so with a
// sentinel the API maps to 400 rather than letting it surface as a 500.
func TestListAlertsRejectsAMalformedCursor(t *testing.T) {
	d := testdb.Open(t)
	org, _ := seedHost(t, d)
	_, _, err := alerts.NewSQLStore(d).ListAlerts(context.Background(), org, alerts.Filter{}, "not-a-cursor", 10)
	if !errors.Is(err, alerts.ErrInvalidCursor) {
		t.Fatalf("ListAlerts with a malformed cursor = %v, want ErrInvalidCursor", err)
	}
}

// TestAlertCursorRoundTrips pins the format itself: the pair the keyset
// resumes from must survive encoding and decoding exactly, to the
// nanosecond, or the first row of the next page is silently the last row
// of the previous one again.
func TestAlertCursorRoundTrips(t *testing.T) {
	at := time.Date(2026, 10, 7, 14, 3, 7, 123456789, time.UTC)
	id := uuid.Must(uuid.NewV7())
	gotAt, gotID, err := alerts.DecodeCursor(alerts.EncodeCursor(at, id))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !gotAt.Equal(at) || gotID != id {
		t.Fatalf("round trip = (%v, %s), want (%v, %s)", gotAt, gotID, at, id)
	}
	if gotAt, gotID, err := alerts.DecodeCursor(""); err != nil || !gotAt.IsZero() || gotID != uuid.Nil {
		t.Fatalf("the empty cursor decoded to (%v, %s, %v), want the zero first-page values", gotAt, gotID, err)
	}
}

// insertAlertAt seeds one open tuple alert with an explicit opened_at, so
// the pagination tests above have a known keyset order to walk.
func insertAlertAt(t *testing.T, d *db.DB, org, host uuid.UUID, checkKey, item, metric string, openedAt time.Time) {
	t.Helper()
	_, err := d.Pool().Exec(context.Background(), `
		INSERT INTO alerts (org_id, host_id, scope, check_key, item, metric, opened_at, severity, peak_severity)
		VALUES ($1, $2, 'tuple', $3, $4, $5, $6, 'warn', 'warn')`,
		org, host, checkKey, item, metric, openedAt)
	if err != nil {
		t.Fatalf("insert alert at %v: %v", openedAt, err)
	}
}
