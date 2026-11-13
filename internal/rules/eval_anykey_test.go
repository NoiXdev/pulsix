package rules_test

import (
	"context"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/items"
	"github.com/noixdev/pulsix/internal/rules"
)

// TestOneOrgRuleJudgesTwoChecksOfAPluginIndependently (backlog 3.13e,
// review focus 2), through the real store: a host runs the disk plugin
// under two keys, one org rule judges both, and each key's tuple gets its
// own state and its own transition — one goes crit, the other stays ok.
func TestOneOrgRuleJudgesTwoChecksOfAPluginIndependently(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "any-key-eval")
	host := seedHost(t, d, org, "h")
	now := time.Now().UTC().Truncate(time.Microsecond)

	plain := rules.Tuple{CheckKey: "disk", Item: "/", Metric: "disk.used_pct"}
	data := rules.Tuple{CheckKey: "disk-data", Item: "/", Metric: "disk.used_pct"}
	insertCurrent(t, d, org, host, plain, ptr(10.0), nil, now.Add(-10*time.Second), "")
	insertCurrent(t, d, org, host, data, ptr(97.0), nil, now.Add(-10*time.Second), "")
	// Prior ok states, so a move to crit is a transition the sink sees.
	for _, tu := range []rules.Tuple{plain, data} {
		insertCheckState(t, d, org, host, stateRow{tuple: tu, state: rules.StateOK, since: now.Add(-time.Hour), evaluatedAt: now.Add(-time.Minute)})
	}

	rule := hostRule(org, host)
	rule.Scope, rule.HostID = rules.ScopeOrg, nil
	rule.CheckKey, rule.Metric, rule.Severity, rule.For = "disk", "disk.used_pct", rules.SeverityCrit, 0
	rule.Trigger = compare(">=", metric("disk.used_pct"), number(95))
	rule.Source = "disk.used_pct >= 95"
	if err := rules.NewSQLStore(d).Create(context.Background(), rule); err != nil {
		t.Fatal(err)
	}
	markDirty(t, d, org, host)

	checks := fixedChecks{intervals: map[string]time.Duration{"disk": time.Minute, "disk-data": time.Minute}}
	sink := &fakeSink{}
	tk := rules.NewTicker(rules.NewEvalSQLStore(d, checks, items.NewSQLStore(d), discard()), sink,
		rules.WithTickerClock(func() time.Time { return now }), rules.WithTickerLogger(discard()))
	if n, err := tk.Tick(context.Background()); err != nil || n != 1 {
		t.Fatalf("Tick = %d, %v; want one host evaluated", n, err)
	}

	states, err := rules.NewSQLStore(d).StatesForHost(context.Background(), org, host)
	if err != nil {
		t.Fatal(err)
	}
	byTuple := map[rules.Tuple]rules.TupleState{}
	for _, s := range states {
		byTuple[s.Tuple] = s
	}
	if len(byTuple) != 2 {
		t.Fatalf("states = %+v, want one per key", states)
	}
	if s := byTuple[data]; s.State != rules.StateCrit || s.RuleID == nil || *s.RuleID != rule.ID {
		t.Fatalf("disk-data state = %+v, want crit by the org rule", s)
	}
	// An ok state names no rule (nothing decided against it), so the
	// evaluation stamp is what shows this tuple was judged in the same run.
	if s := byTuple[plain]; s.State != rules.StateOK || !s.EvaluatedAt.Equal(now) {
		t.Fatalf("disk state = %+v, want ok, evaluated now", s)
	}

	if len(sink.calls) != 1 {
		t.Fatalf("sink called %d times, want once", len(sink.calls))
	}
	changes := sink.calls[0].Changes
	if len(changes) != 1 || changes[0].Tuple != data || changes[0].To != rules.StateCrit {
		t.Fatalf("changes = %+v, want exactly disk-data's move to crit", changes)
	}
}
