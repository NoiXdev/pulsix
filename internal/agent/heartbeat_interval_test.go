package agent

import (
	"testing"
	"time"
)

// TestReportedHeartbeatInterval: the interval goes out in whole seconds,
// rounded up, so a reported interval is never shorter than the real one
// and the manager's online window never too narrow.
func TestReportedHeartbeatInterval(t *testing.T) {
	for _, tc := range []struct {
		in   time.Duration
		want int32
	}{
		{30 * time.Second, 30},
		{10 * time.Minute, 600},
		{7500 * time.Millisecond, 8},
		{20 * time.Millisecond, 1},
		{0, 0},
	} {
		if got := reportedHeartbeatInterval(tc.in); got != tc.want {
			t.Errorf("reportedHeartbeatInterval(%v) = %d, want %d", tc.in, got, tc.want)
		}
	}
}
