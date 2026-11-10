package rules_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/items"
	"github.com/noixdev/pulsix/internal/rules"
)

// fakeItemStore is the fake rules.ItemStore the Seen-derivation unit test
// uses: it records exactly what Discover and Decisions were called with,
// isolating that derivation from items.SQLStore's own persistence (which
// store_sql_test.go already covers) and from the database altogether for
// the Decisions half.
type fakeItemStore struct {
	discovered   []items.Seen
	decisions    []items.Decision
	discoverErr  error
	decisionsErr error

	// decisionsCalls counts how many times Decisions was invoked, not what
	// it was invoked with — Load must read it exactly once per host and
	// resolve every item's decision from that one result in memory
	// (ItemStore.Decisions' own doc comment). A regression that moved the
	// call into the per-item loop instead would still pass every assertion
	// on fake.decisions' content, since this fake answers the same rows
	// however many times it is asked; only a count catches the extra
	// queries.
	decisionsCalls int
}

func (f *fakeItemStore) Discover(_ context.Context, _, _ uuid.UUID, seen []items.Seen) error {
	f.discovered = append(f.discovered, seen...)
	return f.discoverErr
}

func (f *fakeItemStore) Decisions(_ context.Context, _ uuid.UUID, _ *uuid.UUID) ([]items.Decision, error) {
	f.decisionsCalls++
	return f.decisions, f.decisionsErr
}

// These tests exercise EvalSQLStore against the real database, because
// every claim it makes is about SQL: that the claim is exclusive between
// two managers, that a window over a hypertable returns the four
// aggregates (and nothing at all when it holds no row), and that one
// host's write-back is atomic. None of that can be observed against a
// fake.

// discard is the logger every store in this file is built with: these
// tests deliberately provoke failures the store logs, and a test's output
// should stay pristine.
func discard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// fixedChecks is the check pipeline's side of the port, pinned to a fixed
// answer: internal/rules never imports internal/checks (internal/app
// adapts the two), so the store's tests supply the intervals and groups
// directly.
type fixedChecks struct {
	intervals map[string]time.Duration
	groups    []rules.GroupRank
	err       error
}

func (f fixedChecks) ContextForHost(context.Context, uuid.UUID, uuid.UUID) (rules.CheckContext, error) {
	if f.err != nil {
		return rules.CheckContext{}, f.err
	}
	return rules.CheckContext{Intervals: f.intervals, Groups: f.groups}, nil
}

// cpuMinute is the ordinary case: one check, "cpu", resolved at a
// one-minute interval.
func cpuMinute() fixedChecks {
	return fixedChecks{intervals: map[string]time.Duration{"cpu": time.Minute}}
}

// insertCurrent writes one check_current row — the ingest path's own
// output, which is what Load reads a host's tuples from.
func insertCurrent(t *testing.T, d *db.DB, org, host uuid.UUID, tuple rules.Tuple, value *float64, label *string, at time.Time, errText string) {
	t.Helper()
	var errPtr *string
	if errText != "" {
		errPtr = &errText
	}
	if err := gen.New(d.Pool()).UpsertCheckCurrent(context.Background(), gen.UpsertCheckCurrentParams{
		OrgID: org, HostID: host, CheckKey: tuple.CheckKey, Item: tuple.Item, Metric: tuple.Metric,
		Value: value, Label: label, At: at, Error: errPtr,
	}); err != nil {
		t.Fatal(err)
	}
}

// insertSample writes one check_samples row, the hypertable the window
// statement aggregates over.
func insertSample(t *testing.T, d *db.DB, org, host uuid.UUID, tuple rules.Tuple, at time.Time, value *float64, label *string) {
	t.Helper()
	if err := gen.New(d.Pool()).InsertCheckSample(context.Background(), gen.InsertCheckSampleParams{
		At: at, OrgID: org, HostID: host, CheckKey: tuple.CheckKey, Item: tuple.Item, Metric: tuple.Metric,
		Value: value, Label: label,
	}); err != nil {
		t.Fatal(err)
	}
}

// markDirty is the ingest path's one statement, called directly: this is
// exactly what internal/checks' RecordObservations runs inside its own
// transaction (see that package's own store test for the wiring).
func markDirty(t *testing.T, d *db.DB, org, host uuid.UUID) {
	t.Helper()
	if err := gen.New(d.Pool()).MarkHostDirty(context.Background(), gen.MarkHostDirtyParams{OrgID: org, HostID: host}); err != nil {
		t.Fatal(err)
	}
}

// cursorRow reads one host's cursor straight from the table.
func cursorRow(t *testing.T, d *db.DB, host uuid.UUID) (dirtyAt, evaluatedAt, nextDueAt time.Time) {
	t.Helper()
	err := d.Pool().QueryRow(context.Background(),
		`SELECT dirty_at, evaluated_at, next_due_at FROM check_eval_cursor WHERE host_id = $1`, host).
		Scan(&dirtyAt, &evaluatedAt, &nextDueAt)
	if err != nil {
		t.Fatal(err)
	}
	return dirtyAt, evaluatedAt, nextDueAt
}

// setCursor forces a cursor into a known position — a fixture for "this
// host is overdue" or "this host is neither dirty nor due" that does not
// depend on having ingested anything. The three moments are given as
// offsets from NOW, and the offsets are applied by the database, not here.
//
// That is not a stylistic preference, it is the only correct way to write
// this fixture. dirty_at and evaluated_at are compared against each other
// by the claim, and in production both are written by the database's own
// now() (MarkHostDirty and AdvanceEvalCursor; backlog 2.80). A fixture
// that bound a Go time into either column would be comparing two machines'
// clocks — and the two are not in step: measured against this test's own
// container, the database read up to 0.7 ms BEHIND the Go process. Any
// offset smaller than that skew inverts. That is exactly how
// TestMarkHostDirtyMakesAHostDue came to fail under the full package load
// and pass in isolation: it set evaluated_at from Go's clock and then
// marked the host dirty from the database's, and when the gap between the
// two statements was under the skew the "fresh mark" landed BEFORE the
// evaluation it was supposed to invalidate.
//
// Give a negative offset for the past and a positive one for the future;
// zero means "right now" as the database reckons it.
func setCursor(t *testing.T, d *db.DB, org, host uuid.UUID, dirtyAt, evaluatedAt, nextDueAt time.Duration) {
	t.Helper()
	_, err := d.Pool().Exec(context.Background(), `
		INSERT INTO check_eval_cursor (org_id, host_id, dirty_at, evaluated_at, next_due_at)
		VALUES ($1, $2, now() + $3::interval, now() + $4::interval, now() + $5::interval)
		ON CONFLICT (host_id) DO UPDATE SET dirty_at = excluded.dirty_at,
			evaluated_at = excluded.evaluated_at, next_due_at = excluded.next_due_at`,
		org, host, dirtyAt, evaluatedAt, nextDueAt)
	if err != nil {
		t.Fatal(err)
	}
}

// stillDue asks the database the claim's own question about one host:
// is it dirty, or is it due? Tests that assert "this host must stay due"
// ask this rather than comparing a timestamp they read back against their
// own clock — the predicate is the property, and evaluating it in the
// database keeps every moment in it on one clock (see setCursor).
func stillDue(t *testing.T, d *db.DB, host uuid.UUID) bool {
	t.Helper()
	var due bool
	err := d.Pool().QueryRow(context.Background(),
		`SELECT dirty_at > evaluated_at OR next_due_at <= now() FROM check_eval_cursor WHERE host_id = $1`, host).
		Scan(&due)
	if err != nil {
		t.Fatal(err)
	}
	return due
}

