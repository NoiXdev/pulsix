// White-box tests for the retest scheduler's zero-value fallbacks and the
// built-in interruptible sleep.
package schedule

import (
	"context"
	"testing"
	"time"
)

func TestRetestZeroValueDefaults(t *testing.T) {
	r := NewRetest(nil, nil, nil, 0, nil, nil, nil)
	if got := r.intervalValue(); got != DefaultInterval {
		t.Fatalf("intervalValue() = %v, want %v", got, DefaultInterval)
	}
	if r.nowValue().IsZero() {
		t.Fatal("nowValue() must fall back to the wall clock")
	}
	if r.logger() == nil {
		t.Fatal("logger() must fall back to slog's default")
	}
}

// TestRetestSleepDefaultIsInterruptible drives the built-in sleep (nil
// injection): it waits and unblocks on cancellation.
func TestRetestSleepDefaultIsInterruptible(t *testing.T) {
	r := NewRetest(nil, nil, nil, 0, nil, nil, nil)
	done := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { done <- r.sleepFor(ctx, time.Hour) }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("the cancelled sleep must return the context's error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the built-in sleep did not unblock on cancellation")
	}
}

// TestRetestSleepDefaultCompletes drives the built-in sleep's happy path:
// a finite wait returns nil.
func TestRetestSleepDefaultCompletes(t *testing.T) {
	r := NewRetest(nil, nil, nil, 0, nil, nil, nil)
	if err := r.sleepFor(context.Background(), time.Millisecond); err != nil {
		t.Fatalf("sleepFor = %v", err)
	}
}
