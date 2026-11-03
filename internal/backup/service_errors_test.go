package backup_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/backup"
)

func TestStorageFor(t *testing.T) {
	open := backup.StorageFor("/srv/backups")
	st, err := open(backup.DefaultSettings(), "")
	if err != nil || st.Describe() != "/srv/backups" {
		t.Errorf("folder = %v, %v", st, err)
	}
	in := s3Settings()
	st, err = open(in, "secret")
	if err != nil || !strings.Contains(st.Describe(), "s3://backups/pulsix") {
		t.Errorf("s3 = %v, %v", st, err)
	}
	if _, err := open(in, ""); err == nil {
		t.Error("s3 without a secret opened")
	}
	in.Destination = "ftp"
	if _, err := open(in, "x"); !errors.Is(err, backup.ErrInvalid) {
		t.Errorf("ftp = %v, want ErrInvalid", err)
	}
}

func TestTickerRunsTheScheduleUntilCancelled(t *testing.T) {
	h := newHarness(t)
	h.enableSchedule(t, "03:00", backup.Retention{Daily: 7})
	h.clock.Set(time.Date(2026, 11, 2, 3, 0, 0, 0, time.UTC))
	h.store.mu.Lock()
	h.store.panicOn = "SlotClaimed" // the first ticks panic; the ticker survives them
	h.store.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		backup.NewTicker(h.svc, 5*time.Millisecond).Run(ctx)
	}()
	time.Sleep(20 * time.Millisecond)
	h.store.mu.Lock()
	h.store.panicOn = ""
	h.store.mu.Unlock()

	deadline := time.Now().Add(5 * time.Second)
	for len(h.store.allAttempts()) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
	if a := h.store.allAttempts(); len(a) != 1 || a[0].State != backup.StateSucceeded {
		t.Errorf("attempts = %+v, want the slot's backup", a)
	}
	if backup.NewTicker(h.svc, 0) == nil {
		t.Error("NewTicker with a zero interval")
	}
}

func TestADumpThatPanicsFailsTheAttempt(t *testing.T) {
	h := newHarness(t)
	h.dump.panics = true
	e, err := h.svc.Backup(context.Background(), backup.KindManual, nil, h.storage, true)
	if err == nil || e.State != backup.StateFailed || !strings.Contains(e.Error, "panic") {
		t.Fatalf("Backup = %+v, %v; want a failed attempt naming the panic", e, err)
	}
	if h.store.locked {
		t.Error("the lock is still held")
	}
}

func TestStoreFailuresSurface(t *testing.T) {
	boom := errors.New("database gone")
	for _, tc := range []struct {
		method string
		call   func(h *harness) error
	}{
		{"LoadSetting", func(h *harness) error { _, err := h.svc.Settings(context.Background()); return err }},
		{"LoadSetting", func(h *harness) error { return h.svc.SaveSettings(context.Background(), backup.DefaultSettings(), nil) }},
		{"LoadSetting", func(h *harness) error { return h.svc.Test(context.Background(), backup.DefaultSettings(), nil) }},
		{"LoadSetting", func(h *harness) error { return h.svc.RunDue(context.Background()) }},
		{"LoadSetting", func(h *harness) error { _, _, err := h.svc.Status(context.Background()); return err }},
		{"LoadSetting", func(h *harness) error { return h.svc.Delete(context.Background(), "x") }},
		{"LoadSetting", func(h *harness) error { _, err := h.svc.StartManual(context.Background(), uuid.New()); return err }},
		{"SaveSetting", func(h *harness) error { return h.svc.SaveSettings(context.Background(), backup.DefaultSettings(), nil) }},
		{"TryLock", func(h *harness) error { return h.svc.AbandonStale(context.Background()) }},
		{"TryLock", func(h *harness) error {
			_, err := h.svc.Backup(context.Background(), backup.KindManual, nil, h.storage, true)
			return err
		}},
		{"TryLock", func(h *harness) error { _, err := h.svc.StartManual(context.Background(), uuid.New()); return err }},
		{"AbandonRunning", func(h *harness) error { return h.svc.AbandonStale(context.Background()) }},
		{"AbandonRunning", func(h *harness) error { _, err := h.svc.StartManual(context.Background(), uuid.New()); return err }},
		{"InsertAttempt", func(h *harness) error {
			_, err := h.svc.Backup(context.Background(), backup.KindManual, nil, h.storage, true)
			return err
		}},
		{"RecentAttempts", func(h *harness) error { _, err := h.svc.List(context.Background()); return err }},
		{"AnyRunning", func(h *harness) error { return h.svc.RunDue(context.Background()) }},
	} {
		t.Run(tc.method, func(t *testing.T) {
			h := newHarness(t)
			h.store.fail[tc.method] = boom
			if err := tc.call(h); !errors.Is(err, boom) {
				t.Errorf("%s failing: got %v, want it passed on", tc.method, err)
			}
			if h.store.locked {
				t.Error("the lock is still held")
			}
		})
	}
}

func TestScheduledStoreFailuresSurface(t *testing.T) {
	boom := errors.New("database gone")
	for _, method := range []string{"SlotClaimed", "TryLock", "InsertAttempt"} {
		t.Run(method, func(t *testing.T) {
			h := newHarness(t)
			h.enableSchedule(t, "03:00", backup.Retention{Daily: 7})
			h.clock.Set(time.Date(2026, 11, 2, 3, 0, 0, 0, time.UTC))
			h.store.fail[method] = boom
			if err := h.svc.RunDue(context.Background()); !errors.Is(err, boom) {
				t.Errorf("RunDue = %v, want %v", err, boom)
			}
			if method == "SlotClaimed" {
				if _, _, err := h.svc.Status(context.Background()); !errors.Is(err, boom) {
					t.Errorf("Status = %v, want %v", err, boom)
				}
			}
			if h.store.locked {
				t.Error("the lock is still held")
			}
		})
	}
}

