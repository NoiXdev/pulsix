package gen

import (
	"regexp"
	"testing"
)

// TestJobEventsReadIsBoundedAndPaged guards the job log read: job_events is
// a hypertable, so the read must carry the `at >=` bound that lets
// TimescaleDB skip the chunks older than the job, its LIMIT, and its order
// by seq (the order next_after pages on). The constant is unexported, which
// is why the guard lives in this package (as jobs_reaper_guard_test.go).
func TestJobEventsReadIsBoundedAndPaged(t *testing.T) {
	for _, re := range []*regexp.Regexp{
		regexp.MustCompile(`(?is)FROM\s+job_events\s+WHERE\s+[^;]*\bat\s*>=\s*\$\d+::timestamptz`),
		regexp.MustCompile(`(?is)ORDER\s+BY\s+seq\s+LIMIT\s+\$\d+::int\s*$`),
	} {
		if !re.MatchString(listJobEvents) {
			t.Errorf("listJobEvents does not match %s:\n%s", re, listJobEvents)
		}
	}
}
