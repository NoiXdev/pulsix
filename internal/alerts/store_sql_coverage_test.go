package alerts_test

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/alerts"
	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/testdb"
	"github.com/noixdev/pulsix/internal/maintenance"
)

// longAgo is the creation instant every coverage fixture gets unless a
// case says otherwise: well before any stretch these tests ask about, so
// only the cases that are about creation time see it at all.
var longAgo = time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

// createdAt moves a window's created_at, which the fixtures' INSERT
// otherwise leaves at the database's wall-clock now().
func createdAt(t *testing.T, d *db.DB, window uuid.UUID, at time.Time) {
	t.Helper()
	if _, err := d.Pool().Exec(context.Background(),
		`UPDATE maintenance_windows SET created_at = $2, updated_at = $2 WHERE id = $1`, window, at); err != nil {
		t.Fatalf("set created_at: %v", err)
	}
}

// coverageTarget is one case's organization and hosts: host is asked
// about, other is a second host in the same organization, and root is an
// inventory group host descends into through a child group it belongs to.
type coverageTarget struct {
	org, host, other, root uuid.UUID
}

// TestLatestCoverageEnd is backlog 4.41's store half: the latest end in
// (from, to] of any stretch covering the host. A stretch that opened and
// closed inside the range counts even though no single instant
// WindowsForHost could be asked about would see it; a stretch outside the
// range, still running at to, on another host, or over before its window
// was even created does not. Every target kind the candidate query
// resolves takes part in the one maximum. Recurring stretches are walked
// through their own zone. Each case gets its own organization, so the
// cases cannot see each other's windows.
func TestLatestCoverageEnd(t *testing.T) {
	d := testdb.Open(t)
	st := alerts.NewSQLStore(d)
	from := time.Date(2026, 1, 5, 10, 0, 0, 0, time.UTC)
	to := from.Add(10 * time.Minute)
	at := func(m int) time.Time { return from.Add(time.Duration(m) * time.Minute) }
	springDay := time.Date(2026, 3, 29, 0, 0, 0, 0, time.UTC)  // Berlin: at 01:00Z, 02:00 CET -> 03:00 CEST
	autumnDay := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC) // Berlin: at 01:00Z, 03:00 CEST -> 02:00 CET

	// Window fixtures, created at `created`; nil host and group means an
	// organization-wide window.
	oneOff := func(c coverageTarget, host, group *uuid.UUID, start, end, created time.Time) {
		createdAt(t, d, oneOffWindow(t, d, c.org, host, group, maintenance.ReasonMaintenance, start, end), created)
	}
	recurring := func(c coverageTarget, host *uuid.UUID, cron, zone string, dur time.Duration, created time.Time) {
		createdAt(t, d, recurringWindow(t, d, c.org, host, maintenance.ReasonMaintenance, cron, zone, dur), created)
	}
	onHost := func(start, end time.Time) func(c coverageTarget) {
		return func(c coverageTarget) { oneOff(c, &c.host, nil, start, end, longAgo) }
	}
	recurringOnHost := func(cron, zone string, dur time.Duration) func(c coverageTarget) {
		return func(c coverageTarget) { recurring(c, &c.host, cron, zone, dur, longAgo) }
	}

	cases := []struct {
		name     string
		from, to time.Time // zero: the shared from/to
		windows  func(c coverageTarget)
		want     time.Time // zero: none ended in the range
	}{
		{name: "no windows", windows: func(coverageTarget) {}},
		{name: "one-off inside", windows: onHost(at(2), at(3)), want: at(3)},
		{name: "one-off straddling from", windows: onHost(at(-60), at(1)), want: at(1)},
		{name: "one-off ending at from", windows: onHost(at(-60), at(0))},
		{name: "one-off starting at to", windows: onHost(at(10), at(60))},
		{name: "one-off ending exactly at to", windows: onHost(at(5), at(10)), want: at(10)},
		{name: "one-off still running at to", windows: onHost(at(5), at(20))},
		{name: "the latest of several, created last-first", windows: func(c coverageTarget) {
			onHost(at(6), at(8))(c)
			onHost(at(1), at(2))(c)
		}, want: at(8)},
		{name: "another host's window", windows: func(c coverageTarget) {
			oneOff(c, &c.other, nil, at(2), at(3), longAgo)
		}},

		// Target kinds (final review Minor 4): the candidate query's three
		// arms, and one maximum across all of them.
		{name: "organization window", windows: func(c coverageTarget) {
			oneOff(c, nil, nil, at(2), at(3), longAgo)
		}, want: at(3)},
		{name: "ancestor inventory group window", windows: func(c coverageTarget) {
			oneOff(c, nil, &c.root, at(2), at(4), longAgo)
		}, want: at(4)},
		{name: "a mix of kinds: the group's end is the latest", windows: func(c coverageTarget) {
			oneOff(c, nil, nil, at(1), at(3), longAgo)                       // org, ends 10:03
			recurring(c, nil, "2 10 * * *", "UTC", time.Minute, longAgo)     // org, ends 10:03
			oneOff(c, nil, &c.root, at(6), at(8), longAgo)                   // group, ends 10:08
			oneOff(c, &c.host, nil, at(4), at(5), longAgo)                   // host, ends 10:05
			recurring(c, &c.host, "0 10 * * *", "UTC", time.Minute, longAgo) // host, ends 10:01
		}, want: at(8)},

		{name: "recurring, several stretches", windows: recurringOnHost("*/4 10 * * *", "UTC", time.Minute), want: at(9)},                  // 10:00, 10:04, 10:08
		{name: "recurring stretch straddling from", windows: recurringOnHost("58 9 * * *", "UTC", 5*time.Minute), want: at(3)},             // [09:58, 10:03)
		{name: "recurring, last stretch still running at to", windows: recurringOnHost("*/4 10 * * *", "UTC", 3*time.Minute), want: at(7)}, // ..., [10:04, 10:07), [10:08, 10:11)

		// Created after the fact (final review Important 1): a stretch that
		// ended before its window existed muted nothing, so it holds
		// nothing back; a window created mid-stretch counts for the rest.
		{name: "back-dated one-off, created after it ended", windows: func(c coverageTarget) {
			oneOff(c, &c.host, nil, at(2), at(3), at(5))
		}},
		{name: "one-off created mid-stretch", windows: func(c coverageTarget) {
			oneOff(c, &c.host, nil, at(1), at(4), at(2))
		}, want: at(4)},
		{name: "new recurring window, today's stretch already over", windows: func(c coverageTarget) {
			recurring(c, &c.host, "0 10 * * *", "UTC", 3*time.Minute, at(5)) // [10:00, 10:03), created 10:05
		}},

		// DST, the UTC offset on each side of the change.
		{name: "recurring after spring-forward", from: springDay, to: springDay.Add(3 * time.Hour),
			windows: recurringOnHost("30 3 * * *", "Europe/Berlin", 10*time.Minute), // 03:30 CEST = 01:30Z
			want:    springDay.Add(time.Hour + 40*time.Minute)},
		{name: "recurring after fall-back", from: autumnDay, to: autumnDay.Add(3 * time.Hour),
			windows: recurringOnHost("30 3 * * *", "Europe/Berlin", 10*time.Minute), // 03:30 CET = 02:30Z
			want:    autumnDay.Add(2*time.Hour + 40*time.Minute)},
		// DST, a local time inside the change itself (final review Minor 3),
		// pinning what internal/cronexpr does there rather than choosing it:
		// on spring-forward 02:30 does not exist, and the stretch starts at
		// the first instant after the gap, 03:00 CEST = 01:00Z; on fall-back
		// 02:30 happens twice, and only the first, 02:30 CEST = 00:30Z,
		// starts a stretch — the repeated 02:30 CET (01:30Z) does not.
		{name: "recurring at a skipped local time", from: springDay, to: springDay.Add(3 * time.Hour),
			windows: recurringOnHost("30 2 * * *", "Europe/Berlin", 10*time.Minute),
			want:    springDay.Add(time.Hour + 10*time.Minute)},
		{name: "recurring at a repeated local time", from: autumnDay, to: autumnDay.Add(3 * time.Hour),
			windows: recurringOnHost("30 2 * * *", "Europe/Berlin", 10*time.Minute),
			want:    autumnDay.Add(40 * time.Minute)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var c coverageTarget
			c.org, c.host = seedHost(t, d)
			c.other = seedAdditionalHost(t, d, c.org)
			c.root = seedInventoryGroup(t, d, c.org, "root", nil)
			leaf := seedInventoryGroup(t, d, c.org, "leaf", &c.root)
			addHostToGroup(t, d, c.org, leaf, c.host)
			tc.windows(c)
			f, tt := from, to
			if !tc.from.IsZero() {
				f, tt = tc.from, tc.to
			}
			got, ok, err := st.LatestCoverageEnd(context.Background(), c.org, c.host, f, tt)
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
	w := recurringWindow(t, d, org, &host, maintenance.ReasonMaintenance, "*/2 * * * *", "UTC", time.Minute) // 720 a day
	createdAt(t, d, w, longAgo)

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
	good := recurringWindow(t, d, org, &host, maintenance.ReasonMaintenance, "0 12 * * *", "UTC", time.Hour)
	createdAt(t, d, broken, longAgo)
	createdAt(t, d, good, longAgo)

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
