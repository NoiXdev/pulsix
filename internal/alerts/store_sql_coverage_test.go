package alerts_test

import (
	"context"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/alerts"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/maintenance"
)

// TestWindowsForHostBetweenReturnsEveryStretchOverlappingTheRange is
// backlog 4.41's store half: a stretch that opened and closed inside the
// range is returned even though no single instant WindowsForHost could be
// asked about would see it; a stretch outside the range, or on another
// host, is not; and a recurring window yields each of its stretches in
// the range, not only the active one.
func TestWindowsForHostBetweenReturnsEveryStretchOverlappingTheRange(t *testing.T) {
	d := testdb.Open(t)
	org, host := seedHost(t, d)
	otherHost := seedAdditionalHost(t, d, org)
	st := alerts.NewSQLStore(d)
	from := time.Date(2026, 1, 5, 10, 0, 0, 0, time.UTC)
	to := from.Add(10 * time.Minute)

	inside := oneOffWindow(t, d, org, &host, nil, maintenance.ReasonMaintenance, from.Add(2*time.Minute), from.Add(3*time.Minute))
	straddling := oneOffWindow(t, d, org, nil, nil, maintenance.ReasonSilence, from.Add(-time.Hour), from.Add(time.Minute))
	_ = oneOffWindow(t, d, org, &host, nil, maintenance.ReasonMaintenance, from.Add(-time.Hour), from)                // ends at from
	_ = oneOffWindow(t, d, org, &host, nil, maintenance.ReasonMaintenance, to, to.Add(time.Hour))                     // starts at to
	_ = oneOffWindow(t, d, org, &otherHost, nil, maintenance.ReasonMaintenance, from, to)                             // another host
	recurring := recurringWindow(t, d, org, &host, maintenance.ReasonMaintenance, "*/4 10 * * *", "UTC", time.Minute) // 10:00, 10:04, 10:08

	got, err := st.WindowsForHostBetween(context.Background(), org, host, from, to)
	if err != nil {
		t.Fatalf("WindowsForHostBetween: %v", err)
	}
	type stretch struct {
		id         uuid.UUID
		start, end time.Time
	}
	want := []stretch{
		{straddling, from.Add(-time.Hour), from.Add(time.Minute)},
		{inside, from.Add(2 * time.Minute), from.Add(3 * time.Minute)},
		{recurring, from, from.Add(time.Minute)},
		{recurring, from.Add(4 * time.Minute), from.Add(5 * time.Minute)},
		{recurring, from.Add(8 * time.Minute), from.Add(9 * time.Minute)},
	}
	var have []stretch
	for _, o := range got {
		have = append(have, stretch{o.WindowID, o.Start.UTC(), o.End.UTC()})
	}
	cmp := func(a, b stretch) int {
		if c := a.start.Compare(b.start); c != 0 {
			return c
		}
		return strings.Compare(a.id.String(), b.id.String())
	}
	slices.SortFunc(want, cmp)
	slices.SortFunc(have, cmp)
	if !slices.Equal(have, want) {
		t.Fatalf("stretches =\n  %+v\nwant\n  %+v", have, want)
	}
}

// TestWindowsForHostBetweenPagesPastOneCall: a recurring window with more
// stretches in the range than one maintenance.Occurrences call returns
// must still yield the latest ones — they are the ones the escalation
// anchor reads.
func TestWindowsForHostBetweenPagesPastOneCall(t *testing.T) {
	d := testdb.Open(t)
	org, host := seedHost(t, d)
	st := alerts.NewSQLStore(d)
	from := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)
	to := from.Add(24 * time.Hour)
	_ = recurringWindow(t, d, org, &host, maintenance.ReasonMaintenance, "*/2 * * * *", "UTC", time.Minute) // 720 a day

	got, err := st.WindowsForHostBetween(context.Background(), org, host, from, to)
	if err != nil {
		t.Fatalf("WindowsForHostBetween: %v", err)
	}
	if len(got) != 720 {
		t.Fatalf("got %d stretches, want 720", len(got))
	}
	last := slices.MaxFunc(got, func(a, b maintenance.Occurrence) int { return a.End.Compare(b.End) })
	if wantEnd := to.Add(-time.Minute); !last.End.Equal(wantEnd) {
		t.Fatalf("latest stretch ends at %s, want %s", last.End, wantEnd)
	}
}

// TestWindowsForHostBetweenLogsAWindowItCannotEvaluate keeps WindowsForHost's
// best-effort contract (backlog 4.29) for the range form: a broken row is
// logged by id and skipped, the healthy one still answers.
func TestWindowsForHostBetweenLogsAWindowItCannotEvaluate(t *testing.T) {
	d := testdb.Open(t)
	org, host := seedHost(t, d)
	st := alerts.NewSQLStore(d)
	from := time.Date(2026, 1, 1, 11, 0, 0, 0, time.UTC)
	broken := recurringWindow(t, d, org, &host, maintenance.ReasonSilence, "not a cron", "UTC", time.Hour)
	good := recurringWindow(t, d, org, &host, maintenance.ReasonMaintenance, "0 12 * * *", "UTC", time.Hour)

	logs := &lockedBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	got, err := st.WindowsForHostBetween(context.Background(), org, host, from, from.Add(3*time.Hour))
	if err != nil {
		t.Fatalf("WindowsForHostBetween must not fail over a bad row: %v", err)
	}
	if len(got) != 1 || got[0].WindowID != good {
		t.Fatalf("WindowsForHostBetween = %+v, want only the healthy window %s", got, good)
	}
	if !strings.Contains(logs.String(), broken.String()) {
		t.Fatalf("log does not name the broken window %s:\n%s", broken, logs.String())
	}
}
