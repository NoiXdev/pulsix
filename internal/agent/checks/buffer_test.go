package checks

import (
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
