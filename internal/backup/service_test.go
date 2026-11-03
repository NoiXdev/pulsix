package backup_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/backup"
)

func ptr[T any](v T) *T { return &v }

func s3Settings() backup.Settings {
	in := backup.DefaultSettings()
	in.Destination = backup.DestinationS3
	in.S3 = backup.S3Settings{Endpoint: "http://minio:9000", Bucket: "backups", Prefix: "pulsix", AccessKey: "AK", PathStyle: true}
	return in
}

func TestSettingsDefaults(t *testing.T) {
	h := newHarness(t)
	got, err := h.svc.Settings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := backup.Settings{
		Destination: backup.DestinationFolder, Enabled: false, TimeOfDay: "03:00", TimeZone: "UTC",
		Retention: backup.Retention{Daily: 7, Weekly: 4, Monthly: 6}, IncludeMetrics: true,
	}
	if got != want {
		t.Errorf("defaults = %+v, want %+v", got, want)
	}
}

func TestSaveSettingsKeepsStoredSecret(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	in := s3Settings()
	if err := h.svc.SaveSettings(ctx, in, ptr("s1")); err != nil {
		t.Fatalf("save with secret: %v", err)
	}
	in.S3.Prefix = "other"
	if err := h.svc.SaveSettings(ctx, in, nil); err != nil {
		t.Fatalf("save keeping the secret: %v", err)
	}

	got, err := h.svc.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !got.S3.SecretKeySet || got.S3.Prefix != "other" || got.S3.Bucket != "backups" {
		t.Errorf("settings = %+v, want the secret still set and the new prefix", got)
	}
	raw := string(h.store.settings["backup"])
	if strings.Contains(raw, "s1") {
		t.Errorf("stored JSON carries the secret in the clear: %s", raw)
	}

	// The stored secret is what the storage gets.
	if err := h.svc.Test(ctx, got, nil); err != nil {
		t.Fatalf("test: %v", err)
	}
	if last := h.secrets[len(h.secrets)-1]; last != "s1" {
		t.Errorf("storage built with secret %q, want the stored s1", last)
	}
	// A new secret replaces it.
	if err := h.svc.SaveSettings(ctx, in, ptr("s2")); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.Test(ctx, in, nil); err != nil {
		t.Fatal(err)
	}
	if last := h.secrets[len(h.secrets)-1]; last != "s2" {
		t.Errorf("storage built with secret %q, want s2", last)
	}
}

func TestSaveSettingsRejectsInvalid(t *testing.T) {
	for _, tc := range []struct {
		name   string
		edit   func(*backup.Settings)
		secret *string
	}{
		{"hour out of range", func(s *backup.Settings) { s.TimeOfDay = "25:00" }, nil},
		{"not HH:MM", func(s *backup.Settings) { s.TimeOfDay = "3:00" }, nil},
		{"unknown zone", func(s *backup.Settings) { s.TimeZone = "Mars/Base" }, nil},
		{"empty zone", func(s *backup.Settings) { s.TimeZone = "" }, nil},
		{"local zone", func(s *backup.Settings) { s.TimeZone = "Local" }, nil},
		{"unknown destination", func(s *backup.Settings) { s.Destination = "ftp" }, nil},
		{"negative retention", func(s *backup.Settings) { s.Retention = backup.Retention{Daily: -1} }, nil},
		{"negative monthly", func(s *backup.Settings) { s.Retention.Monthly = -3 }, nil},
		{"s3 without bucket", func(s *backup.Settings) { *s = s3Settings(); s.S3.Bucket = "" }, ptr("secret")},
		{"s3 without access key", func(s *backup.Settings) { *s = s3Settings(); s.S3.AccessKey = "" }, ptr("secret")},
		{"s3 without secret", func(s *backup.Settings) { *s = s3Settings() }, nil},
		{"s3 with empty secret", func(s *backup.Settings) { *s = s3Settings() }, ptr("")},
		{"s3 with a bad endpoint", func(s *backup.Settings) { *s = s3Settings(); s.S3.Endpoint = "ftp://x" }, ptr("secret")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			in := backup.DefaultSettings()
			tc.edit(&in)
			err := h.svc.SaveSettings(context.Background(), in, tc.secret)
			if !errors.Is(err, backup.ErrInvalid) {
				t.Fatalf("SaveSettings = %v, want ErrInvalid", err)
			}
			if err := h.svc.Test(context.Background(), in, tc.secret); !errors.Is(err, backup.ErrInvalid) {
				t.Errorf("Test = %v, want ErrInvalid", err)
			}
			if len(h.store.settings) != 0 {
				t.Errorf("an invalid save stored %v", h.store.settings)
			}
		})
	}
}

