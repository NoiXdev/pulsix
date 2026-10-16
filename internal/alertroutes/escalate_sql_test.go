// The escalation store's own tests: ReplaceSteps, ClaimStep and
// ChainsNeedingAttention against a real database, the same shape
// store_sql_test.go and ledger_sql_test.go already take for this package's
// other stores. seedOrg, seedChannel (store_sql_test.go) and seedHost,
// seedOpenAlert, beginTx (ledger_sql_test.go) are reused as-is — nothing
// here needed a fixture those files do not already provide.
//
// Deliberately not t.Parallel(): testdb.Open truncates every table in the
// one shared container this test binary opens once (sync.Once) when ITS
// OWN test ends, not when the whole binary does — store_sql_test.go and
// ledger_sql_test.go never call t.Parallel() for exactly that reason,
// confirmed the hard way here first: with it, one test's teardown
// truncates a row a still-running sibling just inserted, and the failure
// shows up as an unrelated foreign-key violation or a deadlock between two
// concurrent TRUNCATEs, never as the thing actually under test.
package alertroutes_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/alertroutes"
	"github.com/noixdev/pulsix/internal/alerts"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/rules"
)

// seedRouteWithSteps creates a bare, enabled, org-scoped route (Create,
// store_sql.go) and lays steps onto it (ReplaceSteps) inside its own
// committed transaction — the two-call shape a real caller takes too,
// since a route's ladder is edited after the route itself exists.
func seedRouteWithSteps(t *testing.T, d *db.DB, org uuid.UUID, steps []alertroutes.Step) uuid.UUID {
	t.Helper()
	return seedRouteWithStepsAt(t, d, org, steps, time.Now())
}

// seedRouteWithStepsAt is seedRouteWithSteps with an explicit creation
// instant instead of time.Now() — Create's own UpdatedAt starts equal to
// it, and escalate_sql.go now folds alert_routes.updated_at into
// LastUrgentAt (migration 00058's review, Critical 3), so the handful of
// ChainsNeedingAttention tests that pin a SPECIFIC anchor need the route
// itself to predate it, the same reason seedOpenAlertAt exists
// (ledger_sql_test.go's own doc comment).
func seedRouteWithStepsAt(t *testing.T, d *db.DB, org uuid.UUID, steps []alertroutes.Step, at time.Time) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	ch := seedChannel(t, d, org)
	s := alertroutes.NewSQLStore(d)
	route, err := s.Create(ctx, org, uuid.New(), alertroutes.Input{
		Name: "route-" + uuid.NewString(), Enabled: true, Scope: alertroutes.ScopeOrg,
		Severities: []rules.State{rules.StateCrit, rules.StateWarn},
		Events:     []alerts.EventKind{alerts.EventOpened},
		Channels:   []uuid.UUID{ch},
	}, at)
	if err != nil {
		t.Fatalf("seed route: %v", err)
	}
	mustReplaceSteps(t, d, org, route.ID, steps)
	return route.ID
}

// mustReplaceSteps calls ReplaceSteps inside its own committed
// transaction — the follower (a later task) always has one of its own
// open already, but nothing here does, so each helper opens and commits
// its own, the same posture mustClaim and mustChains below take.
func mustReplaceSteps(t *testing.T, d *db.DB, org, route uuid.UUID, steps []alertroutes.Step) {
	t.Helper()
	ctx := context.Background()
	s := alertroutes.NewSQLStore(d)
	tx := beginTx(t, d)
	if err := s.ReplaceSteps(ctx, tx, org, route, steps); err != nil {
		t.Fatalf("replace steps: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit replace steps: %v", err)
	}
}

// mustReadSteps reads a route's ladder back with a plain query against the
// real table, the same "read the row back directly" posture
// TestExpireClosedHeld (ledger_sql_test.go) already takes rather than
// adding a store method only a test would ever call.
func mustReadSteps(t *testing.T, d *db.DB, org, route uuid.UUID) []alertroutes.Step {
	t.Helper()
	rows, err := d.Pool().Query(context.Background(),
		`SELECT position, delay_seconds FROM alert_route_steps WHERE org_id = $1 AND route_id = $2 ORDER BY position`,
		org, route)
	if err != nil {
		t.Fatalf("read steps: %v", err)
	}
	defer rows.Close()
	var out []alertroutes.Step
	for rows.Next() {
		var position, delaySeconds int
		if err := rows.Scan(&position, &delaySeconds); err != nil {
			t.Fatalf("scan step: %v", err)
		}
		out = append(out, alertroutes.Step{Position: position, Delay: time.Duration(delaySeconds) * time.Second})
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read steps: %v", err)
	}
	return out
}

