package checks

import (
	"context"
	"math"
	"testing"
	"time"

	managerchecks "github.com/noixdev/pulsix/internal/checks"
)

// TestSchedulerTurnsNonFiniteValuesIntoErrors (backlog 3.11c): a plugin bug
// that yields ±Inf or NaN must not reach the manager as a value — the
// manager would store it and the host's checks could no longer be encoded
// as JSON — and neither may one beyond ±1e100, which can overflow the
// manager's averages. The scheduler reports such a value as an error on the same item
// and metric, so the bug stays visible, and every finite value of the
// batch still goes out.
func TestSchedulerTurnsNonFiniteValuesIntoErrors(t *testing.T) {
	plugin := &fakePlugin{obs: []Observation{
		{Item: "", Metric: "x.good", Value: floatPtr(1.5)},
		{Item: "a", Metric: "x.v", Value: floatPtr(math.Inf(1))},
		{Item: "b", Metric: "x.v", Value: floatPtr(math.Inf(-1))},
		{Item: "c", Metric: "x.v", Value: floatPtr(math.NaN())},
		{Item: "d", Metric: "x.v", Value: floatPtr(1.5e100)},
		{Item: "e", Metric: "x.v", Value: floatPtr(-math.MaxFloat64)},
		{Item: "f", Metric: "x.v", Value: floatPtr(-1e100)},
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
	if len(batch) != 8 {
		t.Fatalf("batch = %+v, want all 8 observations", batch)
	}
	if good := findResult(t, batch, "", "x.good"); good.Value == nil || *good.Value != 1.5 || good.Err != "" {
		t.Errorf("finite value = %+v, want 1.5 unchanged", good)
	}
	if state := findResult(t, batch, "", "x.state"); state.Label == nil || *state.Label != "running" {
		t.Errorf("label = %+v, want it unchanged", state)
	}
	for item, want := range map[string]string{
		"a": "value is not a finite number", "b": "value is not a finite number", "c": "value is not a finite number",
		"d": "value is out of range", "e": "value is out of range",
	} {
		r := findResult(t, batch, item, "x.v")
		if r.Value != nil || r.Label != nil || r.Err != want {
			t.Errorf("item %q = %+v, want only the error %q", item, r, want)
		}
	}
	if edge := findResult(t, batch, "f", "x.v"); edge.Value == nil || *edge.Value != -1e100 || edge.Err != "" {
		t.Errorf("value at the bound = %+v, want -1e100 unchanged", edge)
	}

	cancel()
	if err := <-done; err != nil {
		t.Errorf("done: %v", err)
	}
}

func strPtr(s string) *string { return &s }

// TestMaxAbsValueMatchesTheManagersContract keeps the agent's copy of the
// value bound on the manager's.
func TestMaxAbsValueMatchesTheManagersContract(t *testing.T) {
	if maxAbsValue != managerchecks.MaxAbsValue {
		t.Fatalf("maxAbsValue = %g, the manager's contract says %g", maxAbsValue, managerchecks.MaxAbsValue)
	}
}

// TestValueErrorsMatchTheManagers keeps the agent's error texts on the
// manager's, which the host page shows for a value stored before the bound.
func TestValueErrorsMatchTheManagers(t *testing.T) {
	if errNotFinite != managerchecks.ValueNotFiniteText || errOutOfRange != managerchecks.ValueOutOfRangeText {
		t.Fatalf("agent texts %q, %q differ from the manager's %q, %q",
			errNotFinite, errOutOfRange, managerchecks.ValueNotFiniteText, managerchecks.ValueOutOfRangeText)
	}
}
