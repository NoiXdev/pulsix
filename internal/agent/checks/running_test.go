package checks

import (
	"reflect"
	"testing"
	"time"
)

// TestSchedulerChecks: the checks the scheduler runs, by key, the manager's
// and the local ones; a shadowed local check is not among them (backlog
// 3.132's status page lists them).
func TestSchedulerChecks(t *testing.T) {
	ticks := &fakeTickers{}
	sched := NewScheduler(map[string]Plugin{"cpu": &fakePlugin{}, "mem": &fakePlugin{}}, time.Now, ticks.factory())
	sched.Apply(Config{Version: "v1", Checks: []Spec{{Key: "b", Plugin: "cpu", Interval: time.Minute}, {Key: "shared", Plugin: "cpu", Interval: time.Minute}}})
	sched.ApplyLocal([]Spec{{Key: "a-local", Plugin: "mem", Interval: 30 * time.Second}, {Key: "shared", Plugin: "mem", Interval: time.Minute}})
	want := []RunningCheck{
		{Key: "a-local", Plugin: "mem", Interval: 30 * time.Second, Local: true},
		{Key: "b", Plugin: "cpu", Interval: time.Minute},
		{Key: "shared", Plugin: "cpu", Interval: time.Minute},
	}
	if got := sched.Checks(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Checks = %+v, want %+v", got, want)
	}
}

// TestRingStats: how many entries wait and the oldest one's time.
func TestRingStats(t *testing.T) {
	now := time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)
	b := NewBuffer(time.Hour, func() time.Time { return now })
	if n, oldest := b.Stats(); n != 0 || !oldest.IsZero() {
		t.Fatalf("empty Stats = %d, %v", n, oldest)
	}
	b.Add([]Result{{At: now.Add(-2 * time.Minute)}, {At: now.Add(-time.Minute)}})
	if n, oldest := b.Stats(); n != 2 || !oldest.Equal(now.Add(-2*time.Minute)) {
		t.Fatalf("Stats = %d, %v", n, oldest)
	}
	b.Drain()
	if n, _ := b.Stats(); n != 0 {
		t.Fatalf("after Drain = %d", n)
	}
}