// countRows is the "exactly one set of changes" assertion's workhorse.
func countRows(t *testing.T, d *db.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := d.Pool().QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// claimAll runs one ProcessDue pass collecting the hosts it claimed,
// without evaluating anything. The time it passes is the ticker's own
// clock reading, which the claim deliberately does not use — every
// comparison in that statement is against the database's now() — so any
// value does here.
func claimAll(t *testing.T, store *rules.EvalSQLStore, limit int) []rules.Due {
	t.Helper()
	var got []rules.Due
	n, err := store.ProcessDue(context.Background(), time.Now(), limit, func(_ context.Context, d rules.Due) error {
		got = append(got, d)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n != len(got) {
		t.Fatalf("ProcessDue reported %d evaluated but ran fn %d times", n, len(got))
	}
	return got
}

// TestNewEvalSQLStoreRefusesANilItemStore pins the constructor's own
// refusal. A nil ItemStore is not a store with a feature missing: Load
// calls it for every host of every tick, so it is a nil-pointer
// dereference on the first one — and the ticker's per-host recover would
// turn that into one logged failure per host per tick, for ever, with the
// wiring mistake nowhere in the message. It must fail where the mistake
// was made instead. No database is needed to prove it, so this test does
// not open one.
func TestNewEvalSQLStoreRefusesANilItemStore(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("NewEvalSQLStore(nil items) returned a store, want a panic at construction")
		}
	}()
	rules.NewEvalSQLStore(nil, cpuMinute(), nil, discard())
}

// TestWindowStatementOverASeededHypertable is review focus 2: every
// aggregate over a real hypertable, plus the two windows whose answer is
// the interesting one — a window holding no row at all (no entry, which
// is what makes the rule a non-match rather than a threshold breach) and a
// window holding exactly one row, where rate is zero rather than a
// division by zero or a missing value.
func TestWindowStatementOverASeededHypertable(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "windows")
	host := seedHost(t, d, org, "win-host")
	store := rules.NewEvalSQLStore(d, cpuMinute(), items.NewSQLStore(d), discard())

	now := time.Now().UTC().Truncate(time.Microsecond)
	cpu := rules.Tuple{CheckKey: "cpu", Metric: "cpu.used_pct"}
	insertSample(t, d, org, host, cpu, now.Add(-9*time.Minute), ptr(10.0), nil)
	insertSample(t, d, org, host, cpu, now.Add(-5*time.Minute), ptr(20.0), nil)
	insertSample(t, d, org, host, cpu, now.Add(-1*time.Minute), ptr(60.0), nil)
	// A discrete metric of the same check: its samples carry a label and no
	// value, and must never contribute to a numeric aggregate.
	svc := rules.Tuple{CheckKey: "service", Item: "nginx", Metric: "service.state"}
	insertSample(t, d, org, host, svc, now.Add(-2*time.Minute), nil, ptr("active"))
	// Another host's samples, to prove the statement is scoped.
	other := seedHost(t, d, org, "other-host")
	insertSample(t, d, org, other, cpu, now.Add(-1*time.Minute), ptr(999.0), nil)

	cpuKey := rules.ItemKey{CheckKey: "cpu"}
	var (
		avg10    = rules.WindowRequest{Key: cpuKey, Metric: "cpu.used_pct", Agg: "avg", Seconds: 600}
		min10    = rules.WindowRequest{Key: cpuKey, Metric: "cpu.used_pct", Agg: "min", Seconds: 600}
		max10    = rules.WindowRequest{Key: cpuKey, Metric: "cpu.used_pct", Agg: "max", Seconds: 600}
		rate10   = rules.WindowRequest{Key: cpuKey, Metric: "cpu.used_pct", Agg: "rate", Seconds: 600}
		avg2     = rules.WindowRequest{Key: cpuKey, Metric: "cpu.used_pct", Agg: "avg", Seconds: 120}
		rate2    = rules.WindowRequest{Key: cpuKey, Metric: "cpu.used_pct", Agg: "rate", Seconds: 120}
		empty    = rules.WindowRequest{Key: cpuKey, Metric: "cpu.used_pct", Agg: "avg", Seconds: 30}
		noMetric = rules.WindowRequest{Key: cpuKey, Metric: "cpu.load1", Agg: "avg", Seconds: 600}
		discrete = rules.WindowRequest{
			Key: rules.ItemKey{CheckKey: "service", Item: "nginx"}, Metric: "service.state", Agg: "avg", Seconds: 600,
		}
	)
	reqs := []rules.WindowRequest{avg10, min10, max10, rate10, avg2, rate2, empty, noMetric, discrete}

	got, err := store.Windows(context.Background(), rules.Due{OrgID: org, HostID: host}, reqs, now)
	if err != nil {
		t.Fatalf("Windows: %v", err)
	}

	want := map[rules.WindowRequest]float64{
		avg10:  30,
		min10:  10,
		max10:  60,
		rate10: 50.0 / 600.0, // (last - first) / seconds
		avg2:   60,           // exactly one row in the window
		rate2:  0,            // one row: last == first, and never a division by zero
	}
	for req, wantValue := range want {
		value, ok := got[req]
		if !ok {
			t.Fatalf("%s(%s, %ds) is missing from the result", req.Agg, req.Metric, req.Seconds)
		}
		if math.Abs(value-wantValue) > 1e-9 {
			t.Fatalf("%s(%s, %ds) = %v, want %v", req.Agg, req.Metric, req.Seconds, value, wantValue)
		}
	}
	for _, req := range []rules.WindowRequest{empty, noMetric, discrete} {
		if value, ok := got[req]; ok {
			t.Fatalf("%s(%s, %ds) returned %v; a window holding no numeric sample must return no entry at all",
				req.Agg, req.Metric, req.Seconds, value)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("Windows returned %d entries, want %d", len(got), len(want))
	}
}

// TestWindowsWithNoRequestsCostsNoStatement: the empty request list is a
// legitimate call (a host whose rules name no window) and must not build
// SQL with an empty VALUES list.
func TestWindowsWithNoRequestsCostsNoStatement(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "no-windows")
	host := seedHost(t, d, org, "h")
	store := rules.NewEvalSQLStore(d, cpuMinute(), items.NewSQLStore(d), discard())
	got, err := store.Windows(context.Background(), rules.Due{OrgID: org, HostID: host}, nil, time.Now())
	if err != nil {
		t.Fatalf("Windows: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("Windows returned %+v, want nothing", got)
	}
}

// TestMarkHostDirtyMakesAHostDue is the ingest path's contribution: one
// upsert marks the host, and the very next claim takes it.
func TestMarkHostDirtyMakesAHostDue(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "dirty")
	host := seedHost(t, d, org, "h")
	store := rules.NewEvalSQLStore(d, cpuMinute(), items.NewSQLStore(d), discard())

	if got := claimAll(t, store, 10); len(got) != 0 {
		t.Fatalf("a host that never ingested was claimed: %+v", got)
	}
	markDirty(t, d, org, host)
	got := claimAll(t, store, 10)
	if len(got) != 1 || got[0].HostID != host || got[0].OrgID != org {
		t.Fatalf("claimed %+v, want exactly the dirty host", got)
	}

	// A second mark on an already-evaluated cursor makes it dirty again:
	// the claim compares dirty_at against evaluated_at, not against a flag
	// somebody has to remember to clear. Both moments come from the
	// database's clock — the one MarkHostDirty is about to write from too
	// (see setCursor for what binding a Go time here cost).
	setCursor(t, d, org, host, -time.Hour, 0, time.Hour)
	if got := claimAll(t, store, 10); len(got) != 0 {
		t.Fatalf("an evaluated, not-yet-due host was claimed: %+v", got)
	}
	markDirty(t, d, org, host)
	if got := claimAll(t, store, 10); len(got) != 1 {
		t.Fatalf("claimed %+v after a fresh mark, want the host", got)
	}
}

// TestClaimTakesAHostWhoseNextDueHasPassed: no new samples at all, no
// dirty mark — only an expired next_due_at. This is the path that brings a
// silent agent's tuples to unknown, so it has to work on its own.
func TestClaimTakesAHostWhoseNextDueHasPassed(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "overdue")
	host := seedHost(t, d, org, "h")
	store := rules.NewEvalSQLStore(d, cpuMinute(), items.NewSQLStore(d), discard())

	// Evaluated exactly as recently as it was marked, so the host is not
	// dirty; only next_due_at, a minute in the past, can claim it.
	setCursor(t, d, org, host, -time.Hour, -time.Hour, -time.Minute)
	got := claimAll(t, store, 10)
	if len(got) != 1 || got[0].HostID != host {
		t.Fatalf("claimed %+v, want the overdue host", got)
	}
}

// TestClaimBoundsItsBatch: the limit is the batch, and the oldest
// next_due_at goes first so a backlog drains in order rather than
// starving its oldest rows.
func TestClaimBoundsItsBatch(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "batch")
	store := rules.NewEvalSQLStore(d, cpuMinute(), items.NewSQLStore(d), discard())

	oldest := seedHost(t, d, org, "oldest")
	middle := seedHost(t, d, org, "middle")
	newest := seedHost(t, d, org, "newest")
	const past = -time.Hour // evaluated as recently as marked: overdue, not dirty
	setCursor(t, d, org, newest, past, past, -1*time.Minute)
	setCursor(t, d, org, middle, past, past, -2*time.Minute)
	setCursor(t, d, org, oldest, past, past, -3*time.Minute)

	got := claimAll(t, store, 2)
	if len(got) != 2 {
		t.Fatalf("claimed %d hosts with a limit of 2", len(got))
	}
	if got[0].HostID != oldest || got[1].HostID != middle {
		t.Fatalf("claimed %+v, want the two oldest next_due_at first", got)
	}
}

// TestLoadGathersEveryInputTheSnapshotNeeds walks one host's whole load:
// its tuples with the resolved interval that defines staleness, the
// effective rules resolved for it, its facts, its previous states, the
// flapping counts inside the 30-minute window and — the field Task 8
// documents a quiet degradation for — the previous roll-up.
func TestLoadGathersEveryInputTheSnapshotNeeds(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "load")
	host := seedHost(t, d, org, "h")
	now := time.Now().UTC().Truncate(time.Microsecond)

	cpu := rules.Tuple{CheckKey: "cpu", Metric: "cpu.used_pct"}
	gone := rules.Tuple{CheckKey: "removed", Metric: "removed.metric"}
	insertCurrent(t, d, org, host, cpu, ptr(95.0), nil, now.Add(-30*time.Second), "")
	insertCurrent(t, d, org, host, gone, ptr(1.0), nil, now.Add(-30*time.Second), "")

	rule := hostRule(org, host)
	if err := rules.NewSQLStore(d).Create(context.Background(), rule); err != nil {
		t.Fatal(err)
	}
	// A disabled rule takes part in nothing; it must not reach Effective.
	disabled := hostRule(org, host)
	disabled.ID = uuid.Must(uuid.NewV7())
	disabled.Enabled = false
	disabled.Severity = rules.SeverityCrit
	if err := rules.NewSQLStore(d).Create(context.Background(), disabled); err != nil {
		t.Fatal(err)
	}

	if _, err := d.Pool().Exec(context.Background(),
		`UPDATE hosts SET facts = '{"cpu_count":"8","kernel":"6.1"}'::jsonb WHERE id = $1`, host); err != nil {
		t.Fatal(err)
	}
	insertCheckState(t, d, org, host, stateRow{tuple: cpu, state: rules.StateWarn, since: now.Add(-time.Hour), evaluatedAt: now.Add(-time.Minute)})
	// These two `at` values are Go times measured against a 30-minute
	// window the database computes from its own now(), so this is a
	// cross-clock comparison — a legitimate one, unlike setCursor's:
	// 10 and 20 minutes clear of the boundary, against a skew measured in
	// hundreds of microseconds. Keep the margins large and it stays
	// legitimate; do not tighten them to a second.
	insertStateChange(t, d, org, host, now.Add(-10*time.Minute), cpu, rules.StateOK, rules.StateWarn)
	insertStateChange(t, d, org, host, now.Add(-40*time.Minute), cpu, rules.StateWarn, rules.StateOK)
	insertHostState(t, d, org, host, rules.StateWarn, 0, 1, 0, now.Add(-time.Hour), now.Add(-time.Minute))

	store := rules.NewEvalSQLStore(d, cpuMinute(), items.NewSQLStore(d), discard())
	load, err := store.Load(context.Background(), rules.Due{OrgID: org, HostID: host})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if len(load.Tuples) != 2 {
		t.Fatalf("tuples = %+v, want both current rows", load.Tuples)
	}
	byTuple := map[rules.Tuple]rules.TupleSnapshot{}
	for _, ts := range load.Tuples {
		byTuple[ts.Tuple] = ts
	}
	if got := byTuple[cpu]; got.Interval != time.Minute || got.Value == nil || *got.Value != 95 {
		t.Fatalf("cpu tuple = %+v, want the resolved one-minute interval and its value", got)
	}
	if got := byTuple[gone]; got.Interval != 0 {
		t.Fatalf("a tuple whose check no longer resolves carries interval %v, want 0 so the evaluator leaves it out", got.Interval)
	}
	if len(load.Effective) != 1 || load.Effective[0].Winner.ID != rule.ID {
		t.Fatalf("effective = %+v, want only the enabled rule", load.Effective)
	}
	if load.Facts["cpu_count"] != "8" || load.Facts["kernel"] != "6.1" {
		t.Fatalf("facts = %+v, want the host's own", load.Facts)
	}
	if prev, ok := load.Previous[cpu]; !ok || prev.State != rules.StateWarn {
		t.Fatalf("previous = %+v, want the stored warn", load.Previous)
	}
	if got := load.Changes30[cpu]; got != 1 {
		t.Fatalf("Changes30 = %d, want 1 — only the change inside the 30-minute window counts", got)
	}
	if load.PreviousHost == nil || load.PreviousHost.State != rules.StateWarn {
		t.Fatalf("PreviousHost = %+v, want the stored roll-up", load.PreviousHost)
	}
}