// mustClaim calls ClaimStep inside its own committed transaction and
// fails the test on error — every call site below only cares that the
// claim succeeded, never about the reported bool, so this keeps that out
// of the call sites that do not test idempotency itself
// (TestClaimingAStepTwiceIsANoOp calls store.ClaimStep directly instead,
// to inspect the bool).
func mustClaim(t *testing.T, d *db.DB, org, alert, route uuid.UUID, position int, at time.Time) {
	t.Helper()
	ctx := context.Background()
	s := alertroutes.NewSQLStore(d)
	tx := beginTx(t, d)
	if _, err := s.ClaimStep(ctx, tx, org, alert, route, position, at); err != nil {
		t.Fatalf("claim step: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit claim: %v", err)
	}
}

// mustChains runs ChainsNeedingAttention inside its own transaction — read-
// only, so a plain beginTx without a commit is enough, the same posture
// TestNotifyStatusForAlertEmptyWhenNothingIsHeld's own reads outside a
// follower tick take for the ledger's other table.
func mustChains(t *testing.T, d *db.DB, org uuid.UUID) []alertroutes.Chain {
	t.Helper()
	ctx := context.Background()
	s := alertroutes.NewSQLStore(d)
	tx := beginTx(t, d)
	chains, err := s.ChainsNeedingAttention(ctx, tx, org, 100)
	if err != nil {
		t.Fatalf("chains needing attention: %v", err)
	}
	return chains
}

// mustCloseAlert closes alertID directly, the same shape closeAlert
// (ledger_sql_test.go) already takes but against the pool rather than a
// caller's tx — TestChainsNeedingAttentionSkipsClosedAlerts reads the
// close back through a fresh transaction of its own, so there is no
// shared transaction to join here.
func mustCloseAlert(t *testing.T, d *db.DB, alertID uuid.UUID) {
	t.Helper()
	if _, err := d.Pool().Exec(context.Background(),
		`UPDATE alerts SET closed_at = now(), closed_reason = 'resolved' WHERE id = $1`, alertID); err != nil {
		t.Fatalf("close alert: %v", err)
	}
}

// mustSnoozeThenClear simulates a human snoozing alertID and then clearing
// that snooze themselves — alerts.Snooze followed by alerts.Unsnooze
// (internal/alerts/store_sql.go), inline here rather than through that
// package (alertroutes cannot import it without a cycle, and this file
// already reads/writes alerts/alert_events directly: seedOpenAlert,
// seedAlertEventAt, mustCloseAlert). Unsnooze's own shape is what spec
// decision 5 leans on: it clears snoozed_until back to NULL and records
// only the 'unsnoozed' event — nothing else is left behind once a person
// clears a snooze, which is exactly what made the missing 'unsnoozed'
// anchor a silent gap rather than a loud failure.
func mustSnoozeThenClear(t *testing.T, d *db.DB, alertID uuid.UUID, until, clearedAt time.Time) {
	t.Helper()
	ctx := context.Background()
	if _, err := d.Pool().Exec(ctx,
		`UPDATE alerts SET snoozed_until = $1 WHERE id = $2`, until, alertID); err != nil {
		t.Fatalf("snooze alert: %v", err)
	}
	if _, err := d.Pool().Exec(ctx,
		`UPDATE alerts SET snoozed_until = NULL WHERE id = $1`, alertID); err != nil {
		t.Fatalf("clear snooze: %v", err)
	}
	org, err := alertOrgID(d, alertID)
	if err != nil {
		t.Fatalf("read alert org: %v", err)
	}
	seedAlertEventAt(t, d, org, alertID, "unsnoozed", clearedAt)
}

// alertOrgID reads back the org_id a bare alert id belongs to —
// mustSnoozeThenClear's own caller only has alertID in hand, but
// seedAlertEventAt (ledger_sql_test.go) needs org_id too.
func alertOrgID(d *db.DB, alertID uuid.UUID) (uuid.UUID, error) {
	var org uuid.UUID
	err := d.Pool().QueryRow(context.Background(),
		`SELECT org_id FROM alerts WHERE id = $1`, alertID).Scan(&org)
	return org, err
}

