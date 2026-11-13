package checks

import (
	"context"
	"math"
	"testing"
	"time"
)

// TestSchedulerTurnsNonFiniteValuesIntoErrors (backlog 3.11c): a plugin bug
// that yields ±Inf or NaN must not reach the manager as a value — the
// manager would store it and the host's checks could no longer be encoded
// as JSON. The scheduler reports such a value as an error on the same item
// and metric, so the bug stays visible, and every finite value of the
// batch still goes out.
func TestSchedulerTurnsNonFiniteValuesIntoErrors(t *testing.T) {
	plugin := &fakePlugin{obs: []Observation{
		{Item: "", Metric: "x.good", Value: floatPtr(1.5)},
		{Item: "a", Metric: "x.v", Value: floatPtr(math.Inf(1))},
		{Item: "b", Metric: "x.v", Value: floatPtr(math.Inf(-1))},
		{Item: "c", Metric: "x.v", Value: floatPtr(math.NaN())},
		{Item: "", Metric: "x.state", Label: strPtr("running")},
	}}
	ticks := &fakeTickers{}
	sched := NewScheduler(map[string]Plugin{"x": plugin}, time.Now, ticks.factory())
	sched.Apply(Config{Version: "v1", Checks: []Spec{{Key: "x", Plugin: "x", Interval: time.Minute}}})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sink := &fakeSink{}
	done := make(chan error, 1)
	go func() { done <- sched.Run(ctx, sink.collect) }()

	_, tickers := ticks.recorded()
	tickers[0].c <- time.Now()
	waitForSchedulerTest(t, time.Second, func() bool { return len(sink.all()) == 1 })

	batch := sink.all()[0]
	if len(batch) != 5 {
		t.Fatalf("batch = %+v, want all 5 observations", batch)
	}
	if good := findResult(t, batch, "", "x.good"); good.Value == nil || *good.Value != 1.5 || good.Err != "" {
		t.Errorf("finite value = %+v, want 1.5 unchanged", good)
	}
	if state := findResult(t, batch, "", "x.state"); state.Label == nil || *state.Label != "running" {
		t.Errorf("label = %+v, want it unchanged", state)
	}
	for _, item := range []string{"a", "b", "c"} {
		r := findResult(t, batch, item, "x.v")
		if r.Value != nil || r.Label != nil || r.Err != "value is not a finite number" {
			t.Errorf("item %q = %+v, want only the error %q", item, r, "value is not a finite number")
		}
	}

	cancel()
	if err := <-done; err != nil {
		t.Errorf("done: %v", err)
	}
}

func strPtr(s string) *string { return &s }
