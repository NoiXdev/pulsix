package cronexpr_test

import (
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/cronexpr"
)

func loc(t *testing.T, name string) *time.Location {
	t.Helper()
	l, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func mustParse(t *testing.T, spec string) cronexpr.Expr {
	t.Helper()
	e, err := cronexpr.Parse(spec)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// fmtAll renders instants in RFC 3339 with their zone offset, so a test
// failure shows which of two same-wall-clock instants was chosen.
func fmtAll(ts []time.Time, l *time.Location) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.In(l).Format(time.RFC3339)
	}
	return out
}

func TestNextN(t *testing.T) {
	berlin := loc(t, "Europe/Berlin")
	tokyo := loc(t, "Asia/Tokyo")
	cases := []struct {
		name string
		spec string
		loc  *time.Location
		from time.Time
		want []string
	}{
		{
			// 2026-03-29 02:00 CET jumps to 03:00 CEST: 02:30 does not exist
			// and runs at the first valid minute after the gap.
			name: "spring forward runs at the first valid minute",
			spec: "30 2 * * *", loc: berlin,
			from: time.Date(2026, 3, 28, 12, 0, 0, 0, berlin),
			want: []string{"2026-03-29T03:00:00+02:00", "2026-03-30T02:30:00+02:00"},
		},
		{
			name: "spring forward collapses a whole skipped hour into one run",
			spec: "*/15 * * * *", loc: berlin,
			from: time.Date(2026, 3, 29, 1, 50, 0, 0, berlin),
			want: []string{"2026-03-29T03:00:00+02:00", "2026-03-29T03:15:00+02:00"},
		},
		{
			// 2026-10-25 03:00 CEST falls back to 02:00 CET: 02:30 happens
			// twice and runs once, at its first occurrence.
			name: "fall back runs once at the first occurrence",
			spec: "30 2 * * *", loc: berlin,
			from: time.Date(2026, 10, 24, 12, 0, 0, 0, berlin),
			want: []string{"2026-10-25T02:30:00+02:00", "2026-10-26T02:30:00+01:00"},
		},
		{
			name: "fall back does not replay the repeated hour",
			spec: "*/30 * * * *", loc: berlin,
			from: time.Date(2026, 10, 25, 1, 50, 0, 0, berlin),
			want: []string{"2026-10-25T02:00:00+02:00", "2026-10-25T02:30:00+02:00", "2026-10-25T03:00:00+01:00"},
		},
		{
			// 2026-03-08 02:00 EST jumps to 03:00 EDT: a zone west of UTC,
			// where time.Date normalizes a gap wall time backwards (into
			// the period before the gap) rather than forwards.
			name: "spring forward west of UTC runs at the first valid minute",
			spec: "30 2 * * *", loc: loc(t, "America/New_York"),
			from: time.Date(2026, 3, 7, 12, 0, 0, 0, loc(t, "America/New_York")),
			want: []string{"2026-03-08T03:00:00-04:00", "2026-03-09T02:30:00-04:00"},
		},
		{
			// 2026-11-01 02:00 EDT falls back to 01:00 EST: 01:30 happens
			// twice and runs once, at its first occurrence.
			name: "fall back west of UTC runs once at the first occurrence",
			spec: "30 1 * * *", loc: loc(t, "America/New_York"),
			from: time.Date(2026, 10, 31, 12, 0, 0, 0, loc(t, "America/New_York")),
			want: []string{"2026-11-01T01:30:00-04:00", "2026-11-02T01:30:00-05:00"},
		},
		{
			// 2026-09-06 00:00 -03 jumps to 01:00 -03 in the southern
			// hemisphere's spring (a gap at midnight, west of UTC): 00:30
			// does not exist and runs at the first valid minute after it.
			name: "spring forward at midnight, southern hemisphere, west of UTC",
			spec: "30 0 * * *", loc: loc(t, "America/Santiago"),
			from: time.Date(2026, 9, 5, 12, 0, 0, 0, loc(t, "America/Santiago")),
			want: []string{"2026-09-06T01:00:00-03:00", "2026-09-07T00:30:00-03:00"},
		},
		{
			// 2026-10-04 02:00 +10:30 jumps to 02:30 +11:00: a 30-minute
			// gap, so 02:15 does not exist and runs at 02:30.
			name: "spring forward with a 30-minute shift",
			spec: "15 2 * * *", loc: loc(t, "Australia/Lord_Howe"),
			from: time.Date(2026, 10, 3, 12, 0, 0, 0, loc(t, "Australia/Lord_Howe")),
			want: []string{"2026-10-04T02:30:00+11:00", "2026-10-05T02:15:00+11:00"},
		},
		{
			// 2026-04-05 02:00 +11:00 falls back to 01:30 +10:30: a
			// 30-minute repeat of 01:30-01:59, which does not replay.
			name: "fall back with a 30-minute shift does not replay",
			spec: "*/15 * * * *", loc: loc(t, "Australia/Lord_Howe"),
			from: time.Date(2026, 4, 5, 1, 20, 0, 0, loc(t, "Australia/Lord_Howe")),
			want: []string{"2026-04-05T01:30:00+11:00", "2026-04-05T01:45:00+11:00", "2026-04-05T02:00:00+10:30"},
		},
		{
			name: "day of month OR day of week when both are restricted",
			spec: "0 0 15 * FRI", loc: berlin,
			from: time.Date(2026, 9, 1, 0, 0, 0, 0, berlin),
			want: []string{"2026-09-04T00:00:00+02:00", "2026-09-11T00:00:00+02:00", "2026-09-15T00:00:00+02:00", "2026-09-18T00:00:00+02:00"},
		},
		{
			name: "a star-prefixed day of month leaves day of week alone",
			spec: "0 0 */2 * MON", loc: time.UTC,
			from: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
			want: []string{"2026-09-07T00:00:00Z", "2026-09-14T00:00:00Z"},
		},
		{
			name: "sunday as 7",
			spec: "0 0 * * 7", loc: time.UTC,
			from: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
			want: []string{"2026-09-06T00:00:00Z"},
		},
		{
			name: "weekly macro",
			spec: "@weekly", loc: berlin,
			from: time.Date(2026, 9, 1, 0, 0, 0, 0, berlin),
			want: []string{"2026-09-06T00:00:00+02:00", "2026-09-13T00:00:00+02:00"},
		},
		{
			name: "time zone",
			spec: "0 9 * * 1-5", loc: tokyo,
			from: time.Date(2026, 9, 17, 23, 0, 0, 0, time.UTC), // Fri 08:00 JST
			want: []string{"2026-09-18T09:00:00+09:00", "2026-09-21T09:00:00+09:00"},
		},
		{
			name: "strictly after: an exact match of the reference is skipped",
			spec: "0 * * * *", loc: time.UTC,
			from: time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC),
			want: []string{"2026-09-18T11:00:00Z"},
		},
		{
			name: "seconds of the reference are ignored",
			spec: "* * * * *", loc: time.UTC,
			from: time.Date(2026, 9, 18, 10, 0, 59, 999, time.UTC),
			want: []string{"2026-09-18T10:01:00Z"},
		},
		{
			name: "leap day",
			spec: "0 0 29 2 *", loc: time.UTC,
			from: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			want: []string{"2028-02-29T00:00:00Z"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := fmtAll(mustParse(t, c.spec).NextN(c.from, c.loc, len(c.want)), c.loc)
			for i := range c.want {
				if i >= len(got) || got[i] != c.want[i] {
					t.Fatalf("NextN = %v, want %v", got, c.want)
				}
			}
		})
	}
}

