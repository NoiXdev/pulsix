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

// documentedPoolRule is the zfs example rule docs/upgrades.md (and the
// user docs) give, verbatim: the rule form cannot say "not in", so it is an
// expression.
const documentedPoolRule = `zfs.state != "ONLINE" and zfs.state != "EXPORTED"`

// TestAnAbsentPoolsOtherMetricsAreNotAsked (backlog 3.74), through the real
// store and ticker with the zfs descriptor's own metric rule: a pool the
// agent reports as EXPORTED or MISSING reports zfs.state and nothing else,
// so its last zfs.capacity_pct — old enough to be stale, but not so far
// behind zfs.state that it vanished (rules.Vanished) — is not asked while
// that state is current. It is held (backlog 3.9b ruling): not judged, not
// unmeasured, its stored ok standing in the roll-up. The documented rule,
// parsed from its text (documentedPoolRule), leaves the host ok on
// EXPORTED and ONLINE and fires on MISSING. Once the pool reports ONLINE
// again, the same capacity reading is judged again: stale, so unknown. A
// zfs.state carried by an error is no state, so it asks for everything as
// before.
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

			trigger, err := rules.Parse(documentedPoolRule)
			if err != nil {
				t.Fatalf("Parse(%s): %v", documentedPoolRule, err)
			}
			if err := rules.TypeCheck(trigger); err != nil {
				t.Fatalf("TypeCheck(%s): %v", documentedPoolRule, err)
			}
			rule := hostRule(org, host)
			rule.Name, rule.CheckKey, rule.Metric, rule.Severity, rule.For = "pool not healthy", "zfs", "zfs.state", rules.SeverityCrit, 0
			rule.Trigger, rule.Source = trigger, documentedPoolRule
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

			if len(got.Unmeasured) != 0 {
				t.Fatalf("unmeasured = %+v, want none: a held capacity is not unmeasured", got.Unmeasured)
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

// TestASleepingDisksOtherMetricsAreNotAsked (backlog 3.9b), through the
// real store and ticker with the smart descriptor's own metric rule: a
// disk the agent reports as smart.power_mode standby is left asleep and
// reports nothing else, so its last smart.temperature_celsius — stale
// against the clock, not vanished behind smart.power_mode — is held while
// the disk sleeps: not judged, not unmeasured, its stored row untouched
// and its stored warn standing in the roll-up. Once the disk reports
// active again, or with an older agent that reports no power mode at all,
// the same reading is judged again: stale, so unknown.
func TestASleepingDisksOtherMetricsAreNotAsked(t *testing.T) {
	for _, tc := range []struct {
		name      string
		mode      *string
		wantAsked bool
		wantHost  rules.State
	}{
		{name: "standby", mode: ptr(checkplugins.SmartPowerModeStandby), wantAsked: false, wantHost: rules.StateWarn},
		{name: "active again", mode: ptr(checkplugins.SmartPowerModeActive), wantAsked: true, wantHost: rules.StateUnknown},
		{name: "older agent without a power mode", wantAsked: true, wantHost: rules.StateUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := testdb.Open(t)
			org := seedOrg(t, d, "sleeping-disk")
			host := seedHost(t, d, org, "h")
			now := time.Now().UTC().Truncate(time.Microsecond)

			mode := rules.Tuple{CheckKey: "smart", Item: "/dev/sdd", Metric: "smart.power_mode"}
			passed := rules.Tuple{CheckKey: "smart", Item: "/dev/sdd", Metric: "smart.passed"}
			temp := rules.Tuple{CheckKey: "smart", Item: "/dev/sdd", Metric: "smart.temperature_celsius"}
			if tc.mode != nil {
				insertCurrent(t, d, org, host, mode, nil, tc.mode, now.Add(-10*time.Second), "")
			} else {
				// An older agent's disk: a fresh health reading keeps the
				// item reporting, the temperature trails it.
				insertCurrent(t, d, org, host, passed, ptr(1.0), nil, now.Add(-10*time.Second), "")
			}
			// Stale against the clock (more than three one-minute
			// intervals), not vanished behind the fresh reading (less
			// than three).
			insertCurrent(t, d, org, host, temp, ptr(34.0), nil, now.Add(-3*time.Minute-5*time.Second), "")
			insertCheckState(t, d, org, host, stateRow{tuple: temp, state: rules.StateWarn, since: now.Add(-time.Hour), evaluatedAt: now.Add(-4 * time.Minute)})
			markDirty(t, d, org, host)

			checks := fixedChecks{
				intervals: map[string]time.Duration{"smart": time.Minute},
				asksMetric: map[string]func(map[string]string, string) bool{
					"smart": func(labels map[string]string, metric string) bool {
						return checkplugins.AsksForMetric("smart", nil, labels, metric)
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

			if len(got.Unmeasured) != 0 {
				t.Fatalf("unmeasured = %+v, want none: a held temperature is not unmeasured", got.Unmeasured)
			}
			byTuple := map[rules.Tuple]rules.State{}
			for _, st := range got.States {
				byTuple[st.Tuple] = st.State
			}
			if s, judged := byTuple[temp]; judged != tc.wantAsked || (judged && s != rules.StateUnknown) {
				t.Fatalf("states = %+v, want temperature judged = %v (unknown, stale)", got.States, tc.wantAsked)
			}
			if tc.mode != nil && byTuple[mode] != rules.StateOK {
				t.Fatalf("smart.power_mode = %q, want ok (states %+v)", byTuple[mode], got.States)
			}
			if got.Host.State != tc.wantHost {
				t.Fatalf("roll-up = %+v, want %s", got.Host, tc.wantHost)
			}
			if !tc.wantAsked && got.Host.Unknown != 0 {
				t.Fatalf("roll-up = %+v, want nothing unknown", got.Host)
			}
			if !tc.wantAsked {
				// Held: the stored row is exactly as it was, not re-stamped
				// and not marked unmeasured.
				var evaluatedAt time.Time
				var unmeasuredAt *time.Time
				var stored string
				if err := d.Pool().QueryRow(context.Background(),
					`SELECT state, evaluated_at, unmeasured_at FROM check_states WHERE host_id = $1 AND metric = $2`,
					host, temp.Metric).Scan(&stored, &evaluatedAt, &unmeasuredAt); err != nil {
					t.Fatal(err)
				}
				if stored != string(rules.StateWarn) || !evaluatedAt.Equal(now.Add(-4*time.Minute)) || unmeasuredAt != nil {
					t.Fatalf("held row = %s evaluated %v unmeasured %v, want the stored warn untouched", stored, evaluatedAt, unmeasuredAt)
				}
			}
		})
	}
}
