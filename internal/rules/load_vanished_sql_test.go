package rules_test

import (
	"context"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/items"
	"github.com/noixdev/pulsix/internal/rules"
)

// TestLoadUsesTheVanishedRule (backlog 3.88): Load takes a vanished tuple
// out of the evaluation exactly like an item its check no longer asks for
// (interval 0), and decides "vanished" with rules.Vanished itself — every
// case is checked against both the expected answer and the function, which
// is what pins this caller to it (the host page's own test pins the other,
// TestHostChecksReportsVanishedValues in internal/api).
func TestLoadUsesTheVanishedRule(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "vanished")
	host := seedHost(t, d, org, "h")
	now := time.Now().UTC().Truncate(time.Microsecond)
	newest := now.Add(-10 * time.Second)

	type row struct {
		tuple   rules.Tuple
		at      time.Time
		errText string
		want    time.Duration
	}
	web := func(metric string) rules.Tuple { return rules.Tuple{CheckKey: "web", Metric: metric} }
	rows := []row{
		{tuple: web("http.up"), at: newest, want: time.Minute},
		{tuple: web("http.connect_ms"), at: newest.Add(-3*time.Minute + time.Second), want: time.Minute},
		{tuple: web("http.ttfb_ms"), at: newest.Add(-3 * time.Minute), want: time.Minute},
		{tuple: web("http.dns_ms"), at: newest.Add(-3*time.Minute - time.Second), want: 0},
		{tuple: web("http.tls_ms"), at: newest.Add(-30 * time.Minute), want: 0},
		// A fresh error is a reading: never vanished itself, and the
		// newest reading of its item.
		{tuple: rules.Tuple{CheckKey: "web", Item: "status", Metric: "http.json.value"}, at: now, errText: "no such path", want: time.Minute},
		{tuple: rules.Tuple{CheckKey: "web", Item: "status", Metric: "http.json.text"}, at: now.Add(-10 * time.Minute), want: 0},
		// A whole item gone quiet together has nothing newer to trail: it
		// stays measured, so it turns stale and unknown as before.
		{tuple: rules.Tuple{CheckKey: "disk", Item: "/data", Metric: "disk.used_pct"}, at: now.Add(-time.Hour), want: 5 * time.Minute},
		{tuple: rules.Tuple{CheckKey: "disk", Item: "/data", Metric: "disk.free_bytes"}, at: now.Add(-time.Hour), want: 5 * time.Minute},
		{tuple: rules.Tuple{CheckKey: "disk", Item: "/", Metric: "disk.used_pct"}, at: now, want: 5 * time.Minute},
	}
	newestOf := rules.ItemNewest{}
	for _, r := range rows {
		value := ptr(1.0)
		if r.errText != "" {
			value = nil
		}
		insertCurrent(t, d, org, host, r.tuple, value, nil, r.at, r.errText)
		newestOf.Observe(r.tuple, r.at)
	}

	checks := fixedChecks{intervals: map[string]time.Duration{"web": time.Minute, "disk": 5 * time.Minute}}
	store := rules.NewEvalSQLStore(d, checks, items.NewSQLStore(d), testAgentWindow, discard())
	load, err := store.Load(context.Background(), rules.Due{OrgID: org, HostID: host})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := map[rules.Tuple]time.Duration{}
	for _, ts := range load.Tuples {
		got[ts.Tuple] = ts.Interval
	}
	if len(got) != len(rows) {
		t.Fatalf("tuples = %v, want every current row (none deleted)", got)
	}
	for _, r := range rows {
		if got[r.tuple] != r.want {
			t.Errorf("interval of %+v = %v, want %v", r.tuple, got[r.tuple], r.want)
		}
		interval := checks.intervals[r.tuple.CheckKey]
		if vanished := newestOf.Vanished(r.tuple, r.at, interval); vanished != (got[r.tuple] == 0) {
			t.Errorf("%+v: rules.Vanished = %v but Load left interval %v", r.tuple, vanished, got[r.tuple])
		}
	}
}

