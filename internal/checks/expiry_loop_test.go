package checks_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/checks"
)

type fakeForgetter struct {
	mu      sync.Mutex
	calls   int
	period  time.Duration
	limit   int
	counts  map[uuid.UUID]int
	err     error
	panicIt bool
	// deadline is the context deadline the last call saw.
	deadline    time.Time
	hadDeadline bool
	called      chan struct{}

	// The metric half of a round (backlog 3.88).
	metricCalls       int
	metricPeriod      time.Duration
	metricLimit       int
	metricCounts      map[uuid.UUID]int
	metricErr         error
	metricDeadline    time.Time
	metricHadDeadline bool
}

func (f *fakeForgetter) ForgetExpiredMetrics(ctx context.Context, period time.Duration, limit int) (map[uuid.UUID]int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.metricCalls++
	f.metricPeriod, f.metricLimit = period, limit
	f.metricDeadline, f.metricHadDeadline = ctx.Deadline()
	return f.metricCounts, f.metricErr
}

func (f *fakeForgetter) ForgetExpired(ctx context.Context, period time.Duration, limit int) (map[uuid.UUID]int, error) {
	f.mu.Lock()
	f.calls++
	f.period, f.limit = period, limit
	f.deadline, f.hadDeadline = ctx.Deadline()
	f.mu.Unlock()
	if f.called != nil {
		select {
		case f.called <- struct{}{}:
		default:
		}
	}
	if f.panicIt {
		panic("boom")
	}
	return f.counts, f.err
}

type fakeLocker struct {
	got      bool
	err      error
	tries    int
	unlocked int
}

func (l *fakeLocker) TryLock(context.Context) (bool, error) { l.tries++; return l.got, l.err }
func (l *fakeLocker) Unlock(context.Context)                { l.unlocked++ }

func newLoop(f checks.ExpiryForgetter, l checks.ExpiryLocker, period time.Duration, buf *bytes.Buffer) *checks.ExpiryLoop {
	log := slog.New(slog.NewTextHandler(buf, nil))
	return checks.NewExpiryLoop(f, l, period, time.Millisecond, log)
}

func TestExpiryRoundForgetsOnceWithFullLimitAndDeadline(t *testing.T) {
	org, metricOrg := uuid.New(), uuid.New()
	f := &fakeForgetter{counts: map[uuid.UUID]int{org: 3}, metricCounts: map[uuid.UUID]int{metricOrg: 4}}
	l := &fakeLocker{got: true}
	var buf bytes.Buffer
	newLoop(f, l, 168*time.Hour, &buf).Round(context.Background())

	if f.calls != 1 || f.period != 168*time.Hour || f.limit != checks.ExpiryRoundLimit {
		t.Fatalf("calls=%d period=%v limit=%d", f.calls, f.period, f.limit)
	}
	if checks.ExpiryRoundLimit != 5000 {
		t.Fatalf("round limit = %d, want 5000", checks.ExpiryRoundLimit)
	}
	if !f.hadDeadline || time.Until(f.deadline) > checks.ExpiryRoundTimeout {
		t.Fatalf("round has no deadline within %v", checks.ExpiryRoundTimeout)
	}
	// The same round forgets dead metrics too (backlog 3.88), under the
	// same lock and deadline, with the same period and limit.
	if f.metricCalls != 1 || f.metricPeriod != 168*time.Hour || f.metricLimit != checks.ExpiryRoundLimit {
		t.Fatalf("metric calls=%d period=%v limit=%d", f.metricCalls, f.metricPeriod, f.metricLimit)
	}
	if !f.metricHadDeadline || !f.metricDeadline.Equal(f.deadline) {
		t.Fatalf("metric forget deadline = %v (%v), want the round's %v", f.metricDeadline, f.metricHadDeadline, f.deadline)
	}
	if l.unlocked != 1 {
		t.Fatalf("unlocked %d times, want 1", l.unlocked)
	}
	out := buf.String()
	if !strings.Contains(out, "level=INFO") || !strings.Contains(out, org.String()) || !strings.Contains(out, "3") {
		t.Fatalf("log lacks the per-org count: %s", out)
	}
	if !strings.Contains(out, metricOrg.String()) || !strings.Contains(out, "4") {
		t.Fatalf("log lacks the per-org metric count: %s", out)
	}
}

// TestExpiryRoundForgetsMetricsAfterAnItemFailure: a failing item half
// does not keep the round from forgetting dead metrics; both outcomes are
// logged as one failed round.
func TestExpiryRoundForgetsMetricsAfterAnItemFailure(t *testing.T) {
	metricOrg := uuid.New()
	f := &fakeForgetter{err: errors.New("1 of 3 failed"), metricCounts: map[uuid.UUID]int{metricOrg: 2}}
	var buf bytes.Buffer
	newLoop(f, &fakeLocker{got: true}, time.Hour, &buf).Round(context.Background())
	if f.metricCalls != 1 {
		t.Fatalf("metric calls = %d, want 1 after the item half failed", f.metricCalls)
	}
	out := buf.String()
	if !strings.Contains(out, "level=ERROR") || !strings.Contains(out, "1 of 3 failed") || !strings.Contains(out, metricOrg.String()) {
		t.Fatalf("log: %s", out)
	}
	// The message keeps its name for log continuity; failed says which
	// half of the round it was.
	if !strings.Contains(out, "failed=items ") {
		t.Fatalf("log does not name the failed half: %s", out)
	}
}

