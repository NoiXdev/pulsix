// clampListLimit, unit-tested directly: the query tag
// (minimum/maximum/default) already refuses an out-of-range HTTP request
// before the handler runs, but this is the defense schedules.Service.Runs
// and notify.Service.Deliveries themselves still need — both pass LIMIT
// straight to Postgres, and a negative one is a database error, not a
// validation error (Task 5 deferred minor; generalized in Task 4 fix
// round 1, see schedule_handlers.go's own doc comment on the const block).
package api

import "testing"

func TestClampListLimit(t *testing.T) {
	cases := []struct {
		name  string
		limit int
		want  int
	}{
		{"zero defaults", 0, listLimitDefault},
		{"negative defaults", -1, listLimitDefault},
		{"large negative defaults", -1000, listLimitDefault},
		{"in range unchanged", 7, 7},
		{"at minimum unchanged", listLimitMin, listLimitMin},
		{"at maximum unchanged", listLimitMax, listLimitMax},
		{"above maximum clamps down", 1000, listLimitMax},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := clampListLimit(c.limit); got != c.want {
				t.Fatalf("clampListLimit(%d) = %d, want %d", c.limit, got, c.want)
			}
		})
	}
}
