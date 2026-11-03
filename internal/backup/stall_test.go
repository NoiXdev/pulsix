package backup_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/backup"
)

// testStallTimeout stands in for the ten minutes a backup may go without
// progress.
const testStallTimeout = 100 * time.Millisecond

// waitIdleWithin is waitIdle failing the test when the background backup
// has not finished within d. The harness's Stop then cancels it.
func waitIdleWithin(t *testing.T, h *harness, d time.Duration) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		waitIdle(h)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("the backup still runs after %v", d)
	}
}

// startStalled starts a manual backup under the shortened stall timeout and
// waits for it; it returns the recorded attempt.
func startStalled(t *testing.T, h *harness) backup.Attempt {
	t.Helper()
	backup.SetStallTimeout(h.svc, testStallTimeout)
	id, err := h.svc.StartManual(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("StartManual: %v", err)
	}
	waitIdleWithin(t, h, 10*time.Second)
	return h.store.attempt(t, id)
}

// assertStalledAndFree checks that a is recorded failed as stalled, that
// nothing was stored, and that the lock is free again: the next backup
// starts and succeeds.
func assertStalledAndFree(t *testing.T, h *harness, a backup.Attempt) {
	t.Helper()
	want := "stalled: no progress for 100ms"
	if a.State != backup.StateFailed || a.Error != want || a.FinishedAt == nil {
		t.Fatalf("attempt = %+v, want a finished, failed attempt with error %q", a, want)
	}
	if h.storage.has(a.ObjectName) {
		t.Errorf("the stalled backup %s was stored", a.ObjectName)
	}
	h.store.mu.Lock()
	locked := h.store.locked
	h.store.mu.Unlock()
	if locked {
		t.Fatal("the backup lock is still held after the stall")
	}

	h.storage.stallAfter = 0
	h.dump.mu.Lock()
	h.dump.block = false
	h.dump.mu.Unlock()
	id, err := h.svc.StartManual(context.Background(), uuid.New())
	if errors.Is(err, backup.ErrBusy) {
		t.Fatal("the next backup is refused as busy after the stall")
	}
	if err != nil {
		t.Fatalf("StartManual after the stall: %v", err)
	}
	waitIdleWithin(t, h, 10*time.Second)
	if next := h.store.attempt(t, id); next.State != backup.StateSucceeded {
		t.Fatalf("the next backup = %+v, want succeeded", next)
	}
}

func TestStallTimeoutDefaultsToTenMinutes(t *testing.T) {
	h := newHarness(t)
	if got := backup.StallTimeoutOf(h.svc); got != 10*time.Minute {
		t.Fatalf("stall timeout = %v, want 10m0s", got)
	}
}

// TestBackupToAHangingDestinationIsCancelled (backlog 7.48.1): the
// destination stops reading (a dead network mount, a stuck upload); the
// dump then blocks writing into the pipe. No byte reaches the destination
// for the stall timeout, so the run is cancelled and recorded failed.
func TestBackupToAHangingDestinationIsCancelled(t *testing.T) {
	h := newHarness(t)
	h.storage.stallAfter = 8
	a := startStalled(t, h)
	assertStalledAndFree(t, h, a)
}

// TestBackupWithAStuckDumpIsCancelled: the dump stops producing after its
// header, so the destination waits in Read for bytes that never come.
func TestBackupWithAStuckDumpIsCancelled(t *testing.T) {
	h := newHarness(t)
	h.dump.mu.Lock()
	h.dump.block = true
	h.dump.mu.Unlock()
	a := startStalled(t, h)
	assertStalledAndFree(t, h, a)
}

// TestSlowButSteadyBackupIsNotCancelled (review focus 5): a destination
// that keeps taking bytes slowly runs well past the stall timeout in total
// and still completes, since every read is progress.
func TestSlowButSteadyBackupIsNotCancelled(t *testing.T) {
	h := newHarness(t)
	h.storage.slowRead, h.storage.slowChunk = testStallTimeout/10, 4
	backup.SetStallTimeout(h.svc, testStallTimeout)
	start := time.Now()
	id, err := h.svc.StartManual(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("StartManual: %v", err)
	}
	waitIdleWithin(t, h, 30*time.Second)
	if elapsed := time.Since(start); elapsed < 3*testStallTimeout {
		t.Fatalf("the backup took %v, under three stall timeouts: the fixture proves nothing", elapsed)
	}
	a := h.store.attempt(t, id)
	if a.State != backup.StateSucceeded || a.Error != "" {
		t.Fatalf("attempt = %+v, want succeeded", a)
	}
	if !h.storage.has(a.ObjectName) {
		t.Errorf("object %s not stored", a.ObjectName)
	}
}