// TestLoadLeavesMetricsNobodyAsksForUnmeasured (backlog 3.88): a metric the
// check's parameters no longer switch on (http.body_match once
// body_contains is removed) leaves the evaluation at once, on the
// whole-host item "" too — the item guard does not apply to metrics.
func TestLoadLeavesMetricsNobodyAsksForUnmeasured(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "asks-metric")
	host := seedHost(t, d, org, "h")
	now := time.Now().UTC().Truncate(time.Microsecond)

	up := rules.Tuple{CheckKey: "web", Metric: "http.up"}
	match := rules.Tuple{CheckKey: "web", Metric: "http.body_match"}
	for _, tu := range []rules.Tuple{up, match} {
		insertCurrent(t, d, org, host, tu, ptr(1.0), nil, now.Add(-10*time.Second), "")
	}
	checks := fixedChecks{
		intervals:  map[string]time.Duration{"web": time.Minute},
		asksMetric: map[string]func(string) bool{"web": func(metric string) bool { return metric != "http.body_match" }},
	}
	store := rules.NewEvalSQLStore(d, checks, items.NewSQLStore(d), testAgentWindow, discard())
	load, err := store.Load(context.Background(), rules.Due{OrgID: org, HostID: host})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := map[rules.Tuple]time.Duration{}
	for _, ts := range load.Tuples {
		got[ts.Tuple] = ts.Interval
	}
	if len(got) != 2 || got[up] != time.Minute || got[match] != 0 {
		t.Fatalf("intervals = %v, want http.up measured and http.body_match at 0", got)
	}
}

// TestAVanishedTupleIsUnmeasuredAndLeavesTheRollUp (backlog 3.88), through
// the real store and ticker: the timing metric a down probe stopped
// reporting used to turn stale, then unknown, and keep the host unknown
// for ever. Vanished, it is reported as unmeasured — the report that
// closes its open alerts as stale — gets no new state and no part in the
// host's roll-up, while its fresh sibling is judged as before.
func TestAVanishedTupleIsUnmeasuredAndLeavesTheRollUp(t *testing.T) {
	d := testdb.Open(t)
	org := seedOrg(t, d, "vanished-rollup")
	host := seedHost(t, d, org, "h")
	now := time.Now().UTC().Truncate(time.Microsecond)

	up := rules.Tuple{CheckKey: "web", Metric: "http.up"}
	dns := rules.Tuple{CheckKey: "web", Metric: "http.dns_ms"}
	insertCurrent(t, d, org, host, up, ptr(1.0), nil, now.Add(-10*time.Second), "")
	insertCurrent(t, d, org, host, dns, ptr(12.0), nil, now.Add(-10*time.Minute), "")
	insertCheckState(t, d, org, host, stateRow{tuple: up, state: rules.StateOK, since: now.Add(-time.Hour), evaluatedAt: now.Add(-time.Minute)})
	insertCheckState(t, d, org, host, stateRow{tuple: dns, state: rules.StateUnknown, since: now.Add(-7 * time.Minute), evaluatedAt: now.Add(-time.Minute)})
	markDirty(t, d, org, host)

	checks := fixedChecks{intervals: map[string]time.Duration{"web": time.Minute}}
	store := rules.NewEvalSQLStore(d, checks, items.NewSQLStore(d), testAgentWindow, discard())
	sink := &fakeSink{}
	tk := rules.NewTicker(store, sink,
		rules.WithTickerClock(func() time.Time { return now }), rules.WithTickerLogger(discard()))
	if n, err := tk.Tick(context.Background()); err != nil || n != 1 {
		t.Fatalf("Tick = %d, %v; want one host evaluated", n, err)
	}
	if len(sink.calls) != 1 {
		t.Fatalf("sink called %d times, want once", len(sink.calls))
	}
	got := sink.calls[0]
	if len(got.Unmeasured) != 1 || got.Unmeasured[0] != dns {
		t.Fatalf("unmeasured = %+v, want only %+v", got.Unmeasured, dns)
	}
	for _, st := range got.States {
		if st.Tuple == dns {
			t.Fatalf("states = %+v, want no new state for the vanished tuple", got.States)
		}
	}
	if len(got.States) != 1 || got.States[0].Tuple != up {
		t.Fatalf("states = %+v, want http.up judged", got.States)
	}
	if got.Host.State != rules.StateOK || got.Host.Unknown != 0 {
		t.Fatalf("roll-up = %+v, want ok with nothing unknown", got.Host)
	}
}