func TestTestProbesTheStorage(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if err := h.svc.Test(ctx, backup.DefaultSettings(), nil); err != nil {
		t.Fatalf("test folder: %v", err)
	}
	if len(h.storage.deletedNames()) != 1 {
		t.Errorf("probe left %v deleted, want its one probe object", h.storage.deletedNames())
	}
	in := s3Settings()
	in.S3.Bucket = "refused"
	if err := h.svc.Test(ctx, in, ptr("x")); !errors.Is(err, errStorageConfig) {
		t.Errorf("Test = %v, want the storage error", err)
	}
	h.storage.putErr = errors.New("bucket not writable")
	if err := h.svc.Test(ctx, backup.DefaultSettings(), nil); err == nil || !strings.Contains(err.Error(), "bucket not writable") {
		t.Errorf("Test = %v, want the probe's write error", err)
	}
}

// waitIdle waits until every background backup StartManual began is done.
func waitIdle(h *harness) { backup.WaitIdle(h.svc) }

func TestStartManualRunsInTheBackground(t *testing.T) {
	h := newHarness(t)
	actor := uuid.New()
	id, err := h.svc.StartManual(context.Background(), actor)
	if err != nil {
		t.Fatalf("StartManual: %v", err)
	}
	waitIdle(h)
	a := h.store.attempt(t, id)
	if a.State != backup.StateSucceeded || a.Kind != backup.KindManual || a.Actor == nil || *a.Actor != actor || a.FinishedAt == nil {
		t.Fatalf("attempt = %+v, want a finished, succeeded manual attempt by the actor", a)
	}
	if !h.storage.has(a.ObjectName) || a.Size == 0 {
		t.Errorf("object %s (size %d) not stored", a.ObjectName, a.Size)
	}
	if c := h.dump.calls[0]; c.Kind != backup.KindManual || c.PulsixVersion != "1.2.3" || !c.IncludeMetrics {
		t.Errorf("dump options = %+v", c)
	}
	if h.store.locked {
		t.Error("the backup lock is still held after the backup")
	}
}

func TestStartManualRefusesWhileRunning(t *testing.T) {
	h := newHarness(t)
	running := h.store.addAttempt(backup.Attempt{Kind: backup.KindManual, StartedAt: h.clock.Now().Add(-time.Minute), State: backup.StateRunning})
	h.store.lockedElsewhere = true // another manager is backing up

	if _, err := h.svc.StartManual(context.Background(), uuid.New()); !errors.Is(err, backup.ErrBusy) {
		t.Fatalf("StartManual = %v, want ErrBusy", err)
	}
	if _, err := h.svc.Backup(context.Background(), backup.KindManual, nil, h.storage, true); !errors.Is(err, backup.ErrBusy) {
		t.Fatalf("Backup = %v, want ErrBusy", err)
	}
	if a := h.store.attempt(t, running); a.State != backup.StateRunning {
		t.Errorf("the other backup's attempt became %s", a.State)
	}
	if h.dump.count() != 0 {
		t.Error("a refused backup dumped")
	}
}

func TestStaleRunningAttemptDoesNotBlock(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	// A manager crashed (or a restore brought the row back) seven hours ago.
	stale := h.store.addAttempt(backup.Attempt{Kind: backup.KindManual, StartedAt: h.clock.Now().Add(-7 * time.Hour), State: backup.StateRunning})

	id, err := h.svc.StartManual(ctx, uuid.New())
	if err != nil {
		t.Fatalf("StartManual with a stale running attempt = %v, want it to start", err)
	}
	waitIdle(h)
	if a := h.store.attempt(t, stale); a.State != backup.StateAbandoned || a.FinishedAt == nil || a.Error == "" {
		t.Errorf("stale attempt = %+v, want abandoned with a reason", a)
	}
	if a := h.store.attempt(t, id); a.State != backup.StateSucceeded {
		t.Errorf("new attempt = %s, want succeeded", a.State)
	}

	// At manager start every running attempt without a lock holder is
	// stale, however young.
	young := h.store.addAttempt(backup.Attempt{Kind: backup.KindManual, StartedAt: h.clock.Now().Add(-time.Minute), State: backup.StateRunning})
	if err := h.svc.AbandonStale(ctx); err != nil {
		t.Fatal(err)
	}
	if a := h.store.attempt(t, young); a.State != backup.StateAbandoned {
		t.Errorf("AbandonStale left a 1-minute-old attempt %s", a.State)
	}
}

