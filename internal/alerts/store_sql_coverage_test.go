package alerts_test

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/alerts"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/maintenance"
)

// TestLatestCoverageEnd is backlog 4.41's store half: the latest end in
// (from, to] of any stretch covering the host. A stretch that opened and
// closed inside the range counts even though no single instant
// WindowsForHost could be asked about would see it; a stretch outside the
// range, still running at to, or on another host does not. Recurring
// stretches are walked through their own zone, so a range across a DST
// change lands on the right UTC instant. Each case gets its own
// organization, so the cases cannot see each other's windows.
func TestLatestCoverageEnd(t *testing.T) {
	d := testdb.Open(t)
	st := alerts.NewSQLStore(d)
	var org uuid.UUID // the running case's own
	from := time.Date(2026, 1, 5, 10, 0, 0, 0, time.UTC)
	to := from.Add(10 * time.Minute)
	at := func(m int) time.Time { return from.Add(time.Duration(m) * time.Minute) }
	springDay := time.Date(2026, 3, 29, 0, 0, 0, 0, time.UTC)  // Berlin: 01:00Z, 02:00 CET -> 03:00 CEST
	autumnDay := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC) // Berlin: 01:00Z, 03:00 CEST -> 02:00 CET
	oneOff := func(host uuid.UUID, start, end time.Time) {
		oneOffWindow(t, d, org, &host, nil, maintenance.ReasonMaintenance, start, end)
	}
	recurring := func(host uuid.UUID, cron, zone string, dur time.Duration) {
		recurringWindow(t, d, org, &host, maintenance.ReasonMaintenance, cron, zone, dur)
	}

	cases := []struct {
		name     string
		from, to time.Time // zero: the shared from/to
		windows  func(host, other uuid.UUID)
		want     time.Time // zero: none ended in the range
	}{
		{name: "no windows", windows: func(uuid.UUID, uuid.UUID) {}},
		{name: "one-off inside", windows: func(h, _ uuid.UUID) { oneOff(h, at(2), at(3)) }, want: at(3)},
		{name: "one-off straddling from", windows: func(h, _ uuid.UUID) { oneOff(h, at(-60), at(1)) }, want: at(1)},
		{name: "one-off ending at from", windows: func(h, _ uuid.UUID) { oneOff(h, at(-60), at(0)) }},
		{name: "one-off starting at to", windows: func(h, _ uuid.UUID) { oneOff(h, at(10), at(60)) }},
		{name: "one-off ending exactly at to", windows: func(h, _ uuid.UUID) { oneOff(h, at(5), at(10)) }, want: at(10)},
		{name: "one-off still running at to", windows: func(h, _ uuid.UUID) { oneOff(h, at(5), at(20)) }},
		{name: "the latest of several, created last-first", windows: func(h, _ uuid.UUID) {
			oneOff(h, at(6), at(8))
			oneOff(h, at(1), at(2))
		}, want: at(8)},
		{name: "another host's window", windows: func(_, o uuid.UUID) { oneOff(o, at(2), at(3)) }},
		{name: "recurring, several stretches", windows: func(h, _ uuid.UUID) {
			recurring(h, "*/4 10 * * *", "UTC", time.Minute) // 10:00, 10:04, 10:08
		}, want: at(9)},
		{name: "recurring stretch straddling from", windows: func(h, _ uuid.UUID) {
			recurring(h, "58 9 * * *", "UTC", 5*time.Minute) // [09:58, 10:03)
		}, want: at(3)},
		{name: "recurring, last stretch still running at to", windows: func(h, _ uuid.UUID) {
			recurring(h, "*/4 10 * * *", "UTC", 3*time.Minute) // ..., [10:04, 10:07), [10:08, 10:11)
		}, want: at(7)},
		{name: "recurring across spring-forward", from: springDay, to: springDay.Add(3 * time.Hour),
			windows: func(h, _ uuid.UUID) {
				recurring(h, "30 3 * * *", "Europe/Berlin", 10*time.Minute) // 03:30 CEST = 01:30Z
			}, want: springDay.Add(time.Hour + 40*time.Minute)},
		{name: "recurring across fall-back", from: autumnDay, to: autumnDay.Add(3 * time.Hour),
			windows: func(h, _ uuid.UUID) {
				recurring(h, "30 3 * * *", "Europe/Berlin", 10*time.Minute) // 03:30 CET = 02:30Z
			}, want: autumnDay.Add(2*time.Hour + 40*time.Minute)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var host uuid.UUID
			org, host = seedHost(t, d)
			other := seedAdditionalHost(t, d, org)
			tc.windows(host, other)
			f, tt := from, to
			if !tc.from.IsZero() {
				f, tt = tc.from, tc.to
			}
			got, ok, err := st.LatestCoverageEnd(context.Background(), org, host, f, tt)
			if err != nil {
				t.Fatalf("LatestCoverageEnd: %v", err)
			}
			if tc.want.IsZero() {
				if ok {
					t.Fatalf("LatestCoverageEnd = %s, want none", got.UTC())
				}
				return
			}
			if !ok || !got.Equal(tc.want) {
				t.Fatalf("LatestCoverageEnd = %s, %v; want %s", got.UTC(), ok, tc.want)
			}
		})
	}
}

// TestLatestCoverageEndPagesPastOneCall: a recurring window with many more
// stretches in the range than one maintenance.Occurrences page must still
// yield the latest end — the one the escalation anchor reads.
func TestLatestCoverageEndPagesPastOneCall(t *testing.T) {
	d := testdb.Open(t)
	org, host := seedHost(t, d)
	st := alerts.NewSQLStore(d)
	from := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)
	to := from.Add(24 * time.Hour)
	recurringWindow(t, d, org, &host, maintenance.ReasonMaintenance, "*/2 * * * *", "UTC", time.Minute) // 720 a day

	got, ok, err := st.LatestCoverageEnd(context.Background(), org, host, from, to)
	if err != nil {
		t.Fatalf("LatestCoverageEnd: %v", err)
	}
	if want := to.Add(-time.Minute); !ok || !got.Equal(want) {
		t.Fatalf("LatestCoverageEnd = %s, %v; want %s", got.UTC(), ok, want)
	}
}

// TestLatestCoverageEndLogsAWindowItCannotEvaluate keeps WindowsForHost's
// best-effort contract (backlog 4.29) for the range form: a broken row is
// logged by id and skipped, the healthy one still answers.
func TestLatestCoverageEndLogsAWindowItCannotEvaluate(t *testing.T) {
	d := testdb.Open(t)
	org, host := seedHost(t, d)
	st := alerts.NewSQLStore(d)
	from := time.Date(2026, 1, 1, 11, 0, 0, 0, time.UTC)
	broken := recurringWindow(t, d, org, &host, maintenance.ReasonSilence, "not a cron", "UTC", time.Hour)
	_ = recurringWindow(t, d, org, &host, maintenance.ReasonMaintenance, "0 12 * * *", "UTC", time.Hour)

	logs := &lockedBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	got, ok, err := st.LatestCoverageEnd(context.Background(), org, host, from, from.Add(3*time.Hour))
	if err != nil {
		t.Fatalf("LatestCoverageEnd must not fail over a bad row: %v", err)
	}
	if want := from.Add(2 * time.Hour); !ok || !got.Equal(want) {
		t.Fatalf("LatestCoverageEnd = %s, %v; want the healthy window's end %s", got.UTC(), ok, want)
	}
	if !strings.Contains(logs.String(), broken.String()) {
		t.Fatalf("log does not name the broken window %s:\n%s", broken, logs.String())
	}
}
