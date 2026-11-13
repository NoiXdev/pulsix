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
	return newAdvisoryLog(slog.New(slog.NewTextHandler(buf, nil)), clk.now), clk, buf
}

func TestAdvisoryLog(t *testing.T) {
	a, clk, buf := newTestAdvisory()
	e := errors.New("two names refused")

	a.note("k1", "postgres", e)
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
	a.note("k1", "postgres", errors.New("two names refused"))
	if buf.lines() != 1 {
		t.Fatalf("same text inside 1h must stay quiet: %q", buf.String())
	}

	clk.add(time.Minute)
	a.note("k1", "postgres", e)
	if buf.lines() != 2 {
		t.Fatalf("after 1h want a second line: %q", buf.String())
	}

	a.note("k1", "postgres", errors.New("three names refused"))
	if buf.lines() != 3 {
		t.Fatalf("changed text must be logged at once: %q", buf.String())
	}

	a.note("k2", "postgres", e)
	if buf.lines() != 4 {
		t.Fatalf("state is per key: %q", buf.String())
	}

	a.note("k1", "postgres", nil) // clears, no line
	if buf.lines() != 4 {
		t.Fatalf("nil must not log: %q", buf.String())
	}
	a.note("k1", "postgres", errors.New("three names refused"))
	if buf.lines() != 5 {
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
	for _, want := range []string{"level=WARN", "check=db", "plugin=adv", `advisory="two names refused"`} {
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
