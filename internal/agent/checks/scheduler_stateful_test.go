package checks

import (
	"context"
	"sync"
	"testing"
	"time"
)

// fakeStatefulPlugin is a prototype that hands every check key its own
// fakePlugin instance and records them in hand-out order.
type fakeStatefulPlugin struct {
	mu         sync.Mutex
	instances  []*fakePlugin
	protoCalls int
}

func (p *fakeStatefulPlugin) Name() string { return "stateful" }

func (p *fakeStatefulPlugin) Collect(context.Context, map[string]any) ([]Observation, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.protoCalls++
	return []Observation{{Metric: "m", Value: floatPtr(1)}}, nil
}

func (p *fakeStatefulPlugin) NewInstance() Plugin {
	p.mu.Lock()
	defer p.mu.Unlock()
	fp := &fakePlugin{obs: []Observation{{Metric: "m", Value: floatPtr(1)}}}
	p.instances = append(p.instances, fp)
	return fp
}

func (p *fakeStatefulPlugin) handedOut() ([]*fakePlugin, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]*fakePlugin(nil), p.instances...), p.protoCalls
}

// A stateful plugin gets one instance per check key; an unchanged key keeps
// its instance across a re-apply, and a key whose definition changed gets a
// fresh one (fresh baselines), never the prototype itself.
func TestSchedulerStatefulPluginInstancePerKeyAndPerDefinition(t *testing.T) {
	proto := &fakeStatefulPlugin{}
	ticks := &fakeTickers{}
	sched := NewScheduler(map[string]Plugin{"stateful": proto}, time.Now, ticks.factory())
	cfg := func(version string, bInterval time.Duration) Config {
		return Config{Version: version, Checks: []Spec{
			{Key: "a", Plugin: "stateful", Interval: time.Minute},
			{Key: "b", Plugin: "stateful", Interval: bInterval},
		}}
	}
	sched.Apply(cfg("v1", time.Minute))
	if inst, _ := proto.handedOut(); len(inst) != 2 {
		t.Fatalf("instances after the first apply = %d, want 2 (one per key)", len(inst))
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sink := &fakeSink{}
	done := make(chan error, 1)
	go func() { done <- sched.Run(ctx, sink.collect) }()
	_, tickers := ticks.recorded()
	tickers[0].c <- time.Now()
	tickers[1].c <- time.Now()
	waitForSchedulerTest(t, time.Second, func() bool { return len(sink.all()) == 2 })
	inst, protoCalls := proto.handedOut()
	if inst[0].callCount() != 1 || inst[1].callCount() != 1 || protoCalls != 0 {
		t.Fatalf("calls a=%d b=%d prototype=%d, want 1, 1, 0", inst[0].callCount(), inst[1].callCount(), protoCalls)
	}

	sched.Apply(cfg("v1", time.Minute)) // unchanged
	if inst, _ := proto.handedOut(); len(inst) != 2 {
		t.Fatalf("instances after an unchanged re-apply = %d, want still 2", len(inst))
	}

	sched.Apply(cfg("v2", 2*time.Minute)) // b's definition changed
	inst, _ = proto.handedOut()
	if len(inst) != 3 {
		t.Fatalf("instances after b changed = %d, want 3 (a fresh one for b)", len(inst))
	}
	_, tickers = ticks.recorded()
	tickers[2].c <- time.Now()
	waitForSchedulerTest(t, time.Second, func() bool { return len(sink.all()) == 3 })
	if inst[2].callCount() != 1 || inst[1].callCount() != 1 {
		t.Fatalf("calls old b=%d new b=%d, want 1, 1", inst[1].callCount(), inst[2].callCount())
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run = %v", err)
	}
}
