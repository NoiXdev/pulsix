// The ledger's own tests exercise UndecidedEvents, CatchUpEvents,
// RecordDecision and ExpireClosedHeld against a real database, the
// same way store_sql_test.go exercises route CRUD. seedOrg (that file) is
// reused as-is; everything below that touches alerts or alert_events is
// new to this package, because the route-CRUD fixtures never needed an
// alert at all.
package alertroutes_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/alertroutes"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
)

// beginTx opens a plain, unscoped transaction on d's pool and registers its
// rollback, the same shape internal/alerts' own store_sql_test.go uses for
// the same reason: UndecidedEvents and RecordDecision run inside the
// follower's own tick transaction (a later task), never on their own, so
// the tests exercise them the same way.
func beginTx(t *testing.T, d *db.DB) pgx.Tx {
	t.Helper()
	tx, err := d.Pool().Begin(context.Background())
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	return tx
}

// seedHost creates a bare zone and host in org, the minimal fixture an
// alert's composite FK (org_id, host_id) needs.
func seedHost(t *testing.T, d *db.DB, org uuid.UUID) uuid.UUID {
	t.Helper()
	q := gen.New(d.Pool())
	z, err := q.CreateZone(context.Background(), gen.CreateZoneParams{
		ID: uuid.Must(uuid.NewV7()), OrgID: org, Slug: "z1", Name: "z1",
	})
	if err != nil {
		t.Fatal(err)
	}
	h, err := q.CreateHost(context.Background(), gen.CreateHostParams{
		ID: uuid.Must(uuid.NewV7()), OrgID: org, ZoneID: z.ID, Name: "h1", Facts: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	return h.ID
}

// seedOpenAlert inserts one open, tuple-scoped 'warn' alert on host, the
// subject every event below hangs off. severity_changed_at starts equal to
// opened_at — migration 00058's own backfill reading, "a brand new alert's
// severity has never changed, so the moment it last did is the moment it
// opened" — same as InsertAlert's own real write path (queries/alerts.sql).
// Stamped here for consistency only: escalate_sql.go does NOT fold this
// column into a chain's LastUrgentAt (its own doc comment explains why).
func seedOpenAlert(t *testing.T, d *db.DB, org, host uuid.UUID) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	err := d.Pool().QueryRow(context.Background(), `
		INSERT INTO alerts (org_id, host_id, scope, check_key, item, metric, opened_at, severity, peak_severity, severity_changed_at)
		VALUES ($1, $2, 'tuple', 'cpu', '', 'used_percent', now(), 'warn', 'warn', now())
		RETURNING id`, org, host).Scan(&id)
	if err != nil {
		t.Fatalf("seed open alert: %v", err)
	}
	return id
}

// seedOpenAlertAt is seedOpenAlert with an explicit opened_at/
// severity_changed_at instead of now() — for the handful of
// ChainsNeedingAttention tests (escalate_sql_test.go) that need the alert
// to predate an artificially backdated escalation base, so the route's
// own updated_at (seedRouteWithStepsAt, escalate_sql_test.go — which
// escalate_sql.go DOES fold into LastUrgentAt) cannot outrank the specific
// anchor each of those tests means to pin. severity_changed_at is stamped
// here only to keep the row's own timestamps consistent; escalate_sql.go
// does not fold it into anything (its own doc comment explains why a fold
// there could stall a chain that oscillates inside a route's severity
// set).
func seedOpenAlertAt(t *testing.T, d *db.DB, org, host uuid.UUID, at time.Time) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	err := d.Pool().QueryRow(context.Background(), `
		INSERT INTO alerts (org_id, host_id, scope, check_key, item, metric, opened_at, severity, peak_severity, severity_changed_at)
		VALUES ($1, $2, 'tuple', 'cpu', '', 'used_percent', $3, 'warn', 'warn', $3)
		RETURNING id`, org, host, at).Scan(&id)
	if err != nil {
		t.Fatalf("seed open alert at %v: %v", at, err)
	}
	return id
}

