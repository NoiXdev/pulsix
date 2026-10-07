// These tests are decision 5's own acceptance criteria, driven by their
// REAL causes rather than by a hand-made end state (v0.4a final review,
// Critical). The design names four ways a tuple leaves without ever
// reaching ok — the rule is deleted, the check is taken off the host, the
// item is dropped by an operator's decision, the host is decommissioned —
// and the review found that the one most routine of them never closed its
// alert at all, because the test that covered it asserted the end state
// ("no check_states row") instead of performing the cause ("delete the
// check"), and the assumption joining the two was false.
//
// So each test below performs the cause through the production surface —
// rules.Service.Delete, checks.Service.DeleteDefinition,
// items.Service.CreateDecision, DELETE FROM hosts — and then runs a real
// rules.Ticker whose AlertSink is a real alerts.Service, wired exactly as
// internal/app.Build wires them (the port adapters are app's own, not
// second copies). Nothing between the operator's action and the alerts
// table is faked.
package alerts_test

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/alerts"
	"github.com/noixdev/pulsix/internal/app"
	"github.com/noixdev/pulsix/internal/checks"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/items"
	"github.com/noixdev/pulsix/internal/rules"
)

// lifecycleRig is one organization, one monitored host and the whole
// evaluation stack above them, built from the same constructors
// internal/app.Build uses.
type lifecycleRig struct {
	d            *db.DB
	org, host    uuid.UUID
	checks       *checks.Service
	rules        *rules.Service
	items        *items.Service
	ticker       *rules.Ticker
	now          time.Time
	diskDefID    uuid.UUID
	diskRuleID   uuid.UUID
	scopedCtx    context.Context //nolint:containedctx // the fixture's org-bound context, reused by every helper below
	tupleSubject alerts.Subject
}

// newLifecycleRig builds the rig and leaves it one tick away from an open
// alert: a monitored host, an organization-bound "disk" check at a
// one-minute interval, one crit rule on disk.used_pct, and a single
// check_current row sitting well above the threshold.
//
// The clock is a mutable field every component reads through a closure,
// because the sequence these tests have to reproduce is a real one with
// time in it: an operator removes a check, the agent stops reporting it,
// and the host comes back round on its own next_due_at rather than
// because anything marked it dirty.
func newLifecycleRig(t *testing.T) *lifecycleRig {
	t.Helper()
	d := testdb.Open(t)
	org, host := seedHost(t, d)
	// The resolver only resolves checks for a MONITORED host
	// (checks.Service.ResolveHost's own gate), and seedHost creates a bare
	// one.
	if _, err := d.Pool().Exec(context.Background(),
		`UPDATE hosts SET monitored = true WHERE id = $1`, host); err != nil {
		t.Fatalf("mark host monitored: %v", err)
	}

	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	hostsSvc := hosts.NewService(hosts.NewSQLStore(d), time.Now)
	checksSvc := checks.NewService(checks.NewSQLStore(d),
		checks.WithMatcher(app.NewChecksHostMatcher(hostsSvc)),
		checks.WithHostRoster(app.NewChecksHostRoster(hostsSvc)))
	rulesSvc := rules.NewService(rules.NewSQLStore(d))
	itemsStore := items.NewSQLStore(d)
	itemsSvc := items.NewService(itemsStore, app.ItemCheckKeys)

	r := &lifecycleRig{
		d: d, org: org, host: host,
		checks: checksSvc, rules: rulesSvc, items: itemsSvc,
		now: time.Now().UTC().Truncate(time.Microsecond),
		tupleSubject: alerts.Subject{
			HostID: host, Scope: alerts.ScopeTuple,
			CheckKey: checks.PluginDisk, Item: "/", Metric: "disk.used_pct",
		},
	}
	clock := func() time.Time { return r.now }
	r.ticker = rules.NewTicker(
		rules.NewEvalSQLStore(d, app.NewRulesHostChecks(checksSvc), itemsStore, quiet),
		alerts.NewService(alerts.NewSQLStore(d), clock),
		rules.WithTickerClock(clock),
		rules.WithTickerLogger(quiet),
	)

	scoped := db.WithOrgScope(context.Background(), org)
	r.scopedCtx = scoped
	def, err := checksSvc.CreateDefinition(scoped, org, checks.DefinitionInput{
		Key: checks.PluginDisk, Plugin: checks.PluginDisk,
		Interval: time.Minute, Enabled: true, BindingKind: checks.BindingOrg,
	})
	if err != nil {
		t.Fatalf("create disk definition: %v", err)
	}
	rule, err := rulesSvc.Create(scoped, org, rules.RuleInput{
		Name: "disk full", Enabled: true, Scope: rules.ScopeOrg,
		Metric: "disk.used_pct", Severity: rules.SeverityCrit, Mode: "form",
		Form: &rules.Form{Metric: "disk.used_pct", Op: ">", Value: 90},
	})
	if err != nil {
		t.Fatalf("create rule: %v", err)
	}

	r.diskDefID, r.diskRuleID = def.ID, rule.ID
	r.report(t, 96)
	return r
}

