package checks

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func (s *syncBuf) lines() int {
	out := strings.TrimSpace(s.String())
	if out == "" {
		return 0
	}
	return strings.Count(out, "\n") + 1
}

func newTestAdvisory() (*advisoryLog, *fakeClock, *syncBuf) {
	buf := &syncBuf{}
	clk := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	return newAdvisoryLog(slog.New(slog.NewTextHandler(buf, nil)), clk.now, "k1", "postgres"), clk, buf
}

func TestAdvisoryLog(t *testing.T) {
	a, clk, buf := newTestAdvisory()
	e := errors.New("two names refused")

	a.note(e)
	if buf.lines() != 1 {
		t.Fatalf("first: want 1 line, got %q", buf.String())
	}
	out := buf.String()
	for _, want := range []string{"level=WARN", "check=k1", "plugin=postgres", `advisory="two names refused"`} {
		if !strings.Contains(out, want) {
			t.Errorf("line lacks %q: %s", want, out)
		}
	}

	clk.add(59 * time.Minute)
	a.note(errors.New("two names refused"))
	if buf.lines() != 1 {
		t.Fatalf("same text inside 1h must stay quiet: %q", buf.String())
	}

	clk.add(time.Minute)
	a.note(e)
	if buf.lines() != 2 {
		t.Fatalf("after 1h want a second line: %q", buf.String())
	}

	a.note(errors.New("three names refused"))
	if buf.lines() != 3 {
		t.Fatalf("changed text must be logged at once: %q", buf.String())
	}

	a.note(nil) // clears, no line
	if buf.lines() != 3 {
		t.Fatalf("nil must not log: %q", buf.String())
	}
	a.note(errors.New("three names refused"))
	if buf.lines() != 4 {
		t.Fatalf("after a clear the same text logs again: %q", buf.String())
	}
}

type advisoryPlugin struct{ err error }

func (advisoryPlugin) Name() string { return "adv" }
func (p advisoryPlugin) Collect(context.Context, map[string]any) ([]Observation, error) {
	return []Observation{{Metric: "adv.up", Value: floatPtr(1)}}, p.err
}

func TestSchedulerLogsAdvisoryOfPlainPlugin(t *testing.T) {
	buf := &syncBuf{}
	tick := secretRun(t, advisoryPlugin{err: errors.New("two names refused")}, nil,
		WithLogger(slog.New(slog.NewTextHandler(buf, nil))))
	tick()
	tick()
	if buf.lines() != 1 {
		t.Fatalf("want exactly one warn line over two rounds, got %q", buf.String())
	}
	for _, want := range []string{"level=WARN", "check=db", "plugin=db", `advisory="two names refused"`} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("line lacks %q: %s", want, buf.String())
		}
	}
}

func TestSchedulerNeverLogsASecretPluginsAdvisory(t *testing.T) {
	buf := &syncBuf{}
	plugin := &fakeSecretPlugin{} // its advisory error echoes the params
	tick := secretRun(t, plugin, map[string]any{"note": secretValue},
		WithLogger(slog.New(slog.NewTextHandler(buf, nil))))
	tick()
	if calls := func() int { _, c, _, _ := plugin.seen(); return c }(); calls != 1 {
		t.Fatalf("plugin called %d times", calls)
	}
	if buf.String() != "" {
		t.Fatalf("a secret plugin's advisory was logged: %q", buf.String())
	}
}

// runAdvisory starts a scheduler with one plain plugin that always returns
// the advisory text and gives the test the pieces to drive it.
func runAdvisory(t *testing.T) (s *Scheduler, ticks *fakeTickers, sink *fakeSink, buf *syncBuf) {
	t.Helper()
	buf = &syncBuf{}
	ticks = &fakeTickers{}
	clk := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	s = NewScheduler(map[string]Plugin{"adv": advisoryPlugin{err: errors.New("same text")}}, clk.now, ticks.factory(),
		WithLogger(slog.New(slog.NewTextHandler(buf, nil))))
	sink = &fakeSink{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx, sink.collect) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Run = %v", err)
		}
	})
	return s, ticks, sink, buf
}

func fireAndWait(t *testing.T, ticks *fakeTickers, sink *fakeSink, idx int) {
	t.Helper()
	before := len(sink.all())
	_, tickers := ticks.recorded()
	tickers[idx].c <- time.Now()
	waitForSchedulerTest(t, 2*time.Second, func() bool { return len(sink.all()) > before })
}

// A check that is reconfigured or removed and added again starts with no
// advisory state: the same text is logged again at once, however recent.
func TestAdvisoryStateDiesWithItsCheck(t *testing.T) {
	s, ticks, sink, buf := runAdvisory(t)
	spec := Spec{Key: "k", Plugin: "adv", Interval: time.Minute}

	s.Apply(Config{Version: "v1", Checks: []Spec{spec}})
	waitForSchedulerTest(t, 2*time.Second, func() bool { n, _ := ticks.recorded(); return len(n) == 1 })
	fireAndWait(t, ticks, sink, 0)
	if buf.lines() != 1 {
		t.Fatalf("first round: %q", buf.String())
	}

	spec.Interval = 2 * time.Minute // reconfigured: a new instance
	s.Apply(Config{Version: "v2", Checks: []Spec{spec}})
	waitForSchedulerTest(t, 2*time.Second, func() bool { n, _ := ticks.recorded(); return len(n) == 2 })
	fireAndWait(t, ticks, sink, 1)
	if buf.lines() != 2 {
		t.Fatalf("after a reconfigure the text must be logged again: %q", buf.String())
	}

	s.Apply(Config{Version: "v3"}) // removed
	s.Apply(Config{Version: "v4", Checks: []Spec{spec}})
	waitForSchedulerTest(t, 2*time.Second, func() bool { n, _ := ticks.recorded(); return len(n) == 3 })
	fireAndWait(t, ticks, sink, 2)
	if buf.lines() != 3 {
		t.Fatalf("after remove and add the text must be logged again: %q", buf.String())
	}
}

// cancelingPlugin ends the scheduler's context during Collect, as a shutdown
// does, and returns the advisory the cancelled call produces.
type cancelingPlugin struct{ cancel func() }

func (p *cancelingPlugin) Name() string { return "adv" }
func (p *cancelingPlugin) Collect(ctx context.Context, _ map[string]any) ([]Observation, error) {
	p.cancel()
	return []Observation{{Metric: "adv.up", Value: floatPtr(1)}}, context.Canceled
}

func TestNoAdvisoryLoggedOnceTheContextEnded(t *testing.T) {
	buf := &syncBuf{}
	ticks := &fakeTickers{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := NewScheduler(map[string]Plugin{"adv": &cancelingPlugin{cancel: cancel}}, time.Now, ticks.factory(),
		WithLogger(slog.New(slog.NewTextHandler(buf, nil))))
	s.Apply(Config{Version: "v1", Checks: []Spec{{Key: "k", Plugin: "adv", Interval: time.Minute}}})
	sink := &fakeSink{}
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx, sink.collect) }()
	_, tickers := ticks.recorded()
	tickers[0].c <- time.Now()
	waitForSchedulerTest(t, 2*time.Second, func() bool { return len(sink.all()) == 1 })
	cancel()
	if err := <-done; err != nil {
		t.Errorf("Run = %v", err)
	}
	if buf.String() != "" {
		t.Fatalf("shutdown noise logged: %q", buf.String())
	}
}
