package rules_test

import (
	"context"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/checkplugins"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/items"
	"github.com/noixdev/pulsix/internal/rules"
)

// TestAnAbsentPoolsOtherMetricsAreNotAsked (backlog 3.74), through the real
// store and ticker with the zfs descriptor's own metric rule: a pool the
// agent reports as EXPORTED or MISSING reports zfs.state and nothing else,
// so its last zfs.capacity_pct — old enough to be stale, but not so far
// behind zfs.state that it vanished (rules.Vanished) — is not asked while
// that state is current: unmeasured, no state of its own, no part in the
// roll-up. The documented rule "zfs.state is not ONLINE and not EXPORTED"
// leaves the host ok on EXPORTED and fires on MISSING. Once the pool
// reports ONLINE again, the same capacity reading is judged again: stale,
// so unknown. A zfs.state carried by an error is no state, so it asks for
// everything as before.
func TestAnAbsentPoolsOtherMetricsAreNotAsked(t *testing.T) {
	for _, tc := range []struct {
		name         string
		label        *string
		errText      string
		wantAsked    bool
		wantStateVal rules.State
		wantHost     rules.State
	}{
		{name: "exported", label: ptr("EXPORTED"), wantAsked: false, wantStateVal: rules.StateOK, wantHost: rules.StateOK},
		{name: "missing", label: ptr("MISSING"), wantAsked: false, wantStateVal: rules.StateCrit, wantHost: rules.StateCrit},
		{name: "online again", label: ptr("ONLINE"), wantAsked: true, wantStateVal: rules.StateOK, wantHost: rules.StateUnknown},
		{name: "state is an error", errText: "zpool list failed", wantAsked: true, wantStateVal: rules.StateUnknown, wantHost: rules.StateUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := testdb.Open(t)
			org := seedOrg(t, d, "absent-pool")
			host := seedHost(t, d, org, "h")
			now := time.Now().UTC().Truncate(time.Microsecond)

			state := rules.Tuple{CheckKey: "zfs", Item: "backup", Metric: "zfs.state"}
			capacity := rules.Tuple{CheckKey: "zfs", Item: "backup", Metric: "zfs.capacity_pct"}
			insertCurrent(t, d, org, host, state, nil, tc.label, now.Add(-10*time.Second), tc.errText)
			// Stale against the clock (more than three one-minute
			// intervals), not vanished behind zfs.state (less than three).
			insertCurrent(t, d, org, host, capacity, ptr(91.0), nil, now.Add(-3*time.Minute-5*time.Second), "")
			insertCheckState(t, d, org, host, stateRow{tuple: capacity, state: rules.StateOK, since: now.Add(-time.Hour), evaluatedAt: now.Add(-4 * time.Minute)})

			rule := hostRule(org, host)
			rule.Name, rule.CheckKey, rule.Metric, rule.Severity, rule.For = "pool not healthy", "zfs", "zfs.state", rules.SeverityCrit, 0
			rule.Trigger = rules.Node{Kind: rules.KindAnd, Args: []rules.Node{
				label("zfs.state", "!=", "ONLINE"), label("zfs.state", "!=", "EXPORTED"),
			}}
			rule.Source = `zfs.state != "ONLINE" and zfs.state != "EXPORTED"`
			if err := rules.NewSQLStore(d).Create(context.Background(), rule); err != nil {
				t.Fatal(err)
			}
			markDirty(t, d, org, host)

			checks := fixedChecks{
				intervals: map[string]time.Duration{"zfs": time.Minute},
				asksMetric: map[string]func(map[string]string, string) bool{
					"zfs": func(labels map[string]string, metric string) bool {
						return checkplugins.AsksForMetric("zfs", nil, labels, metric)
					},
				},
			}
			sink := &fakeSink{}
			tk := rules.NewTicker(rules.NewEvalSQLStore(d, checks, items.NewSQLStore(d), testAgentWindow, discard()), sink,
				rules.WithTickerClock(func() time.Time { return now }), rules.WithTickerLogger(discard()))
			if n, err := tk.Tick(context.Background()); err != nil || n != 1 {
				t.Fatalf("Tick = %d, %v; want one host evaluated", n, err)
			}
			if len(sink.calls) != 1 {
				t.Fatalf("sink called %d times, want once", len(sink.calls))
			}
			got := sink.calls[0]

			unmeasured := len(got.Unmeasured) == 1 && got.Unmeasured[0] == capacity
			if unmeasured == tc.wantAsked || (tc.wantAsked && len(got.Unmeasured) != 0) {
				t.Fatalf("unmeasured = %+v, want capacity asked = %v", got.Unmeasured, tc.wantAsked)
			}
			byTuple := map[rules.Tuple]rules.State{}
			for _, st := range got.States {
				byTuple[st.Tuple] = st.State
			}
			if s, judged := byTuple[capacity]; judged != tc.wantAsked || (judged && s != rules.StateUnknown) {
				t.Fatalf("states = %+v, want capacity judged = %v (unknown, stale)", got.States, tc.wantAsked)
			}
			if byTuple[state] != tc.wantStateVal {
				t.Fatalf("zfs.state = %q, want %q (states %+v)", byTuple[state], tc.wantStateVal, got.States)
			}
			if got.Host.State != tc.wantHost {
				t.Fatalf("roll-up = %+v, want %s", got.Host, tc.wantHost)
			}
			if !tc.wantAsked && got.Host.Unknown != 0 {
				t.Fatalf("roll-up = %+v, want nothing unknown", got.Host)
			}
		})
	}
}