// TestLoadOfANeverEvaluatedHostCarriesNoPreviousRollUp: nil is what "this
// host has never been evaluated" looks like, and Evaluate's own fallback
// is written for exactly that.
func TestLoadOfANeverEvaluatedHostCarriesNoPreviousRollUp(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "fresh")
	host := seedHost(t, d, org, "h")
	store := rules.NewEvalSQLStore(d, cpuMinute(), items.NewSQLStore(d), discard())
	load, err := store.Load(context.Background(), rules.Due{OrgID: org, HostID: host})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if load.PreviousHost != nil {
		t.Fatalf("PreviousHost = %+v, want nil", load.PreviousHost)
	}
}

// TestLoadIgnoresAnotherOrganizationsRows: the ticker runs unscoped (it
// claims across every organization), so each statement's own org_id is
// the only thing keeping one tenant's rules off another tenant's host.
func TestLoadIgnoresAnotherOrganizationsRows(t *testing.T) {
	d := testdb.Open(t)
	orgA := seedOrg(t, d, "tenant-a")
	orgB := seedOrg(t, d, "tenant-b")
	hostA := seedHost(t, d, orgA, "a-host")
	hostB := seedHost(t, d, orgB, "b-host")
	now := time.Now().UTC().Truncate(time.Microsecond)

	cpu := rules.Tuple{CheckKey: "cpu", Metric: "cpu.used_pct"}
	insertCurrent(t, d, orgA, hostA, cpu, ptr(95.0), nil, now, "")
	insertCurrent(t, d, orgB, hostB, cpu, ptr(1.0), nil, now, "")
	if err := rules.NewSQLStore(d).Create(context.Background(), hostRule(orgB, hostB)); err != nil {
		t.Fatal(err)
	}

	store := rules.NewEvalSQLStore(d, cpuMinute(), items.NewSQLStore(d), discard())
	load, err := store.Load(context.Background(), rules.Due{OrgID: orgA, HostID: hostA})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(load.Tuples) != 1 || load.Tuples[0].Value == nil || *load.Tuples[0].Value != 95 {
		t.Fatalf("tuples = %+v, want only this organization's host", load.Tuples)
	}
	if len(load.Effective) != 0 {
		t.Fatalf("effective = %+v, want none — the only rule belongs to another organization", load.Effective)
	}
}

// TestLoadDiscoversNonEmptyItemsAndSkipsWholeHostChecks: a host with two
// mounts gets two check_items rows — one each, even though /var is
// reported by two metrics — and cpu's own whole-host reading, which
// carries no item at all, gets none (the design's own "Items with an
// empty name" ruling).
func TestLoadDiscoversNonEmptyItemsAndSkipsWholeHostChecks(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "discover")
	host := seedHost(t, d, org, "h")
	now := time.Now().UTC().Truncate(time.Microsecond)

	cpu := rules.Tuple{CheckKey: "cpu", Metric: "cpu.used_pct"}
	varUsed := rules.Tuple{CheckKey: "disk", Item: "/var", Metric: "disk.used_pct"}
	varFree := rules.Tuple{CheckKey: "disk", Item: "/var", Metric: "disk.free_bytes"}
	opt := rules.Tuple{CheckKey: "disk", Item: "/opt", Metric: "disk.used_pct"}
	insertCurrent(t, d, org, host, cpu, ptr(10.0), nil, now, "")
	insertCurrent(t, d, org, host, varUsed, ptr(50.0), nil, now, "")
	insertCurrent(t, d, org, host, varFree, ptr(1e9), nil, now, "")
	insertCurrent(t, d, org, host, opt, ptr(60.0), nil, now, "")

	store := rules.NewEvalSQLStore(d, cpuMinute(), items.NewSQLStore(d), discard())
	if _, err := store.Load(context.Background(), rules.Due{OrgID: org, HostID: host}); err != nil {
		t.Fatalf("Load: %v", err)
	}

	views, err := items.NewSQLStore(d).ItemsForHost(context.Background(), org, host)
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 2 {
		t.Fatalf("check_items rows = %+v, want exactly 2 (the two mounts, none for cpu)", views)
	}
	names := map[string]bool{}
	for _, v := range views {
		if v.CheckKey != "disk" {
			t.Fatalf("discovered an item for check %q, want only disk", v.CheckKey)
		}
		names[v.Name] = true
	}
	if !names["/var"] || !names["/opt"] {
		t.Fatalf("discovered items = %+v, want /var and /opt", names)
	}
}

// TestLoadSeenItemsDropsEmptyAndDedupesDuplicates is the Seen derivation's
// own unit test: a fake ItemStore in place of the real one isolates what
// Load derives from a host's tuples from what items.SQLStore does with it
// (store_sql_test.go already covers that half), and Load still runs
// against the real database for everything else it needs.
//
// It also pins the cost ItemStore.Decisions' own doc comment names: Load
// reads it once per host and resolves every item's decision from that one
// result in memory, not once per item. The fixture seeds two distinct
// items (/var and /opt, on top of cpu's whole-host reading that carries no
// item at all) precisely so a regression that moved the Decisions call
// into a per-item loop would be caught here — with only one item in play,
// "once per host" and "once per item" are the same number, and the
// distinction those two shapes make would be invisible.
func TestLoadSeenItemsDropsEmptyAndDedupesDuplicates(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "seen")
	host := seedHost(t, d, org, "h")
	now := time.Now().UTC().Truncate(time.Microsecond)

	cpu := rules.Tuple{CheckKey: "cpu", Metric: "cpu.used_pct"}
	varUsed := rules.Tuple{CheckKey: "disk", Item: "/var", Metric: "disk.used_pct"}
	varFree := rules.Tuple{CheckKey: "disk", Item: "/var", Metric: "disk.free_bytes"}
	opt := rules.Tuple{CheckKey: "disk", Item: "/opt", Metric: "disk.used_pct"}
	insertCurrent(t, d, org, host, cpu, ptr(10.0), nil, now, "")
	insertCurrent(t, d, org, host, varUsed, ptr(50.0), nil, now, "")
	insertCurrent(t, d, org, host, varFree, ptr(1e9), nil, now, "")
	insertCurrent(t, d, org, host, opt, ptr(60.0), nil, now, "")

	fake := &fakeItemStore{}
	store := rules.NewEvalSQLStore(d, cpuMinute(), fake, discard())
	if _, err := store.Load(context.Background(), rules.Due{OrgID: org, HostID: host}); err != nil {
		t.Fatalf("Load: %v", err)
	}

	// ListCheckCurrent orders rows by (check_key, item, metric), so /opt
	// sorts ahead of /var.
	want := []items.Seen{{CheckKey: "disk", Name: "/opt"}, {CheckKey: "disk", Name: "/var"}}
	if !reflect.DeepEqual(fake.discovered, want) {
		t.Fatalf("Discover called with %+v, want %+v — cpu's empty item dropped, disk's two metrics of /var collapsed to one item", fake.discovered, want)
	}
	if fake.decisionsCalls != 1 {
		t.Fatalf("Decisions called %d times for one host with two items, want exactly 1", fake.decisionsCalls)
	}
}

// TestIgnoredResolvesPerTickEvenWhenTheDecisionPredatesTheItem is review
// focus 3: an operator may ignore a pattern before the item it matches has
// ever been seen — ahead of a planned rollout, the design's own example —
// and the very first evaluation after the item appears must already see
// it ignored. Decide runs fresh every tick against this tick's own
// decisions and this tick's own items; there is no "seed it once" step to
// miss, so there is nothing to have missed here either.
func TestIgnoredResolvesPerTickEvenWhenTheDecisionPredatesTheItem(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "predates")
	host := seedHost(t, d, org, "h")
	now := time.Now().UTC().Truncate(time.Microsecond)

	// The decision is created first — check_items has no row for this item
	// yet, since Discover has never run for this host.
	decision := items.Decision{
		ID: uuid.Must(uuid.NewV7()), OrgID: org, Scope: items.ScopeHost, HostID: &host,
		CheckKey: "disk", MatchKind: items.MatchGlob, Pattern: "/data*", Verdict: items.Ignore,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := items.NewSQLStore(d).CreateDecision(context.Background(), decision); err != nil {
		t.Fatal(err)
	}

	// The item appears only now, on the tick this test evaluates.
	disk := rules.Tuple{CheckKey: "disk", Item: "/data", Metric: "disk.used_pct"}
	insertCurrent(t, d, org, host, disk, ptr(90.0), nil, now, "")

	store := rules.NewEvalSQLStore(d, cpuMinute(), items.NewSQLStore(d), discard())
	load, err := store.Load(context.Background(), rules.Due{OrgID: org, HostID: host})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	key := rules.ItemKey{CheckKey: "disk", Item: "/data"}
	if !load.Ignored[key] {
		t.Fatalf("Ignored = %+v, want %+v ignored on its very first tick", load.Ignored, key)
	}
}

// TestIgnoredDoesNotLeakAcrossHosts: a host-scoped decision on one host
// must never govern another host's item of the same name, even though
// Decisions is one query covering the whole organization's own decisions
// plus this host's.
func TestIgnoredDoesNotLeakAcrossHosts(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "no-leak")
	host := seedHost(t, d, org, "h")
	other := seedHost(t, d, org, "other")
	now := time.Now().UTC().Truncate(time.Microsecond)

	decision := items.Decision{
		ID: uuid.Must(uuid.NewV7()), OrgID: org, Scope: items.ScopeHost, HostID: &other,
		CheckKey: "disk", MatchKind: items.MatchGlob, Pattern: "*", Verdict: items.Ignore,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := items.NewSQLStore(d).CreateDecision(context.Background(), decision); err != nil {
		t.Fatal(err)
	}

	disk := rules.Tuple{CheckKey: "disk", Item: "/data", Metric: "disk.used_pct"}
	insertCurrent(t, d, org, host, disk, ptr(90.0), nil, now, "")

	store := rules.NewEvalSQLStore(d, cpuMinute(), items.NewSQLStore(d), discard())
	load, err := store.Load(context.Background(), rules.Due{OrgID: org, HostID: host})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	key := rules.ItemKey{CheckKey: "disk", Item: "/data"}
	if load.Ignored[key] {
		t.Fatalf("Ignored = %+v, want %+v watched — the matching decision belongs to another host", load.Ignored, key)
	}
}