func TestAbandonStaleLeavesARealBackupAlone(t *testing.T) {
	h := newHarness(t)
	running := h.store.addAttempt(backup.Attempt{Kind: backup.KindManual, StartedAt: h.clock.Now(), State: backup.StateRunning})
	h.store.lockedElsewhere = true
	if err := h.svc.AbandonStale(context.Background()); err != nil {
		t.Fatal(err)
	}
	if a := h.store.attempt(t, running); a.State != backup.StateRunning {
		t.Errorf("attempt of a backup still holding the lock became %s", a.State)
	}
}

func TestStartManualFailsWithoutAUsableDestination(t *testing.T) {
	h := newHarness(t)
	h.store.settings["backup"] = []byte(`{"destination":"s3","s3":{"bucket":"refused","access_key":"a"},"time_of_day":"03:00","time_zone":"UTC"}`)
	if _, err := h.svc.StartManual(context.Background(), uuid.New()); !errors.Is(err, errStorageConfig) {
		t.Fatalf("StartManual = %v, want the storage error", err)
	}
	if len(h.store.allAttempts()) != 0 || h.store.locked {
		t.Error("a backup that could not start left an attempt or the lock behind")
	}
}

func TestStopAbandonsAManualBackupInFlight(t *testing.T) {
	h := newHarness(t)
	h.dump.block = true
	id, err := h.svc.StartManual(context.Background(), uuid.New())
	if err != nil {
		t.Fatal(err)
	}
	h.svc.Stop()
	a := h.store.attempt(t, id)
	if a.State != backup.StateAbandoned || a.FinishedAt == nil {
		t.Errorf("attempt after Stop = %+v, want abandoned", a)
	}
	if h.storage.has(a.ObjectName) {
		t.Error("an abandoned backup left its object")
	}
	if _, err := h.svc.StartManual(context.Background(), uuid.New()); err == nil {
		t.Error("StartManual after Stop succeeded")
	}
}

func TestBackupRecordsAFailure(t *testing.T) {
	h := newHarness(t)
	h.storage.putErr = errors.New("disk full")
	e, err := h.svc.Backup(context.Background(), backup.KindManual, nil, h.storage, false)
	if err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("Backup = %v, want the storage error", err)
	}
	if e.State != backup.StateFailed || !strings.Contains(e.Error, "disk full") {
		t.Errorf("entry = %+v, want failed with the error", e)
	}
	attempts := h.store.allAttempts()
	if len(attempts) != 1 || attempts[0].State != backup.StateFailed || !strings.Contains(attempts[0].Error, "disk full") {
		t.Errorf("attempts = %+v, want one failed attempt with the error", attempts)
	}
	if len(h.mailer.sent()) != 0 {
		t.Error("a manual failure sent mail")
	}

	h.storage.putErr = nil
	h.dump.err = errors.New("snapshot refused")
	if _, err := h.svc.Backup(context.Background(), backup.KindManual, nil, h.storage, false); err == nil || !strings.Contains(err.Error(), "snapshot refused") {
		t.Fatalf("Backup = %v, want the dump error rather than the upload's", err)
	}
	if _, err := h.svc.Backup(context.Background(), "weekly", nil, h.storage, false); !errors.Is(err, backup.ErrInvalid) {
		t.Errorf("Backup with kind weekly = %v, want ErrInvalid", err)
	}
}

func TestBackupReturnsTheEntry(t *testing.T) {
	h := newHarness(t)
	h.clock.Set(time.Date(2026, 11, 2, 10, 11, 12, 500, time.UTC))
	e, err := h.svc.Backup(context.Background(), backup.KindManual, nil, h.storage, false)
	if err != nil {
		t.Fatal(err)
	}
	if e.Name != "pulsix-20261102T101112Z-manual.pxbak" || e.State != backup.StateSucceeded || e.Size == 0 ||
		e.PulsixVersion != "1.2.3" || !e.CreatedAt.Equal(time.Date(2026, 11, 2, 10, 11, 12, 0, time.UTC)) {
		t.Errorf("entry = %+v", e)
	}
	if h.dump.calls[0].IncludeMetrics {
		t.Error("includeMetrics false was not passed on")
	}
}

func TestRunDueDisabledDoesNothing(t *testing.T) {
	h := newHarness(t)
	h.clock.Set(time.Date(2026, 11, 2, 3, 0, 0, 0, time.UTC))
	if err := h.svc.RunDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	if h.dump.count() != 0 || len(h.store.allAttempts()) != 0 || h.store.lockCalls != 0 {
		t.Errorf("disabled schedule: %d dumps, %d attempts, %d lock calls", h.dump.count(), len(h.store.allAttempts()), h.store.lockCalls)
	}
}