// TestExpiryRoundLogsAMetricFailure: the metric half's error makes the
// round a failed one, with the item counts still reported.
func TestExpiryRoundLogsAMetricFailure(t *testing.T) {
	org := uuid.New()
	f := &fakeForgetter{counts: map[uuid.UUID]int{org: 1}, metricErr: errors.New("metric boom")}
	var buf bytes.Buffer
	newLoop(f, &fakeLocker{got: true}, time.Hour, &buf).Round(context.Background())
	out := buf.String()
	if !strings.Contains(out, "level=ERROR") || !strings.Contains(out, "metric boom") || !strings.Contains(out, org.String()) {
		t.Fatalf("log: %s", out)
	}
	if !strings.Contains(out, "failed=metrics ") {
		t.Fatalf("log does not name the failed half: %s", out)
	}
}

// TestExpiryRoundNamesBothFailedHalves: both halves failing is one failed
// round naming both.
func TestExpiryRoundNamesBothFailedHalves(t *testing.T) {
	f := &fakeForgetter{err: errors.New("item boom"), metricErr: errors.New("metric boom")}
	var buf bytes.Buffer
	newLoop(f, &fakeLocker{got: true}, time.Hour, &buf).Round(context.Background())
	out := buf.String()
	if !strings.Contains(out, "item boom") || !strings.Contains(out, "metric boom") || !strings.Contains(out, "failed=items,metrics ") {
		t.Fatalf("log: %s", out)
	}
}

func TestExpiryRoundSkipsWithoutTheLock(t *testing.T) {
	f := &fakeForgetter{}
	l := &fakeLocker{got: false}
	var buf bytes.Buffer
	newLoop(f, l, time.Hour, &buf).Round(context.Background())
	if f.calls != 0 || f.metricCalls != 0 || l.unlocked != 0 {
		t.Fatalf("calls=%d metric calls=%d unlocked=%d, want none", f.calls, f.metricCalls, l.unlocked)
	}
}

func TestExpiryRoundLockErrorIsLoggedAndSkips(t *testing.T) {
	f := &fakeForgetter{}
	l := &fakeLocker{err: errors.New("db down")}
	var buf bytes.Buffer
	newLoop(f, l, time.Hour, &buf).Round(context.Background())
	if f.calls != 0 || !strings.Contains(buf.String(), "level=ERROR") {
		t.Fatalf("calls=%d log=%s", f.calls, buf.String())
	}
}

func TestExpiryRoundLogsAFailureAndStillReportsCounts(t *testing.T) {
	org := uuid.New()
	f := &fakeForgetter{counts: map[uuid.UUID]int{org: 2}, err: errors.New("1 of 3 failed")}
	var buf bytes.Buffer
	newLoop(f, &fakeLocker{got: true}, time.Hour, &buf).Round(context.Background())
	out := buf.String()
	if !strings.Contains(out, "level=ERROR") || !strings.Contains(out, "1 of 3 failed") || !strings.Contains(out, org.String()) {
		t.Fatalf("log: %s", out)
	}
}

func TestExpiryRoundRecoversAPanicAndUnlocks(t *testing.T) {
	f := &fakeForgetter{panicIt: true}
	l := &fakeLocker{got: true}
	var buf bytes.Buffer
	newLoop(f, l, time.Hour, &buf).Round(context.Background())
	out := buf.String()
	if !strings.Contains(out, "panic") || !strings.Contains(out, "stack") {
		t.Fatalf("panic not logged with a stack: %s", out)
	}
	if l.unlocked != 1 {
		t.Fatalf("lock not released after the panic")
	}
}

func TestExpiryLoopDisabledDoesNothing(t *testing.T) {
	f := &fakeForgetter{}
	l := &fakeLocker{got: true}
	var buf bytes.Buffer
	done := make(chan struct{})
	go func() { newLoop(f, l, 0, &buf).Run(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("a disabled loop must return at once")
	}
	if f.calls != 0 || l.tries != 0 {
		t.Fatalf("calls=%d tries=%d", f.calls, l.tries)
	}
}

func TestExpiryLoopRunsARoundPerTickUntilCancelled(t *testing.T) {
	f := &fakeForgetter{called: make(chan struct{}, 1)}
	l := &fakeLocker{got: true}
	var buf bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { newLoop(f, l, time.Hour, &buf).Run(ctx); close(done) }()
	<-f.called
	<-f.called
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}
