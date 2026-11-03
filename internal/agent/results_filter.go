package agent

import (
	"sync"
	"time"
	"unicode/utf8"

	"github.com/noixdev/pulsix/internal/agent/checks"
)

// invalidUTF8WarnEvery is the least time between two warnings about results
// dropped for carrying text that is not valid UTF-8. A check that reads a
// mangled file name reports it on every run; one line per run would fill
// the host's journal, which is where the agent logs.
const invalidUTF8WarnEvery = time.Minute

// invalidUTF8Warner rate-limits that warning. handleResults runs on the
// scheduler's per-check goroutines, so it guards itself.
type invalidUTF8Warner struct {
	mu      sync.Mutex
	last    time.Time
	dropped int // dropped since the last warning that went out
}

// note records one dropped result and reports whether a warning is due now,
// with how many results it covers.
func (w *invalidUTF8Warner) note(now time.Time) (covers int, due bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.dropped++
	if !w.last.IsZero() && now.Sub(w.last) < invalidUTF8WarnEvery {
		return 0, false
	}
	covers, w.dropped, w.last = w.dropped, 0, now
	return covers, true
}

// validUTF8Results returns the results whose check_key, item and metric are
// valid UTF-8. gRPC refuses to marshal a message carrying anything else and
// finishes the stream, so one such result would take every result of its
// chunk down with it (see unsentAfter for the chunk-level backstop); only
// that result is dropped here, before it is buffered or sent, and logged at
// most once per invalidUTF8WarnEvery. The common case, nothing invalid,
// returns the slice itself.
func (l *Loop) validUTF8Results(results []checks.Result) []checks.Result {
	valid, copied := results, false
	for i, r := range results {
		if utf8.ValidString(r.CheckKey) && utf8.ValidString(r.Item) && utf8.ValidString(r.Metric) {
			if copied {
				valid = append(valid, r)
			}
			continue
		}
		if !copied {
			// First invalid result: keep a copy of what was valid so far,
			// never writing into the caller's slice.
			valid = append(make([]checks.Result, 0, len(results)-1), results[:i]...)
			copied = true
		}
		if covers, due := l.utf8Warn.note(l.now()); due {
			l.log.Warn("check results dropped: a result carries text that is not valid UTF-8 "+
				"(for example a file name in the wrong encoding)",
				"dropped", covers, "check_key", r.CheckKey, "item", r.Item, "metric", r.Metric)
		}
	}
	return valid
}
