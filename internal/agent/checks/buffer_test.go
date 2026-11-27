package checks

import (
	"strings"
	"testing"
	"time"
)

func TestBufferAddAndDrainPreservesOrderAndTimestamps(t *testing.T) {
	base := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)
	now := base
	buf := NewBuffer(15*time.Minute, func() time.Time { return now })

	r1 := Result{CheckKey: "cpu", Metric: "cpu.used_pct", Value: floatPtr(1), At: base}
	r2 := Result{CheckKey: "cpu", Metric: "cpu.used_pct", Value: floatPtr(2), At: base.Add(time.Second)}
	if dropped := buf.Add([]Result{r1}); dropped != 0 {
		t.Fatalf("dropped = %d, want 0", dropped)
	}
	if dropped := buf.Add([]Result{r2}); dropped != 0 {
		t.Fatalf("dropped = %d, want 0", dropped)
	}

	out := buf.Drain()
	if len(out) != 2 {
		t.Fatalf("drained = %d, want 2", len(out))
	}
	if !out[0].At.Equal(r1.At) || !out[1].At.Equal(r2.At) {
		t.Errorf("drain order/timestamps = %v, %v; want %v, %v (original order and clock preserved)",
			out[0].At, out[1].At, r1.At, r2.At)
	}
	if *out[0].Value != 1 || *out[1].Value != 2 {
		t.Errorf("drained values = %v, %v; want 1, 2", *out[0].Value, *out[1].Value)
	}

	// Draining empties the ring.
	if out := buf.Drain(); len(out) != 0 {
		t.Errorf("second drain = %v, want empty", out)
	}
}

func TestBufferAddEvictsOldestPastWindow(t *testing.T) {
	base := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)
	now := base
	buf := NewBuffer(15*time.Minute, func() time.Time { return now })

	old := Result{CheckKey: "cpu", At: base}
	if dropped := buf.Add([]Result{old}); dropped != 0 {
		t.Fatalf("dropped on first add = %d, want 0", dropped)
	}

	// The outage outlasts the window: 16 minutes pass.
	now = base.Add(16 * time.Minute)
	fresh := Result{CheckKey: "cpu", At: now}
	dropped := buf.Add([]Result{fresh})
	if dropped != 1 {
		t.Fatalf("dropped = %d, want 1 (the aged-out sample)", dropped)
	}

	out := buf.Drain()
	if len(out) != 1 {
		t.Fatalf("remaining = %d, want 1", len(out))
	}
	if !out[0].At.Equal(fresh.At) {
		t.Errorf("remaining sample = %v, want the fresh one %v", out[0].At, fresh.At)
	}
}

func TestBufferAddEvictsMultipleAgedEntriesAndCountsAll(t *testing.T) {
	base := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)
	now := base
	buf := NewBuffer(15*time.Minute, func() time.Time { return now })

	for i := 0; i < 5; i++ {
		buf.Add([]Result{{CheckKey: "cpu", At: base.Add(time.Duration(i) * time.Second)}})
	}
	now = base.Add(20 * time.Minute)
	dropped := buf.Add([]Result{{CheckKey: "cpu", At: now}})
	if dropped != 5 {
		t.Fatalf("dropped = %d, want 5 (every aged entry from the first batch)", dropped)
	}
	if out := buf.Drain(); len(out) != 1 {
		t.Fatalf("remaining = %d, want 1", len(out))
	}
}

func TestBufferAddEmptyBatchIsNoOp(t *testing.T) {
	buf := NewBuffer(15*time.Minute, time.Now)
	if dropped := buf.Add(nil); dropped != 0 {
		t.Errorf("dropped = %d, want 0", dropped)
	}
	if out := buf.Drain(); len(out) != 0 {
		t.Errorf("drain of an empty buffer = %v, want empty", out)
	}
}

func TestBufferWithinWindowNeverDrops(t *testing.T) {
	base := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)
	now := base
	buf := NewBuffer(15*time.Minute, func() time.Time { return now })

	buf.Add([]Result{{CheckKey: "cpu", At: base}})
	now = base.Add(14 * time.Minute)
	dropped := buf.Add([]Result{{CheckKey: "cpu", At: now}})
	if dropped != 0 {
		t.Errorf("dropped = %d, want 0 (still inside the 15-minute window)", dropped)
	}
	if out := buf.Drain(); len(out) != 2 {
		t.Errorf("remaining = %d, want 2", len(out))
	}
}

