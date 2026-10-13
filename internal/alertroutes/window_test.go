package alertroutes

import (
	"testing"
	"time"

	"github.com/noixdev/pulsix/internal/cronexpr"
)

func mustParseCron(t *testing.T, spec string) cronexpr.Expr {
	t.Helper()
	expr, err := cronexpr.Parse(spec)
	if err != nil {
		t.Fatalf("parse %q: %v", spec, err)
	}
	return expr
}

// minOccurrenceGap walks a cron's own occurrences to find its smallest gap,
// and validateWindow calls it on every route create and update. Over a full
// year's horizon "* * * * *" has 527 040 of them: walking all of them costs
// about 235 ms of a request that does nothing else expensive, and over 3 s
// under -race, the suite this package is actually run in (whole-branch
// review, Critical #1's second half). The walk is bounded by a probe cap;
// the answer must not change, because the cap only ever clips gaps longer
// than ones already seen.
func TestMinOccurrenceGapIsBoundedOnADenseCron(t *testing.T) {
	start := time.Now()
	gap, err := minOccurrenceGap(mustParseCron(t, "* * * * *"), time.UTC, windowEpoch)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("minOccurrenceGap: %v", err)
	}
	if gap != time.Minute {
		t.Errorf("the smallest gap of a once-a-minute cron = %s, want 1m", gap)
	}
	// Generous against the bounded walk (tens of milliseconds under -race)
	// and far under the unbounded one it replaces.
	if elapsed > time.Second {
		t.Errorf("measuring a once-a-minute cron's own gap took %s, want well under a second", elapsed)
	}
}

// The cap must not change any answer the unbounded walk already gave. Each
// case below is one the probe cap could plausibly have clipped — the
// unevenly spaced pattern whose smaller gap the anchor never surfaces
// first, the one whose true minimum sits at a day boundary rather than
// inside a day, and the annual-or-rarer cron that has no second occurrence
// to compare against at all.
func TestMinOccurrenceGapAnswersAreUnchangedByTheProbeCap(t *testing.T) {
	for _, tc := range []struct {
		spec string
		want time.Duration
	}{
		{"0 9 * * 1,5", 72 * time.Hour},  // Mondays and Fridays: Fri->Mon, not Mon->Fri
		{"0,59 0,23 * * *", time.Minute}, // the minimum is 23:59 -> 00:00, across a day
		{"*/5 * * * *", 5 * time.Minute}, // dense enough to be clipped, evenly spaced
		{"0 0 29 2 *", minGapHorizon},    // one occurrence inside the horizon at most
		{"0 9 * * *", 24 * time.Hour},    // the ordinary daily window
	} {
		gap, err := minOccurrenceGap(mustParseCron(t, tc.spec), time.UTC, windowEpoch)
		if err != nil {
			t.Errorf("%s: %v", tc.spec, err)
			continue
		}
		if gap != tc.want {
			t.Errorf("%s: smallest gap = %s, want %s", tc.spec, gap, tc.want)
		}
	}
}
