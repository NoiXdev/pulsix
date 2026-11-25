package rules_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/items"
	"github.com/noixdev/pulsix/internal/rules"
)

// switchedChecks is the check pipeline's side of the port with the cpu
// check switched on or off, the way an operator disables and re-enables it.
type switchedChecks struct{ on bool }

func (s *switchedChecks) ContextForHost(context.Context, uuid.UUID, uuid.UUID) (rules.CheckContext, error) {
	if !s.on {
		return rules.CheckContext{}, nil
	}
	return rules.CheckContext{Intervals: map[string]time.Duration{"cpu": time.Minute}}, nil
}

// unmeasuredAt reads one tuple's check_states.unmeasured_at straight from
// the table; nil when the row has no mark.
func unmeasuredAt(t *testing.T, d *db.DB, host uuid.UUID, tu rules.Tuple) *time.Time {
	t.Helper()
	var at *time.Time
	if err := d.Pool().QueryRow(context.Background(),
		`SELECT unmeasured_at FROM check_states WHERE host_id = $1 AND check_key = $2 AND item = $3 AND metric = $4`,
		host, tu.CheckKey, tu.Item, tu.Metric).Scan(&at); err != nil {
		t.Fatalf("read unmeasured_at: %v", err)
	}
	return at
}

// TestATupleMeasuredAgainAfterAGapStartsFresh (backlog 4.42) drives the
// real store: a crit cpu tuple that has flapped, its check switched off for
// two ticks and on again. The row stands through the gap, marked with the
// gap's first moment; measured again, the tuple starts fresh — its since is
// the re-enabled evaluation's own moment, no transition is written for the
// gap, and the transitions from before it no longer count towards
// flapping, on that tick or the next. A second gap is marked anew.
func TestATupleMeasuredAgainAfterAGapStartsFresh(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "unmeasured-fresh")
	host := seedHost(t, d, org, "h")
	now := time.Now().UTC().Truncate(time.Microsecond)
	cpu := rules.Tuple{CheckKey: "cpu", Metric: "cpu.used_pct"}

	rule := hostRule(org, host)
	rule.Severity = rules.SeverityCrit
	rule.For = 0
	if err := rules.NewSQLStore(d).Create(context.Background(), rule); err != nil {
		t.Fatal(err)
	}
	// Five transitions inside the flapping window, all before the gap.
	for i, to := range []rules.State{rules.StateCrit, rules.StateOK, rules.StateCrit, rules.StateOK, rules.StateCrit} {
		from := rules.StateOK
		if to == rules.StateOK {
			from = rules.StateCrit
		}
		insertStateChange(t, d, org, host, now.Add(time.Duration(i-20)*time.Minute), cpu, from, to)
	}

	checks := &switchedChecks{on: true}
	store := rules.NewEvalSQLStore(d, checks, items.NewSQLStore(d), testAgentWindow, discard())
	sink := &fakeSink{}
	tick := func(at time.Time, on bool) {
		t.Helper()
		checks.on = on
		insertCurrent(t, d, org, host, cpu, ptr(95.0), nil, at, "")
		markDirty(t, d, org, host)
		tk := rules.NewTicker(store, sink,
			rules.WithTickerClock(func() time.Time { return at }),
			rules.WithTickerLogger(discard()))
		if n, err := tk.Tick(context.Background()); err != nil || n != 1 {
			t.Fatalf("Tick at %v: evaluated %d, err %v; want 1 host", at, n, err)
		}
	}
	state := func() rules.TupleState {
		t.Helper()
		states, err := rules.NewSQLStore(d).StatesForHost(context.Background(), org, host)
		if err != nil {
			t.Fatal(err)
		}
		if len(states) != 1 {
			t.Fatalf("states = %+v, want the one cpu row", states)
		}
		return states[0]
	}
	changes := func() int {
		t.Helper()
		return countRows(t, d, `SELECT count(*) FROM check_state_changes WHERE host_id = $1`, host)
	}

	t1, t2, t3, t4, t5, t6 := now.Add(-10*time.Minute), now.Add(-8*time.Minute), now.Add(-6*time.Minute),
		now.Add(-4*time.Minute), now.Add(-2*time.Minute), now

	tick(t1, true)
	if st := state(); st.State != rules.StateCrit || !st.Since.Equal(t1) || !st.Flapping || st.UnmeasuredAt != nil {
		t.Fatalf("before the gap: %+v, want crit since t1, flapping, unmarked", st)
	}

	tick(t2, false)
	tick(t3, false)
	if got := unmeasuredAt(t, d, host, cpu); got == nil || !got.Equal(t2) {
		t.Fatalf("unmeasured_at through the gap = %v, want %v (its first tick)", got, t2)
	}
	if st := state(); !st.Since.Equal(t1) || !st.EvaluatedAt.Equal(t1) {
		t.Fatalf("row through the gap: %+v, want it standing as t1 left it", st)
	}
	for i, call := range sink.calls[1:3] {
		if len(call.Unmeasured) != 1 || call.Unmeasured[0] != cpu {
			t.Fatalf("gap tick %d reported unmeasured %v, want [cpu]", i+1, call.Unmeasured)
		}
	}

	tick(t4, true)
	st := state()
	if st.State != rules.StateCrit || !st.Since.Equal(t4) {
		t.Fatalf("measured again: state %s since %v, want crit since %v (the re-enabled evaluation), not %v",
			st.State, st.Since, t4, t1)
	}
	if st.Flapping {
		t.Fatal("measured again: flapping carried from before the gap")
	}
	if n := changes(); n != 5 {
		t.Fatalf("check_state_changes = %d, want the 5 from before the gap and none for it", n)
	}
	if got := sink.calls[3].States; len(got) != 1 || !got[0].Since.Equal(t4) {
		t.Fatalf("alert sink states = %+v, want the fresh since t4", got)
	}

	tick(t5, true)
	if st := state(); !st.Since.Equal(t4) || st.Flapping {
		t.Fatalf("the tick after: %+v, want crit since t4 and not flapping (the pre-gap transitions stay out)", st)
	}

	tick(t6, false)
	if got := unmeasuredAt(t, d, host, cpu); got == nil || !got.Equal(t6) {
		t.Fatalf("unmeasured_at after a second gap = %v, want %v", got, t6)
	}
}

