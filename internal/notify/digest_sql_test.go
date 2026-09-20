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
	// Settings that no longer evaluate disable the digest.
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
