package auth

import (
	"context"
	"testing"
	"time"
)

// TestReaperIntervalAndClockDefaults pins the constructor fallbacks the
// production wiring relies on: a zero interval keeps the hourly default,
// an injected interval is kept, and a nil clock degrades to time.Now.
func TestReaperIntervalAndClockDefaults(t *testing.T) {
	r := NewReaper(&stubReaperStore{}, nil, 0, nil, nil)
	if got := r.intervalValue(); got != DefaultReaperInterval {
		t.Fatalf("intervalValue = %v, want DefaultReaperInterval %v", got, DefaultReaperInterval)
	}
	if r.nowValue().IsZero() {
		t.Fatal("nil clock must degrade to time.Now, not a zero time")
	}

	injected := 17 * time.Minute
	r = NewReaper(&stubReaperStore{}, nil, injected, func() time.Time { return time.Unix(0, 0) }, nil)
	if got := r.intervalValue(); got != injected {
		t.Fatalf("intervalValue = %v, want the injected %v", got, injected)
	}
	if got := r.nowValue(); !got.Equal(time.Unix(0, 0)) {
		t.Fatalf("nowValue = %v, want the injected clock", got)
	}
}

// stubReaperStore is the minimal ReaperStore stand-in for white-box
// constructor tests.
type stubReaperStore struct{}

func (stubReaperStore) DeleteExpiredCeremonies(context.Context, time.Time) (int64, error) {
	return 0, nil
}

func (stubReaperStore) DeleteExpiredSessions(context.Context, time.Time) (int64, error) {
	return 0, nil
}

func (stubReaperStore) ClearAbandonedTOTPEnrolments(context.Context, time.Time) (int64, error) {
	return 0, nil
}
