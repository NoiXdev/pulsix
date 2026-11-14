// Tests for local checks in the running agent (backlog 3.5a): the checks
// section of agent.yaml starts running before any stream, follows edits of
// the file without one, stops when the file is refused, and every
// heartbeat reports the running local checks and why others do not run.
package agent

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/agent/checks"
	"github.com/noixdev/pulsix/internal/agent/secrets"
	"github.com/noixdev/pulsix/internal/pb/agentpb"
)

// fakeCPU stands in for the cpu plugin: one value per collect.
type fakeCPU struct{}

func (fakeCPU) Name() string { return "cpu" }

func (fakeCPU) Collect(context.Context, map[string]any) ([]checks.Observation, error) {
	return []checks.Observation{checks.ValueObservation("", "cpu.usage_pct", 7)}, nil
}

// testClock is a clock the test moves forward.
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// blockingSleep waits out the backoff until shutdown, so an unreachable
// manager is retried once rather than in a hot loop.
func blockingSleep(ctx context.Context, _ time.Duration) error {
	<-ctx.Done()
	return ctx.Err()
}

// unreachableManager makes every dial fail and builds the loop with the
// fake cpu plugin.
func (lt *loopTest) unreachableManager() *Loop {
	lt.dialer.agentDialErr = errors.New("connection refused")
	opts := append(lt.options(), withRegistry(map[string]checks.Plugin{"cpu": fakeCPU{}}), WithSleep(blockingSleep))
	return NewLoop(lt.cfg, lt.dialer, lt.facts, opts...)
}

// waitForBuffered drains the loop's disconnect buffer until it has seen a
// result of key.
func waitForBuffered(t *testing.T, l *Loop, key string) []checks.Result {
	t.Helper()
	var got []checks.Result
	waitFor(t, 2*time.Second, func() bool {
		got = append(got, l.buffer.Drain()...)
		for _, r := range got {
			if r.CheckKey == key {
				return true
			}
		}
		return false
	})
	return got
}

// tickerOf is the one ticker handed out with interval d.
func tickerOf(t *testing.T, ft *fakeTickers, d time.Duration) *fakeTicker {
	t.Helper()
	intervals, tickers := ft.recorded()
	var found *fakeTicker
	for i, iv := range intervals {
		if iv == d {
			if found != nil {
				t.Fatalf("two tickers of %v in %v", d, intervals)
			}
			found = tickers[i]
		}
	}
	if found == nil {
		t.Fatalf("no ticker of %v in %v", d, intervals)
	}
	return found
}

func TestLoopRunsALocalCheckBeforeAnyStream(t *testing.T) {
	lt := newLoopTest(t)
	lt.withIdentity()
	writeSecretsFile(t, lt.cfg.SecretsPath, "checks:\n  local-cpu:\n    plugin: cpu\n    interval_seconds: 30\n")
	l := lt.unreachableManager()

	cancel, done := runLoop(t, l)
	intervals, tickers := lt.waitForTickers(1)
	if !reflect.DeepEqual(intervals, []time.Duration{30 * time.Second}) {
		t.Fatalf("tickers = %v, want the local check's 30s only", intervals)
	}
	tickers[0].c <- time.Now()
	got := waitForBuffered(t, l, "local-cpu")
	if got[0].Metric != "cpu.usage_pct" || got[0].Value == nil || *got[0].Value != 7 {
		t.Fatalf("buffered = %+v, want the fake cpu value", got)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run = %v", err)
	}
}

// TestLoopFollowsTheLocalChecksWithoutAStream: the periodic look applies an
// edit of the file, and a refused file stops the local checks.
func TestLoopFollowsTheLocalChecksWithoutAStream(t *testing.T) {
	lt := newLoopTest(t)
	lt.withIdentity()
	clock := &testClock{now: time.Now()}
	lt.now = clock.Now
	writeSecretsFile(t, lt.cfg.SecretsPath, "checks:\n  local-cpu:\n    plugin: cpu\n    interval_seconds: 30\n")
	l := lt.unreachableManager()

	cancel, done := runLoop(t, l)
	_, tickers := lt.waitForTickers(1)
	waitFor(t, 2*time.Second, func() bool { _, looks := lt.lookTicks.recorded(); return len(looks) == 1 })
	lookIntervals, looks := lt.lookTicks.recorded()
	if lookIntervals[0] != 30*time.Second {
		t.Fatalf("look interval = %v, want 30s", lookIntervals[0])
	}

	writeSecretsFile(t, lt.cfg.SecretsPath, "checks:\n  local-cpu:\n    plugin: cpu\n    interval_seconds: 45\n")
	clock.advance(time.Minute)
	looks[0].c <- time.Now()
	_, tickers2 := lt.waitForTickers(2)
	waitFor(t, 2*time.Second, func() bool { return tickers[0].stopped.Load() })
	if intervals, _ := lt.ticks.recorded(); intervals[1] != 45*time.Second {
		t.Fatalf("tickers = %v, want the edited check on 45s", intervals)
	}
	tickers2[1].c <- time.Now()
	waitForBuffered(t, l, "local-cpu")

	writeSecretsFile(t, lt.cfg.SecretsPath, "checks: [\n")
	clock.advance(time.Minute)
	looks[0].c <- time.Now()
	waitFor(t, 2*time.Second, func() bool { return tickers2[1].stopped.Load() })
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run = %v", err)
	}
	if !looks[0].stopped.Load() {
		t.Fatal("the periodic look's ticker is still running after Run returned")
	}
	if intervals, _ := lt.ticks.recorded(); len(intervals) != 2 {
		t.Fatalf("tickers = %v, want 2 (nothing started for the refused file)", intervals)
	}
}