// TestApplyWritesEverythingInTheClaimsTransaction is review focus 5's
// write half: states, changes, roll-up and the cursor's own advance all
// land, and they land inside the transaction ProcessDue opened.
func TestApplyWritesEverythingInTheClaimsTransaction(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "apply")
	host := seedHost(t, d, org, "h")
	store := rules.NewEvalSQLStore(d, cpuMinute(), items.NewSQLStore(d), discard())
	markDirty(t, d, org, host)

	now := time.Now().UTC().Truncate(time.Microsecond)
	cpu := rules.Tuple{CheckKey: "cpu", Metric: "cpu.used_pct"}
	res := rules.Result{
		States: []rules.TupleState{{
			Tuple: cpu, State: rules.StateCrit, Since: now, LastValue: ptr(95.0),
			Candidate: rules.StateWarn, CandidateSince: &now, Flapping: true, EvaluatedAt: now,
		}},
		Changes: []rules.Change{{At: now, Tuple: cpu, From: rules.StateOK, To: rules.StateCrit, Value: ptr(95.0)}},
		Host:    rules.HostState{State: rules.StateCrit, Crit: 1, Since: now, EvaluatedAt: now},
		// next_due_at genuinely is written from the manager's clock in
		// production (Result.NextDue) and compared against the database's
		// now() by the claim, so a Go time is right here. 90 seconds is
		// far enough clear of the two clocks' skew for "not due any more"
		// to mean what it says.
		NextDue: now.Add(90 * time.Second),
	}

	n, err := store.ProcessDue(context.Background(), now, 10, func(ctx context.Context, due rules.Due) error {
		return store.Apply(ctx, due, res)
	})
	if err != nil {
		t.Fatalf("ProcessDue: %v", err)
	}
	if n != 1 {
		t.Fatalf("ProcessDue evaluated %d hosts, want 1", n)
	}

	states, err := rules.NewSQLStore(d).StatesForHost(context.Background(), org, host)
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 1 {
		t.Fatalf("check_states rows = %d, want 1", len(states))
	}
	got := states[0]
	if got.State != rules.StateCrit || got.Candidate != rules.StateWarn || got.CandidateSince == nil || !got.Flapping {
		t.Fatalf("state = %+v, want the crit with its candidate and flapping flag", got)
	}
	if got.LastValue == nil || *got.LastValue != 95 {
		t.Fatalf("last_value = %v, want 95", got.LastValue)
	}
	if n := countRows(t, d, `SELECT count(*) FROM check_state_changes WHERE host_id = $1`, host); n != 1 {
		t.Fatalf("check_state_changes rows = %d, want 1", n)
	}
	hostStates, err := rules.NewSQLStore(d).HostStates(context.Background(), org, rules.FleetFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(hostStates) != 1 || hostStates[0].State != rules.StateCrit || hostStates[0].Crit != 1 {
		t.Fatalf("roll-up = %+v, want one crit host", hostStates)
	}
	_, evaluatedAt, nextDue := cursorRow(t, d, host)
	if !nextDue.Equal(res.NextDue) {
		t.Fatalf("next_due_at = %v, want the evaluation's own %v", nextDue, res.NextDue)
	}
	// evaluated_at is written by the database; `now` is this process's
	// clock. A minute of slack is what makes comparing them across the two
	// meaningful — the assertion is "recent", not "equal".
	if evaluatedAt.Before(now.Add(-time.Minute)) {
		t.Fatalf("evaluated_at = %v, want the database's own clock at the claim", evaluatedAt)
	}
	// The cursor is no longer dirty and no longer due, so a second pass
	// takes nothing.
	if got := claimAll(t, store, 10); len(got) != 0 {
		t.Fatalf("the evaluated host was claimed again: %+v", got)
	}

	// A second evaluation upserts in place rather than accumulating rows.
	res.States[0].State = rules.StateWarn
	res.Changes = nil
	res.Host.State = rules.StateWarn
	markDirty(t, d, org, host)
	if _, err := store.ProcessDue(context.Background(), now, 10, func(ctx context.Context, due rules.Due) error {
		return store.Apply(ctx, due, res)
	}); err != nil {
		t.Fatalf("second ProcessDue: %v", err)
	}
	if n := countRows(t, d, `SELECT count(*) FROM check_states WHERE host_id = $1`, host); n != 1 {
		t.Fatalf("check_states rows after a second evaluation = %d, want 1", n)
	}
	if n := countRows(t, d, `SELECT count(*) FROM check_host_states WHERE host_id = $1`, host); n != 1 {
		t.Fatalf("check_host_states rows after a second evaluation = %d, want 1", n)
	}
}

// TestApplyDeletesTheRollUpWhenNothingIsJudged: a host every one of whose
// checks lost its resolved interval must not keep showing the colour it
// had when its last check was deleted. The roll-up is derived state; the
// history it came from stays in check_state_changes.
func TestApplyDeletesTheRollUpWhenNothingIsJudged(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "rollup-gone")
	host := seedHost(t, d, org, "h")
	store := rules.NewEvalSQLStore(d, cpuMinute(), items.NewSQLStore(d), discard())
	now := time.Now().UTC().Truncate(time.Microsecond)
	markDirty(t, d, org, host)
	insertHostState(t, d, org, host, rules.StateCrit, 1, 0, 0, now.Add(-time.Hour), now.Add(-time.Minute))
	insertStateChange(t, d, org, host, now.Add(-time.Hour), rules.Tuple{CheckKey: "cpu", Metric: "cpu.used_pct"}, rules.StateOK, rules.StateCrit)

	if _, err := store.ProcessDue(context.Background(), now, 10, func(ctx context.Context, due rules.Due) error {
		return store.Apply(ctx, due, rules.Result{NextDue: now.Add(time.Minute)})
	}); err != nil {
		t.Fatalf("ProcessDue: %v", err)
	}
	if n := countRows(t, d, `SELECT count(*) FROM check_host_states WHERE host_id = $1`, host); n != 0 {
		t.Fatalf("check_host_states rows = %d, want the stale roll-up deleted", n)
	}
	if n := countRows(t, d, `SELECT count(*) FROM check_state_changes WHERE host_id = $1`, host); n != 1 {
		t.Fatalf("check_state_changes rows = %d, want the history untouched", n)
	}
}

// TestApplyDropsAnIgnoredTuplesRowAndReWatchingStartsFresh is the Dropped
// integration case: Apply really removes the row a Result.Dropped names,
// and a tuple that becomes watched again on a later tick gets a Since of
// that tick, never of the crossing stored in the row that was deleted —
// because nothing judged it for every tick in between, so no continuity
// across that gap is true to claim.
func TestApplyDropsAnIgnoredTuplesRowAndReWatchingStartsFresh(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "drop-and-rewatch")
	host := seedHost(t, d, org, "h")
	store := rules.NewEvalSQLStore(d, cpuMinute(), items.NewSQLStore(d), discard())

	diskVar := rules.Tuple{CheckKey: "disk", Item: "/var", Metric: "disk.used_pct"}
	firstTick := time.Now().UTC().Truncate(time.Microsecond)
	originalCrossing := firstTick.Add(-24 * time.Hour)

	// The row as it stood before the operator ignored the item: crit, and
	// has been since yesterday.
	insertCheckState(t, d, org, host, stateRow{
		tuple: diskVar, state: rules.StateCrit,
		since: originalCrossing, evaluatedAt: firstTick.Add(-time.Minute),
	})
	markDirty(t, d, org, host)

	// Tick one: the evaluation that judges the item ignored. No state, no
	// change, no roll-up — only Dropped naming the row to remove.
	if _, err := store.ProcessDue(context.Background(), firstTick, 10, func(ctx context.Context, due rules.Due) error {
		return store.Apply(ctx, due, rules.Result{
			Dropped: []rules.Tuple{diskVar},
			NextDue: firstTick.Add(time.Minute),
		})
	}); err != nil {
		t.Fatalf("first ProcessDue: %v", err)
	}
	if n := countRows(t, d, `SELECT count(*) FROM check_states WHERE host_id = $1`, host); n != 0 {
		t.Fatalf("check_states rows after the drop = %d, want 0", n)
	}

	// Tick two, a day later: the operator has re-watched the item (or
	// withdrawn the decision), and it is judged again — ok, this time.
	secondTick := firstTick.Add(24 * time.Hour)
	markDirty(t, d, org, host)
	if _, err := store.ProcessDue(context.Background(), secondTick, 10, func(ctx context.Context, due rules.Due) error {
		return store.Apply(ctx, due, rules.Result{
			States: []rules.TupleState{{
				Tuple: diskVar, State: rules.StateOK, Since: secondTick, EvaluatedAt: secondTick,
			}},
			Host:    rules.HostState{State: rules.StateOK, Since: secondTick, EvaluatedAt: secondTick},
			NextDue: secondTick.Add(time.Minute),
		})
	}); err != nil {
		t.Fatalf("second ProcessDue: %v", err)
	}

	states, err := rules.NewSQLStore(d).StatesForHost(context.Background(), org, host)
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 1 {
		t.Fatalf("check_states rows after re-watching = %d, want 1", len(states))
	}
	got := states[0]
	if got.State != rules.StateOK {
		t.Fatalf("state = %s, want ok", got.State)
	}
	if !got.Since.Equal(secondTick) {
		t.Fatalf("since = %v, want %v (the re-watching tick) — not %v (the original, now-untrue crossing)",
			got.Since, secondTick, originalCrossing)
	}
}

// TestAFailedHostLeavesNothingBehind: fn's error rolls that host's whole
// transaction back — the writes Apply already made, and the cursor — so
// the host is still exactly as due as it was.
func TestAFailedHostLeavesNothingBehind(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "rollback")
	good := seedHost(t, d, org, "good")
	bad := seedHost(t, d, org, "bad")
	store := rules.NewEvalSQLStore(d, cpuMinute(), items.NewSQLStore(d), discard())
	now := time.Now().UTC().Truncate(time.Microsecond)
	// The bad host is claimed first (older next_due_at), so the good host's
	// own transaction commits after the failure rather than before it.
	setCursor(t, d, org, bad, 0, -time.Hour, -2*time.Minute)
	setCursor(t, d, org, good, 0, -time.Hour, -1*time.Minute)

	cpu := rules.Tuple{CheckKey: "cpu", Metric: "cpu.used_pct"}
	res := rules.Result{
		States:  []rules.TupleState{{Tuple: cpu, State: rules.StateCrit, Since: now, EvaluatedAt: now}},
		Changes: []rules.Change{{At: now, Tuple: cpu, From: rules.StateOK, To: rules.StateCrit}},
		Host:    rules.HostState{State: rules.StateCrit, Crit: 1, Since: now, EvaluatedAt: now},
		NextDue: now.Add(time.Hour),
	}
	boom := errors.New("evaluation failed")
	n, err := store.ProcessDue(context.Background(), now, 10, func(ctx context.Context, due rules.Due) error {
		if err := store.Apply(ctx, due, res); err != nil {
			return err
		}
		if due.HostID == bad {
			return boom
		}
		return nil
	})
	if err != nil {
		t.Fatalf("ProcessDue: %v", err)
	}
	if n != 1 {
		t.Fatalf("ProcessDue reported %d evaluated, want 1 (the other failed)", n)
	}
	if n := countRows(t, d, `SELECT count(*) FROM check_states WHERE host_id = $1`, bad); n != 0 {
		t.Fatalf("the failed host left %d check_states rows behind", n)
	}
	if n := countRows(t, d, `SELECT count(*) FROM check_state_changes WHERE host_id = $1`, bad); n != 0 {
		t.Fatalf("the failed host left %d check_state_changes rows behind", n)
	}
	if n := countRows(t, d, `SELECT count(*) FROM check_host_states WHERE host_id = $1`, bad); n != 0 {
		t.Fatalf("the failed host left %d check_host_states rows behind", n)
	}
	if !stillDue(t, d, bad) {
		t.Fatal("the failed host's cursor advanced; it must stay due for the next tick")
	}
	// The host that succeeded committed regardless.
	if n := countRows(t, d, `SELECT count(*) FROM check_states WHERE host_id = $1`, good); n != 1 {
		t.Fatalf("the healthy host wrote %d check_states rows, want 1", n)
	}
	if _, _, nextDue := cursorRow(t, d, good); !nextDue.Equal(res.NextDue) {
		t.Fatalf("the healthy host's next_due_at = %v, want %v", nextDue, res.NextDue)
	}
}

