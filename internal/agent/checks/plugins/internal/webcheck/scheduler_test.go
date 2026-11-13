package webcheck_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/agent/checks"
	"github.com/noixdev/pulsix/internal/agent/checks/plugins/internal/webcheck"
	"github.com/noixdev/pulsix/internal/agent/checks/plugins/internal/webcheck/webchecktest"
)

// panickyPlugin is a web server plugin built on webcheck.Collect whose
// parser panics with the page in the panic value.
type panickyPlugin struct{}

func (panickyPlugin) Name() string              { return "x" }
func (panickyPlugin) SecretErrorMetric() string { return "x.up" }
func (p panickyPlugin) Collect(ctx context.Context, params map[string]any) ([]checks.Observation, error) {
	return p.CollectWithSecret(ctx, params, nil)
}

func (panickyPlugin) CollectWithSecret(ctx context.Context, params map[string]any, fields map[string]string) ([]checks.Observation, error) {
	url, _ := params["url"].(string)
	return webcheck.Collect(ctx, page, url, params, fields,
		func(b []byte) (string, error) { panic("page said " + string(b)) },
		func(string) []checks.Observation { return nil })
}

type manualTicker struct{ c chan time.Time }

func (m manualTicker) C() <-chan time.Time { return m.c }
func (manualTicker) Stop()                 {}

// TestSchedulerRecoversAPanicInsideRound runs the plugin through the real
// scheduler: the panic on Round's goroutine reaches the scheduler's
// recovery, which reports its one fixed observation and keeps running.
func TestSchedulerRecoversAPanicInsideRound(t *testing.T) {
	srv := webchecktest.NewServer(t, "page "+webchecktest.PlantPass)
	tick := manualTicker{c: make(chan time.Time)}
	sched := checks.NewScheduler(map[string]checks.Plugin{"x": panickyPlugin{}}, time.Now,
		func(time.Duration) checks.Ticker { return tick })
	sched.Apply(checks.Config{Version: "v1", Checks: []checks.Spec{{Key: "x", Plugin: "x", Params: map[string]any{"url": srv.URL}, Interval: time.Minute}}})
	var mu sync.Mutex
	var got [][]checks.Result
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		done <- sched.Run(ctx, func(r []checks.Result) {
			mu.Lock()
			defer mu.Unlock()
			got = append(got, r)
		})
	}()
	defer func() {
		cancel()
		if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("Run = %v", err)
		}
	}()
	results := func() [][]checks.Result {
		mu.Lock()
		defer mu.Unlock()
		return append([][]checks.Result(nil), got...)
	}
	for round := 1; round <= 2; round++ {
		select {
		case tick.c <- time.Now():
		case <-time.After(5 * time.Second):
			t.Fatal("the scheduler took no tick")
		}
		deadline := time.Now().Add(5 * time.Second)
		for len(results()) < round && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		all := results()
		if len(all) < round {
			t.Fatalf("round %d: no results", round)
		}
		r := all[round-1]
		if len(r) != 1 || r[0].Metric != "x.up" || r[0].Err != "x: collection failed unexpectedly" || r[0].Value != nil {
			t.Fatalf("round %d: results = %+v", round, r)
		}
	}
}