// TestAMarkIsOnlyStampedLaterThanTheLastJudgement: an evaluation whose
// moment is not later than the row's evaluated_at (two evaluations at one
// instant, or a manager whose clock is behind) leaves the row unmarked, so
// every mark on a row was active when it was stamped and the flapping
// count never leaves out the history of a state Evaluate carries. The next
// evaluation with a later moment stamps it.
func TestAMarkIsOnlyStampedLaterThanTheLastJudgement(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "unmeasured-stamp")
	host := seedHost(t, d, org, "h")
	store := rules.NewEvalSQLStore(d, cpuMinute(), items.NewSQLStore(d), testAgentWindow, discard())
	cpu := rules.Tuple{CheckKey: "cpu", Metric: "cpu.used_pct"}
	judged := time.Now().UTC().Truncate(time.Microsecond)
	insertCheckState(t, d, org, host, stateRow{tuple: cpu, state: rules.StateCrit, since: judged, evaluatedAt: judged})

	apply := func(at time.Time) {
		t.Helper()
		markDirty(t, d, org, host)
		if _, err := store.ProcessDue(context.Background(), at, 10, func(ctx context.Context, due rules.Due) error {
			return store.Apply(ctx, due, rules.Result{Unmeasured: []rules.Tuple{cpu}, At: at, NextDue: at.Add(time.Minute)})
		}); err != nil {
			t.Fatalf("ProcessDue at %v: %v", at, err)
		}
	}

	apply(judged.Add(-time.Second))
	apply(judged)
	if got := unmeasuredAt(t, d, host, cpu); got != nil {
		t.Fatalf("unmeasured_at after evaluations not later than the judgement = %v, want none", got)
	}
	later := judged.Add(time.Second)
	apply(later)
	if got := unmeasuredAt(t, d, host, cpu); got == nil || !got.Equal(later) {
		t.Fatalf("unmeasured_at = %v, want %v", got, later)
	}
}

// TestTheFlappingCountLeavesOutOnlyTheMarkedTuplesTransitionsUpToTheMark:
// Load's flapping window drops the transitions at or before a tuple's mark
// (one exactly at it included), keeps the one after it, and leaves every
// other tuple's transitions alone — a sibling metric of the same item and
// the same metric of another item, which a join missing a key column would
// mix up.
func TestTheFlappingCountLeavesOutOnlyTheMarkedTuplesTransitionsUpToTheMark(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "unmeasured-count")
	host := seedHost(t, d, org, "h")
	now := time.Now().UTC().Truncate(time.Microsecond)
	mark := now.Add(-10 * time.Minute)

	marked := rules.Tuple{CheckKey: "disk", Item: "/", Metric: "disk.used_pct"}
	otherMetric := rules.Tuple{CheckKey: "disk", Item: "/", Metric: "disk.free_bytes"}
	otherItem := rules.Tuple{CheckKey: "disk", Item: "/var", Metric: "disk.used_pct"}
	// The mark is superseded (judged since), as it is on every tick after
	// the return: it still bounds the count.
	insertCheckState(t, d, org, host, stateRow{tuple: marked, state: rules.StateOK, since: now.Add(-time.Minute), evaluatedAt: now.Add(-time.Minute)})
	if _, err := d.Pool().Exec(context.Background(),
		`UPDATE check_states SET unmeasured_at = $2 WHERE host_id = $1`, host, mark); err != nil {
		t.Fatal(err)
	}
	for _, tu := range []rules.Tuple{marked, otherMetric, otherItem} {
		insertStateChange(t, d, org, host, mark.Add(-time.Second), tu, rules.StateOK, rules.StateCrit)
		insertStateChange(t, d, org, host, mark, tu, rules.StateCrit, rules.StateOK)
		insertStateChange(t, d, org, host, mark.Add(time.Second), tu, rules.StateOK, rules.StateCrit)
	}

	store := rules.NewEvalSQLStore(d, fixedChecks{intervals: map[string]time.Duration{"disk": time.Minute}},
		items.NewSQLStore(d), testAgentWindow, discard())
	load, err := store.Load(context.Background(), rules.Due{OrgID: org, HostID: host}, now)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := map[rules.Tuple]int{marked: 1, otherMetric: 3, otherItem: 3}
	for tu, n := range want {
		if got := load.Changes30[tu]; got != n {
			t.Errorf("Changes30[%+v] = %d, want %d", tu, got, n)
		}
	}
}