func TestNextNeverFires(t *testing.T) {
	ref := time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC)
	if got := mustParse(t, "0 0 30 2 *").Next(ref, time.UTC); !got.IsZero() {
		t.Fatalf("Feb 30 fired at %v", got)
	}
	if got := mustParse(t, "0 0 30 2 *").NextN(ref, time.UTC, 3); len(got) != 0 {
		t.Fatalf("NextN = %v", got)
	}
}

func TestNextNilLocationIsUTC(t *testing.T) {
	got := mustParse(t, "0 12 * * *").Next(time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC), nil)
	if got.Format(time.RFC3339) != "2026-09-18T12:00:00Z" {
		t.Fatalf("got %v", got)
	}
}

func TestBetween(t *testing.T) {
	e := mustParse(t, "*/10 * * * *")
	from := time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)
	count, latest := e.Between(from, from.Add(35*time.Minute), time.UTC, 1000)
	if count != 4 || !latest.Equal(from.Add(30*time.Minute)) {
		t.Fatalf("Between = %d, %v", count, latest)
	}
	count, _ = e.Between(from, from.Add(24*time.Hour), time.UTC, 5)
	if count != 5 {
		t.Fatalf("capped count = %d, want 5", count)
	}
	if count, latest := e.Between(from.Add(time.Minute), from.Add(2*time.Minute), time.UTC, 10); count != 0 || !latest.IsZero() {
		t.Fatalf("empty window = %d, %v", count, latest)
	}
}

// TestBetweenAtItsCap is backlog 2.60. Between stops counting at max,
// and what it returns then is easy to misread: the count is a FLOOR,
// and `latest` is the max-th occurrence rather than the newest one in
// the range. internal/schedules read it as the newest and decided a
// schedule's fate from how old it was.
func TestBetweenAtItsCap(t *testing.T) {
	e, err := cronexpr.Parse("* * * * *")
	if err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)
	to := from.Add(24 * time.Hour) // 1440 occurrences in range

	count, latest := e.Between(from, to, time.UTC, 10)
	if count != 10 {
		t.Fatalf("count = %d, want the cap 10", count)
	}
	if want := from.Add(9 * time.Minute); !latest.Equal(want) {
		t.Fatalf("latest = %s, want the 10th occurrence %s — NOT the newest in the range", latest, want)
	}
	// Uncapped, the same range gives the real answers.
	count, latest = e.Between(from, to, time.UTC, 10000)
	if count != 1441 || !latest.Equal(to) {
		t.Fatalf("uncapped = %d, %s; want 1441 and %s", count, latest, to)
	}
}

// TestLookback: the newest occurrence at or before `to`, searched no
// further back than `within` — what a caller needs when it only cares
// how recent the newest one is, and the reason Between's cap does not
// have to be raised to answer it.
func TestLookback(t *testing.T) {
	e, err := cronexpr.Parse("* 0-22 * * *") // every minute except hour 23
	if err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, 9, 18, 10, 0, 0, 0, time.UTC)
	at2330 := time.Date(2026, 9, 26, 23, 30, 0, 0, time.UTC)

	// Inside the hole: the newest is 22:59 on the same day.
	got := e.Lookback(from, at2330, time.UTC, 2*time.Hour)
	if want := time.Date(2026, 9, 26, 22, 59, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("Lookback = %s, want %s", got, want)
	}
	// A window too short to contain one reports none rather than
	// guessing.
	if got := e.Lookback(from, at2330, time.UTC, 5*time.Minute); !got.IsZero() {
		t.Fatalf("Lookback over five minutes of a dead hour = %s, want none", got)
	}
	// It never looks earlier than `from`, even when asked to.
	if got := e.Lookback(at2330.Add(-time.Hour), at2330, time.UTC, 30*24*time.Hour); !got.Equal(time.Date(2026, 9, 26, 22, 59, 0, 0, time.UTC)) {
		t.Fatalf("Lookback clamped to from = %s", got)
	}
}
