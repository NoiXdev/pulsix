package alertroutes

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/alerts"
	"github.com/noixdev/pulsix/internal/maintenance"
)

// coverageCall is one LatestCoverageEnd call the fake saw.
type coverageCall struct{ from, to time.Time }

// fakeCoverage answers LatestCoverageEnd with a fixed end and records
// every call; the point-in-time halves of muteSource are never reached by
// foldMaintenanceAnchor.
type fakeCoverage struct {
	end   time.Time
	ok    bool
	calls []coverageCall
}

func (f *fakeCoverage) GetAlert(context.Context, uuid.UUID, uuid.UUID) (alerts.AlertWithHost, error) {
	panic("foldMaintenanceAnchor reads no alert")
}

func (f *fakeCoverage) WindowsForHost(context.Context, uuid.UUID, uuid.UUID, time.Time) ([]maintenance.Occurrence, error) {
	panic("foldMaintenanceAnchor reads no point-in-time windows")
}

func (f *fakeCoverage) LatestCoverageEnd(_ context.Context, _, _ uuid.UUID, from, to time.Time) (time.Time, bool, error) {
	f.calls = append(f.calls, coverageCall{from, to})
	return f.end, f.ok, nil
}

// TestFoldMaintenanceAnchor is the fold's own contract (backlog 4.41,
// review Important 1): a chain not yet due by its plain anchor costs no
// coverage read at all, since the fold can only move the anchor later; a
// naively due chain reads exactly [now - delay, now]; and only an end
// later than the current LastUrgentAt moves it.
func TestFoldMaintenanceAnchor(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 1, 5, 10, 0, 0, 0, time.UTC)
	chain := ChainState{
		Steps:       []Step{step(1, 5*time.Minute)},
		FiredUpTo:   0,
		LastFiredAt: base,
	}

	cases := []struct {
		name       string
		now        time.Time
		cs         ChainState
		end        time.Time
		ok         bool
		wantCalls  []coverageCall
		wantUrgent time.Time
	}{
		{
			name: "not yet due: no read",
			now:  base.Add(4 * time.Minute), cs: chain,
			end: base.Add(3 * time.Minute), ok: true,
		},
		{
			name: "no rung left: no read",
			now:  base.Add(time.Hour), cs: ChainState{Steps: chain.Steps, FiredUpTo: 1, LastFiredAt: base},
			end: base.Add(3 * time.Minute), ok: true,
		},
		{
			name: "due, nothing covered",
			now:  base.Add(6 * time.Minute), cs: chain,
			wantCalls: []coverageCall{{base.Add(time.Minute), base.Add(6 * time.Minute)}},
		},
		{
			name: "due, a window ended inside the range",
			now:  base.Add(6 * time.Minute), cs: chain,
			end: base.Add(2 * time.Minute), ok: true,
			wantCalls:  []coverageCall{{base.Add(time.Minute), base.Add(6 * time.Minute)}},
			wantUrgent: base.Add(2 * time.Minute),
		},
		{
			name: "due, a window ended exactly now",
			now:  base.Add(6 * time.Minute), cs: chain,
			end: base.Add(6 * time.Minute), ok: true,
			wantCalls:  []coverageCall{{base.Add(time.Minute), base.Add(6 * time.Minute)}},
			wantUrgent: base.Add(6 * time.Minute),
		},
		{
			name: "an end no later than LastUrgentAt moves nothing",
			now:  base.Add(22 * time.Minute),
			cs:   ChainState{Steps: chain.Steps, FiredUpTo: 0, LastFiredAt: base, LastUrgentAt: base.Add(16 * time.Minute)},
			end:  base.Add(16 * time.Minute), ok: true,
			wantCalls:  []coverageCall{{base.Add(17 * time.Minute), base.Add(22 * time.Minute)}},
			wantUrgent: base.Add(16 * time.Minute),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fake := &fakeCoverage{end: tc.end, ok: tc.ok}
			f := &Follower{mutes: fake}
			got, err := f.foldMaintenanceAnchor(context.Background(), uuid.New(), uuid.New(), tc.cs, tc.now)
			if err != nil {
				t.Fatalf("foldMaintenanceAnchor: %v", err)
			}
			if len(fake.calls) != len(tc.wantCalls) {
				t.Fatalf("calls = %+v, want %+v", fake.calls, tc.wantCalls)
			}
			for i, c := range tc.wantCalls {
				if !fake.calls[i].from.Equal(c.from) || !fake.calls[i].to.Equal(c.to) {
					t.Fatalf("call %d = %+v, want %+v", i, fake.calls[i], c)
				}
			}
			want := tc.cs.LastUrgentAt
			if !tc.wantUrgent.IsZero() {
				want = tc.wantUrgent
			}
			if !got.LastUrgentAt.Equal(want) {
				t.Fatalf("LastUrgentAt = %s, want %s", got.LastUrgentAt, want)
			}
		})
	}
}