// advance moves the rig's clock forward — the evaluation's own view of
// time, not the database's.
func (r *lifecycleRig) advance(d time.Duration) { r.now = r.now.Add(d) }

// makeDue brings the host back round for the next tick. In production
// either its own next_due_at (a 15-second heartbeat, or a tuple's
// staleness deadline) or any further ingest does this; a test cannot use
// the first, because ClaimDueEvalCursors compares next_due_at against the
// DATABASE's clock (rules.sql's own comment: "next_due_at is the one
// value that crosses clocks"), which no injected clock can move. So it
// uses the second: MarkHostDirty, the single statement internal/checks'
// ingest transaction runs, stamped with the database's own now().
func (r *lifecycleRig) makeDue(t *testing.T) {
	t.Helper()
	if err := gen.New(r.d.Pool()).MarkHostDirty(context.Background(),
		gen.MarkHostDirtyParams{OrgID: r.org, HostID: r.host}); err != nil {
		t.Fatalf("mark host dirty: %v", err)
	}
}

// report writes the one check_current row the evaluation reads and marks
// the host dirty — exactly what internal/checks' ingest transaction does.
func (r *lifecycleRig) report(t *testing.T, value float64) {
	t.Helper()
	q := gen.New(r.d.Pool())
	if err := q.UpsertCheckCurrent(context.Background(), gen.UpsertCheckCurrentParams{
		OrgID: r.org, HostID: r.host, CheckKey: checks.PluginDisk, Item: "/", Metric: "disk.used_pct",
		Value: &value, At: r.now,
	}); err != nil {
		t.Fatalf("upsert check_current: %v", err)
	}
	if err := q.MarkHostDirty(context.Background(), gen.MarkHostDirtyParams{OrgID: r.org, HostID: r.host}); err != nil {
		t.Fatalf("mark host dirty: %v", err)
	}
}

// tick runs one whole evaluation tick — ProcessDue plus the sweep, the
// same call internal/app's own loop makes.
func (r *lifecycleRig) tick(t *testing.T) {
	t.Helper()
	if _, err := r.ticker.Tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}
}

// openTupleAlert is the alert currently open on the rig's disk tuple, or a
// failure when there is none.
func (r *lifecycleRig) openTupleAlert(t *testing.T) alerts.Alert {
	t.Helper()
	tx := beginTx(t, r.d)
	defer func() { _ = tx.Rollback(context.Background()) }()
	open, err := alerts.NewSQLStore(r.d).OpenForHost(context.Background(), tx, r.org, r.host)
	if err != nil {
		t.Fatalf("open for host: %v", err)
	}
	for _, a := range open {
		if a.Subject == r.tupleSubject {
			return a
		}
	}
	t.Fatalf("no open alert on %+v; open alerts: %+v", r.tupleSubject, open)
	return alerts.Alert{}
}

// closure reads one alert's closed_at/closed_reason straight from the
// table, so a still-open alert is distinguishable from a closed one by
// something other than its absence from OpenForHost.
func (r *lifecycleRig) closure(t *testing.T, id uuid.UUID) (closed bool, reason string) {
	t.Helper()
	var closedAt *time.Time
	var closedReason *string
	err := r.d.Pool().QueryRow(context.Background(),
		`SELECT closed_at, closed_reason FROM alerts WHERE id = $1`, id).Scan(&closedAt, &closedReason)
	if err != nil {
		t.Fatalf("read alert %s: %v", id, err)
	}
	if closedAt == nil {
		return false, ""
	}
	if closedReason == nil {
		return true, ""
	}
	return true, *closedReason
}