func TestAFailedFinishKeepsTheBackup(t *testing.T) {
	h := newHarness(t)
	h.store.fail["FinishAttempt"] = errors.New("database gone")
	e, err := h.svc.Backup(context.Background(), backup.KindManual, nil, h.storage, true)
	if err != nil || e.State != backup.StateSucceeded || !h.storage.has(e.Name) {
		t.Errorf("Backup = %+v, %v; want the backup kept although its attempt could not be finished", e, err)
	}
}

func TestPruneFailuresDoNotFailTheBackup(t *testing.T) {
	h := newHarness(t)
	h.enableSchedule(t, "03:00", backup.Retention{Daily: 1})
	h.clock.Set(time.Date(2026, 11, 2, 3, 0, 0, 0, time.UTC))
	h.storage.listErr = errors.New("listing refused")
	if err := h.svc.RunDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	if a := h.store.allAttempts(); len(a) != 1 || a[0].State != backup.StateSucceeded {
		t.Errorf("attempts = %+v", a)
	}
	if len(h.mailer.sent()) != 0 {
		t.Error("a pruning problem mailed a backup failure")
	}
}

func TestUnreadableStoredSettings(t *testing.T) {
	h := newHarness(t)
	h.store.settings["backup"] = []byte(`not json`)
	if _, err := h.svc.Settings(context.Background()); err == nil {
		t.Error("Settings accepted a damaged blob")
	}
	// A secret sealed under another master key does not open.
	h.store.settings["backup"] = []byte(`{"destination":"s3","s3":{"bucket":"b","access_key":"a","secret_sealed":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=="},"time_of_day":"03:00","time_zone":"UTC"}`)
	if _, err := h.svc.StartManual(context.Background(), uuid.New()); err == nil || !strings.Contains(err.Error(), "master key") {
		t.Errorf("StartManual = %v, want the secret error", err)
	}
	if _, _, err := h.svc.Open(context.Background(), "x"); err == nil {
		t.Error("Open with an unreadable secret succeeded")
	}
	if err := h.svc.Test(context.Background(), s3Settings(), nil); err == nil {
		t.Error("Test with an unreadable secret succeeded")
	}
}

func TestDestinationFailures(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	in := s3Settings()
	in.S3.Bucket = "refused"
	if err := h.svc.SaveSettings(ctx, in, ptr("x")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := h.svc.Status(ctx); !errors.Is(err, errStorageConfig) || !errors.Is(err, backup.ErrStorageUnavailable) {
		t.Errorf("Status = %v, want the storage error as ErrStorageUnavailable", err)
	}
	if _, err := h.svc.List(ctx); !errors.Is(err, backup.ErrStorageUnavailable) {
		t.Errorf("List = %v, want ErrStorageUnavailable", err)
	}

	h2 := newHarness(t)
	h2.storage.listErr = errors.New("listing refused")
	if err := h2.svc.Delete(ctx, "x"); err == nil || errors.Is(err, backup.ErrNotFound) {
		t.Errorf("Delete with an unlistable destination = %v", err)
	}
	if err := h2.svc.SaveSettings(ctx, backup.Settings{Destination: backup.DestinationFolder, TimeOfDay: "03:00", TimeZone: "Europe/Berlin", Enabled: true}, nil); err != nil {
		t.Fatal(err)
	}
	// Changing only the retention keeps when the schedule was set.
	h2.clock.Set(time.Date(2026, 11, 2, 3, 0, 0, 0, time.UTC))
	if err := h2.svc.SaveSettings(ctx, backup.Settings{Destination: backup.DestinationFolder, TimeOfDay: "03:00", TimeZone: "Europe/Berlin", Enabled: true, Retention: backup.Retention{Daily: 2}}, nil); err != nil {
		t.Fatal(err)
	}
	if err := h2.svc.RunDue(ctx); err != nil {
		t.Fatal(err)
	}
	if len(h2.store.allAttempts()) != 1 {
		t.Error("a retention change postponed the due slot")
	}
}

// TestStatusKeepsTheScheduleWithoutADestination (final review minor 6): a
// destination that cannot be built does not hide the schedule. Status
// still returns the next run, with an empty label and the storage error,
// so the strip does not say "Schedule off" for a schedule that is on.
func TestStatusKeepsTheScheduleWithoutADestination(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	in := s3Settings()
	in.S3.Bucket = "refused"
	in.Enabled, in.TimeOfDay, in.TimeZone = true, "03:00", "UTC"
	if err := h.svc.SaveSettings(ctx, in, ptr("x")); err != nil {
		t.Fatal(err)
	}
	next, label, err := h.svc.Status(ctx)
	if !errors.Is(err, backup.ErrStorageUnavailable) {
		t.Fatalf("Status error = %v, want ErrStorageUnavailable", err)
	}
	if want := time.Date(2026, 11, 2, 3, 0, 0, 0, time.UTC); next == nil || !next.Equal(want) {
		t.Fatalf("next run = %v, want %v", next, want)
	}
	if label != "" {
		t.Errorf("label = %q, want empty", label)
	}

	in.Enabled = false
	if err := h.svc.SaveSettings(ctx, in, nil); err != nil {
		t.Fatal(err)
	}
	if next, _, err := h.svc.Status(ctx); next != nil || !errors.Is(err, backup.ErrStorageUnavailable) {
		t.Fatalf("Status with the schedule off = %v, %v; want no next run and the storage error", next, err)
	}
}