func TestRunDueAbandonsStaleAttempts(t *testing.T) {
	h := newHarness(t)
	stale := h.store.addAttempt(backup.Attempt{Kind: backup.KindManual, StartedAt: h.clock.Now(), State: backup.StateRunning})
	if err := h.svc.RunDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	if a := h.store.attempt(t, stale); a.State != backup.StateAbandoned {
		t.Errorf("stale attempt = %s, want abandoned", a.State)
	}
	if h.dump.count() != 0 {
		t.Error("a disabled schedule dumped")
	}
}

func TestRunDueRunsEachSlotOnce(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.enableSchedule(t, "03:00", backup.Retention{Daily: 7, Weekly: 4, Monthly: 6}) // saved 2026-11-01 12:00

	// Today's 03:00 slot was before the schedule was saved: no backup yet.
	h.clock.Set(time.Date(2026, 11, 1, 12, 1, 0, 0, time.UTC))
	if err := h.svc.RunDue(ctx); err != nil {
		t.Fatal(err)
	}
	if h.dump.count() != 0 {
		t.Fatal("saving a schedule ran a backup for a slot already past")
	}

	for _, at := range []time.Time{
		time.Date(2026, 11, 2, 3, 0, 0, 0, time.UTC),
		time.Date(2026, 11, 2, 3, 1, 0, 0, time.UTC),
		time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC),
	} {
		h.clock.Set(at)
		if err := h.svc.RunDue(ctx); err != nil {
			t.Fatal(err)
		}
	}
	attempts := h.store.allAttempts()
	if h.dump.count() != 1 || len(attempts) != 1 {
		t.Fatalf("%d dumps, attempts %+v; want one for the 03:00 slot", h.dump.count(), attempts)
	}
	a := attempts[0]
	if a.Kind != backup.KindScheduled || a.Slot == nil || !a.Slot.Equal(time.Date(2026, 11, 2, 3, 0, 0, 0, time.UTC)) || a.State != backup.StateSucceeded {
		t.Errorf("attempt = %+v", a)
	}
	if c := h.dump.calls[0]; c.Kind != backup.KindScheduled {
		t.Errorf("dump kind %q", c.Kind)
	}
}

func TestRunDueRetriesWhenBusy(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.enableSchedule(t, "03:00", backup.Retention{Daily: 7})
	h.clock.Set(time.Date(2026, 11, 2, 3, 0, 0, 0, time.UTC))
	h.store.lockedElsewhere = true
	if err := h.svc.RunDue(ctx); err != nil {
		t.Fatal(err)
	}
	if len(h.store.allAttempts()) != 0 {
		t.Fatal("a busy tick claimed the slot")
	}
	h.store.lockedElsewhere = false
	h.clock.Set(time.Date(2026, 11, 2, 3, 1, 0, 0, time.UTC))
	if err := h.svc.RunDue(ctx); err != nil {
		t.Fatal(err)
	}
	if a := h.store.allAttempts(); len(a) != 1 || !a[0].Slot.Equal(time.Date(2026, 11, 2, 3, 0, 0, 0, time.UTC)) {
		t.Errorf("attempts after the lock came free = %+v, want the 03:00 slot", a)
	}
}