// TestLoadFailureLeavesTheHostDue: the port into the check pipeline is
// allowed to fail (a resolver error), and when it does the host is left
// exactly as it was.
func TestLoadFailureLeavesTheHostDue(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "load-fail")
	host := seedHost(t, d, org, "h")
	store := rules.NewEvalSQLStore(d, fixedChecks{err: errors.New("resolver down")}, items.NewSQLStore(d), discard())
	if _, err := store.Load(context.Background(), rules.Due{OrgID: org, HostID: host}); err == nil {
		t.Fatal("Load returned no error although the check pipeline failed")
	}
}

// gateStore wraps an EvalStore and pauses inside Load, which is the point
// at which its host's cursor row is already locked by the transaction
// evaluating it. It is how the test below holds one ticker inside its
// claim while the other one runs.
type gateStore struct {
	rules.EvalStore
	entered chan struct{}
	release chan struct{}
}

func (g *gateStore) Load(ctx context.Context, d rules.Due) (rules.HostLoad, error) {
	close(g.entered)
	<-g.release
	return g.EvalStore.Load(ctx, d)
}

// TestTwoTickersEvaluateOneDueHostExactlyOnce is review focus 5. The two
// tickers genuinely overlap: the first is held inside its own claim — its
// cursor row locked, its transaction open — for the whole time the second
// one runs. That is what makes this a test of FOR UPDATE SKIP LOCKED
// rather than of two sequential ticks: without SKIP LOCKED the second
// ticker would block on the first one's row lock until the deadline below
// fires, and with no exclusion at all the host would be evaluated twice
// and write two sets of changes.
func TestTwoTickersEvaluateOneDueHostExactlyOnce(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "two-tickers")
	host := seedHost(t, d, org, "h")
	now := time.Now().UTC().Truncate(time.Microsecond)

	cpu := rules.Tuple{CheckKey: "cpu", Metric: "cpu.used_pct"}
	insertCurrent(t, d, org, host, cpu, ptr(95.0), nil, now.Add(-10*time.Second), "")
	insertCheckState(t, d, org, host, stateRow{tuple: cpu, state: rules.StateOK, since: now.Add(-time.Hour), evaluatedAt: now.Add(-time.Minute)})
	rule := hostRule(org, host)
	rule.Severity = rules.SeverityCrit
	rule.For = 0
	if err := rules.NewSQLStore(d).Create(context.Background(), rule); err != nil {
		t.Fatal(err)
	}
	markDirty(t, d, org, host)

	store := rules.NewEvalSQLStore(d, cpuMinute(), items.NewSQLStore(d), discard())
	gate := &gateStore{EvalStore: store, entered: make(chan struct{}), release: make(chan struct{})}
	opts := []rules.TickerOption{
		rules.WithTickerClock(func() time.Time { return now }),
		rules.WithTickerLogger(discard()),
	}
	first := rules.NewTicker(gate, &fakeSink{}, opts...)
	second := rules.NewTicker(store, &fakeSink{}, opts...)

	type tick struct {
		n   int
		err error
	}
	firstDone := make(chan tick, 1)
	go func() {
		n, err := first.Tick(context.Background())
		firstDone <- tick{n, err}
	}()

	select {
	case <-gate.entered:
	case <-time.After(30 * time.Second):
		t.Fatal("the first ticker never reached its claimed host")
	}

	secondDone := make(chan tick, 1)
	go func() {
		n, err := second.Tick(context.Background())
		secondDone <- tick{n, err}
	}()
	var secondTick tick
	select {
	case secondTick = <-secondDone:
	case <-time.After(30 * time.Second):
		close(gate.release)
		t.Fatal("the second ticker blocked on the first one's claim; FOR UPDATE SKIP LOCKED is what must keep it from waiting")
	}
	close(gate.release)
	firstTick := <-firstDone

	if firstTick.err != nil {
		t.Fatalf("first ticker: %v", firstTick.err)
	}
	if secondTick.err != nil {
		t.Fatalf("second ticker: %v", secondTick.err)
	}
	if firstTick.n != 1 {
		t.Fatalf("the first ticker evaluated %d hosts, want 1", firstTick.n)
	}
	if secondTick.n != 0 {
		t.Fatalf("the second ticker evaluated %d hosts, want 0 — the only due host was already claimed", secondTick.n)
	}
	if n := countRows(t, d, `SELECT count(*) FROM check_state_changes WHERE host_id = $1`, host); n != 1 {
		t.Fatalf("check_state_changes rows = %d, want exactly one set of changes", n)
	}
	if n := countRows(t, d, `SELECT count(*) FROM check_states WHERE host_id = $1`, host); n != 1 {
		t.Fatalf("check_states rows = %d, want 1", n)
	}
	states, err := rules.NewSQLStore(d).StatesForHost(context.Background(), org, host)
	if err != nil {
		t.Fatal(err)
	}
	if states[0].State != rules.StateCrit || states[0].RuleID == nil || *states[0].RuleID != rule.ID {
		t.Fatalf("state = %+v, want crit decided by the seeded rule", states[0])
	}
	// Both sides are this process's clock: next_due_at is what Evaluate
	// derived from the tick's injected now, read back unchanged.
	if _, _, nextDue := cursorRow(t, d, host); !nextDue.After(now) {
		t.Fatalf("next_due_at = %v, want a moment after the tick's own now", nextDue)
	}
}

// TestTickerEvaluatesThroughTheRealStore is the whole path end to end: a
// dirty host with a value, a rule and a window, evaluated by the ticker
// against the real database. It is the one test that would catch a
// Snapshot field the loader forgot to fill.
func TestTickerEvaluatesThroughTheRealStore(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "end-to-end")
	host := seedHost(t, d, org, "h")
	now := time.Now().UTC().Truncate(time.Microsecond)

	cpu := rules.Tuple{CheckKey: "cpu", Metric: "cpu.used_pct"}
	insertCurrent(t, d, org, host, cpu, ptr(95.0), nil, now.Add(-10*time.Second), "")
	insertSample(t, d, org, host, cpu, now.Add(-5*time.Minute), ptr(90.0), nil)
	insertSample(t, d, org, host, cpu, now.Add(-1*time.Minute), ptr(100.0), nil)
	if _, err := d.Pool().Exec(context.Background(),
		`UPDATE hosts SET facts = '{"cpu_count":"8"}'::jsonb WHERE id = $1`, host); err != nil {
		t.Fatal(err)
	}

	// avg(cpu.used_pct, 10m) > fact("cpu_count") * 10 — one rule that needs
	// the window fetch and the facts to be right, so neither can be
	// forgotten without this failing.
	rule := hostRule(org, host)
	rule.Severity = rules.SeverityCrit
	rule.For = 0
	rule.Mode = "expression"
	rule.Trigger = compare(">", window("cpu.used_pct", "avg", 600),
		rules.Node{Kind: rules.KindArith, Op: "*", Args: []rules.Node{fact("cpu_count"), number(10)}})
	if err := rules.NewSQLStore(d).Create(context.Background(), rule); err != nil {
		t.Fatal(err)
	}
	markDirty(t, d, org, host)

	store := rules.NewEvalSQLStore(d, cpuMinute(), items.NewSQLStore(d), discard())
	tk := rules.NewTicker(store, &fakeSink{},
		rules.WithTickerClock(func() time.Time { return now }),
		rules.WithTickerLogger(discard()))
	n, err := tk.Tick(context.Background())
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if n != 1 {
		t.Fatalf("Tick evaluated %d hosts, want 1", n)
	}
	states, err := rules.NewSQLStore(d).StatesForHost(context.Background(), org, host)
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 1 || states[0].State != rules.StateCrit {
		t.Fatalf("states = %+v, want one crit (avg 95 > 8 * 10)", states)
	}
	hostStates, err := rules.NewSQLStore(d).HostStates(context.Background(), org, rules.FleetFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(hostStates) != 1 || hostStates[0].State != rules.StateCrit {
		t.Fatalf("roll-up = %+v, want one crit host", hostStates)
	}
	if _, _, nextDue := cursorRow(t, d, host); !nextDue.After(now) {
		t.Fatalf("next_due_at = %v, want a moment after now", nextDue)
	}
}

