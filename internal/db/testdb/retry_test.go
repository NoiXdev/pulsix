package testdb

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

var errReaper = fmt.Errorf("generic container: create container: wait for reaper: %w",
	errors.New(`unexpected container status "removing"`))

func TestStartWithRetryRetriesReaperRemoving(t *testing.T) {
	calls := 0
	var sleeps []time.Duration
	var logs int
	err := startWithRetry(func() error {
		calls++
		if calls < 3 {
			return errReaper
		}
		return nil
	}, func(d time.Duration) { sleeps = append(sleeps, d) }, func(string, ...any) { logs++ })
	if err != nil {
		t.Fatalf("expected success on the third attempt, got %v", err)
	}
	if calls != 3 || len(sleeps) != 2 || logs != 2 {
		t.Errorf("calls=%d sleeps=%d logs=%d, want 3, 2, 2", calls, len(sleeps), logs)
	}
}

func TestStartWithRetryGivesUpAfterThreeAttempts(t *testing.T) {
	calls := 0
	err := startWithRetry(func() error { calls++; return errReaper },
		func(time.Duration) {}, func(string, ...any) {})
	if !errors.Is(err, errReaper) && err == nil {
		t.Fatal("expected the reaper error after exhausting attempts")
	}
	if calls != 3 {
		t.Errorf("calls=%d, want 3", calls)
	}
}

func TestStartWithRetryFailsAtOnceOnOtherErrors(t *testing.T) {
	calls := 0
	boom := errors.New("pull access denied")
	err := startWithRetry(func() error { calls++; return boom },
		func(time.Duration) { t.Error("must not back off") }, func(string, ...any) {})
	if !errors.Is(err, boom) || calls != 1 {
		t.Errorf("err=%v calls=%d, want boom after 1 call", err, calls)
	}
}

func TestIsReaperRemoving(t *testing.T) {
	if !isReaperRemoving(errReaper) {
		t.Error("reaper removing error not recognised")
	}
	if isReaperRemoving(errors.New("wait for reaper: connection refused")) {
		t.Error("other reaper errors must not retry")
	}
	if isReaperRemoving(nil) {
		t.Error("nil must not retry")
	}
}