// TestLoopHeartbeatReportsTheLocalChecks: the running local checks with
// their canonical params, then the file's problems and each key the
// manager's checks shadow; the shadowing is logged once, not per
// heartbeat.
func TestLoopHeartbeatReportsTheLocalChecks(t *testing.T) {
	lt := newLoopTest(t)
	lt.withIdentity()
	writeSecretsFile(t, lt.cfg.SecretsPath, "checks:\n"+
		"  local-cpu:\n    plugin: cpu\n    interval_seconds: 120\n"+
		"  local-bad:\n    plugin: nope\n"+
		"  local-backup:\n    plugin: script\n    interval_seconds: 300\n    params:\n      commands: [backup_age]\n      format: json\n")
	stream := newFakeStream()
	lt.dialer.agentConn = &fakeAgentConn{streams: []*fakeStream{stream}}
	l := NewLoop(lt.cfg, lt.dialer, lt.facts, append(lt.options(), withRegistry(map[string]checks.Plugin{"cpu": fakeCPU{}}))...)

	cancel, done := runLoop(t, l)
	waitFor(t, 2*time.Second, func() bool { return len(heartbeats(stream)) > 0 })
	hb := heartbeats(stream)[0]
	wantChecks := []*agentpb.LocalCheck{
		{Key: "local-backup", Plugin: "script", ParamsJson: `{"commands":["backup_age"],"format":"json"}`, IntervalSeconds: 300},
		{Key: "local-cpu", Plugin: "cpu", ParamsJson: "{}", IntervalSeconds: 120},
	}
	assertLocalChecks(t, hb.GetLocalChecks(), wantChecks)
	badLine := "line 5: unknown plugin"
	if got := hb.GetLocalCheckProblems(); !reflect.DeepEqual(got, []string{badLine}) {
		t.Fatalf("local_check_problems = %q, want [%q]", got, badLine)
	}

	// A manager definition created before local- keys were reserved.
	stream.pushManagerMessage(&agentpb.ManagerMessage{CheckConfig: &agentpb.CheckConfig{
		Version: "cfg-v1",
		Checks:  []*agentpb.CheckSpec{{Key: "local-cpu", Plugin: "cpu", ParamsJson: "{}", IntervalSeconds: 30}},
	}})
	waitFor(t, 2*time.Second, func() bool { return len(l.scheduler.Shadowed()) == 1 })
	hbTicker := tickerOf(t, lt.ticks, lt.cfg.HeartbeatInterval)
	for range 2 {
		before := len(heartbeats(stream))
		hbTicker.c <- time.Now()
		waitFor(t, 2*time.Second, func() bool { return len(heartbeats(stream)) > before })
		hb = heartbeats(stream)[before]
		assertLocalChecks(t, hb.GetLocalChecks(), wantChecks[:1])
		want := []string{badLine, `check "local-cpu": a check from the manager has the same key`}
		if got := hb.GetLocalCheckProblems(); !reflect.DeepEqual(got, want) {
			t.Fatalf("local_check_problems = %q, want %q", got, want)
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run = %v", err)
	}
	if n := strings.Count(lt.logText(), "shadowed"); n != 1 {
		t.Fatalf("shadowed logged %d times, want once:\n%s", n, lt.logText())
	}
}

// TestLoopHeartbeatAppliesAnEditedLocalCheck: with a stream up, the
// heartbeat's own look applies an edit of the file — the periodic look
// never fires here.
func TestLoopHeartbeatAppliesAnEditedLocalCheck(t *testing.T) {
	lt := newLoopTest(t)
	lt.withIdentity()
	clock := &testClock{now: time.Now()}
	lt.now = clock.Now
	writeSecretsFile(t, lt.cfg.SecretsPath, "checks:\n  local-cpu:\n    plugin: cpu\n    interval_seconds: 120\n")
	stream := newFakeStream()
	lt.dialer.agentConn = &fakeAgentConn{streams: []*fakeStream{stream}}
	l := NewLoop(lt.cfg, lt.dialer, lt.facts, append(lt.options(), withRegistry(map[string]checks.Plugin{"cpu": fakeCPU{}}))...)

	cancel, done := runLoop(t, l)
	waitFor(t, 2*time.Second, func() bool { return len(heartbeats(stream)) > 0 })
	lt.waitForTickers(4) // the local check's, then heartbeat, facts, config
	before := tickerOf(t, lt.ticks, 120*time.Second)

	writeSecretsFile(t, lt.cfg.SecretsPath, "checks:\n  local-cpu:\n    plugin: cpu\n    interval_seconds: 150\n")
	clock.advance(time.Minute)
	n := len(heartbeats(stream))
	tickerOf(t, lt.ticks, lt.cfg.HeartbeatInterval).c <- time.Now()
	waitFor(t, 2*time.Second, func() bool { return len(heartbeats(stream)) > n })
	if !before.stopped.Load() {
		t.Fatal("the local check on its old interval is still running after the heartbeat's look")
	}
	after := tickerOf(t, lt.ticks, 150*time.Second)
	assertLocalChecks(t, heartbeats(stream)[n].GetLocalChecks(),
		[]*agentpb.LocalCheck{{Key: "local-cpu", Plugin: "cpu", ParamsJson: "{}", IntervalSeconds: 150}})
	if _, looks := lt.lookTicks.recorded(); len(looks) != 1 {
		t.Fatalf("look tickers = %d, want 1", len(looks))
	}
	after.c <- time.Now()
	waitFor(t, 2*time.Second, func() bool {
		for _, m := range checkResultMessages(stream) {
			for _, r := range m.GetResults() {
				if r.GetCheckKey() == "local-cpu" {
					return true
				}
			}
		}
		return false
	})
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run = %v", err)
	}
}

