// clampScheduleRunsLimit, unit-tested directly: the query tag
// (minimum/maximum/default) already refuses an out-of-range HTTP request
// before the handler runs, but this is the defense schedules.Service.Runs
// itself still needs — it passes LIMIT straight to Postgres, and a
// negative one is a database error, not a validation error (Task 5
// deferred minor).
package api

import "testing"

func TestClampScheduleRunsLimit(t *testing.T) {
	cases := []struct {
		name  string
		limit int
		want  int
	}{
		{"zero defaults", 0, scheduleRunsDefaultLimit},
		{"negative defaults", -1, scheduleRunsDefaultLimit},
		{"large negative defaults", -1000, scheduleRunsDefaultLimit},
		{"in range unchanged", 7, 7},
		{"at minimum unchanged", scheduleRunsMinLimit, scheduleRunsMinLimit},
		{"at maximum unchanged", scheduleRunsMaxLimit, scheduleRunsMaxLimit},
		{"above maximum clamps down", 1000, scheduleRunsMaxLimit},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := clampScheduleRunsLimit(c.limit); got != c.want {
				t.Fatalf("clampScheduleRunsLimit(%d) = %d, want %d", c.limit, got, c.want)
			}
		})
	}
}