// Review Focus 1: a shared alert_id is where an over-eager WHERE turns
// two independent ladders into one.
func TestTwoRoutesOnOneAlertKeepSeparateChains(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d)
	host := seedHost(t, d, org)
	alert := seedOpenAlert(t, d, org, host)
	// r1 carries a second step so it still has an unfired rung (position 2)
	// after position 1 fires below — otherwise a fully-climbed ladder
	// would legitimately drop out of ChainsNeedingAttention's own result
	// (it has nothing left to escalate), and this test would no longer be
	// able to tell "r1 correctly left the result" apart from "r1's state
	// was corrupted by r2's claim", which is the one thing it exists to
	// catch.
	r1 := seedRouteWithSteps(t, d, org, []alertroutes.Step{
		{Position: 1, Delay: 5 * time.Minute}, {Position: 2, Delay: 5 * time.Minute},
	})
	r2 := seedRouteWithSteps(t, d, org, []alertroutes.Step{{Position: 1, Delay: 9 * time.Minute}})
	base := time.Now().UTC().Truncate(time.Second)
	mustClaim(t, d, org, alert, r1, 0, base)
	mustClaim(t, d, org, alert, r2, 0, base)

	// Firing r1's rung must leave r2's untouched.
	mustClaim(t, d, org, alert, r1, 1, base.Add(5*time.Minute))

	chains := mustChains(t, d, org)
	found := map[uuid.UUID]bool{}
	for _, c := range chains {
		switch c.RouteID {
		case r1:
			found[r1] = true
			if c.State.FiredUpTo != 1 {
				t.Fatalf("route 1 FiredUpTo = %d, want 1", c.State.FiredUpTo)
			}
		case r2:
			found[r2] = true
			if c.State.FiredUpTo != 0 {
				t.Fatalf("route 2 FiredUpTo = %d, want 0 — one route's step moved another's chain", c.State.FiredUpTo)
			}
		}
	}
	if !found[r1] || !found[r2] {
		t.Fatalf("chains = %+v, want both routes represented", chains)
	}
}

// Review Focus 3: two managers tick at once, or one tick overlaps the
// next. The second claim must be a quiet no-op, not an error that aborts
// the whole pass.
func TestClaimingAStepTwiceIsANoOp(t *testing.T) {
	d := testdb.Open(t)
	ctx := context.Background()
	org := seedOrg(t, d)
	host := seedHost(t, d, org)
	alert := seedOpenAlert(t, d, org, host)
	route := seedRouteWithSteps(t, d, org, []alertroutes.Step{{Position: 1, Delay: time.Minute}})
	at := time.Now().UTC().Truncate(time.Second)
	s := alertroutes.NewSQLStore(d)
	tx := beginTx(t, d)

	first, err := s.ClaimStep(ctx, tx, org, alert, route, 1, at)
	if err != nil || !first {
		t.Fatalf("first claim: ok=%v err=%v, want ok=true err=nil", first, err)
	}
	// Same transaction, same (alert, route, position): exactly the shape
	// two managers ticking at once or one tick overlapping the next
	// produces — ON CONFLICT DO NOTHING is what must turn this into a
	// quiet no-op rather than a constraint-violation error that would
	// abort the whole pass.
	second, err := s.ClaimStep(ctx, tx, org, alert, route, 1, at.Add(time.Minute))
	if err != nil {
		t.Fatalf("second claim errored instead of reporting false: %v", err)
	}
	if second {
		t.Fatal("second claim reported a fresh row; the step would fire twice")
	}
}

func TestReplaceStepsIsDeleteThenInsert(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d)
	route := seedRouteWithSteps(t, d, org, []alertroutes.Step{
		{Position: 1, Delay: 5 * time.Minute},
		{Position: 2, Delay: 10 * time.Minute},
	})
	// Keep position 1, drop 2, add 3 — the shape that lost a row in
	// v0.4b when it was one data-modifying CTE.
	mustReplaceSteps(t, d, org, route, []alertroutes.Step{
		{Position: 1, Delay: 5 * time.Minute},
		{Position: 3, Delay: 20 * time.Minute},
	})
	got := mustReadSteps(t, d, org, route)
	if len(got) != 2 || got[0].Position != 1 || got[1].Position != 3 {
		t.Fatalf("steps = %+v, want positions 1 and 3", got)
	}
}

