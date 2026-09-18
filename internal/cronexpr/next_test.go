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
	if got := mustParse(t, "0 0 30 2 *").Next(time.Now(), time.UTC); !got.IsZero() {
		t.Fatalf("Feb 30 fired at %v", got)
	}
	if got := mustParse(t, "0 0 30 2 *").NextN(time.Now(), time.UTC, 3); len(got) != 0 {
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
