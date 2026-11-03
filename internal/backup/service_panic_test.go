package backup_test

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/backup"
)

// TestStartManualSurvivesAPanickingStorage (final review minor 5): the
// background backup runs outside any request's recover, so a panic in the
// destination's upload must not end the manager. The attempt is recorded
// as failed, the lock is released and the next backup can start.
func TestStartManualSurvivesAPanickingStorage(t *testing.T) {
	h := newHarness(t)
	h.storage.putPanics = true
	id, err := h.svc.StartManual(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("StartManual: %v", err)
	}
	waitIdle(h)
	a := h.store.attempt(t, id)
	if a.State != backup.StateFailed || !strings.Contains(a.Error, "panicked") || a.FinishedAt == nil {
		t.Fatalf("attempt = %+v, want a finished, failed attempt naming the panic", a)
	}
	if h.store.locked {
		t.Fatal("the backup lock is still held after the panic")
	}

	h.storage.putPanics = false
	id, err = h.svc.StartManual(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("StartManual after the panic: %v", err)
	}
	waitIdle(h)
	if a := h.store.attempt(t, id); a.State != backup.StateSucceeded {
		t.Fatalf("the next backup = %+v, want succeeded", a)
	}
}

// TestStartManualSurvivesAPanicWhileFinishing: recording the end panics
// too (and again when the panic is recorded); the manager survives and
// the lock is released. The attempt stays running for the next backup to
// mark abandoned.
func TestStartManualSurvivesAPanicWhileFinishing(t *testing.T) {
	h := newHarness(t)
	h.store.mu.Lock()
	h.store.panicOn = "FinishAttempt"
	h.store.mu.Unlock()
	if _, err := h.svc.StartManual(context.Background(), uuid.New()); err != nil {
		t.Fatalf("StartManual: %v", err)
	}
	waitIdle(h)
	h.store.mu.Lock()
	locked := h.store.locked
	h.store.panicOn = ""
	h.store.mu.Unlock()
	if locked {
		t.Fatal("the backup lock is still held after the panic")
	}
}

// TestStartManualReleasesTheLockWhenRecordingPanics: a panic between
// taking the lock and starting the background backup still releases the
// lock (the panic itself reaches the caller, whose recover answers the
// request).
func TestStartManualReleasesTheLockWhenRecordingPanics(t *testing.T) {
	h := newHarness(t)
	h.store.mu.Lock()
	h.store.panicOn = "InsertAttempt"
	h.store.mu.Unlock()
	func() {
		defer func() {
			if recover() == nil {
				t.Error("the panic did not reach the caller")
			}
		}()
		_, _ = h.svc.StartManual(context.Background(), uuid.New())
	}()
	waitIdle(h) // no background backup was counted
	h.store.mu.Lock()
	locked := h.store.locked
	h.store.panicOn = ""
	h.store.mu.Unlock()
	if locked {
		t.Fatal("the backup lock leaked")
	}
	id, err := h.svc.StartManual(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("StartManual after the panic: %v", err)
	}
	waitIdle(h)
	if a := h.store.attempt(t, id); a.State != backup.StateSucceeded {
		t.Fatalf("the next backup = %+v, want succeeded", a)
	}
}
