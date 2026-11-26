package api

import (
	"context"
	"testing"
	"time"
)

// fakeWaitClock is a wait's time source: a pause advances it by its
// duration, and a read may advance it too (a slow database).
type fakeWaitClock struct {
	now    time.Time
	pauses int
}

func (c *fakeWaitClock) deps() Deps {
	return Deps{
		WaitNow: func() time.Time { return c.now },
		WaitSleep: func(_ context.Context, d time.Duration) error {
			c.pauses++
			c.now = c.now.Add(d)
			return nil
		},
	}
}

func TestWaitForChangeIsBoundedByWallTime(t *testing.T) {
	for _, tc := range []struct {
		name       string
		readCost   time.Duration
		wait       int
		wantPauses int
	}{
		{name: "instant reads", readCost: 0, wait: 2, wantPauses: 4},
		// Each read takes a second: the 2 s budget runs out after the first
		// pause and its read, not after four pauses.
		{name: "slow reads", readCost: time.Second, wait: 2, wantPauses: 1},
		{name: "reads slower than the budget", readCost: 3 * time.Second, wait: 2, wantPauses: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clock := &fakeWaitClock{now: time.Unix(1000, 0)}
			reads := 0
			_, err := waitForChange(context.Background(), clock.deps(), tc.wait, "", func() (int, string, error) {
				reads++
				clock.now = clock.now.Add(tc.readCost)
				return reads, "queued", nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if clock.pauses != tc.wantPauses {
				t.Fatalf("%d pauses, want %d", clock.pauses, tc.wantPauses)
			}
		})
	}
}

func TestWaitForChangeShortensTheLastPause(t *testing.T) {
	clock := &fakeWaitClock{now: time.Unix(1000, 0)}
	var pauses []time.Duration
	deps := clock.deps()
	deps.WaitSleep = func(_ context.Context, d time.Duration) error {
		pauses = append(pauses, d)
		clock.now = clock.now.Add(d)
		return nil
	}
	_, err := waitForChange(context.Background(), deps, 1, "", func() (int, string, error) {
		clock.now = clock.now.Add(200 * time.Millisecond)
		return 0, "queued", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// 0.2 s read, 0.5 s pause, 0.2 s read: 0.1 s left for one last pause.
	want := []time.Duration{500 * time.Millisecond, 100 * time.Millisecond}
	if len(pauses) != len(want) || pauses[0] != want[0] || pauses[1] != want[1] {
		t.Fatalf("pauses %v, want %v", pauses, want)
	}
}