func TestRunDuePrunesOnlyAfterSuccess(t *testing.T) {
	r := backup.Retention{Daily: 1}
	seed := func(h *harness) []string {
		return []string{
			h.storage.putSignedBackup(t, time.Date(2026, 10, 30, 3, 0, 0, 0, time.UTC), backup.KindScheduled),
			h.storage.putSignedBackup(t, time.Date(2026, 10, 31, 3, 0, 0, 0, time.UTC), backup.KindScheduled),
			h.storage.putSignedBackup(t, time.Date(2026, 10, 29, 9, 0, 0, 0, time.UTC), backup.KindManual),
		}
	}
	due := time.Date(2026, 11, 2, 3, 0, 30, 0, time.UTC)

	t.Run("failure", func(t *testing.T) {
		h := newHarness(t)
		seed(h)
		h.enableSchedule(t, "03:00", r)
		h.dump.err = errors.New("snapshot refused")
		h.clock.Set(due)
		if err := h.svc.RunDue(context.Background()); err != nil {
			t.Fatal(err)
		}
		if d := h.storage.deletedNames(); len(d) != 0 {
			t.Errorf("a failed backup pruned %v", d)
		}
		a := h.store.allAttempts()
		if len(a) != 1 || a[0].State != backup.StateFailed || !strings.Contains(a[0].Error, "snapshot refused") {
			t.Errorf("attempts = %+v, want one failed", a)
		}
		mails := h.mailer.sent()
		if len(mails) != 1 {
			t.Fatalf("mails = %v, want one", mails)
		}
		subject, body, _ := strings.Cut(mails[0], "\n")
		if subject != "Pulsix backup failed" {
			t.Errorf("subject %q", subject)
		}
		for _, want := range []string{"snapshot refused", "memory", "2026-11-02 03:00"} {
			if !strings.Contains(body, want) {
				t.Errorf("body %q lacks %q", body, want)
			}
		}
	})

	t.Run("success", func(t *testing.T) {
		h := newHarness(t)
		seeded := seed(h)
		h.enableSchedule(t, "03:00", r)
		h.clock.Set(due)
		if err := h.svc.RunDue(context.Background()); err != nil {
			t.Fatal(err)
		}
		after, _ := h.storage.List(context.Background())
		if len(after) != 2 {
			t.Errorf("objects after the scheduled backup = %d, want the new one and the manual one", len(after))
		}
		// Daily 1 keeps only the new backup of the scheduled ones.
		want := seeded[:2]
		got := h.storage.deletedNames()
		slices.Sort(got)
		if !slices.Equal(got, want) {
			t.Errorf("deleted %v, want %v", got, want)
		}
		if len(h.mailer.sent()) != 0 {
			t.Error("a successful backup sent mail")
		}
	})

	t.Run("storage unusable", func(t *testing.T) {
		h := newHarness(t)
		h.store.settings["backup"] = []byte(`{"destination":"s3","s3":{"bucket":"refused","access_key":"a"},"enabled":true,"time_of_day":"03:00","time_zone":"UTC"}`)
		h.clock.Set(due)
		if err := h.svc.RunDue(context.Background()); err != nil {
			t.Fatal(err)
		}
		a := h.store.allAttempts()
		if len(a) != 1 || a[0].State != backup.StateFailed || !strings.Contains(a[0].Error, errStorageConfig.Error()) {
			t.Errorf("attempts = %+v, want one failed with the storage error", a)
		}
		if len(h.mailer.sent()) != 1 {
			t.Errorf("mails = %v, want one", h.mailer.sent())
		}
	})
}

func TestListMergesAttemptsAndObjects(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	actor := uuid.New()

	// A manual backup by actor, through the service.
	h.clock.Set(time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC))
	manual, err := h.svc.Backup(ctx, backup.KindManual, &actor, h.storage, true)
	if err != nil {
		t.Fatal(err)
	}
	h.store.attempts[0].ActorName = "Ada"
	// A failed scheduled attempt without an object.
	h.store.addAttempt(backup.Attempt{
		Kind: backup.KindScheduled, Slot: ptr(time.Date(2026, 11, 2, 3, 0, 0, 0, time.UTC)), StartedAt: time.Date(2026, 11, 2, 3, 0, 0, 0, time.UTC),
		FinishedAt: ptr(time.Date(2026, 11, 2, 3, 0, 5, 0, time.UTC)), State: backup.StateFailed, Error: "disk full",
		ObjectName: backup.ObjectName(time.Date(2026, 11, 2, 3, 0, 0, 0, time.UTC), backup.KindScheduled),
	})
	// An object another instance wrote, with no attempt here.
	foreign := h.storage.putBackup(t, time.Date(2026, 10, 31, 3, 0, 0, 0, time.UTC), backup.KindScheduled)
	// A succeeded attempt whose object was deleted since: not a backup any more.
	h.store.addAttempt(backup.Attempt{Kind: backup.KindManual, StartedAt: time.Date(2026, 10, 20, 3, 0, 0, 0, time.UTC), State: backup.StateSucceeded, ObjectName: "pulsix-20261020T030000Z-manual.pxbak"})

	entries, err := h.svc.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("entries = %+v, want three", entries)
	}
	failed, mine, other := entries[0], entries[1], entries[2]
	if failed.State != backup.StateFailed || failed.Error != "disk full" || failed.Kind != backup.KindScheduled || failed.Size != 0 {
		t.Errorf("failed entry = %+v", failed)
	}
	if mine.Name != manual.Name || mine.State != backup.StateSucceeded || mine.Kind != backup.KindManual ||
		mine.Actor == nil || *mine.Actor != actor || mine.ActorName != "Ada" || mine.PulsixVersion != "1.2.3" || mine.Size == 0 ||
		!mine.Verified {
		t.Errorf("manual entry = %+v", mine)
	}
	if other.Name != foreign || other.State != backup.StateSucceeded || other.Kind != backup.KindScheduled ||
		other.Actor != nil || other.PulsixVersion != "0.9.0" || !other.CreatedAt.Equal(time.Date(2026, 10, 31, 3, 0, 0, 0, time.UTC)) ||
		other.Verified {
		t.Errorf("foreign entry = %+v", other)
	}
}