func TestChainsNeedingAttentionSkipsClosedAlerts(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d)
	host := seedHost(t, d, org)
	alert := seedOpenAlert(t, d, org, host)
	route := seedRouteWithSteps(t, d, org, []alertroutes.Step{{Position: 1, Delay: time.Minute}})
	mustClaim(t, d, org, alert, route, 0, time.Now().UTC())
	mustCloseAlert(t, d, alert)

	if chains := mustChains(t, d, org); len(chains) != 0 {
		t.Fatalf("a closed alert still offered %d chain(s)", len(chains))
	}
}

// TestChainsNeedingAttentionSkipsFullyFiredLadders proves the other half
// of the structural filter: a route whose every step already has a ledger
// row must not reappear — ChainsNeedingAttentionBase's own
// "max_position > fired_up_to" predicate is what this exercises, not
// merely that a partially-fired ladder is included (the two tests above
// already show that).
func TestChainsNeedingAttentionSkipsFullyFiredLadders(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d)
	host := seedHost(t, d, org)
	alert := seedOpenAlert(t, d, org, host)
	route := seedRouteWithSteps(t, d, org, []alertroutes.Step{{Position: 1, Delay: time.Minute}})
	now := time.Now().UTC()
	mustClaim(t, d, org, alert, route, 0, now)
	mustClaim(t, d, org, alert, route, 1, now.Add(time.Minute))

	if chains := mustChains(t, d, org); len(chains) != 0 {
		t.Fatalf("a fully fired ladder still offered %d chain(s): %+v", len(chains), chains)
	}
}

// TestChainsNeedingAttentionCarriesStepsAndAnchors proves
// ChainsNeedingAttention's own second round trip (ListEscalationStepsForRoutes)
// actually attaches the route's steps to the right Chain, and that
// LastUrgentAt reads as the alert's own severity_raised event rather than
// the zero time.Time an untouched alert would report
// (TestChainsNeedingAttentionSkipsClosedAlerts never reaches this, since
// its own route never appears in the result at all).
func TestChainsNeedingAttentionCarriesStepsAndAnchors(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d)
	host := seedHost(t, d, org)
	base := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
	// The alert and the route both predate the base: in reality neither can
	// be created later than the chain's first real delivery, and the
	// route's own updated_at IS one of LastUrgentAt's anchors
	// (escalate_sql.go, migration 00058's review, Critical 3) — created at
	// the real "now" (an hour after base) as seedRouteWithSteps otherwise
	// would, it would swamp the severity_raised event this test means to
	// pin. The alert is backdated the same way for consistency; its own
	// severity_changed_at is NOT one of LastUrgentAt's anchors
	// (escalate_sql.go's own doc comment explains why a fold there could
	// stall a chain that oscillates inside a route's severity set), so
	// backdating it specifically is not load-bearing here.
	alert := seedOpenAlertAt(t, d, org, host, base.Add(-time.Minute))
	ch := seedChannel(t, d, org)
	route := seedRouteWithStepsAt(t, d, org, []alertroutes.Step{
		{Position: 1, Delay: 5 * time.Minute, ChannelIDs: []uuid.UUID{ch}},
		{Position: 2, Delay: 10 * time.Minute},
	}, base.Add(-time.Minute))
	mustClaim(t, d, org, alert, route, 0, base)
	raisedAt := base.Add(2 * time.Minute)
	seedAlertEventAt(t, d, org, alert, "severity_raised", raisedAt)

	chains := mustChains(t, d, org)
	if len(chains) != 1 {
		t.Fatalf("chains = %+v, want exactly one", chains)
	}
	c := chains[0]
	if c.AlertID != alert || c.RouteID != route {
		t.Fatalf("chain identity = %+v, want alert %v route %v", c, alert, route)
	}
	if len(c.State.Steps) != 2 || c.State.Steps[0].Position != 1 || c.State.Steps[1].Position != 2 {
		t.Fatalf("Steps = %+v, want positions 1 and 2 in order", c.State.Steps)
	}
	if len(c.State.Steps[0].ChannelIDs) != 1 || c.State.Steps[0].ChannelIDs[0] != ch {
		t.Fatalf("step 1 channels = %v, want [%v]", c.State.Steps[0].ChannelIDs, ch)
	}
	if !c.State.LastUrgentAt.Equal(raisedAt) {
		t.Fatalf("LastUrgentAt = %v, want the severity_raised event at %v", c.State.LastUrgentAt, raisedAt)
	}
	if !c.State.LastFiredAt.Equal(base) {
		t.Fatalf("LastFiredAt = %v, want the base claim at %v", c.State.LastFiredAt, base)
	}
}