// TestEvaluateFeedsTheAlertSinkInTheHostTransaction is the transactional
// half of the task's own test (ticker_test.go carries the fake-store half,
// which pins the sink's inputs without a database): driven through the
// real EvalSQLStore, a host evaluated to crit reaches the sink exactly
// once, on the same claimed transaction its own write-back just used —
// which TestSinkErrorRollsTheHostBack below proves by making that
// transaction roll back.
func TestEvaluateFeedsTheAlertSinkInTheHostTransaction(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "sink-feed")
	host := seedHost(t, d, org, "h")
	now := time.Now().UTC().Truncate(time.Microsecond)

	cpu := rules.Tuple{CheckKey: "cpu", Metric: "cpu.used_pct"}
	insertCurrent(t, d, org, host, cpu, ptr(95.0), nil, now.Add(-10*time.Second), "")
	// A prior ok state, so this evaluation is a transition and not a first
	// insert — Evaluate only records a Change once there is a state to
	// move away from (evaluate.go: "a tuple's first evaluation is an
	// insert, not a transition").
	insertCheckState(t, d, org, host, stateRow{tuple: cpu, state: rules.StateOK, since: now.Add(-time.Hour), evaluatedAt: now.Add(-time.Minute)})
	rule := hostRule(org, host)
	rule.Severity = rules.SeverityCrit
	rule.For = 0
	if err := rules.NewSQLStore(d).Create(context.Background(), rule); err != nil {
		t.Fatal(err)
	}
	markDirty(t, d, org, host)

	store := rules.NewEvalSQLStore(d, cpuMinute(), items.NewSQLStore(d), discard())
	// onApply reads check_states straight through the tx Apply was handed,
	// rather than trusting that *some* pgx.Tx value arrived: the host's own
	// write-back is still uncommitted at this point, so only the claimed
	// transaction itself can see "crit" here — the pool, any other
	// connection, or a nil tx (which would instead panic the call, caught
	// by evaluateOne's recover and counted as a failed host below) would
	// all see the pre-evaluation "ok" row or nothing at all. An earlier
	// version of this test passed even against Apply(ctx, nil, …) because
	// fakeSink.Apply discarded its tx argument entirely — this is what
	// makes the test's own name actually true.
	var sawStateViaTx string
	var readErr error
	sink := &fakeSink{onApply: func(ctx context.Context, tx pgx.Tx, in rules.AlertInput) error {
		readErr = tx.QueryRow(ctx, `SELECT state FROM check_states WHERE host_id = $1`, in.HostID).Scan(&sawStateViaTx)
		return nil // surfaced below, not as an Apply failure: a wrong read should fail this assertion, not n
	}}
	tk := rules.NewTicker(store, sink,
		rules.WithTickerClock(func() time.Time { return now }), rules.WithTickerLogger(discard()))
	n, err := tk.Tick(context.Background())
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if n != 1 {
		t.Fatalf("Tick evaluated %d hosts, want 1", n)
	}

	if len(sink.calls) != 1 {
		t.Fatalf("sink called %d times, want once per evaluated host", len(sink.calls))
	}
	got := sink.calls[0]
	if got.OrgID != org || got.HostID != host {
		t.Fatalf("sink saw org=%s host=%s, want org=%s host=%s", got.OrgID, got.HostID, org, host)
	}
	if got.Host.State != rules.StateCrit || len(got.States) == 0 {
		t.Fatalf("the sink got %+v, want the post-evaluation states and roll-up", got)
	}
	if len(got.Changes) != 1 || got.Changes[0].To != rules.StateCrit {
		t.Fatalf("sink changes = %+v, want exactly one change to crit", got.Changes)
	}
	if !got.At.Equal(now) {
		t.Fatalf("sink saw At=%v, want the tick's own now=%v", got.At, now)
	}
	if readErr != nil {
		t.Fatalf("reading check_states through the sink's own tx: %v", readErr)
	}
	if sawStateViaTx != string(rules.StateCrit) {
		t.Fatalf("the sink's own tx read check_states.state = %q, want %q — Apply must run on the host's own claimed, still-uncommitted transaction", sawStateViaTx, rules.StateCrit)
	}
}

// TestSinkErrorRollsTheHostBack: the sink runs inside the same per-host
// transaction as the store's own write-back, so a sink failure must roll
// back everything that transaction did — check_states stays unwritten, the
// roll-up stays unwritten and the cursor stays exactly where it was, the
// same guarantee a store.Apply failure already gives (TestAFailedHostLeavesNothingBehind).
func TestSinkErrorRollsTheHostBack(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "sink-error")
	host := seedHost(t, d, org, "h")
	now := time.Now().UTC().Truncate(time.Microsecond)

	cpu := rules.Tuple{CheckKey: "cpu", Metric: "cpu.used_pct"}
	insertCurrent(t, d, org, host, cpu, ptr(95.0), nil, now.Add(-10*time.Second), "")
	// A prior ok state, so this evaluation would otherwise insert a real
	// check_state_changes row — without it, that table's own "stays empty"
	// assertion below would pass whether or not the rollback worked, since
	// a first-ever evaluation writes no change row in the first place (see
	// TestEvaluateFeedsTheAlertSinkInTheHostTransaction's own comment).
	insertCheckState(t, d, org, host, stateRow{tuple: cpu, state: rules.StateOK, since: now.Add(-time.Hour), evaluatedAt: now.Add(-time.Minute)})
	rule := hostRule(org, host)
	rule.Severity = rules.SeverityCrit
	rule.For = 0
	if err := rules.NewSQLStore(d).Create(context.Background(), rule); err != nil {
		t.Fatal(err)
	}
	markDirty(t, d, org, host)

	store := rules.NewEvalSQLStore(d, cpuMinute(), items.NewSQLStore(d), discard())
	sink := &fakeSink{applyErr: errors.New("sink boom")}
	log, buf := quietLogger()
	tk := rules.NewTicker(store, sink,
		rules.WithTickerClock(func() time.Time { return now }), rules.WithTickerLogger(log))

	n, err := tk.Tick(context.Background())
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if n != 0 {
		t.Fatalf("Tick evaluated %d hosts, want 0 — the sink failed", n)
	}
	// The pre-seeded ok row is still exactly what it was: the upsert that
	// would have overwritten it with crit rolled back along with the sink's
	// own failure.
	var state string
	if err := d.Pool().QueryRow(context.Background(), `SELECT state FROM check_states WHERE host_id = $1`, host).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != string(rules.StateOK) {
		t.Fatalf("check_states.state = %q, want %q — the sink's failure must roll back the store's own write-back too", state, rules.StateOK)
	}
	if got := countRows(t, d, `SELECT count(*) FROM check_state_changes WHERE host_id = $1`, host); got != 0 {
		t.Fatalf("check_state_changes rows = %d, want 0 — the ok-to-crit change must not survive the sink's rollback", got)
	}
	if got := countRows(t, d, `SELECT count(*) FROM check_host_states WHERE host_id = $1`, host); got != 0 {
		t.Fatalf("check_host_states rows = %d, want 0", got)
	}
	if !stillDue(t, d, host) {
		t.Fatal("the host's cursor advanced even though the sink failed; it must stay due for the next tick")
	}
	if !strings.Contains(buf.String(), "sink boom") {
		t.Fatalf("the sink failure was not logged; log was %q", buf.String())
	}
}

// TestAPanickingHostLeavesTheBatchIntact: a host whose evaluation panics
// has its own transaction rolled back and the batch carries on — the other
// hosts commit on their own. Without the recover in evaluateOne, one bad
// rule tree would take down the whole ticker goroutine, and with it the
// manager.
func TestAPanickingHostLeavesTheBatchIntact(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "panicking-host")
	bad := seedHost(t, d, org, "bad")
	good := seedHost(t, d, org, "good")
	store := rules.NewEvalSQLStore(d, cpuMinute(), items.NewSQLStore(d), discard())
	now := time.Now().UTC().Truncate(time.Microsecond)
	setCursor(t, d, org, bad, 0, -time.Hour, -2*time.Minute)
	setCursor(t, d, org, good, 0, -time.Hour, -1*time.Minute)

	cpu := rules.Tuple{CheckKey: "cpu", Metric: "cpu.used_pct"}
	res := rules.Result{
		States:  []rules.TupleState{{Tuple: cpu, State: rules.StateOK, Since: now, EvaluatedAt: now}},
		Host:    rules.HostState{State: rules.StateOK, Since: now, EvaluatedAt: now},
		NextDue: now.Add(time.Hour),
	}
	n, err := store.ProcessDue(context.Background(), now, 10, func(ctx context.Context, due rules.Due) error {
		if err := store.Apply(ctx, due, res); err != nil {
			return err
		}
		if due.HostID == bad {
			panic("a rule tree exploded")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("ProcessDue: %v", err)
	}
	if n != 1 {
		t.Fatalf("ProcessDue reported %d evaluated, want 1 (the other panicked)", n)
	}
	if n := countRows(t, d, `SELECT count(*) FROM check_states WHERE host_id = $1`, bad); n != 0 {
		t.Fatalf("the panicking host left %d check_states rows behind", n)
	}
	if n := countRows(t, d, `SELECT count(*) FROM check_states WHERE host_id = $1`, good); n != 1 {
		t.Fatalf("the healthy host wrote %d check_states rows, want 1", n)
	}
	if !stillDue(t, d, bad) {
		t.Fatal("the panicking host's cursor advanced; it must stay due for the next tick")
	}
}

// TestRunHostLogsWhatBecomesOfAFailedHost: a host whose evaluation panics
// or whose commit fails is postponed only while the batch's context is
// live (evaluateOne); once it is done the host is left due, and the log
// line must not claim otherwise.
func TestRunHostLogsWhatBecomesOfAFailedHost(t *testing.T) {
	cases := []struct {
		name string
		// fn runs inside the host's transaction.
		fn func(cancel context.CancelFunc) error
		// want is the log text that names the failure.
		want string
	}{
		{"panic with a live context", func(context.CancelFunc) error { panic("a rule tree exploded") }, "evaluating a host panicked"},
		{"panic after the batch was cut short", func(cancel context.CancelFunc) error {
			cancel()
			panic("a rule tree exploded")
		}, "evaluating a host panicked"},
		{"commit after the batch was cut short", func(cancel context.CancelFunc) error {
			cancel()
			return nil
		}, "commit host transaction"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := testdb.Open(t)
			org := seedOrg(t, d, "failed-host-log")
			host := seedHost(t, d, org, "h")
			log, buf := quietLogger()
			store := rules.NewEvalSQLStore(d, cpuMinute(), items.NewSQLStore(d), log)
			now := time.Now().UTC().Truncate(time.Microsecond)
			setCursor(t, d, org, host, 0, -time.Hour, -time.Minute)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if _, err := store.ProcessDue(ctx, now, 10, func(context.Context, rules.Due) error {
				return tc.fn(cancel)
			}); err != nil {
				t.Fatalf("ProcessDue: %v", err)
			}

			out := buf.String()
			if !strings.Contains(out, tc.want) {
				t.Fatalf("the failure was not logged; log was %q", out)
			}
			if ctx.Err() != nil {
				if !strings.Contains(out, "batch cut short; the host stays due for the next tick") || strings.Contains(out, "postponing") || strings.Contains(out, "held out") {
					t.Fatalf("a host of a cut-short batch is not postponed, but the log says otherwise: %q", out)
				}
				return
			}
			if !strings.Contains(out, "held out of the claim for a while") {
				t.Fatalf("a failed host of a live batch is postponed; log was %q", out)
			}
		})
	}
}