// seedAlertEventAt inserts one alert_events row for alertID at a caller-
// chosen timestamp, returning its id — the knob
// TestUndecidedEventsSurvivesOutOfOrderCommits needs to make a "committed
// late" event carry an earlier `at` than one committed before it.
func seedAlertEventAt(t *testing.T, d *db.DB, org, alertID uuid.UUID, kind string, at time.Time) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	err := d.Pool().QueryRow(context.Background(), `
		INSERT INTO alert_events (org_id, alert_id, at, kind, payload)
		VALUES ($1, $2, $3, $4, '{}'::jsonb)
		RETURNING id`, org, alertID, at, kind).Scan(&id)
	if err != nil {
		t.Fatalf("seed alert event: %v", err)
	}
	return id
}

// seedAlertEvent is seedAlertEventAt at the current time, for the tests
// that do not care about ordering.
func seedAlertEvent(t *testing.T, d *db.DB, org, alertID uuid.UUID, kind string) uuid.UUID {
	t.Helper()
	return seedAlertEventAt(t, d, org, alertID, kind, time.Now())
}

// closeAlert closes alertID inside tx — the same transaction a test's own
// RecordDecision and ExpireClosedHeld calls run on, so the close is
// visible to them without a second connection or a commit.
func closeAlert(t *testing.T, tx pgx.Tx, alertID uuid.UUID) {
	t.Helper()
	_, err := tx.Exec(context.Background(),
		`UPDATE alerts SET closed_at = now(), closed_reason = 'resolved' WHERE id = $1`, alertID)
	if err != nil {
		t.Fatalf("close alert: %v", err)
	}
}

