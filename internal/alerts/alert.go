// This file is the vocabulary Reconcile (reconcile.go) and its executor
// (Task 5) share: the alert as this package sees it, its subject, and the
// actions Reconcile can decide on. Nothing here does anything — Reconcile
// is the only function that turns a tick's states into decisions, and the
// store is the only thing that turns decisions into rows.
//
// The string literals below are not arbitrary: they are exactly the CHECK
// literals migration 00049 put on the alerts table (scope, closed_reason,
// and severity/peak_severity/acknowledged_severity, which the CHECK there
// forbids ever being "ok"), because Task 5 marshals these Go values
// straight into those columns.
package alerts

import (
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/rules"
)

// Scope is whether an alert is about one tuple or a host's whole roll-up
// (design decision 4: unknown opens one alert, at the host). The two
// literals match the alerts table's own CHECK on the scope column.
type Scope string

const (
	ScopeTuple Scope = "tuple"
	ScopeHost  Scope = "host"
)

// Subject is what one alert is about — the same thing the database's
// alerts_one_open_per_subject partial unique index is keyed on. A
// host-scope Subject carries CheckKey, Item and Metric all empty,
// mirroring the subject CHECK migration 00049 puts on the alerts table.
type Subject struct {
	HostID                 uuid.UUID
	Scope                  Scope
	CheckKey, Item, Metric string
}

// CloseReason is why an alert closed. The two literals match the alerts
// table's own CHECK on closed_reason.
type CloseReason string

const (
	Resolved CloseReason = "resolved"
	Stale    CloseReason = "stale"
)

// Alert is one durable episode (design decision 1): opened once, closed
// once, its current and peak severity tracked while it is open, with its
// own acknowledgement and snooze state layered on top. Reconcile only
// reads these to decide what must change about them — it never
// constructs or mutates one itself; that is the store's job (Task 5).
type Alert struct {
	ID      uuid.UUID
	OrgID   uuid.UUID
	Subject Subject

	OpenedAt       time.Time
	Severity, Peak rules.State

	ClosedAt     *time.Time
	ClosedReason CloseReason

	AckAt       *time.Time
	AckBy       *uuid.UUID
	AckSeverity rules.State // "" when unacknowledged
	AckNote     string

	SnoozedUntil      *time.Time
	SnoozeUntilChange bool

	WasFlapping bool
}

// AlertWithHost pairs an Alert with its host's name — Get and List's own
// read shape, not the lifecycle's: Reconcile/Execute (Apply, the per-tick
// path) never need a host's name and keep passing bare Alert values
// around, so this stays out of Alert itself rather than growing every
// internal caller an unused field. The store's GetAlert/ListAlerts join
// hosts for exactly this (store_sql.go's own doc comment on why the join
// names org_id); Service.Get/List unwrap it onto View/Detail's own
// HostName field.
type AlertWithHost struct {
	Alert
	HostName string
}

// ActionKind is what Reconcile decided must happen to one alert.
type ActionKind string

const (
	ActionOpen     ActionKind = "open"
	ActionSeverity ActionKind = "severity"
	ActionFlapped  ActionKind = "flapped"
	ActionClose    ActionKind = "close"
)

// Action is one line of Reconcile's verdict. Subject identifies the new
// alert for ActionOpen, the only kind with nothing else to identify it by
// yet; every other kind instead carries AlertID, naming an Alert already
// passed to Reconcile in its open slice. VoidsAck, ClearsSnoozeUntilChange
// and Reason each apply to exactly one kind, named in their own comments.
type Action struct {
	Kind    ActionKind
	Subject Subject   // ActionOpen only
	AlertID uuid.UUID // every kind except ActionOpen

	At       time.Time
	Severity rules.State // ActionOpen, ActionSeverity

	VoidsAck                bool // ActionSeverity only: the rise outran the acknowledgement
	ClearsSnoozeUntilChange bool // ActionSeverity only: a severity change ends an unbounded snooze

	Reason CloseReason // ActionClose only
}

// EventKind is one row of alert_events.kind. The seven constants below
// are exactly the CHECK list migration 00049 declares, and
// EventKindsMatchTheSchema pins them to it: a Go copy of a database enum
// with no compiler link between them is how internal/api's fleetStateRank
// drifted from rules.StateRank, which that function's own doc comment
// records. internal/alertroutes needs these names; the type lives here,
// with the table it describes.
type EventKind string

const (
	EventOpened         EventKind = "opened"
	EventSeverityRaised EventKind = "severity_raised"
	EventAcknowledged   EventKind = "acknowledged"
	EventAckVoided      EventKind = "ack_voided"
	EventSnoozed        EventKind = "snoozed"
	EventUnsnoozed      EventKind = "unsnoozed"
	EventClosed         EventKind = "closed"
)

// EventKinds lists every kind, in the schema's own order.
func EventKinds() []EventKind {
	return []EventKind{
		EventOpened, EventSeverityRaised, EventAcknowledged,
		EventAckVoided, EventSnoozed, EventUnsnoozed, EventClosed,
	}
}

// ParseEventKind accepts exactly the seven kind names.
func ParseEventKind(s string) (EventKind, bool) {
	for _, k := range EventKinds() {
		if string(k) == s {
			return k, true
		}
	}
	return "", false
}