func assertLocalChecks(t *testing.T, got, want []*agentpb.LocalCheck) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("local_checks = %v, want %v", got, want)
	}
	for i := range want {
		g, w := got[i], want[i]
		if g.GetKey() != w.GetKey() || g.GetPlugin() != w.GetPlugin() ||
			g.GetParamsJson() != w.GetParamsJson() || g.GetIntervalSeconds() != w.GetIntervalSeconds() {
			t.Fatalf("local_checks[%d] = %v, want %v", i, g, w)
		}
	}
}

func TestLocalCheckReport(t *testing.T) {
	cpu := secrets.LocalCheck{Key: "local-cpu", Plugin: "cpu", Interval: time.Minute, ParamsJSON: "{}"}
	pg := secrets.LocalCheck{Key: "local-pg", Plugin: "postgres", Interval: 90 * time.Second, ParamsJSON: `{"secret":"pg"}`}
	cpuRow := &agentpb.LocalCheck{Key: "local-cpu", Plugin: "cpu", ParamsJson: "{}", IntervalSeconds: 60}
	pgRow := &agentpb.LocalCheck{Key: "local-pg", Plugin: "postgres", ParamsJson: `{"secret":"pg"}`, IntervalSeconds: 90}
	many := make([]string, 40)
	for i := range many {
		many[i] = fmt.Sprintf("line %d: unknown plugin", i+1)
	}
	tests := []struct {
		name         string
		checks       []secrets.LocalCheck
		problems     []string
		shadowed     []string
		wantChecks   []*agentpb.LocalCheck
		wantProblems []string
	}{
		{name: "none"},
		{name: "all running", checks: []secrets.LocalCheck{cpu, pg}, wantChecks: []*agentpb.LocalCheck{cpuRow, pgRow}},
		{
			name: "one shadowed", checks: []secrets.LocalCheck{cpu, pg}, shadowed: []string{"local-cpu"},
			wantChecks:   []*agentpb.LocalCheck{pgRow},
			wantProblems: []string{`check "local-cpu": a check from the manager has the same key`},
		},
		{
			name: "shadowed after the file's problems", checks: []secrets.LocalCheck{cpu, pg},
			problems: []string{"line 9: unknown plugin"}, shadowed: []string{"local-cpu", "local-pg"},
			wantProblems: []string{
				"line 9: unknown plugin",
				`check "local-cpu": a check from the manager has the same key`,
				`check "local-pg": a check from the manager has the same key`,
			},
		},
		{
			name: "capped at 32", checks: []secrets.LocalCheck{cpu}, problems: many, shadowed: []string{"local-cpu"},
			wantProblems: many[:32],
		},
		{
			name: "invalid UTF-8 replaced", problems: []string{"line 2: bad \xff"},
			wantProblems: []string{"line 2: bad �"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertLocalChecks(t, runningLocalChecks(tt.checks, tt.shadowed), tt.wantChecks)
			if p := localCheckProblems(tt.problems, tt.shadowed); !reflect.DeepEqual(p, tt.wantProblems) {
				t.Fatalf("problems = %q, want %q", p, tt.wantProblems)
			}
		})
	}
}