func TestListShowsDamagedObjectsAndRestoredAttempts(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.storage.objects["pulsix-20261101T030000Z-scheduled.pxbak"] = []byte("not a backup")
	// A restore brought back the row of the backup that took the snapshot
	// as abandoned, while its object exists: the object is the truth.
	name := h.storage.putBackup(t, time.Date(2026, 10, 30, 3, 0, 0, 0, time.UTC), backup.KindManual)
	h.store.addAttempt(backup.Attempt{Kind: backup.KindManual, StartedAt: time.Date(2026, 10, 30, 3, 0, 0, 0, time.UTC), State: backup.StateAbandoned, ObjectName: name, Error: "restored"})

	entries, err := h.svc.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %+v, want two", entries)
	}
	if e := entries[0]; e.State != backup.StateDamaged || e.Error == "" || !e.CreatedAt.Equal(time.Date(2026, 11, 1, 3, 0, 0, 0, time.UTC)) {
		t.Errorf("damaged entry = %+v", e)
	}
	if e := entries[1]; e.Name != name || e.State != backup.StateSucceeded || e.Error != "" {
		t.Errorf("restored entry = %+v, want succeeded", e)
	}
}

func TestListStillShowsAttemptsWhenTheStorageFails(t *testing.T) {
	h := newHarness(t)
	h.storage.listErr = errors.New("bucket unreachable")
	h.store.addAttempt(backup.Attempt{Kind: backup.KindScheduled, StartedAt: h.clock.Now(), State: backup.StateFailed, Error: "bucket unreachable"})
	entries, err := h.svc.List(context.Background())
	if !errors.Is(err, backup.ErrStorageUnavailable) || !strings.Contains(err.Error(), "bucket unreachable") {
		t.Fatalf("List = %v, want ErrStorageUnavailable with the cause", err)
	}
	if len(entries) != 1 || entries[0].State != backup.StateFailed {
		t.Errorf("entries = %+v, want the failed attempt", entries)
	}
}

func TestDeleteAndOpen(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	name := h.storage.putBackup(t, time.Date(2026, 10, 30, 3, 0, 0, 0, time.UTC), backup.KindManual)

	rc, size, err := h.svc.Open(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(rc)
	_ = rc.Close()
	if int64(len(data)) != size || size == 0 {
		t.Errorf("Open returned %d bytes, size %d", len(data), size)
	}
	if err := h.svc.Delete(ctx, name); err != nil {
		t.Fatal(err)
	}
	if h.storage.has(name) {
		t.Error("Delete left the object")
	}
	if err := h.svc.Delete(ctx, name); !errors.Is(err, backup.ErrNotFound) {
		t.Errorf("Delete of a missing object = %v, want ErrNotFound", err)
	}
	if _, _, err := h.svc.Open(ctx, name); !errors.Is(err, backup.ErrNotFound) {
		t.Errorf("Open of a missing object = %v, want ErrNotFound", err)
	}
	h.storage.objects["notes.txt"] = []byte("x") // not a backup: not listed, not served
	if _, _, err := h.svc.Open(ctx, "notes.txt"); !errors.Is(err, backup.ErrNotFound) {
		t.Errorf("Open of a non-backup = %v, want ErrNotFound", err)
	}
}

func TestStatus(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	next, dest, err := h.svc.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if next != nil || dest != "memory" {
		t.Errorf("disabled: next %v, destination %q", next, dest)
	}

	in := backup.DefaultSettings()
	in.Enabled, in.TimeZone = true, "Europe/Berlin"
	if err := h.svc.SaveSettings(ctx, in, nil); err != nil {
		t.Fatal(err)
	}
	next, _, err = h.svc.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Saved at 2026-11-01 12:00 UTC: the next 03:00 Berlin is 02:00 UTC
	// on 11-02.
	if want := time.Date(2026, 11, 2, 2, 0, 0, 0, time.UTC); next == nil || !next.Equal(want) {
		t.Errorf("next run = %v, want %s", next, want)
	}

	// A slot due but not yet taken is the next run.
	h.clock.Set(time.Date(2026, 11, 2, 2, 0, 20, 0, time.UTC))
	next, _, _ = h.svc.Status(ctx)
	if want := time.Date(2026, 11, 2, 2, 0, 0, 0, time.UTC); next == nil || !next.Equal(want) {
		t.Errorf("next run with the slot due = %v, want %s", next, want)
	}
	if err := h.svc.RunDue(ctx); err != nil {
		t.Fatal(err)
	}
	next, _, _ = h.svc.Status(ctx)
	if want := time.Date(2026, 11, 3, 2, 0, 0, 0, time.UTC); next == nil || !next.Equal(want) {
		t.Errorf("next run after the slot ran = %v, want %s", next, want)
	}
}

// TestInterruptedScheduledBackupIsRetried: a scheduled backup cut short by
// a shutdown leaves its slot abandoned; a later tick for the same slot
// takes that day's backup after all.
func TestInterruptedScheduledBackupIsRetried(t *testing.T) {
	h := newHarness(t)
	h.enableSchedule(t, "03:00", backup.Retention{Daily: 7})
	slot := time.Date(2026, 11, 2, 3, 0, 0, 0, time.UTC)
	h.clock.Set(slot)
	h.dump.block = true

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.svc.RunDue(ctx) }()
	for h.dump.count() == 0 {
		time.Sleep(time.Millisecond)
	}
	cancel() // the ticker stops: the manager shuts down mid-backup
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if a := h.store.allAttempts(); len(a) != 1 || a[0].State != backup.StateAbandoned {
		t.Fatalf("attempts after the interruption = %+v, want one abandoned", a)
	}

	h.dump.mu.Lock()
	h.dump.block = false
	h.dump.mu.Unlock()
	h.clock.Set(slot.Add(5 * time.Minute))
	if err := h.svc.RunDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	var succeeded []backup.Attempt
	for _, a := range h.store.allAttempts() {
		if a.State == backup.StateSucceeded {
			succeeded = append(succeeded, a)
		}
	}
	if len(succeeded) != 1 || !succeeded[0].Slot.Equal(slot) {
		t.Fatalf("attempts = %+v, want the slot's backup taken on the retry", h.store.allAttempts())
	}
	// Claimed once more: no third run.
	if err := h.svc.RunDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	if h.dump.count() != 2 {
		t.Errorf("%d dumps, want the interrupted one and the retry", h.dump.count())
	}
}

