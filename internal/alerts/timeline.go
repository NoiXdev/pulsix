// Get's timeline is a merge, not a query (Task 6 brief): one alert's own
// alert_events interleaved with the check_state_changes rows inside its
// lifetime, ordered by time — and, where two rows share an instant, with
// the state change first. That tie-break is the whole point of this file.
// raiseSeverity (store_sql.go) stamps a severity_raised state change and
// its own severity_raised/ack_voided events with the very same `a.At`
// inside one Execute call; a merge that let the event sort first would
// read as "acknowledged, then went critical" when the truth is the
// opposite. mergeTimeline is pure — no database access, no clock — like
// Reconcile and Mute before it; the store is what fetches either slice.
package alerts

import (
	"sort"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/rules"
)

// Event is one alert_events row (migration 00049): an alert's own append-
// only history, independent of its current-state columns. Payload carries
// the raw jsonb column: '{}' (InsertAlertEvent's own default) for every
// kind except EventEscalated, whose shape is EscalatedPayload below
// (alertroutes.RecordEscalation, migration 00056, writes it) — Task 8's
// own GUI work is what first reads one back out.
type Event struct {
	ID      uuid.UUID
	At      time.Time
	Kind    string
	ActorID *uuid.UUID
	Payload []byte
}

// EscalatedPayload is the EventEscalated event's own payload shape: the
// ladder step that fired and the channels it reached (empty, not absent,
// when the step reached nobody — RecordEscalation's own doc comment).
// Defined here, beside the EventKind it belongs to, rather than in
// internal/alertroutes where it is written, so the write side
// (RecordEscalation) and every future read side share one struct instead
// of two json tags that could drift apart.
type EscalatedPayload struct {
	Position int         `json:"position"`
	Channels []uuid.UUID `json:"channels"`
}

// StateChange is one check_state_changes row inside an alert's own
// lifetime: the tuple's own transition log, which predates alerts
// entirely and belongs to internal/rules, not to this package's own
// history.
type StateChange struct {
	At        time.Time
	FromState rules.State
	ToState   rules.State
}

// TimelineKind tags one merged entry Get's timeline returns.
type TimelineKind string

const (
	TimelineEvent       TimelineKind = "event"
	TimelineStateChange TimelineKind = "state_change"
)

// timelineRank orders TimelineKind for mergeTimeline's own tie-break: a
// state change sorts before an event at the same instant, because the
// state change is what caused the event, never the other way around —
// this file's own doc comment gives the concrete case
// (severity_raised/ack_voided sharing their state change's own `at`).
func timelineRank(k TimelineKind) int {
	if k == TimelineStateChange {
		return 0
	}
	return 1
}

// TimelineEntry is one merged row: exactly one of Event or Change is set,
// never both, named by Kind.
type TimelineEntry struct {
	At     time.Time
	Kind   TimelineKind
	Event  *Event
	Change *StateChange
}

// mergeTimeline interleaves events and changes by time, breaking a tie in
// timelineRank's own order. sort.SliceStable, not sort.Slice: two entries
// of the same kind that share an instant keep whatever order their own
// slice already had (both arrive here already ordered by the store's own
// ORDER BY), rather than an unspecified one.
func mergeTimeline(events []Event, changes []StateChange) []TimelineEntry {
	out := make([]TimelineEntry, 0, len(events)+len(changes))
	for i := range events {
		out = append(out, TimelineEntry{At: events[i].At, Kind: TimelineEvent, Event: &events[i]})
	}
	for i := range changes {
		out = append(out, TimelineEntry{At: changes[i].At, Kind: TimelineStateChange, Change: &changes[i]})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].At.Equal(out[j].At) {
			return out[i].At.Before(out[j].At)
		}
		return timelineRank(out[i].Kind) < timelineRank(out[j].Kind)
	})
	return out
}
