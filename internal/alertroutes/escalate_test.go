package alertroutes

import (
	"testing"
	"time"
)

func step(pos int, d time.Duration) Step { return Step{Position: pos, Delay: d} }

func TestDueStep(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 3, 1, 2, 0, 0, 0, time.UTC)

	cases := []struct {
		name string
		cs   ChainState
		now  time.Time
		want int // position, or -1 for "nothing due"
	}{
		{
			name: "the base has not fired, so no rung is due",
			cs:   ChainState{Steps: []Step{step(1, 5*time.Minute)}, FiredUpTo: -1},
			now:  base.Add(time.Hour),
			want: -1,
		},
		{
			name: "step 1 waits out its delay after the base",
			cs:   ChainState{Steps: []Step{step(1, 5*time.Minute)}, FiredUpTo: 0, LastFiredAt: base},
			now:  base.Add(4 * time.Minute),
			want: -1,
		},
		{
			name: "step 1 is due once the delay has passed",
			cs:   ChainState{Steps: []Step{step(1, 5*time.Minute)}, FiredUpTo: 0, LastFiredAt: base},
			now:  base.Add(5 * time.Minute),
			want: 1,
		},
		{
			// One rung at a time: even hours late, step 2 may not jump
			// ahead of step 1. This is what makes a pile-up impossible.
			name: "only the next rung is due, never two at once",
			cs: ChainState{
				Steps:     []Step{step(1, 5*time.Minute), step(2, 5*time.Minute)},
				FiredUpTo: 0, LastFiredAt: base,
			},
			now:  base.Add(6 * time.Hour),
			want: 1,
		},
		{
			name: "the ladder ends when its last rung has fired",
			cs: ChainState{
				Steps:     []Step{step(1, 5*time.Minute)},
				FiredUpTo: 1, LastFiredAt: base,
			},
			now:  base.Add(6 * time.Hour),
			want: -1,
		},
		{
			// Spec decision 5: a step gets its full delay after the last
			// thing that made the alert newly urgent, so a chain that
			// resumes does not fire at once.
			name: "a later urgency restarts the next rung's clock",
			cs: ChainState{
				Steps:     []Step{step(1, 5*time.Minute)},
				FiredUpTo: 0, LastFiredAt: base,
				LastUrgentAt: base.Add(50 * time.Minute),
			},
			now:  base.Add(54 * time.Minute),
			want: -1,
		},
		{
			name: "and that rung is due once the restarted clock elapses",
			cs: ChainState{
				Steps:     []Step{step(1, 5*time.Minute)},
				FiredUpTo: 0, LastFiredAt: base,
				LastUrgentAt: base.Add(50 * time.Minute),
			},
			now:  base.Add(55 * time.Minute),
			want: 1,
		},
		{
			// An urgency older than the last firing changes nothing —
			// the anchor is the later of the two, not the newer concept.
			// Using now strictly between the two candidate times (base-1h
			// vs base) pins min-anchor vs max-anchor: with max-anchor
			// (correct), not due; with min-anchor (wrong), would be due.
			name: "an urgency before the last firing is ignored",
			cs: ChainState{
				Steps:     []Step{step(1, 5*time.Minute)},
				FiredUpTo: 0, LastFiredAt: base,
				LastUrgentAt: base.Add(-time.Hour),
			},
			now:  base.Add(2 * time.Minute),
			want: -1,
		},
		{
			// The guard FiredUpTo < 0 must block escalation, not just
			// coincidental position numbering. A step at position 0 would
			// be incorrectly returned if the guard were deleted.
			name: "no base and a position-0 step still block escalation",
			cs: ChainState{
				Steps:     []Step{step(0, 5*time.Minute)},
				FiredUpTo: -1,
			},
			now:  base.Add(6 * time.Hour),
			want: -1,
		},
		{
			// Delays are relative to the previous rung's own firing, not
			// absolute from the base. This prevents pile-ups: each rung's
			// clock only starts after its predecessor really fired. With
			// base at t0, step 1 fired at t0+30m, both delays 5m, step 2
			// should be due at t0+35m (t0+30m + 5m), not at t0+5m
			// (t0 + 5m absolute).
			name: "step 2 waits after step 1's own firing, not the base",
			cs: ChainState{
				Steps:     []Step{step(1, 5*time.Minute), step(2, 5*time.Minute)},
				FiredUpTo: 1, LastFiredAt: base.Add(30 * time.Minute),
			},
			now:  base.Add(34 * time.Minute),
			want: -1,
		},
		{
			name: "step 2 is due after its own delay from step 1's firing",
			cs: ChainState{
				Steps:     []Step{step(1, 5*time.Minute), step(2, 5*time.Minute)},
				FiredUpTo: 1, LastFiredAt: base.Add(30 * time.Minute),
			},
			now:  base.Add(35 * time.Minute),
			want: 2,
		},
		{
			name: "a route with no steps never escalates",
			cs:   ChainState{Steps: nil, FiredUpTo: 0, LastFiredAt: base},
			now:  base.Add(6 * time.Hour),
			want: -1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := DueStep(tc.cs, tc.now)
			if tc.want == -1 {
				if ok {
					t.Fatalf("expected nothing due, got position %d", got.Position)
				}
				return
			}
			if !ok {
				t.Fatalf("expected position %d, got nothing due", tc.want)
			}
			if got.Position != tc.want {
				t.Fatalf("position = %d, want %d", got.Position, tc.want)
			}
		})
	}
}
