package notify_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/notify"
)

func TestDigestStoreTickAndOutbox(t *testing.T) {
	w := newOutboxWorld(t, "digest-flow")
	scoped := db.WithOrgScope(ctx, w.org)
	store := notify.NewSQLStore(w.d)
	now := time.Date(2026, 9, 22, 8, 0, 0, 0, time.UTC)
	svc := notify.NewService(notify.Deps{Channels: store, Digests: store, Now: func() time.Time { return now }})
	set, err := svc.SetDigest(scoped, w.org, notify.DigestInput{Enabled: true, Weekday: "mon", Time: "08:00", Timezone: "UTC", Channels: []uuid.UUID{w.chA.ID, w.chDisabled.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if len(set.Channels) != 2 || !set.NextRunAt.Equal(time.Date(2026, 9, 28, 8, 0, 0, 0, time.UTC)) {
		t.Fatalf("stored = %+v", set)
	}
	if _, err := svc.SetDigest(scoped, w.org, notify.DigestInput{Enabled: true, Weekday: "mon", Time: "08:00", Timezone: "UTC", Channels: []uuid.UUID{uuid.Must(uuid.NewV7())}}); err == nil {
		t.Fatal("an unknown channel was accepted")
	}
	// Not due yet.
	due := now.AddDate(0, 0, 6) // Monday 28 Sep 08:00
	ticker := func(at time.Time, b notify.DigestBuilder) *notify.DigestTicker {
		return notify.NewDigestTicker(notify.NewDigestSQLStore(w.d, quiet()), b, w.outbox, time.Minute, func() time.Time { return at }, nil, quiet())
	}
	week := notify.DigestSummary{Schedules: []notify.DigestSchedule{{Name: "Nightly", Runs: 2}}, Slowest: []notify.DigestRun{{Schedule: "Nightly", RunID: uuid.Must(uuid.NewV7()), DurationSeconds: 60}}}
	if n, _ := ticker(due.Add(-time.Minute), fixedBuilder{sum: week}).Tick(ctx); n != 0 {
		t.Fatal("a digest was due before its time")
	}
	if n, err := ticker(due.Add(30*time.Second), fixedBuilder{sum: week}).Tick(ctx); err != nil || n != 1 {
		t.Fatalf("Tick = %d, %v", n, err)
	}
	got := w.queued(t)
	if len(got) != 1 || got[0].channel != w.chA.ID || got[0].key != "digest:2026-09-28T08:00:00Z" || got[0].ev.Kind != notify.EventDigest {
		t.Fatalf("queued = %+v, want one digest for the enabled channel A", got)
	}
	if got[0].ev.Digest.Timezone != "UTC" || got[0].ev.Digest.Slowest[0].URL != "https://pulsix.example.test/o/digest-flow/runs/"+week.Slowest[0].RunID.String() {
		t.Fatalf("digest = %+v", got[0].ev.Digest)
	}
	after, err := svc.Digest(scoped, w.org)
	if err != nil || !after.NextRunAt.Equal(time.Date(2026, 10, 5, 8, 0, 0, 0, time.UTC)) {
		t.Fatalf("next after the send = %+v, %v", after.NextRunAt, err)
	}
	var period string
	if err := w.d.Pool().QueryRow(ctx, `SELECT last_period FROM notification_digests WHERE org_id = $1`, w.org).Scan(&period); err != nil || period != "digest:2026-09-28T08:00:00Z" {
		t.Fatalf("last_period = %q, %v", period, err)
	}
	// A builder failure leaves the next digest due.
	if _, err := ticker(time.Date(2026, 10, 5, 8, 0, 30, 0, time.UTC), fixedBuilder{err: true}).Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if still, _ := svc.Digest(scoped, w.org); !still.NextRunAt.Equal(time.Date(2026, 10, 5, 8, 0, 0, 0, time.UTC)) {
		t.Fatalf("a failed digest advanced to %s", still.NextRunAt)
	}
	// Settings that no longer evaluate disable the digest — and, since the
	// timezone is checked before the digest is even built (fix round 1,
	// Minor #2), no phantom message is queued for the broken row either.
	queuedBefore := len(w.queued(t))
	if _, err := w.d.Pool().Exec(ctx, `UPDATE notification_digests SET timezone = 'Mars/Olympus' WHERE org_id = $1`, w.org); err != nil {
		t.Fatal(err)
	}
	if _, err := ticker(time.Date(2026, 10, 5, 8, 1, 0, 0, time.UTC), fixedBuilder{sum: week}).Tick(ctx); err != nil {
		t.Fatal(err)
	}
	var enabled bool
	if err := w.d.Pool().QueryRow(ctx, `SELECT enabled FROM notification_digests WHERE org_id = $1`, w.org).Scan(&enabled); err != nil || enabled {
		t.Fatalf("enabled = %v, %v; want the broken digest disabled", enabled, err)
	}
	if got := len(w.queued(t)); got != queuedBefore {
		t.Fatalf("queued = %d, want %d; a digest whose timezone no longer evaluates must not be sent", got, queuedBefore)
	}
}

// TestDigestLateDigestAdvancesWithoutSending is fix round 1, Minor #4: a
// digest more than maxDigestLate (24h) overdue is skipped by Tick's
// handle closure before it ever reaches the builder or the outbox, but
// processOne still must advance next_run_at — otherwise the same stale
// occurrence stays due forever and every tick re-skips it.
func TestDigestLateDigestAdvancesWithoutSending(t *testing.T) {
	w := newOutboxWorld(t, "digest-late")
	scoped := db.WithOrgScope(ctx, w.org)
	store := notify.NewSQLStore(w.d)
	setAt := time.Date(2026, 9, 22, 8, 0, 0, 0, time.UTC)
	svc := notify.NewService(notify.Deps{Channels: store, Digests: store, Now: func() time.Time { return setAt }})
	if _, err := svc.SetDigest(scoped, w.org, notify.DigestInput{Enabled: true, Weekday: "mon", Time: "08:00", Timezone: "UTC", Channels: []uuid.UUID{w.chA.ID}}); err != nil {
		t.Fatal(err)
	}
	due := time.Date(2026, 9, 28, 8, 0, 0, 0, time.UTC) // the digest's next_run_at
	// now is 49h after due — more than maxDigestLate (24h) — so this
	// occurrence must be skipped, not sent 49h late.
	now := due.Add(49 * time.Hour)
	week := notify.DigestSummary{Schedules: []notify.DigestSchedule{{Name: "Nightly", Runs: 1}}}
	tk := notify.NewDigestTicker(notify.NewDigestSQLStore(w.d, quiet()), fixedBuilder{sum: week}, w.outbox, time.Minute, func() time.Time { return now }, nil, quiet())
	if n, err := tk.Tick(ctx); err != nil || n != 1 {
		t.Fatalf("Tick = %d, %v", n, err)
	}
	if got := w.queued(t); len(got) != 0 {
		t.Fatalf("queued = %+v, want nothing for a digest 49h late", got)
	}
	after, err := svc.Digest(scoped, w.org)
	if err != nil {
		t.Fatal(err)
	}
	// The stale occurrence must not repeat: the row is advanced to the
	// next Monday 08:00 UTC strictly after now (Wed 30 Sep 09:00 UTC), not
	// left pointing at the skipped due time — which would re-skip forever.
	want := time.Date(2026, 10, 5, 8, 0, 0, 0, time.UTC)
	if after.NextRunAt == nil || !after.NextRunAt.Equal(want) {
		t.Fatalf("next_run_at = %v, want %s", after.NextRunAt, want)
	}
}

var errBuild = errors.New("db down")

type fixedBuilder struct {
	sum notify.DigestSummary
	err bool
}

func (b fixedBuilder) Digest(context.Context, uuid.UUID, time.Time, time.Time) (notify.DigestSummary, error) {
	if b.err {
		return notify.DigestSummary{}, errBuild
	}
	return b.sum, nil
}