// TestCrashedScheduledBackupIsRetriedInOneTick: a manager crashed during
// the slot's backup, leaving its row running. The next tick finds no lock
// holder, abandons the row and takes the slot's backup.
func TestCrashedScheduledBackupIsRetriedInOneTick(t *testing.T) {
	h := newHarness(t)
	h.enableSchedule(t, "03:00", backup.Retention{Daily: 7})
	slot := time.Date(2026, 11, 2, 3, 0, 0, 0, time.UTC)
	crashed := h.store.addAttempt(backup.Attempt{Kind: backup.KindScheduled, Slot: &slot, StartedAt: slot, State: backup.StateRunning})
	h.clock.Set(slot.Add(10 * time.Minute))
	if err := h.svc.RunDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	if a := h.store.attempt(t, crashed); a.State != backup.StateAbandoned {
		t.Errorf("crashed attempt = %s, want abandoned", a.State)
	}
	if h.dump.count() != 1 || len(h.store.allAttempts()) != 2 {
		t.Errorf("%d dumps, attempts %+v; want the slot retried", h.dump.count(), h.store.allAttempts())
	}
}

// TestOldAbandonedSlotIsNotRerun: only the current slot is retried.
func TestOldAbandonedSlotIsNotRerun(t *testing.T) {
	h := newHarness(t)
	h.enableSchedule(t, "03:00", backup.Retention{Daily: 7})
	yesterday := time.Date(2026, 11, 2, 3, 0, 0, 0, time.UTC)
	h.store.addAttempt(backup.Attempt{Kind: backup.KindScheduled, Slot: &yesterday, StartedAt: yesterday, State: backup.StateAbandoned, FinishedAt: &yesterday})
	today := yesterday.Add(24 * time.Hour)
	h.store.addAttempt(backup.Attempt{Kind: backup.KindScheduled, Slot: &today, StartedAt: today, State: backup.StateSucceeded, FinishedAt: &today})
	h.clock.Set(today.Add(time.Hour))
	if err := h.svc.RunDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	if h.dump.count() != 0 {
		t.Error("an abandoned slot of an earlier day was run again")
	}
}

