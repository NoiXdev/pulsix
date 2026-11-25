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