// TestABatchCutShortKeepsWhatItCommitted is what the batch deadline
// relies on (see rules.batchDeadlineFactor): when the batch's context ends
// mid-pass, the hosts that already committed are kept — each one committed
// its own transaction as it finished — the host that was in flight is
// rolled back, and the hosts never reached are left untouched and still
// due for whichever manager gets there first.
func TestABatchCutShortKeepsWhatItCommitted(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "batch-cut-short")
	store := rules.NewEvalSQLStore(d, cpuMinute(), items.NewSQLStore(d), discard())
	now := time.Now().UTC().Truncate(time.Microsecond)

	first := seedHost(t, d, org, "first")
	second := seedHost(t, d, org, "second")
	third := seedHost(t, d, org, "third")
	setCursor(t, d, org, first, 0, -time.Hour, -3*time.Minute)
	setCursor(t, d, org, second, 0, -time.Hour, -2*time.Minute)
	setCursor(t, d, org, third, 0, -time.Hour, -1*time.Minute)

	cpu := rules.Tuple{CheckKey: "cpu", Metric: "cpu.used_pct"}
	res := rules.Result{
		States:  []rules.TupleState{{Tuple: cpu, State: rules.StateOK, Since: now, EvaluatedAt: now}},
		Host:    rules.HostState{State: rules.StateOK, Since: now, EvaluatedAt: now},
		NextDue: now.Add(time.Hour),
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n, err := store.ProcessDue(ctx, now, 10, func(hostCtx context.Context, due rules.Due) error {
		if due.HostID == second {
			// The manager is cancelled (shutdown, not a deadline: backlog
			// 7.112 postpones the host a deadline cut off) while this host
			// is being loaded: its own work fails on the dead context, and
			// nothing after it starts.
			cancel()
			return hostCtx.Err()
		}
		return store.Apply(hostCtx, due, res)
	})
	if err != nil {
		t.Fatalf("ProcessDue: %v", err)
	}
	if n != 1 {
		t.Fatalf("ProcessDue reported %d evaluated, want 1 — the first host had already committed", n)
	}
	if n := countRows(t, d, `SELECT count(*) FROM check_states WHERE host_id = $1`, first); n != 1 {
		t.Fatalf("the host that finished before the cut wrote %d rows, want 1", n)
	}
	if _, _, nextDue := cursorRow(t, d, first); !nextDue.Equal(res.NextDue) {
		t.Fatalf("the finished host's next_due_at = %v, want %v", nextDue, res.NextDue)
	}
	for name, host := range map[string]uuid.UUID{"in flight": second, "never reached": third} {
		if n := countRows(t, d, `SELECT count(*) FROM check_states WHERE host_id = $1`, host); n != 0 {
			t.Fatalf("the %s host wrote %d rows, want none", name, n)
		}
		if !stillDue(t, d, host) {
			t.Fatalf("the %s host's cursor advanced; it must stay due", name)
		}
		// Not postponed either (backlog 7.58): the in-flight host failed
		// because the manager was cancelled, not because of anything about
		// the host, so it keeps its place at the head of the next claim.
		if countRows(t, d, `SELECT count(*) FROM check_eval_cursor WHERE host_id = $1 AND next_due_at <= now() AND retry_after IS NULL`, host) != 1 {
			t.Fatalf("the %s host was postponed; a cancelled batch must not hold any host back", name)
		}
	}
}

// TestWindowsRefusesAnUnknownAggregate: the statement's CASE would return
// NULL for one, which the filter drops — indistinguishable from a window
// holding no sample, and so a silent non-match. An unknown aggregate is an
// error instead; only a hand-built request can produce one, since
// TypeCheck refuses the same set at save time.
func TestWindowsRefusesAnUnknownAggregate(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "unknown-agg")
	host := seedHost(t, d, org, "h")
	store := rules.NewEvalSQLStore(d, cpuMinute(), items.NewSQLStore(d), discard())
	reqs := []rules.WindowRequest{
		{Key: rules.ItemKey{CheckKey: "cpu"}, Metric: "cpu.used_pct", Agg: "avg", Seconds: 600},
		{Key: rules.ItemKey{CheckKey: "cpu"}, Metric: "cpu.used_pct", Agg: "median", Seconds: 600},
	}
	got, err := store.Windows(context.Background(), rules.Due{OrgID: org, HostID: host}, reqs, time.Now())
	if err == nil {
		t.Fatalf("Windows accepted an unknown aggregate and returned %+v", got)
	}
	if !strings.Contains(err.Error(), "median") {
		t.Fatalf("error = %v, want it to name the aggregate it refused", err)
	}
}

// TestEvaluatingOneHostDoesNotBlockIngestForAnother is the lock-lifetime
// claim ProcessDue's two-part claim exists for, and the one thing a
// batch-wide transaction could never satisfy.
//
// MarkHostDirty is an INSERT … ON CONFLICT on check_eval_cursor, run
// inside internal/checks' ingest transaction — the 7 000-samples-a-second
// hot path the whole cursor design exists to keep free. It takes the same
// row lock the claim does. While the claim held FOR UPDATE on every row of
// a 200-host batch for the length of that batch, every agent whose host
// had been claimed blocked on its ingest commit, holding its own pool
// connection, for as long as the slowest host in the batch took.
//
// So: one host is held mid-evaluation while the ingest path's own
// statement runs for a DIFFERENT host of the same batch. It has to go
// through at once. With the batch-wide lock it blocks until the evaluation
// is released, which the deadline below turns into a failure.
func TestEvaluatingOneHostDoesNotBlockIngestForAnother(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "ingest-not-blocked")
	held := seedHost(t, d, org, "held")
	ingesting := seedHost(t, d, org, "ingesting")
	store := rules.NewEvalSQLStore(d, cpuMinute(), items.NewSQLStore(d), discard())
	now := time.Now().UTC().Truncate(time.Microsecond)
	// The held host is claimed first (older next_due_at), so the other one
	// is still in the batch, unevaluated, while the first one is paused.
	setCursor(t, d, org, held, 0, -time.Hour, -2*time.Minute)
	setCursor(t, d, org, ingesting, 0, -time.Hour, -1*time.Minute)

	cpu := rules.Tuple{CheckKey: "cpu", Metric: "cpu.used_pct"}
	res := rules.Result{
		States:  []rules.TupleState{{Tuple: cpu, State: rules.StateOK, Since: now, EvaluatedAt: now}},
		Host:    rules.HostState{State: rules.StateOK, Since: now, EvaluatedAt: now},
		NextDue: now.Add(time.Hour),
	}

	inFlight := make(chan struct{})
	release := make(chan struct{})
	type batch struct {
		n   int
		err error
	}
	done := make(chan batch, 1)
	go func() {
		n, err := store.ProcessDue(context.Background(), now, 10, func(ctx context.Context, due rules.Due) error {
			if err := store.Apply(ctx, due, res); err != nil {
				return err
			}
			if due.HostID == held {
				close(inFlight)
				<-release
			}
			return nil
		})
		done <- batch{n, err}
	}()

	select {
	case <-inFlight:
	case <-time.After(30 * time.Second):
		t.Fatal("the batch never reached its first host")
	}

	markCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	markErr := gen.New(d.Pool()).MarkHostDirty(markCtx, gen.MarkHostDirtyParams{OrgID: org, HostID: ingesting})
	close(release)
	if markErr != nil {
		t.Fatalf("ingest blocked on the evaluation of another host: %v", markErr)
	}

	got := <-done
	if got.err != nil {
		t.Fatalf("ProcessDue: %v", got.err)
	}
	if got.n != 2 {
		t.Fatalf("ProcessDue evaluated %d hosts, want 2", got.n)
	}
}

// TestAnAlreadyEvaluatedHostIsSkippedBeforeItIsEvaluatedAgain pins the one
// race the two-part claim introduces, and that it is handled rather than
// merely tolerated: the scan lists a host and releases its lock, another
// manager evaluates it in the gap, and LockOneEvalCursor's repeated due
// predicate then returns no row — so the host is skipped instead of being
// evaluated a second time on the strength of a listing that has gone
// stale.
func TestAnAlreadyEvaluatedHostIsSkippedBeforeItIsEvaluatedAgain(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "claim-race")
	taken := seedHost(t, d, org, "taken")
	store := rules.NewEvalSQLStore(d, cpuMinute(), items.NewSQLStore(d), discard())
	now := time.Now().UTC().Truncate(time.Microsecond)
	setCursor(t, d, org, taken, 0, -time.Hour, -time.Minute)

	// Stand in for the other manager finishing this host first: one full
	// evaluation, which advances the cursor out of due-ness.
	cpu := rules.Tuple{CheckKey: "cpu", Metric: "cpu.used_pct"}
	res := rules.Result{
		States:  []rules.TupleState{{Tuple: cpu, State: rules.StateOK, Since: now, EvaluatedAt: now}},
		Host:    rules.HostState{State: rules.StateOK, Since: now, EvaluatedAt: now},
		NextDue: now.Add(time.Hour),
	}
	n, err := store.ProcessDue(context.Background(), now, 10, func(ctx context.Context, due rules.Due) error {
		return store.Apply(ctx, due, res)
	})
	if err != nil || n != 1 {
		t.Fatalf("the first pass evaluated %d hosts, %v; want 1, nil", n, err)
	}
	if stillDue(t, d, taken) {
		t.Fatal("the fixture is wrong: the host is still due after a full evaluation")
	}

	// The scan would no longer list this host — so drive the per-host claim
	// directly, which is exactly the position a manager is in when it
	// listed the host a moment before someone else finished it.
	tx, err := d.Pool().Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background()) //nolint:errcheck // read-only, test cleanup
	if _, err := gen.New(tx).LockOneEvalCursor(context.Background(), taken); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("LockOneEvalCursor on a host that is no longer due = %v, want pgx.ErrNoRows", err)
	}
}

// TestEvalCursorRLS is check_eval_cursor's half of what
// TestRulesAreIsolatedPerOrgByRLS does for check_rules and
// TestStateStoreRLS (states_store_test.go) does for the three state
// tables: it goes through d.WithOrg, so it exercises org_isolation's own
// USING clause rather than the org_isolation_bypass policy every other
// test in this file runs under.
//
// It matters here more than anywhere, not less: this is the one table in
// the set whose every production statement runs UNSCOPED (the ticker
// claims across all organizations at once), so the bypass policy is all
// those statements ever meet and nothing else would notice org_isolation
// being replaced with USING (true).
func TestEvalCursorRLS(t *testing.T) {
	d := testdb.Open(t)
	orgA := seedOrg(t, d, "cursor-rls-a")
	orgB := seedOrg(t, d, "cursor-rls-b")
	hostA := seedHost(t, d, orgA, "h1")
	hostB := seedHost(t, d, orgB, "h1")
	markDirty(t, d, orgA, hostA)
	markDirty(t, d, orgB, hostB)

	var seen []uuid.UUID
	err := d.WithOrg(context.Background(), orgA, func(tx pgx.Tx) error {
		rows, err := tx.Query(context.Background(), "SELECT org_id FROM check_eval_cursor")
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
		t.Fatalf("RLS leak on check_eval_cursor: saw %v, want only [%s]", seen, orgA)
	}
}

// TestEvaluateOneSetsStatementTimeout (backlog 7.58): one host's
// evaluation is bounded on its own, inside its own transaction, so a host
// whose window statement would run for minutes is cut off after 10 s and
// fails alone rather than consuming the whole batch's deadline. SET LOCAL,
// so the setting dies with the transaction and never leaks onto the
// pooled connection.
func TestEvaluateOneSetsStatementTimeout(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "statement-timeout")
	host := seedHost(t, d, org, "h")
	store := rules.NewEvalSQLStore(d, cpuMinute(), items.NewSQLStore(d), discard())
	setCursor(t, d, org, host, 0, -time.Hour, -time.Minute)

	var inside string
	n, err := store.ProcessDue(context.Background(), time.Now(), 10, func(ctx context.Context, _ rules.Due) error {
		tx, ok := rules.ClaimedTx(ctx)
		if !ok {
			return errors.New("fn ran without the host's transaction")
		}
		return tx.QueryRow(ctx, `SHOW statement_timeout`).Scan(&inside)
	})
	if err != nil {
		t.Fatalf("ProcessDue: %v", err)
	}
	if n != 1 {
		t.Fatalf("ProcessDue evaluated %d hosts, want 1", n)
	}
	if inside != "10s" {
		t.Fatalf("statement_timeout inside the host's transaction = %q, want %q", inside, "10s")
	}
}

