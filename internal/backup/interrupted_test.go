package backup_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/backup"
)

// TestRunDueMailsASlotThatWasOnlyInterrupted: every attempt of the
// 2026-11-02 slot was interrupted. While that slot is still the current
// one a later tick may retry it, so nobody is told; once the next slot has
// started it is never run again, and the administrators hear about it
// exactly once.
func TestRunDueMailsASlotThatWasOnlyInterrupted(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.enableSchedule(t, "03:00", backup.Retention{Daily: 7})
	slot := time.Date(2026, 11, 2, 3, 0, 0, 0, time.UTC)
	first := h.store.addAttempt(abandonedAt(slot, slot))
	newest := h.store.addAttempt(abandonedAt(slot, slot.Add(20*time.Minute)))

	// Still the current slot (the retry waits for the lock): no mail.
	h.store.lockedElsewhere = true
	h.clock.Set(time.Date(2026, 11, 2, 23, 59, 0, 0, time.UTC))
	if err := h.svc.RunDue(ctx); err != nil {
		t.Fatal(err)
	}
	if m := h.mailer.sent(); len(m) != 0 {
		t.Fatalf("mails while the slot could still be retried = %v", m)
	}

	// The next day's slot started: the interrupted one is past.
	h.store.lockedElsewhere = false
	h.clock.Set(time.Date(2026, 11, 3, 3, 0, 30, 0, time.UTC))
	if err := h.svc.RunDue(ctx); err != nil {
		t.Fatal(err)
	}
	mails := h.mailer.sent()
	if len(mails) != 1 {
		t.Fatalf("mails = %v, want one", mails)
	}
	subject, body, _ := strings.Cut(mails[0], "\n")
	if subject != "Pulsix backup interrupted" {
		t.Errorf("subject %q", subject)
	}
	for _, want := range []string{"2026-11-02 03:00", "stopped"} {
		if !strings.Contains(body, want) {
			t.Errorf("body %q lacks %q", body, want)
		}
	}
	if !h.store.wasNotified(newest) || h.store.wasNotified(first) {
		t.Errorf("notified: newest %v, first %v; want only the newest attempt marked", h.store.wasNotified(newest), h.store.wasNotified(first))
	}

	// Once: later ticks do not mail it again.
	h.clock.Set(time.Date(2026, 11, 3, 3, 5, 0, 0, time.UTC))
	if err := h.svc.RunDue(ctx); err != nil {
		t.Fatal(err)
	}
	if m := h.mailer.sent(); len(m) != 1 {
		t.Errorf("mails after another tick = %v, want still one", m)
	}
}