// openEpisode drives the rig to one open crit alert and returns it.
func (r *lifecycleRig) openEpisode(t *testing.T) alerts.Alert {
	t.Helper()
	r.tick(t)
	a := r.openTupleAlert(t)
	if a.Severity != rules.StateCrit {
		t.Fatalf("opened alert severity = %q, want crit", a.Severity)
	}
	return a
}

// TestDeletedRuleClosesItsAlertResolved is decision 5's first path, and
// the one whose WORDING the review corrected rather than its code: a tuple
// whose rule is gone is judged against nothing, which is ok, and `resolved`
// is the honest close for a tuple that is genuinely healthy. The test
// deletes the rule through rules.Service, the way the API does.
func TestDeletedRuleClosesItsAlertResolved(t *testing.T) {
	r := newLifecycleRig(t)
	a := r.openEpisode(t)

	if err := r.rules.Delete(r.scopedCtx, r.org, r.diskRuleID); err != nil {
		t.Fatalf("delete rule: %v", err)
	}
	r.report(t, 96)
	r.tick(t)

	closed, reason := r.closure(t, a.ID)
	if !closed || reason != string(alerts.Resolved) {
		t.Fatalf("after deleting the rule: closed=%v reason=%q, want closed with reason resolved", closed, reason)
	}
}

// TestRemovedCheckClosesItsAlertStale is the path the review found broken:
// the check is taken off the host, the evaluator stops judging the tuple
// and deliberately LEAVES its check_states row standing, so the central
// sweep's "no state row exists" test can never see it. It closes `stale`
// now because rules.Result.Unmeasured reports the tuple and
// alerts.Reconcile's staleActions closes it inside the host's own
// transaction. The cause is performed through checks.Service, the way the
// API's own DELETE /checks/{id} does.
func TestRemovedCheckClosesItsAlertStale(t *testing.T) {
	r := newLifecycleRig(t)
	a := r.openEpisode(t)

	if err := r.checks.DeleteDefinition(r.scopedCtx, r.org, r.diskDefID); err != nil {
		t.Fatalf("delete check definition: %v", err)
	}
	// The agent stops reporting a check it no longer has; what brings the
	// host round again is its own cursor (see makeDue).
	r.advance(4 * time.Minute)
	r.makeDue(t)
	// The check_states row must still be there — that is the whole reason
	// the sweep cannot catch this path, and asserting it here is what keeps
	// this test honest if someone later makes the evaluator delete it.
	if !r.checkStateExists(t) {
		t.Fatal("the check_states row was deleted; this test no longer drives the case it exists for")
	}
	r.tick(t)

	closed, reason := r.closure(t, a.ID)
	if !closed || reason != string(alerts.Stale) {
		t.Fatalf("after removing the check: closed=%v reason=%q, want closed with reason stale", closed, reason)
	}
}

// TestDisabledCheckClosesItsAlertStale is the same path by its other
// route: the definition is not deleted, only disabled, which is what an
// operator does far more often. The resolver drops a disabled definition
// from the host's set, so the interval is gone exactly as if the row had
// been deleted.
func TestDisabledCheckClosesItsAlertStale(t *testing.T) {
	r := newLifecycleRig(t)
	a := r.openEpisode(t)

	if _, err := r.checks.UpdateDefinition(r.scopedCtx, r.org, r.diskDefID, checks.DefinitionInput{
		Key: checks.PluginDisk, Plugin: checks.PluginDisk,
		Interval: time.Minute, Enabled: false, BindingKind: checks.BindingOrg,
	}); err != nil {
		t.Fatalf("disable check definition: %v", err)
	}
	r.advance(4 * time.Minute)
	r.makeDue(t)
	r.tick(t)

	closed, reason := r.closure(t, a.ID)
	if !closed || reason != string(alerts.Stale) {
		t.Fatalf("after disabling the check: closed=%v reason=%q, want closed with reason stale", closed, reason)
	}
}

