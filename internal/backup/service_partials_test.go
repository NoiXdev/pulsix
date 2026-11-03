package backup_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/backup"
)

// watchCleanups makes the harness storage note, at every CleanPartials call,
// whether the backup lock is held and how many objects it holds already, so a
// test can tell the cleanup ran under the lock and before the upload.
func watchCleanups(h *harness) {
	h.storage.onClean = func() string {
		h.store.mu.Lock()
		locked := h.store.locked
		h.store.mu.Unlock()
		return fmt.Sprintf("locked=%t objects=%d", locked, len(h.storage.objects))
	}
}

func cleanupNotes(h *harness) []string {
	h.storage.mu.Lock()
	defer h.storage.mu.Unlock()
	return append([]string(nil), h.storage.cleanups...)
}

func TestScheduledBackupCleansPartialsFirstUnderTheLock(t *testing.T) {
	h := newHarness(t)
	watchCleanups(h)
	h.storage.cleaned = 3
	h.enableSchedule(t, "03:00", backup.Retention{Daily: 7})
	h.clock.Set(time.Date(2026, 11, 2, 3, 0, 0, 0, time.UTC))

	if err := h.svc.RunDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	if notes := cleanupNotes(h); len(notes) != 1 || notes[0] != "locked=true objects=0" {
		t.Fatalf("cleanups = %q, want one, under the lock, before the upload", notes)
	}
	if a := h.store.allAttempts(); len(a) != 1 || a[0].State != backup.StateSucceeded {
		t.Fatalf("attempts = %+v, want one succeeded", a)
	}
	if logs := h.logs.String(); !strings.Contains(logs, "count=3") || !strings.Contains(logs, "partial") {
		t.Errorf("the removed count was not logged:\n%s", logs)
	}
}

func TestManualBackupCleansPartialsFirstUnderTheLock(t *testing.T) {
	h := newHarness(t)
	watchCleanups(h)

	if _, err := h.svc.StartManual(context.Background(), uuid.New()); err != nil {
		t.Fatal(err)
	}
	waitIdle(h)
	if notes := cleanupNotes(h); len(notes) != 1 || notes[0] != "locked=true objects=0" {
		t.Fatalf("cleanups = %q, want one, under the lock, before the upload", notes)
	}
	if logs := h.logs.String(); strings.Contains(logs, "partial") {
		t.Errorf("nothing was removed, yet the log mentions partial files:\n%s", logs)
	}
}

func TestBusyBackupDoesNotCleanPartials(t *testing.T) {
	h := newHarness(t)
	h.store.lockedElsewhere = true
	if _, err := h.svc.StartManual(context.Background(), uuid.New()); !errors.Is(err, backup.ErrBusy) {
		t.Fatalf("StartManual = %v, want ErrBusy", err)
	}
	if notes := cleanupNotes(h); len(notes) != 0 {
		t.Errorf("cleanups = %q: another backup holds the lock, its partial file is in use", notes)
	}
}

func TestPartialCleanupFailureDoesNotFailTheBackup(t *testing.T) {
	h := newHarness(t)
	h.storage.cleanErr = errors.New("folder is read-only")

	e, err := h.svc.Backup(context.Background(), backup.KindManual, nil, h.storage, false)
	if err != nil || e.State != backup.StateSucceeded {
		t.Fatalf("Backup = %+v, %v; want it to succeed", e, err)
	}
	if !h.storage.has(e.Name) {
		t.Error("the backup was not stored")
	}
	if logs := h.logs.String(); !strings.Contains(logs, "folder is read-only") {
		t.Errorf("the cleanup failure was not logged:\n%s", logs)
	}
}