// TestRingCarriesAnyStampedType pins the ring's generic form: the probe
// results of internal/agent/probes ride the same 15-minute ring as check
// results, evicted by their own timestamp through the at function.
func TestRingCarriesAnyStampedType(t *testing.T) {
	type stamped struct {
		name string
		at   time.Time
	}
	base := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	now := base
	ring := NewRing(15*time.Minute, func() time.Time { return now }, func(s stamped) time.Time { return s.at })

	ring.Add([]stamped{{"old", base}, {"mid", base.Add(5 * time.Minute)}})
	now = base.Add(16 * time.Minute)
	if dropped := ring.Add([]stamped{{"new", now}}); dropped != 1 {
		t.Fatalf("dropped = %d, want 1 (only the entry past the window)", dropped)
	}
	out := ring.Drain()
	if len(out) != 2 || out[0].name != "mid" || out[1].name != "new" {
		t.Fatalf("drained = %+v, want mid then new", out)
	}
	if len(ring.Drain()) != 0 {
		t.Fatal("a drain does not empty the ring")
	}
}

// TestCappedRingEvictsOldestWholeInstantsOverTheCap: over its cost cap the
// ring drops from the front, oldest first, and then whatever else shares
// the last dropped entry's timestamp, so a round is never left half
// buffered. Put says how many went for the cap and how many for age.
func TestCappedRingEvictsOldestWholeInstantsOverTheCap(t *testing.T) {
	type stamped struct {
		name string
		at   time.Time
	}
	base := time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC)
	now := base
	ring := NewCappedRing(15*time.Minute, 4, func(stamped) int { return 1 },
		func() time.Time { return now }, func(s stamped) time.Time { return s.at })

	t0, t1, t2 := base, base.Add(time.Second), base.Add(2*time.Second)
	if e := ring.Put([]stamped{{"a0", t0}, {"b0", t0}, {"a1", t1}}); e != (Evicted{}) {
		t.Fatalf("under the cap evicted %+v", e)
	}
	// Five entries against a cap of four: a0 goes for the cap, b0 with it
	// (same instant), the rest stay.
	if e := ring.Put([]stamped{{"b1", t1}, {"a2", t2}}); e != (Evicted{OverCap: 2}) {
		t.Fatalf("evicted %+v, want 2 over the cap", e)
	}
	out := ring.Drain()
	if len(out) != 3 || out[0].name != "a1" || out[1].name != "b1" || out[2].name != "a2" {
		t.Fatalf("kept %+v, want a1 b1 a2", out)
	}

	// Age eviction runs first and is counted apart.
	ring.Put([]stamped{{"old", base}})
	now = base.Add(16 * time.Minute)
	batch := []stamped{{"n1", now}, {"n2", now}, {"n3", now.Add(time.Second)}, {"n4", now.Add(time.Second)}, {"n5", now.Add(2 * time.Second)}}
	if e := ring.Put(batch); e != (Evicted{Aged: 1, OverCap: 2}) {
		t.Fatalf("evicted %+v, want 1 aged and 2 over the cap", e)
	}
	if got := ring.Drain(); len(got) != 3 || got[0].name != "n3" {
		t.Fatalf("kept %+v, want n3 n4 n5", got)
	}
	// A drain resets what the ring holds: four fit again.
	if e := ring.Put(batch[:4]); e != (Evicted{}) {
		t.Fatalf("after a drain evicted %+v", e)
	}
}

// TestCappedRingCountsCostNotEntries: the cap is the sum of cost over the
// entries held, and Add reports both causes together.
func TestCappedRingCountsCostNotEntries(t *testing.T) {
	base := time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC)
	ring := NewCappedRing(15*time.Minute, 100, func(s string) int { return len(s) },
		func() time.Time { return base }, func(string) time.Time { return base })
	if dropped := ring.Add([]string{strings.Repeat("x", 60)}); dropped != 0 {
		t.Fatalf("dropped %d under the cap", dropped)
	}
	// 60 + 50 > 100: everything at the first instant goes, which here is
	// every entry (one shared timestamp) — whole instants, even the new one.
	if dropped := ring.Add([]string{strings.Repeat("y", 50)}); dropped != 2 {
		t.Fatalf("dropped %d, want 2 (one instant)", dropped)
	}
}

// TestUncappedRingNeverEvictsForSize: NewRing has no cap; only age evicts.
func TestUncappedRingNeverEvictsForSize(t *testing.T) {
	base := time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC)
	ring := NewRing(15*time.Minute, func() time.Time { return base }, func(int) time.Time { return base })
	batch := make([]int, 100_000)
	if e := ring.Put(batch); e != (Evicted{}) {
		t.Fatalf("an uncapped ring evicted %+v", e)
	}
}