// TestDroppedItemClosesItsAlertStale is decision 5's third path and the
// sweep's own remaining customer: an operator ignores the item, the
// evaluator DELETES its check_states row (rules.Result.Dropped), and the
// sweep closes the alert `stale` at the end of the very same tick.
func TestDroppedItemClosesItsAlertStale(t *testing.T) {
	r := newLifecycleRig(t)
	a := r.openEpisode(t)

	if _, err := r.items.CreateDecision(r.scopedCtx, r.org, uuid.Must(uuid.NewV7()), items.DecisionInput{
		Scope: items.ScopeOrg, CheckKey: checks.PluginDisk,
		MatchKind: items.MatchExact, Pattern: "/", Verdict: items.Ignore,
	}); err != nil {
		t.Fatalf("create ignore decision: %v", err)
	}
	r.report(t, 96)
	r.tick(t)

	if r.checkStateExists(t) {
		t.Fatal("the check_states row survived the drop; this test no longer drives the sweep's own case")
	}
	closed, reason := r.closure(t, a.ID)
	if !closed || reason != string(alerts.Stale) {
		t.Fatalf("after dropping the item: closed=%v reason=%q, want closed with reason stale", closed, reason)
	}
}

// TestDecommissionedHostTakesItsAlertsWithIt is decision 5's fourth path:
// nothing closes the alert because nothing is left to close — the
// composite FK's ON DELETE CASCADE removes the row and its events
// together. Asserted rather than assumed, since the design leans on it to
// justify excluding host-scope alerts from the sweep.
func TestDecommissionedHostTakesItsAlertsWithIt(t *testing.T) {
	r := newLifecycleRig(t)
	a := r.openEpisode(t)

	if _, err := r.d.Pool().Exec(context.Background(), `DELETE FROM hosts WHERE id = $1`, r.host); err != nil {
		t.Fatalf("delete host: %v", err)
	}

	var alertRows, eventRows int
	if err := r.d.Pool().QueryRow(context.Background(),
		`SELECT (SELECT count(*) FROM alerts WHERE id = $1), (SELECT count(*) FROM alert_events WHERE alert_id = $1)`,
		a.ID).Scan(&alertRows, &eventRows); err != nil {
		t.Fatalf("count rows after host delete: %v", err)
	}
	if alertRows != 0 || eventRows != 0 {
		t.Fatalf("after decommissioning the host: %d alert rows, %d event rows, want 0 and 0", alertRows, eventRows)
	}
}

// TestHostStaysDueAfterItsLastCheckIsRemoved settles the premise the
// design asserted and nobody had verified: "a host whose last check was
// taken away never becomes due again". It is false. rules.nextDue gives a
// host with nothing pending a 15-second heartbeat, and ClaimDueEvalCursors
// takes any cursor whose next_due_at has passed, so such a host keeps
// being evaluated — which is exactly what makes closing an unmeasured
// tuple's alert inside the host's own transaction a complete mechanism
// rather than one that misses the case it exists for.
func TestHostStaysDueAfterItsLastCheckIsRemoved(t *testing.T) {
	r := newLifecycleRig(t)
	r.openEpisode(t)

	if err := r.checks.DeleteDefinition(r.scopedCtx, r.org, r.diskDefID); err != nil {
		t.Fatalf("delete check definition: %v", err)
	}
	r.advance(4 * time.Minute)
	r.makeDue(t)
	r.tick(t)

	var nextDue, evaluatedAt time.Time
	if err := r.d.Pool().QueryRow(context.Background(),
		`SELECT next_due_at, evaluated_at FROM check_eval_cursor WHERE host_id = $1`, r.host).
		Scan(&nextDue, &evaluatedAt); err != nil {
		t.Fatalf("read cursor: %v", err)
	}
	if !nextDue.After(evaluatedAt) {
		t.Fatalf("next_due_at = %v is not after evaluated_at = %v: the host would never come back", nextDue, evaluatedAt)
	}
	if d := nextDue.Sub(r.now); d > time.Minute {
		t.Fatalf("next_due_at is %v away, want the heartbeat (seconds, not for ever)", d)
	}
}

// checkStateExists reports whether the rig's tuple still has a
// check_states row — the fact the sweep's own predicate turns on.
func (r *lifecycleRig) checkStateExists(t *testing.T) bool {
	t.Helper()
	var n int
	err := r.d.Pool().QueryRow(context.Background(),
		`SELECT count(*) FROM check_states WHERE org_id = $1 AND host_id = $2 AND check_key = $3 AND item = $4 AND metric = $5`,
		r.org, r.host, r.tupleSubject.CheckKey, r.tupleSubject.Item, r.tupleSubject.Metric).Scan(&n)
	if err != nil {
		t.Fatalf("count check_states: %v", err)
	}
	return n > 0
}