// retryAfter reads one host's retry_after; ok is false while it is NULL.
func retryAfter(t *testing.T, d *db.DB, host uuid.UUID) (at time.Time, ok bool) {
	t.Helper()
	var v *time.Time
	if err := d.Pool().QueryRow(context.Background(),
		`SELECT retry_after FROM check_eval_cursor WHERE host_id = $1`, host).Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v == nil {
		return time.Time{}, false
	}
	return *v, true
}

// TestFailedHostIsPostponed (backlog 7.58): a host whose evaluation fails
// every time (a statement timeout on every try) never advances its
// cursor, so it is dirty for ever, and with the oldest next_due_at it
// headed every claim — a handful of them spent each batch's deadline
// before any other organization's host was reached. Pushing next_due_at
// back would not help: a healthy dirty host's next_due_at is its
// staleness deadline, minutes ahead, so the failed host would still sort
// first. A failed host is instead held out of the claim altogether for
// failedHostBackoff (retry_after), measured on the database's own clock,
// and comes back once that passes; a committed evaluation clears the hold.
func TestFailedHostIsPostponed(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "postpone-failed")
	bad := seedHost(t, d, org, "bad")
	good := seedHost(t, d, org, "good")
	store := rules.NewEvalSQLStore(d, cpuMinute(), items.NewSQLStore(d), discard())
	// The bad host is overdue and dirty. The healthy one is dirty only:
	// its next_due_at is the staleness deadline three minutes ahead, which
	// is where a live host's sits between samples.
	setCursor(t, d, org, bad, 0, -time.Hour, -2*time.Minute)
	setCursor(t, d, org, good, 0, -time.Hour, 3*time.Minute)
	_, _, badNextDue := cursorRow(t, d, bad)

	var before time.Time
	if err := d.Pool().QueryRow(context.Background(), `SELECT now()`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	n, err := store.ProcessDue(context.Background(), time.Now(), 10, func(_ context.Context, due rules.Due) error {
		if due.HostID == bad {
			return errors.New("canceling statement due to statement timeout")
		}
		// The healthy host commits without advancing its cursor, so it is
		// still dirty on the next claim below.
		return nil
	})
	if err != nil {
		t.Fatalf("ProcessDue: %v", err)
	}
	if n != 1 {
		t.Fatalf("ProcessDue evaluated %d hosts, want 1 (the other failed)", n)
	}

	at, held := retryAfter(t, d, bad)
	if !held || at.Before(before.Add(rules.FailedHostBackoff)) {
		t.Fatalf("the failed host's retry_after = %v (set %v), want at least %v (now + %s)", at, held, before.Add(rules.FailedHostBackoff), rules.FailedHostBackoff)
	}
	if _, _, nextDue := cursorRow(t, d, bad); !nextDue.Equal(badNextDue) {
		t.Fatalf("the failed host's next_due_at moved from %v to %v; only retry_after may change", badNextDue, nextDue)
	}
	if !stillDue(t, d, bad) {
		t.Fatal("the failed host is no longer dirty or due; the hold must leave its cursor as it was")
	}

	// While held, the claim takes the healthy dirty host and not the failed
	// one, however much older the failed one's next_due_at is.
	if got := claimAll(t, store, 10); len(got) != 1 || got[0].HostID != good {
		t.Fatalf("the claim while the failed host is held took %+v, want only the healthy host", got)
	}
	// The per-host lock refuses it too, so a host another manager held back
	// between the scan and the lock is not evaluated anyway.
	tx, err := d.Pool().Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gen.New(tx).LockOneEvalCursor(context.Background(), bad); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("LockOneEvalCursor on a held host = %v, want pgx.ErrNoRows", err)
	}
	_ = tx.Rollback(context.Background())

	// Once the hold has passed the host is claimable again, ahead of the
	// healthy one by its older next_due_at.
	if _, err := d.Pool().Exec(context.Background(),
		`UPDATE check_eval_cursor SET retry_after = now() - interval '1 second' WHERE host_id = $1`, bad); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	res := rules.Result{NextDue: now.Add(time.Hour)}
	var order []uuid.UUID
	if _, err := store.ProcessDue(context.Background(), now, 10, func(ctx context.Context, due rules.Due) error {
		order = append(order, due.HostID)
		return store.Apply(ctx, due, res)
	}); err != nil {
		t.Fatalf("ProcessDue after the hold: %v", err)
	}
	if len(order) != 2 || order[0] != bad {
		t.Fatalf("the claim after the hold took %v, want the formerly failed host first, then the healthy one", order)
	}
	// And the evaluation that committed cleared the hold.
	if at, held := retryAfter(t, d, bad); held {
		t.Fatalf("retry_after = %v after a committed evaluation, want NULL", at)
	}
}

// blockingChecks is a HostChecks whose ContextForHost never answers for the
// hosts in block: it holds until its context ends and returns that error,
// the way the real port does when its many group-selector queries outlast
// the deadline (backlog 7.112). Every other host resolves like cpuMinute.
type blockingChecks struct {
	block map[uuid.UUID]bool
}

func (b blockingChecks) ContextForHost(ctx context.Context, org, host uuid.UUID) (rules.CheckContext, error) {
	if b.block[host] {
		<-ctx.Done()
		return rules.CheckContext{}, ctx.Err()
	}
	return cpuMinute().ContextForHost(ctx, org, host)
}

// TestHostCutOffByTheBatchDeadlineIsPostponed (backlog 7.112): the host
// whose evaluation is running when the batch deadline fires is held out of
// the claim exactly like a host that failed, because otherwise it keeps its
// old next_due_at, heads every later claim and spends every later batch's
// deadline again, with every host behind it — from any organization —
// never reached. Host A (organization 1, the oldest due) blocks in
// ContextForHost until the deadline; host B (organization 2) is healthy.
// After the first tick A is postponed (and B, never reached, is not); the
// second tick evaluates B.
func TestHostCutOffByTheBatchDeadlineIsPostponed(t *testing.T) {
	d := testdb.Open(t)
	orgA := seedOrg(t, d, "deadline-cut-a")
	orgB := seedOrg(t, d, "deadline-cut-b")
	slow := seedHost(t, d, orgA, "slow")
	healthy := seedHost(t, d, orgB, "healthy")
	setCursor(t, d, orgA, slow, 0, -time.Hour, -2*time.Minute)
	setCursor(t, d, orgB, healthy, 0, -time.Hour, -time.Minute)
	_, _, slowNextDue := cursorRow(t, d, slow)

	store := rules.NewEvalSQLStore(d, blockingChecks{block: map[uuid.UUID]bool{slow: true}}, items.NewSQLStore(d), discard())
	tk := rules.NewTicker(store, &fakeSink{},
		rules.WithInterval(10*time.Millisecond), rules.WithBatchDeadlineFloor(time.Second),
		rules.WithTickerLogger(discard()))

	var before time.Time
	if err := d.Pool().QueryRow(context.Background(), `SELECT now()`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	n, err := tk.Tick(context.Background())
	if err != nil {
		t.Fatalf("first Tick: %v", err)
	}
	if n != 0 {
		t.Fatalf("first Tick evaluated %d hosts, want 0 (the slow host consumed the deadline)", n)
	}
	at, held := retryAfter(t, d, slow)
	if !held || at.Before(before.Add(rules.FailedHostBackoff)) {
		t.Fatalf("the cut-off host's retry_after = %v (set %v), want at least %v (now + %s)", at, held, before.Add(rules.FailedHostBackoff), rules.FailedHostBackoff)
	}
	if _, _, nextDue := cursorRow(t, d, slow); !nextDue.Equal(slowNextDue) {
		t.Fatalf("the cut-off host's next_due_at moved from %v to %v; only retry_after may change", slowNextDue, nextDue)
	}
	if _, held := retryAfter(t, d, healthy); held || !stillDue(t, d, healthy) {
		t.Fatal("the host that was never reached must be left exactly as it was")
	}

	n, err = tk.Tick(context.Background())
	if err != nil {
		t.Fatalf("second Tick: %v", err)
	}
	if n != 1 {
		t.Fatalf("second Tick evaluated %d hosts, want 1 (the healthy host, no longer behind the slow one)", n)
	}
	if stillDue(t, d, healthy) {
		t.Fatal("the healthy host is still due after the second tick; it was not evaluated")
	}
	if !stillDue(t, d, slow) {
		t.Fatal("the cut-off host is no longer due; the hold must leave its cursor as it was")
	}
}

// TestHostPastItsOwnDeadlineIsPostponedAndTheBatchGoesOn (backlog 7.112):
// the whole per-host evaluation, ContextForHost on its own pool connection
// included, runs under a deadline of its own, shorter than the batch's. A
// host that outlasts it fails alone and is postponed like any failed host,
// and the hosts behind it are still evaluated in the same batch rather than
// waiting for the batch deadline to end the slow one.
func TestHostPastItsOwnDeadlineIsPostponedAndTheBatchGoesOn(t *testing.T) {
	d := testdb.Open(t)
	orgA := seedOrg(t, d, "host-deadline-a")
	orgB := seedOrg(t, d, "host-deadline-b")
	slow := seedHost(t, d, orgA, "slow")
	healthy := seedHost(t, d, orgB, "healthy")
	setCursor(t, d, orgA, slow, 0, -time.Hour, -2*time.Minute)
	setCursor(t, d, orgB, healthy, 0, -time.Hour, -time.Minute)

	store := rules.NewEvalSQLStore(d, blockingChecks{block: map[uuid.UUID]bool{slow: true}}, items.NewSQLStore(d), discard())
	store.SetHostDeadline(500 * time.Millisecond)
	tk := rules.NewTicker(store, &fakeSink{}, rules.WithTickerLogger(discard()))

	n, err := tk.Tick(context.Background())
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if n != 1 {
		t.Fatalf("Tick evaluated %d hosts, want 1 (the healthy host behind the slow one)", n)
	}
	if _, held := retryAfter(t, d, slow); !held {
		t.Fatal("the host past its own deadline has no retry_after")
	}
	if stillDue(t, d, healthy) {
		t.Fatal("the healthy host is still due; the slow host's deadline must not end the batch")
	}
}
