package testdb

import (
	"errors"
	"fmt"
	"slices"
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
	if calls != 3 || logs != 2 {
		t.Errorf("calls=%d logs=%d, want 3, 2", calls, logs)
	}
	// The backoff grows: 2 s after the first attempt, 4 s after the second.
	if want := []time.Duration{2 * time.Second, 4 * time.Second}; !slices.Equal(sleeps, want) {
		t.Errorf("sleeps = %v, want %v", sleeps, want)
	}
}

func TestStartWithRetryGivesUpAfterThreeAttempts(t *testing.T) {
	calls := 0
	var sleeps []time.Duration
	err := startWithRetry(func() error { calls++; return errReaper },
		func(d time.Duration) { sleeps = append(sleeps, d) }, func(string, ...any) {})
	if !errors.Is(err, errReaper) {
		t.Fatalf("err = %v, want the reaper error after exhausting attempts", err)
	}
	if calls != 3 {
		t.Errorf("calls=%d, want 3", calls)
	}
	// No sleep after the last attempt: the error is returned at once.
	if want := []time.Duration{2 * time.Second, 4 * time.Second}; !slices.Equal(sleeps, want) {
		t.Errorf("sleeps = %v, want %v", sleeps, want)
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