// TestRunDueMailsOnlySlotsThatWereOnlyInterrupted: a slot with an attempt
// that ran to an end, is still running, or is the backup a restore came
// from is not mailed by this path; a failure has its own mail when it
// happens. A schedule switched off after the interruption still mails it.
func TestRunDueMailsOnlySlotsThatWereOnlyInterrupted(t *testing.T) {
	slot := time.Date(2026, 11, 2, 3, 0, 0, 0, time.UTC)
	later := slot.Add(time.Hour)
	ended := func(state string) backup.Attempt {
		return backup.Attempt{Kind: backup.KindScheduled, Slot: &slot, StartedAt: later, FinishedAt: &later, State: state}
	}
	restored := abandonedAt(slot, slot)
	restored.Error = backup.ReasonRestored
	manual := abandonedAt(slot, slot)
	manual.Kind, manual.Slot = backup.KindManual, nil

	for _, tc := range []struct {
		name     string
		attempts []backup.Attempt
		disable  bool
		want     int
	}{
		{name: "only interrupted", attempts: []backup.Attempt{abandonedAt(slot, slot)}, want: 1},
		{name: "schedule switched off since", attempts: []backup.Attempt{abandonedAt(slot, slot)}, disable: true, want: 1},
		{name: "a retry succeeded", attempts: []backup.Attempt{abandonedAt(slot, slot), ended(backup.StateSucceeded)}},
		{name: "a retry failed", attempts: []backup.Attempt{abandonedAt(slot, slot), ended(backup.StateFailed)}},
		{name: "a retry still running", attempts: []backup.Attempt{abandonedAt(slot, slot), {Kind: backup.KindScheduled, Slot: &slot, StartedAt: later, State: backup.StateRunning}}},
		{name: "the backup a restore came from", attempts: []backup.Attempt{restored}},
		{name: "manual attempts only", attempts: []backup.Attempt{manual}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			h.enableSchedule(t, "03:00", backup.Retention{Daily: 7})
			if tc.disable {
				in := backup.DefaultSettings()
				if err := h.svc.SaveSettings(context.Background(), in, nil); err != nil {
					t.Fatal(err)
				}
			}
			for _, a := range tc.attempts {
				h.store.addAttempt(a)
			}
			// Another manager holds the lock: no backup runs and no
			// running attempt is abandoned in this tick.
			h.store.lockedElsewhere = true
			h.clock.Set(time.Date(2026, 11, 3, 4, 0, 0, 0, time.UTC))
			if err := h.svc.RunDue(context.Background()); err != nil {
				t.Fatal(err)
			}
			if got := len(h.mailer.sent()); got != tc.want {
				t.Errorf("mails = %v, want %d", h.mailer.sent(), tc.want)
			}
		})
	}
}

// TestRunDueReportsAStoreErrorOfTheInterruptedCheck: a lookup or a mark
// that fails ends the tick with the error and sends nothing; the next tick
// tries again.
func TestRunDueReportsAStoreErrorOfTheInterruptedCheck(t *testing.T) {
	for _, method := range []string{"InterruptedSlots", "MarkNotified"} {
		t.Run(method, func(t *testing.T) {
			h := newHarness(t)
			h.enableSchedule(t, "03:00", backup.Retention{Daily: 7})
			slot := time.Date(2026, 11, 2, 3, 0, 0, 0, time.UTC)
			h.store.addAttempt(abandonedAt(slot, slot))
			h.store.lockedElsewhere = true
			h.clock.Set(time.Date(2026, 11, 3, 4, 0, 0, 0, time.UTC))
			h.store.fail[method] = errors.New("database gone")
			if err := h.svc.RunDue(context.Background()); err == nil || !strings.Contains(err.Error(), "database gone") {
				t.Fatalf("RunDue = %v, want the store error", err)
			}
			if m := h.mailer.sent(); len(m) != 0 {
				t.Fatalf("mails = %v, want none", m)
			}
			delete(h.store.fail, method)
			if err := h.svc.RunDue(context.Background()); err != nil {
				t.Fatal(err)
			}
			if m := h.mailer.sent(); len(m) != 1 {
				t.Errorf("mails on the next tick = %v, want one", m)
			}
		})
	}
}

// abandonedAt is a scheduled attempt of slot, started at started, that a
// stopped manager left behind.
func abandonedAt(slot, started time.Time) backup.Attempt {
	end := started.Add(5 * time.Minute)
	return backup.Attempt{
		Kind: backup.KindScheduled, Slot: &slot, StartedAt: started, FinishedAt: &end,
		State: backup.StateAbandoned, Error: "the manager stopped before this backup finished",
	}
}