func TestUndecidedEventsIgnoresDecidedOnes(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	org := seedOrg(t, d)
	host := seedHost(t, d, org)
	alert := seedOpenAlert(t, d, org, host)
	first := seedAlertEvent(t, d, org, alert, "opened")
	second := seedAlertEvent(t, d, org, alert, "severity_raised")
	s := alertroutes.NewSQLStore(d)

	tx := beginTx(t, d)
	got, err := s.UndecidedEvents(ctx, tx, org, 100)
	if err != nil {
		t.Fatalf("UndecidedEvents: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("UndecidedEvents() = %d, want 2", len(got))
	}
	if err := s.RecordDecision(ctx, tx, org, first, alertroutes.StateSent, time.Now()); err != nil {
		t.Fatalf("RecordDecision: %v", err)
	}
	got, err = s.UndecidedEvents(ctx, tx, org, 100)
	if err != nil {
		t.Fatalf("UndecidedEvents again: %v", err)
	}
	if len(got) != 1 || got[0].EventID != second {
		t.Errorf("after one decision UndecidedEvents() = %v, want only the second event", got)
	}
}

func TestUndecidedEventsSurvivesOutOfOrderCommits(t *testing.T) {
	// The case a cursor would have lost, and the reason decision 7 has
	// no watermark: two events whose timestamps run backwards relative to
	// the order they become visible. Both must be found.
	d := testdb.Open(t)
	ctx := context.Background()
	org := seedOrg(t, d)
	host := seedHost(t, d, org)
	alert := seedOpenAlert(t, d, org, host)

	late := seedAlertEventAt(t, d, org, alert, "opened", time.Now().Add(-time.Hour))
	early := seedAlertEventAt(t, d, org, alert, "severity_raised", time.Now())
	s := alertroutes.NewSQLStore(d)

	tx := beginTx(t, d)
	// Decide the NEWER one first, exactly as a tick would if it ran
	// between the two commits.
	if err := s.RecordDecision(ctx, tx, org, early, alertroutes.StateSent, time.Now()); err != nil {
		t.Fatalf("RecordDecision: %v", err)
	}
	got, err := s.UndecidedEvents(ctx, tx, org, 100)
	if err != nil {
		t.Fatalf("UndecidedEvents: %v", err)
	}
	if len(got) != 1 || got[0].EventID != late {
		t.Fatalf("the older event was lost: got %v, want the one stamped an hour ago", got)
	}
}

func TestExpireClosedHeld(t *testing.T) {
	// A suppressed event whose alert closed while still muted can never
	// be caught up — the catch-up pass requires an open alert — so it is
	// retired rather than left in that pass's working set forever.
	d := testdb.Open(t)
	ctx := context.Background()
	org := seedOrg(t, d)
	host := seedHost(t, d, org)
	alert := seedOpenAlert(t, d, org, host)
	ev := seedAlertEvent(t, d, org, alert, "opened")
	s := alertroutes.NewSQLStore(d)

	tx := beginTx(t, d)
	if err := s.RecordDecision(ctx, tx, org, ev, alertroutes.StateSuppressed, time.Now()); err != nil {
		t.Fatalf("RecordDecision: %v", err)
	}
	closeAlert(t, tx, alert)

	n, err := s.ExpireClosedHeld(ctx, tx, org, time.Now())
	if err != nil {
		t.Fatalf("ExpireClosedHeld: %v", err)
	}
	if n != 1 {
		t.Errorf("expired %d rows, want 1", n)
	}
	// The state written is the one the Go constant names. The statement
	// takes it as a parameter precisely so there is no second copy of the
	// string to drift from StateExpired, and this is what holds the two
	// together — "the catch-up pass no longer offers it" below would pass
	// just as well if the row had been retired to the wrong state.
	var state string
	if err := tx.QueryRow(ctx, `SELECT state FROM alert_notify_state WHERE alert_event_id = $1`, ev).Scan(&state); err != nil {
		t.Fatalf("read the retired row: %v", err)
	}
	if state != string(alertroutes.StateExpired) {
		t.Errorf("retired state = %q, want %q", state, alertroutes.StateExpired)
	}
	pending, err := s.CatchUpEvents(ctx, tx, org, 100)
	if err != nil {
		t.Fatalf("CatchUpEvents: %v", err)
	}
	if len(pending) != 0 {
		t.Errorf("CatchUpEvents() = %d rows, want 0 once the alert closed", len(pending))
	}
}

// TestCatchUpEventsFindsStillSuppressed proves the other half of the
// catch-up pass ExpireClosedHeld's own test does not reach: a
// suppressed event whose alert is still open stays in the working set,
// and RecordDecision moving it to StateSent takes it back out exactly the
// way the forward pass's own idempotency does.
func TestCatchUpEventsFindsStillSuppressed(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	org := seedOrg(t, d)
	host := seedHost(t, d, org)
	alert := seedOpenAlert(t, d, org, host)
	ev := seedAlertEvent(t, d, org, alert, "opened")
	s := alertroutes.NewSQLStore(d)

	tx := beginTx(t, d)
	if err := s.RecordDecision(ctx, tx, org, ev, alertroutes.StateSuppressed, time.Now()); err != nil {
		t.Fatalf("RecordDecision: %v", err)
	}
	got, err := s.CatchUpEvents(ctx, tx, org, 100)
	if err != nil {
		t.Fatalf("CatchUpEvents: %v", err)
	}
	if len(got) != 1 || got[0].EventID != ev {
		t.Fatalf("CatchUpEvents() = %v, want the still-open suppressed event", got)
	}
	if got[0].HostID != host || got[0].CheckKey != "cpu" || got[0].Metric != "used_percent" {
		t.Errorf("CatchUpEvents() subject = %+v, want the seeded alert's own subject", got[0])
	}

	if err := s.RecordDecision(ctx, tx, org, ev, alertroutes.StateSent, time.Now()); err != nil {
		t.Fatalf("RecordDecision (catch-up): %v", err)
	}
	got, err = s.CatchUpEvents(ctx, tx, org, 100)
	if err != nil {
		t.Fatalf("CatchUpEvents after catch-up: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("CatchUpEvents() = %d rows after catch-up, want 0", len(got))
	}
}

// TestRecordDecisionIsIdempotent proves the upsert half of decision 7:
// recording the same event twice with different states leaves exactly one
// row, the shape a retried tick relies on.
func TestRecordDecisionIsIdempotent(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	org := seedOrg(t, d)
	host := seedHost(t, d, org)
	alert := seedOpenAlert(t, d, org, host)
	ev := seedAlertEvent(t, d, org, alert, "opened")
	s := alertroutes.NewSQLStore(d)

	tx := beginTx(t, d)
	if err := s.RecordDecision(ctx, tx, org, ev, alertroutes.StateUnrouted, time.Now()); err != nil {
		t.Fatalf("RecordDecision: %v", err)
	}
	if err := s.RecordDecision(ctx, tx, org, ev, alertroutes.StateSent, time.Now()); err != nil {
		t.Fatalf("RecordDecision (again): %v", err)
	}
	var count int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM alert_notify_state WHERE alert_event_id = $1`, ev).Scan(&count); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if count != 1 {
		t.Errorf("alert_notify_state rows for one event = %d, want 1", count)
	}
	var state string
	if err := tx.QueryRow(ctx, `SELECT state FROM alert_notify_state WHERE alert_event_id = $1`, ev).Scan(&state); err != nil {
		t.Fatalf("read state: %v", err)
	}
	if state != string(alertroutes.StateSent) {
		t.Errorf("state = %q, want %q", state, alertroutes.StateSent)
	}
}

// TestUndecidedEventsIsScopedToOrganization is the ledger's own isolation
// test, the same review focus store_sql_test.go's own
// TestStoreIsolatesOrganizations pins for route CRUD: an event in one
// organization must never appear in another's undecided set, and a
// decision recorded in one organization must not be forgeable against
// another's event id.
//
// The second half is the one that needed a schema to stand on. Until
// migration 00053, alert_notify_state's foreign key named alert_event_id
// alone, so a row pairing orgB's org_id with orgA's event id was
// perfectly legal — and the forward pass's anti-join, which carried no
// organization predicate either, would have read it as "orgA's event is
// already decided" and never offered that event again. The composite
// foreign key is what makes the forgery fail at the write, and the
// predicate is what keeps the read correct even if one ever appeared.
func TestUndecidedEventsIsScopedToOrganization(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	orgA, orgB := seedOrg(t, d), seedOrg(t, d)
	hostA, hostB := seedHost(t, d, orgA), seedHost(t, d, orgB)
	alertA, alertB := seedOpenAlert(t, d, orgA, hostA), seedOpenAlert(t, d, orgB, hostB)
	eventA := seedAlertEvent(t, d, orgA, alertA, "opened")
	seedAlertEvent(t, d, orgB, alertB, "opened")
	s := alertroutes.NewSQLStore(d)

	tx := beginTx(t, d)
	got, err := s.UndecidedEvents(ctx, tx, orgA, 100)
	if err != nil {
		t.Fatalf("UndecidedEvents: %v", err)
	}
	if len(got) != 1 || got[0].EventID != eventA {
		t.Fatalf("UndecidedEvents(orgA) = %v, want exactly orgA's own event", got)
	}

	// Forge orgB's decision against orgA's event while orgA has ALREADY
	// decided it. This is the half the composite foreign key does not
	// cover: the row exists, so the write takes ON CONFLICT's DO UPDATE
	// path, and that path never names org_id — the existing row keeps its
	// own, so the foreign key stays satisfied and the state flips anyway.
	// A tenant could mark another tenant's event sent, and the anti-join
	// would never offer it again: an alert nobody is ever told about.
	decided := beginTx(t, d)
	if err := s.RecordDecision(ctx, decided, orgA, eventA, alertroutes.StateSuppressed, time.Now()); err != nil {
		t.Fatalf("orgA recording its own decision: %v", err)
	}
	if err := s.RecordDecision(ctx, decided, orgB, eventA, alertroutes.StateSent, time.Now()); err == nil {
		t.Error("orgB overwrote orgA's existing decision through ON CONFLICT; the upsert does not name the organization")
	}
	_ = decided.Rollback(ctx)

	// Forge orgB's decision against orgA's event. Its own transaction,
	// because the failure aborts whichever one it runs in.
	forging := beginTx(t, d)
	if err := s.RecordDecision(ctx, forging, orgB, eventA, alertroutes.StateSent, time.Now()); err == nil {
		t.Error("recording orgB's decision against orgA's event succeeded; the ledger's foreign key does not name the organization")
	}
	_ = forging.Rollback(ctx)

	// And orgA's event is still waiting to be decided, by orgA.
	after := beginTx(t, d)
	still, err := s.UndecidedEvents(ctx, after, orgA, 100)
	if err != nil {
		t.Fatalf("UndecidedEvents after the forgery attempt: %v", err)
	}
	if len(still) != 1 || still[0].EventID != eventA {
		t.Fatalf("UndecidedEvents(orgA) = %v after the forgery attempt, want orgA's own event still undecided", still)
	}
}

// TestCatchUpEventsFindsBothHeldStates is the widened pass, against rows
// that already exist rather than against a statement that merely compiles:
// 'suppressed' and 'deferred' are one working set, each row reports which
// of the two it was found in, and neither 'sent' nor 'unrouted' leaks into
// it. The reported state is what lets the follower leave an unchanged
// verdict unwritten, so a query that returned the right rows with the
// wrong state would hold decided_at still on an event whose reason had
// actually changed.
func TestCatchUpEventsFindsBothHeldStates(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	org := seedOrg(t, d)
	host := seedHost(t, d, org)
	alert := seedOpenAlert(t, d, org, host)
	s := alertroutes.NewSQLStore(d)
	tx := beginTx(t, d)

	want := map[uuid.UUID]alertroutes.LedgerState{}
	for _, st := range []alertroutes.LedgerState{
		alertroutes.StateSuppressed, alertroutes.StateDeferred,
		alertroutes.StateSent, alertroutes.StateUnrouted,
	} {
		ev := seedAlertEvent(t, d, org, alert, "opened")
		if err := s.RecordDecision(ctx, tx, org, ev, st, time.Now()); err != nil {
			t.Fatalf("RecordDecision(%s): %v", st, err)
		}
		if st == alertroutes.StateSuppressed || st == alertroutes.StateDeferred {
			want[ev] = st
		}
	}

	got, err := s.CatchUpEvents(ctx, tx, org, 100)
	if err != nil {
		t.Fatalf("CatchUpEvents: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("CatchUpEvents() = %d rows, want %d — exactly the held ones", len(got), len(want))
	}
	for _, ev := range got {
		if ev.Ledger != want[ev.EventID] {
			t.Errorf("event %s reported Ledger = %q, want %q", ev.EventID, ev.Ledger, want[ev.EventID])
		}
	}
}

// TestNotifyStatusForAlertPrefersSuppressedOverDeferred is the detail
// page's own precedence question (v0.4c Task 11): an alert whose events
// carry both held states must report 'suppressed', never 'deferred' —
// StateDeferred's own doc comment gives the reason an operator needs
// "host in maintenance" before "outside the delivery window".
func TestNotifyStatusForAlertPrefersSuppressedOverDeferred(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	org := seedOrg(t, d)
	host := seedHost(t, d, org)
	alert := seedOpenAlert(t, d, org, host)
	deferredEv := seedAlertEvent(t, d, org, alert, "opened")
	suppressedEv := seedAlertEvent(t, d, org, alert, "severity_raised")
	s := alertroutes.NewSQLStore(d)

	tx := beginTx(t, d)
	if err := s.RecordDecision(ctx, tx, org, deferredEv, alertroutes.StateDeferred, time.Now()); err != nil {
		t.Fatalf("RecordDecision(deferred): %v", err)
	}
	if err := s.RecordDecision(ctx, tx, org, suppressedEv, alertroutes.StateSuppressed, time.Now()); err != nil {
		t.Fatalf("RecordDecision(suppressed): %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}

	got, err := s.NotifyStatusForAlert(ctx, org, alert)
	if err != nil {
		t.Fatalf("NotifyStatusForAlert: %v", err)
	}
	if got != string(alertroutes.StateSuppressed) {
		t.Errorf("NotifyStatusForAlert() = %q, want %q", got, alertroutes.StateSuppressed)
	}
}

// TestNotifyStatusForAlertDeferredAlone proves the other half: a
// deferred-only alert reports 'deferred', not "" — the row is still held,
// even with no suppressed row to outrank it.
func TestNotifyStatusForAlertDeferredAlone(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	org := seedOrg(t, d)
	host := seedHost(t, d, org)
	alert := seedOpenAlert(t, d, org, host)
	ev := seedAlertEvent(t, d, org, alert, "opened")
	s := alertroutes.NewSQLStore(d)

	tx := beginTx(t, d)
	if err := s.RecordDecision(ctx, tx, org, ev, alertroutes.StateDeferred, time.Now()); err != nil {
		t.Fatalf("RecordDecision: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}

	got, err := s.NotifyStatusForAlert(ctx, org, alert)
	if err != nil {
		t.Fatalf("NotifyStatusForAlert: %v", err)
	}
	if got != string(alertroutes.StateDeferred) {
		t.Errorf("NotifyStatusForAlert() = %q, want %q", got, alertroutes.StateDeferred)
	}
}

// TestNotifyStatusForAlertEmptyWhenNothingIsHeld proves neither 'sent' nor
// 'unrouted' — nor an alert with no ledger row at all — ever reads as a
// hold: only 'suppressed'/'deferred' are "why did nothing arrive" answers.
func TestNotifyStatusForAlertEmptyWhenNothingIsHeld(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	org := seedOrg(t, d)
	host := seedHost(t, d, org)
	alert := seedOpenAlert(t, d, org, host)
	ev := seedAlertEvent(t, d, org, alert, "opened")
	s := alertroutes.NewSQLStore(d)

	if got, err := s.NotifyStatusForAlert(ctx, org, alert); err != nil || got != "" {
		t.Errorf("NotifyStatusForAlert() with no ledger row = (%q, %v), want (\"\", nil)", got, err)
	}

	tx := beginTx(t, d)
	if err := s.RecordDecision(ctx, tx, org, ev, alertroutes.StateSent, time.Now()); err != nil {
		t.Fatalf("RecordDecision: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}

	got, err := s.NotifyStatusForAlert(ctx, org, alert)
	if err != nil {
		t.Fatalf("NotifyStatusForAlert: %v", err)
	}
	if got != "" {
		t.Errorf("NotifyStatusForAlert() = %q, want empty for a sent event", got)
	}
}

// TestNotifyStatusForAlertScopedToOrganization is this method's own
// isolation test, the same focus TestUndecidedEventsIsScopedToOrganization
// pins for the follower's forward pass: orgB must not be able to read
// orgA's hold state by guessing orgA's alert id.
func TestNotifyStatusForAlertScopedToOrganization(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	orgA, orgB := seedOrg(t, d), seedOrg(t, d)
	hostA := seedHost(t, d, orgA)
	alertA := seedOpenAlert(t, d, orgA, hostA)
	evA := seedAlertEvent(t, d, orgA, alertA, "opened")
	s := alertroutes.NewSQLStore(d)

	tx := beginTx(t, d)
	if err := s.RecordDecision(ctx, tx, orgA, evA, alertroutes.StateSuppressed, time.Now()); err != nil {
		t.Fatalf("RecordDecision: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}

	got, err := s.NotifyStatusForAlert(ctx, orgB, alertA)
	if err != nil {
		t.Fatalf("NotifyStatusForAlert: %v", err)
	}
	if got != "" {
		t.Errorf("NotifyStatusForAlert() across organizations = %q, want empty", got)
	}
}

// TestExpireClosedHeldRetiresDeferredToo covers the half of the widened
// UPDATE ... FROM the suppressed case cannot reach. A 'deferred' row whose
// alert has closed is invisible to the catch-up pass forever (that pass
// requires an open alert) while still sitting in
// alert_notify_state_pending_idx — which is the exact unbounded growth
// this statement exists to prevent, so leaving 'deferred' out of its
// predicate would have reintroduced it silently.
func TestExpireClosedHeldRetiresDeferredToo(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	org := seedOrg(t, d)
	host := seedHost(t, d, org)
	alert := seedOpenAlert(t, d, org, host)
	ev := seedAlertEvent(t, d, org, alert, "opened")
	s := alertroutes.NewSQLStore(d)

	tx := beginTx(t, d)
	if err := s.RecordDecision(ctx, tx, org, ev, alertroutes.StateDeferred, time.Now()); err != nil {
		t.Fatalf("RecordDecision: %v", err)
	}
	closeAlert(t, tx, alert)

	n, err := s.ExpireClosedHeld(ctx, tx, org, time.Now())
	if err != nil {
		t.Fatalf("ExpireClosedHeld: %v", err)
	}
	if n != 1 {
		t.Errorf("expired %d rows, want 1", n)
	}
	var state string
	if err := tx.QueryRow(ctx, `SELECT state FROM alert_notify_state WHERE alert_event_id = $1`, ev).Scan(&state); err != nil {
		t.Fatalf("read the retired row: %v", err)
	}
	if state != string(alertroutes.StateExpired) {
		t.Errorf("retired state = %q, want %q", state, alertroutes.StateExpired)
	}
}