// TestChainsNeedingAttentionAnchorsOnUnsnoozeNotJustBase is the fix for the
// gap review caught: a human clearing a snooze (Unsnooze) sets
// snoozed_until back to NULL and records only the 'unsnoozed' event (spec
// decision 5's own wording for how a mute that a person clears differs
// from one that simply expires) — neither of ChainsNeedingAttentionBase's
// other two LastUrgentAt sources (severity_raised/ack_voided events,
// snoozed_until itself) sees that on its own. Without 'unsnoozed' in that
// query's own event kind filter, LastUrgentAt fell back to the zero
// time.Time here — no severity_raised/ack_voided event fired, and
// snoozed_until is NULL again — so DueStep anchored on the stale base
// fired_at alone and would fire the rung as soon as ITS delay had passed,
// exactly the burst decision 5 exists to prevent, the moment a person
// clears a snooze rather than letting one expire on its own.
func TestChainsNeedingAttentionAnchorsOnUnsnoozeNotJustBase(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d)
	host := seedHost(t, d, org)
	base := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
	// Backdated to predate base, the same reason
	// TestChainsNeedingAttentionCarriesStepsAndAnchors' own setup gives:
	// the route's own updated_at IS one of LastUrgentAt's anchors
	// (escalate_sql.go, migration 00058's review, Critical 3) and must not
	// outrank the unsnoozed event this test means to pin. The alert's own
	// severity_changed_at is backdated too, for consistency, but is not
	// itself an anchor (escalate_sql.go's own doc comment explains why).
	alert := seedOpenAlertAt(t, d, org, host, base.Add(-time.Minute))
	route := seedRouteWithStepsAt(t, d, org, []alertroutes.Step{{Position: 1, Delay: 5 * time.Minute}}, base.Add(-time.Minute))

	mustClaim(t, d, org, alert, route, 0, base)

	// Snoozed shortly after the base, then cleared by a PERSON two minutes
	// in — never allowed to run out on its own, so snoozed_until carries
	// nothing useful by the time ChainsNeedingAttention reads it.
	unsnoozedAt := base.Add(2 * time.Minute)
	mustSnoozeThenClear(t, d, alert, base.Add(50*time.Minute), unsnoozedAt)

	chains := mustChains(t, d, org)
	if len(chains) != 1 {
		t.Fatalf("chains = %+v, want exactly one", chains)
	}
	c := chains[0]
	if !c.State.LastUrgentAt.Equal(unsnoozedAt) {
		t.Fatalf("LastUrgentAt = %v, want the unsnoozed event at %v", c.State.LastUrgentAt, unsnoozedAt)
	}

	// base+5m (the rung's delay measured from the BASE alone) has long
	// passed by this instant, but unsnoozedAt+5m (the rung's delay
	// measured from the moment a person actually cleared the snooze) has
	// not — the rung must not be due yet.
	dueByBaseOnly := base.Add(6 * time.Minute)
	if _, ok := alertroutes.DueStep(c.State, dueByBaseOnly); ok {
		t.Fatalf("DueStep(%v) = due, want not due — anchoring on the stale base fires exactly the burst decision 5 forbids", dueByBaseOnly)
	}

	// Once the unsnooze anchor's own delay has actually elapsed, the rung
	// is due — proving the fix anchors correctly rather than simply never
	// firing.
	dueByUnsnoozeAnchor := unsnoozedAt.Add(5 * time.Minute).Add(time.Second)
	if _, ok := alertroutes.DueStep(c.State, dueByUnsnoozeAnchor); !ok {
		t.Fatalf("DueStep(%v) = not due, want due once the unsnooze anchor's own delay elapsed", dueByUnsnoozeAnchor)
	}
}