// TestRunDueLogsHowManyUnverifiedScheduledBackupsItKeeps: a scheduled
// backup written before header MACs (or one planted without the key) is
// never pruned, so such objects pile up unless an administrator deletes
// them. Each prune run says how many it skipped, once and as a count, not
// per name; unverified manual backups are not part of the count, retention
// never touches manual backups anyway.
func TestRunDueLogsHowManyUnverifiedScheduledBackupsItKeeps(t *testing.T) {
	h := newHarness(t)
	h.enableSchedule(t, "03:00", backup.Retention{Daily: 1})
	old1 := h.storage.putBackup(t, time.Date(2026, 10, 20, 3, 0, 0, 0, time.UTC), backup.KindScheduled)
	old2 := h.storage.putBackup(t, time.Date(2026, 10, 21, 3, 0, 0, 0, time.UTC), backup.KindScheduled)
	h.storage.putBackup(t, time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC), backup.KindManual)
	h.storage.putSignedBackup(t, time.Date(2026, 10, 31, 3, 0, 0, 0, time.UTC), backup.KindScheduled)

	h.clock.Set(time.Date(2026, 11, 2, 3, 0, 30, 0, time.UTC))
	if err := h.svc.RunDue(context.Background()); err != nil {
		t.Fatal(err)
	}

	logs := h.logs.String()
	const msg = "kept scheduled backups that are not verified"
	if n := strings.Count(logs, msg); n != 1 {
		t.Fatalf("want the skipped count logged once per prune run, logged %d times:\n%s", n, logs)
	}
	for _, want := range []string{"count=2", "until an administrator deletes them"} {
		if !strings.Contains(logs, want) {
			t.Errorf("log misses %q:\n%s", want, logs)
		}
	}
	for _, name := range []string{old1, old2} {
		if strings.Contains(logs, name) {
			t.Errorf("log names the skipped object %s; it should only count them:\n%s", name, logs)
		}
		if !h.storage.has(name) {
			t.Errorf("unverified scheduled backup %s was pruned", name)
		}
	}
}

// TestRunDueForgedHeadersCannotPruneAuthenticBackups is the audit's
// reproduction (backlog 7.50): objects planted in the destination by a
// writer without the master key claim to be scheduled backups, dated in
// the future and on the same days as authentic ones. Retention must not
// delete a single authentic backup over them, least of all the new one.
func TestRunDueForgedHeadersCannotPruneAuthenticBackups(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.clock.Set(time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC))
	h.enableSchedule(t, "03:00", backup.DefaultSettings().Retention) // 7/4/6

	var authentic []string
	run := func(at time.Time) {
		t.Helper()
		h.clock.Set(at)
		if err := h.svc.RunDue(ctx); err != nil {
			t.Fatal(err)
		}
		name := backup.ObjectName(at, backup.KindScheduled)
		if !h.storage.has(name) {
			t.Fatalf("the backup %s of the run at %s is not stored; deleted so far: %v", name, at, h.storage.deletedNames())
		}
		authentic = append(authentic, name)
		for _, n := range authentic {
			if !h.storage.has(n) {
				t.Errorf("after the run at %s the authentic backup %s is gone", at, n)
			}
		}
	}
	// A backup from before header MACs, earlier on the day of an authentic
	// one: pruned if it counted, but unverified, so it stays.
	h.storage.putBackup(t, time.Date(2026, 11, 1, 1, 0, 0, 0, time.UTC), backup.KindScheduled)
	run(time.Date(2026, 10, 31, 3, 0, 10, 0, time.UTC))
	run(time.Date(2026, 11, 1, 3, 0, 10, 0, time.UTC))

	forged := func(at time.Time) backup.Header {
		return backup.Header{FormatVersion: backup.FormatVersion, CreatedAt: at, Kind: backup.KindScheduled}
	}
	for m := 1; m <= 7; m++ {
		at := time.Date(2031, time.Month(m), 15, 0, 0, 0, 0, time.UTC)
		switch m % 3 {
		case 0: // the audit's spelling: any name, no MAC
			h.storage.putHeader(t, fmt.Sprintf("zz-forged-%d.pxbak", m), forged(at))
		case 1: // the canonical name, no MAC
			h.storage.putBackup(t, at, backup.KindScheduled)
		default: // the canonical name and a MAC made under another key
			h.storage.putHeader(t, backup.ObjectName(at, backup.KindScheduled), backup.SignHeader(forged(at), backup.HeaderKey([32]byte{1})))
		}
	}
	// Same-day forgeries, later on the day of an authentic backup and
	// earlier on the day of the next run, with a garbage MAC.
	for _, at := range []time.Time{
		time.Date(2026, 11, 1, 3, 0, 20, 0, time.UTC),
		time.Date(2026, 11, 2, 3, 0, 20, 0, time.UTC),
		time.Date(2026, 11, 3, 3, 0, 20, 0, time.UTC),
	} {
		f := forged(at)
		f.MAC = strings.Repeat("ab", 32)
		h.storage.putHeader(t, backup.ObjectName(at, backup.KindScheduled), f)
	}

	run(time.Date(2026, 11, 2, 3, 0, 30, 0, time.UTC))
	run(time.Date(2026, 11, 3, 3, 0, 30, 0, time.UTC))

	if d := h.storage.deletedNames(); len(d) != 0 {
		t.Errorf("deleted %v; four authentic backups fit the retention, and unverified objects are never pruned", d)
	}
}