// TestSQLStoreInterruptedSlots: only a past slot whose every attempt was
// interrupted comes back, with its newest attempt, until that attempt is
// marked notified; marking it a second time does nothing.
func TestSQLStoreInterruptedSlots(t *testing.T) {
	store, _, admin := sqlStore(t)
	ctx := context.Background()
	day := func(d int) time.Time { return time.Date(2026, 10, d, 3, 0, 0, 0, time.UTC) }
	// attempt records a scheduled attempt of slot and ends it as state
	// (with reason, for abandoned); running leaves it running.
	attempt := func(slot time.Time, after time.Duration, state, reason string) uuid.UUID {
		t.Helper()
		var s *time.Time
		if !slot.IsZero() {
			s = &slot
		}
		kind := backup.KindScheduled
		if s == nil {
			kind = backup.KindManual
		}
		id, ok, err := store.InsertAttempt(ctx, backup.NewAttempt{Kind: kind, Slot: s, StartedAt: day(1).Add(after), ObjectName: "x.pxbak"})
		if err != nil || !ok {
			t.Fatalf("insert attempt of %v: %v %v", slot, ok, err)
		}
		switch state {
		case backup.StateAbandoned:
			if _, err := store.AbandonRunning(ctx, day(1).Add(after+time.Minute), reason); err != nil {
				t.Fatal(err)
			}
		case backup.StateSucceeded, backup.StateFailed:
			if err := store.FinishAttempt(ctx, id, backup.Finish{At: day(1).Add(after + time.Minute), State: state}); err != nil {
				t.Fatal(err)
			}
		}
		return id
	}
	const stale = "the manager stopped before this backup finished"
	// Only interrupted, twice: listed with the newer attempt.
	attempt(day(20), time.Hour, backup.StateAbandoned, stale)
	newest := attempt(day(20), 2*time.Hour, backup.StateAbandoned, stale)
	// Only interrupted, once.
	single := attempt(day(21), 3*time.Hour, backup.StateAbandoned, stale)
	// Interrupted, then succeeded / failed / still running.
	attempt(day(22), 4*time.Hour, backup.StateAbandoned, stale)
	attempt(day(22), 5*time.Hour, backup.StateSucceeded, "")
	attempt(day(23), 6*time.Hour, backup.StateAbandoned, stale)
	attempt(day(23), 7*time.Hour, backup.StateFailed, "")
	// The backup a restore came from.
	attempt(day(24), 8*time.Hour, backup.StateAbandoned, backup.ReasonRestored)
	// The slot given as the bound is not before it.
	attempt(day(30), 9*time.Hour, backup.StateAbandoned, stale)
	// A manual attempt has no slot.
	attempt(time.Time{}, 10*time.Hour, backup.StateAbandoned, stale)
	attempt(day(25), 11*time.Hour, backup.StateAbandoned, stale)
	attempt(day(25), 12*time.Hour, "", "") // still running

	got, err := store.InterruptedSlots(ctx, day(30))
	if err != nil {
		t.Fatal(err)
	}
	want := []backup.InterruptedSlot{{Slot: day(20), NewestAttempt: newest}, {Slot: day(21), NewestAttempt: single}}
	if !slices.EqualFunc(got, want, func(a, b backup.InterruptedSlot) bool {
		return a.Slot.Equal(b.Slot) && a.NewestAttempt == b.NewestAttempt
	}) {
		t.Fatalf("InterruptedSlots = %+v, want %+v", got, want)
	}

	at := day(31)
	if marked, err := store.MarkNotified(ctx, newest, at); err != nil || !marked {
		t.Fatalf("MarkNotified = %v %v, want marked", marked, err)
	}
	if marked, err := store.MarkNotified(ctx, newest, at.Add(time.Minute)); err != nil || marked {
		t.Errorf("MarkNotified a second time = %v %v, want not marked", marked, err)
	}
	var notified time.Time
	if err := admin.Pool().QueryRow(ctx, `SELECT notified_at FROM backup_runs WHERE id = $1`, newest).Scan(&notified); err != nil || !notified.Equal(at) {
		t.Errorf("notified_at = %v %v, want %v", notified, err, at)
	}
	got, err = store.InterruptedSlots(ctx, day(30))
	if err != nil || len(got) != 1 || got[0].NewestAttempt != single {
		t.Errorf("InterruptedSlots after marking = %+v %v, want only the 21st", got, err)
	}
}
