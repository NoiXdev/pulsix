package jobs

import (
	"context"
	"testing"
)

// TestRunGroupReportsCheckMode: a finished dry run's group says so, so the
// run-outcome hooks can leave it alone (spec 2026-11-22 §1); a real run's
// group does not.
func TestRunGroupReportsCheckMode(t *testing.T) {
	w := newGroupWorld(t, "rungroup-check-mode")
	dryGroup, _, dry := w.runGroup(t, 2)
	if _, err := w.d.Pool().Exec(context.Background(), `UPDATE jobs SET check_mode = true WHERE run_group_id = $1`, dryGroup); err != nil {
		t.Fatal(err)
	}
	realGroup, _, realRun := w.runGroup(t, 1)
	for _, j := range append(dry, realRun...) {
		if err := w.q.Finish(context.Background(), j.ID, StatusFailed, nil, "exited with code 2"); err != nil {
			t.Fatal(err)
		}
	}
	got := map[bool]int{}
	for _, r := range w.sink.snapshot() {
		switch r.GroupID {
		case dryGroup:
			if !r.CheckMode {
				t.Fatalf("dry run group reported without check mode: %+v", r)
			}
		case realGroup:
			if r.CheckMode {
				t.Fatalf("real run group reported as check mode: %+v", r)
			}
		}
		got[r.CheckMode]++
	}
	if got[true] != 1 || got[false] != 1 {
		t.Fatalf("reports = %v, want one of each", got)
	}
}
