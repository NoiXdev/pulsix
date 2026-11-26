package jobs

import (
	"fmt"
	"strings"
	"time"
)

// MaxEventLimit is the most events one log read returns, and the number it
// returns when the caller names no limit.
const MaxEventLimit = 1000

// EventClockSlack is how far before the job's creation a log read still
// looks: an event's time is the worker's clock (capped at the manager's
// now), so a worker running a little behind stamps a run's first events
// before the job row's created_at. The bound only exists so TimescaleDB
// can skip the chunks older than the job; an hour costs at most one more.
const EventClockSlack = time.Hour

// EventFilter narrows a job's log read (Service.Events). The zero value
// reads the first MaxEventLimit events.
type EventFilter struct {
	// After: only events with a greater sequence number.
	After int64
	// Limit is the most events returned, 1 to MaxEventLimit; 0 means
	// MaxEventLimit.
	Limit int
	// Host: only this host's events (an exact match).
	Host string
	// Task: only events whose task name contains this, in any case.
	Task string
	// Role: only events of tasks from this role, whose task names read
	// "<role> : <task>" (the role compared in any case).
	Role string
	// States: only events in one of these states.
	States []string
}

// eventStates are the states a log read filters on: what the runner writes
// (task, ok, changed, skipped, failed, unreachable, stdout, run_start,
// run_end), recap, and error and fatal, the failed spellings the
// assistant's failed_only has always included.
var eventStates = map[string]bool{
	"ok": true, "changed": true, "skipped": true, "failed": true, "unreachable": true,
	"task": true, "stdout": true, "recap": true, "run_start": true, "run_end": true,
	"error": true, "fatal": true,
}

// ValidEventState reports whether s is a state a log read can filter on.
func ValidEventState(s string) bool { return eventStates[s] }

// normalize checks f against the read's contract and fills in the default
// limit.
func (f EventFilter) normalize() (EventFilter, error) {
	switch {
	case f.Limit == 0:
		f.Limit = MaxEventLimit
	case f.Limit < 0 || f.Limit > MaxEventLimit:
		return f, fmt.Errorf("%w: limit must be between 1 and %d", ErrInvalidInput, MaxEventLimit)
	}
	for _, s := range f.States {
		if !ValidEventState(s) {
			return f, fmt.Errorf("%w: unknown event state %q", ErrInvalidInput, s)
		}
	}
	return f, nil
}

// likeEscape is the escape character of the log search's ILIKE patterns
// (ESCAPE '\' in ListJobEvents).
const likeEscape = '\\'

// escapeLike makes s match itself literally inside a LIKE pattern.
func escapeLike(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r == '%' || r == '_' || r == likeEscape {
			b.WriteRune(likeEscape)
		}
		b.WriteRune(r)
	}
	return b.String()
}

// likeArg is a narrowing's ILIKE pattern, or nil when the narrowing is off.
func likeArg(value, prefix, suffix string) *string {
	if value == "" {
		return nil
	}
	p := prefix + escapeLike(value) + suffix
	return &p
}
