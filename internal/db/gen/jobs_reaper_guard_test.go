package gen

import (
	"regexp"
	"testing"
)

// TestExhaustedStaleJobTerminationChecksStalenessInStatement is the
// regression guard for backlog 2.53: the reaper lists exhausted stale jobs
// first and fails each through the terminal transition afterwards, so a job
// that got a fresh binding (a re-claim refreshing claimed_at) between the
// reaper's scan and the transition's write would be failed for staleness it
// no longer has. The staleness check must therefore ride INSIDE the
// transition's UPDATE — as the same claimed_at bound the reaper's scan used
// — and not in Go code around it.
//
// This inspects the literal SQL text sqlc embedded in the generated query
// constant, which is why it lives in this package instead of
// internal/jobs: those constants are unexported (the same pattern as
// credentials_guard_test.go).
func TestExhaustedStaleJobTerminationChecksStalenessInStatement(t *testing.T) {
	// The staleness bound must be the UPDATE's own WHERE clause — not the
	// inner SELECT's — so the row is re-checked when it is written. sqlc
	// renders named parameters as positional $n.
	re := regexp.MustCompile(`(?is)UPDATE\s+jobs\s+SET[^;]*?WHERE\s+[^;]*?claimed_at\s*<\s*(@\w+|\$\d+)::timestamptz[^;]*?attempts\s*>=\s*(@\w+|\$\d+)::integer`)
	if !re.MatchString(failExhaustedStaleJob) {
		t.Fatalf("failExhaustedStaleJob lost its staleness/attempts predicate in the UPDATE (backlog 2.53):\n%s", failExhaustedStaleJob)
	}
}
