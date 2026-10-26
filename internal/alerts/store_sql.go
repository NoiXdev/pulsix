// This file is the half of the alert lifecycle that carries out what
// Reconcile (reconcile.go) only ever decides, plus the sweep that closes
// an alert whose subject has stopped reporting altogether (design doc
// docs/superpowers/specs/2026-10-06-v0.4a-alerts-design.md, "Lifecycle"
// and "The sweep"). Reconcile never touches the database; everything here
// does.
//
// OpenForHost and Execute both take the caller's own transaction, not
// *db.DB — the same shape internal/rules.EvalSQLStore's own Apply
// requires of its caller (see that package's due_sql.go, claimTx): they
// run inside the evaluator's per-host transaction, so opening, raising or
// closing an alert commits or rolls back with the states, changes and
// roll-up that justified it, never on its own. That transaction carries
// no pulsix.org_id binding (it spans the ticker's own unscoped claim), so
// every statement below names org_id in its own WHERE clause rather than
// leaning on row-level security to supply one. Sweep is the opposite case
// (design doc "The sweep"): it is not scoped to one host or one
// organization at all, so it is the only method here that opens and owns
// its own transaction, on the bare pool, the same posture
// internal/rules' own due-table scan uses for the same reason.
package alerts

import (
	"context"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/maintenance"
	"github.com/noixdev/pulsix/internal/rules"
)

// Event kinds Execute writes — the subset of alert_events' own CHECK
// (migration 00049) this package ever produces. Acknowledged, snoozed and
// unsnoozed belong to Task 6's Service, which a human action drives
// directly; ActionFlapped latches was_flapping with no event at all, since
// the CHECK has no "flapped" kind for it to write.
const (
	eventOpened         = "opened"
	eventSeverityRaised = "severity_raised"
	eventAckVoided      = "ack_voided"
	eventClosed         = "closed"

	// eventAcknowledged, eventSnoozed and eventUnsnoozed are Task 6's own
	// three: the operator-caused events migration 00049's alert_events
	// CHECK reserves a kind for. There is deliberately no
	// "unacknowledged" kind — Store.Unacknowledge writes no event at all
	// (service.go's own doc comment on Service.Unacknowledge says why).
	eventAcknowledged = "acknowledged"
	eventSnoozed      = "snoozed"
	eventUnsnoozed    = "unsnoozed"
)

// Sentinels the API (a later task) maps to HTTP, in the shape
// internal/items and internal/maintenance already use.
var (
	ErrNotFound     = errors.New("not found")
	ErrInvalidInput = errors.New("invalid input")
	// ErrInvalidCursor is what DecodeCursor returns for a ?cursor= value
	// EncodeCursor did not produce, so the API can answer 400 rather than
	// 500 — the same sentinel hosts and audit each expose for their own
	// paginated lists.
	ErrInvalidCursor = errors.New("invalid cursor")
)

// DefaultListLimit and MaxListLimit bound List's page size:
// DefaultListLimit when the caller asks for none (limit <= 0),
// MaxListLimit as a hard ceiling on however large a page they do ask for.
// The numbers are hosts' own (internal/hosts), since the Alerts page is
// the same kind of list read by the same kind of operator.
const (
	DefaultListLimit = 20
	MaxListLimit     = 100
)

// clampLimit applies those two bounds.
func clampLimit(limit int) int {
	switch {
	case limit <= 0:
		return DefaultListLimit
	case limit > MaxListLimit:
		return MaxListLimit
	}
	return limit
}

// EncodeCursor and DecodeCursor are the keyset-cursor format ListAlerts
// pages on: exactly the (opened_at, id) pair a stable
// ORDER BY opened_at DESC, id DESC needs to resume after the last row of
// the previous page. Opaque to callers, and the same shape
// audit.EncodeCursor already gives its own (at, id) feed — nanoseconds
// rather than a formatted instant, so the cursor survives a round trip
// without depending on a timestamp layout.
func EncodeCursor(openedAt time.Time, id uuid.UUID) string {
	raw := strconv.FormatInt(openedAt.UnixNano(), 10) + "|" + id.String()
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// DecodeCursor reverses EncodeCursor. An empty cursor decodes to the zero
// time and uuid.Nil with no error: that is the "first page" case, not a
// malformed cursor.
func DecodeCursor(cursor string) (time.Time, uuid.UUID, error) {
	if cursor == "" {
		return time.Time{}, uuid.Nil, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return time.Time{}, uuid.Nil, ErrInvalidCursor
	}
	nanoStr, idStr, ok := strings.Cut(string(raw), "|")
	if !ok {
		return time.Time{}, uuid.Nil, ErrInvalidCursor
	}
	nano, err := strconv.ParseInt(nanoStr, 10, 64)
	if err != nil {
		return time.Time{}, uuid.Nil, ErrInvalidCursor
	}
	id, err := uuid.Parse(idStr)
	if err != nil {
		return time.Time{}, uuid.Nil, ErrInvalidCursor
	}
	return time.Unix(0, nano), id, nil
}

// Store is what Task 7's evaluator wiring calls to carry out one host's
// Reconcile verdict, what the ticker calls once per tick for the sweep
// (design doc: "Ticker.Tick calls AlertSink.Sweep once per tick"), and
// what Task 6's Service itself calls for every read and operator action:
// list, get, acknowledge, snooze and their reversals.
type Store interface {
	// OpenForHost returns every alert org/host has open right now — the
	// `open []Alert` Reconcile's own signature consumes.
	OpenForHost(ctx context.Context, tx pgx.Tx, orgID, hostID uuid.UUID) ([]Alert, error)
	// Execute applies every action Reconcile returned, in the order
	// given, inside tx: opening a new alert, raising a severity (and
	// voiding its acknowledgement when asked), latching a flap, or
	// closing it. It writes the matching alert_events row for every
	// action that has one (open above).
	Execute(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, actions []Action) error
	// Sweep closes every tuple-scope alert whose subject's check_states
	// row no longer exists, across every organization, and reports how
	// many it closed.
	Sweep(ctx context.Context, now time.Time) (int, error)

	// GetAlert returns one alert by id, org-scoped, with its host's name
	// (query-joined — see the ListAlerts SQL's own doc comment on why the
	// join names org_id). ErrNotFound when no such alert exists in this
	// organization.
	GetAlert(ctx context.Context, orgID, id uuid.UUID) (AlertWithHost, error)
	// ListAlerts returns one page of the organization's alerts matching f,
	// newest opened first, each with its host's name, plus the cursor for
	// the next page (empty when there is none). cursor is opaque — see
	// EncodeCursor/DecodeCursor — and limit arrives already clamped by
	// Service.List, so an implementation may trust it (SQLStore clamps it
	// again anyway; see there).
	ListAlerts(ctx context.Context, orgID uuid.UUID, f Filter, cursor string, limit int) ([]AlertWithHost, string, error)
	// Events returns alertID's own alert_events, in order — half of
	// Get's timeline (the other half is StateChanges below).
	Events(ctx context.Context, orgID, alertID uuid.UUID) ([]Event, error)
	// StateChanges returns subject's check_state_changes rows with
	// from <= at <= to. Callers never ask this for a host-scope Subject
	// (alert.go's own ScopeHost): check_host_states is a roll-up with no
	// change log to match it against.
	StateChanges(ctx context.Context, orgID uuid.UUID, subject Subject, from, to time.Time) ([]StateChange, error)
	// WindowsForHost returns every maintenance window active at now
	// whose target covers hostID — the organization, the host itself, or
	// an inventory group hostID descends into, group resolution included
	// (design doc "Suppression": "that belongs in SQL"). It never
	// resolves a monitoring-group target, the same gap
	// internal/maintenance.Service's own HostsInMaintenance leaves open
	// for that target kind today.
	WindowsForHost(ctx context.Context, orgID, hostID uuid.UUID, now time.Time) ([]maintenance.Occurrence, error)

	// Acknowledge records that actor acknowledged id, stamping both the
	// row and its 'acknowledged' event at `at`. acknowledged_severity is
	// set to the alert's own current severity, read inside this same
	// statement rather than supplied by the caller — decision 2's
	// comparison needs the value as of the moment this commits, and a
	// severity rules.State parameter here would instead be whatever
	// Service's own GetAlert saw one round trip earlier, which a
	// concurrent severity rise could have already moved past (review
	// finding, Important: that gap is exactly the silent acknowledgement
	// decision 2 exists to prevent). ErrInvalidInput if id is already
	// closed, ErrNotFound if it does not exist.
	Acknowledge(ctx context.Context, orgID, id, actor uuid.UUID, note string, at time.Time) error
	// Unacknowledge clears id's acknowledgement. No event: migration
	// 00049's alert_events CHECK has no 'unacknowledged' kind.
	Unacknowledge(ctx context.Context, orgID, id, actor uuid.UUID, at time.Time) error
	// Snooze mutes id until an instant or until its severity next
	// changes, writing the matching 'snoozed' event. Validation (not
	// both, not neither, not in the past) is Service's job; this writes
	// whatever it is given, and ErrNotFound if id does not exist or is
	// already closed.
	Snooze(ctx context.Context, orgID, id, actor uuid.UUID, until *time.Time, untilChange bool, at time.Time) error
	// Unsnooze clears id's snooze, bounded or unbounded, writing the
	// matching 'unsnoozed' event.
	Unsnooze(ctx context.Context, orgID, id, actor uuid.UUID, at time.Time) error
}

// Filter narrows ListAlerts. The zero Filter matches every alert in the
// organization — each pointer field is nil for "either", the same
// "narrow, don't filter in Go" shape internal/items.Filter already uses.
type Filter struct {
	// Open, non-nil, restricts to open (true) or closed (false) alerts.
	Open *bool
	// Severity, non-empty, restricts to one severity.
	Severity rules.State
	// HostID, non-nil, restricts to one host — the design doc's own "the
	// host view is a filter on this list, not its own endpoint".
	HostID *uuid.UUID
	// Scope, non-empty, restricts to 'tuple' or 'host' alerts.
	Scope Scope
	// Acknowledged, non-nil, restricts by COVERAGE (true) or its absence
	// (false) — not merely whether acknowledged_at has ever been set
	// (review finding, Important, corrected from an earlier version of
	// this filter that used exactly that weaker test): an ack given at
	// warn that the alert has since outgrown by rising to crit counts as
	// false here, the same decision-2 comparison isAckCovering makes in
	// Go and the ListAlerts SQL's own CASE makes in the database.
	Acknowledged *bool
	// Muted, non-nil, restricts to muted (true) or unmuted (false)
	// alerts. There is no column for this — Service.List applies it in
	// Go, after Mute has run for each row.
	Muted *bool
}

// var _ Store = (*SQLStore)(nil) pins SQLStore to the full interface at
// compile time, so a forgotten method is a build failure here rather than
// a surprise at whichever call site first tries to use SQLStore as a
// Store.
var _ Store = (*SQLStore)(nil)

// SQLStore implements Store.
type SQLStore struct{ d *db.DB }

// NewSQLStore builds the store on d. Only Sweep ever reaches for d's own
// pool; OpenForHost and Execute run entirely on the pgx.Tx their caller
// passes in.
func NewSQLStore(d *db.DB) *SQLStore { return &SQLStore{d: d} }

// derefOr returns *s, or def when s is nil — the small helper every
// nullable text column below (closed_reason, acknowledged_severity) reads
// through, since CloseReason/rules.State both have "" as a meaningful
// zero value already (CloseReason's own doc comment, rules.State's own
// AckSeverity convention in alert.go).
func derefOr(s *string, def string) string {
	if s == nil {
		return def
	}
	return *s
}

// toAlert converts one gen.Alert row into the package's own Alert — the
// read side of every column InsertAlert, RaiseAlertSeverity, CloseAlert
// and SetAlertFlapping can write.
func toAlert(r gen.Alert) Alert {
	return Alert{
		ID:    r.ID,
		OrgID: r.OrgID,
		Subject: Subject{
			HostID: r.HostID, Scope: Scope(r.Scope),
			CheckKey: r.CheckKey, Item: r.Item, Metric: r.Metric,
		},

		OpenedAt:          r.OpenedAt,
		Severity:          rules.State(r.Severity),
		Peak:              rules.State(r.PeakSeverity),
		SeverityChangedAt: r.SeverityChangedAt,

		ClosedAt:     db.TimePtr(r.ClosedAt),
		ClosedReason: CloseReason(derefOr(r.ClosedReason, "")),

		AckAt:       db.TimePtr(r.AcknowledgedAt),
		AckBy:       db.UUIDPtr(r.AcknowledgedBy),
		AckSeverity: rules.State(derefOr(r.AcknowledgedSeverity, "")),
		AckNote:     r.AckNote,

		SnoozedUntil:      db.TimePtr(r.SnoozedUntil),
		SnoozeUntilChange: r.SnoozeUntilChange,

		WasFlapping: r.WasFlapping,
	}
}

// toAlertFromGetRow and toAlertFromListRow mirror toAlert's own field
// mapping exactly, against the two row shapes GetAlert/ListAlerts gained
// once their queries joined hosts for host_name (alerts.sql's own doc
// comment on why that join names org_id): sqlc gives GetAlert/ListAlerts
// their own named row types the moment a query's result stops matching
// the bare `alerts` table, so neither converts from — or to — gen.Alert
// itself; OpenForHost's own toAlert above is untouched because
// ListOpenAlertsForHost still selects bare `alerts.*` with no join (the
// evaluator's lifecycle has no use for a host's name). Written out
// in full rather than factored through toAlert, the same way
// alert_handlers.go's toAlertDTO already duplicates this package's own
// field list instead of sharing a helper across layers — three row shapes
// this small do not earn an abstraction over them.
func toAlertFromGetRow(r gen.GetAlertRow) AlertWithHost {
	return AlertWithHost{
		Alert: Alert{
			ID:    r.ID,
			OrgID: r.OrgID,
			Subject: Subject{
				HostID: r.HostID, Scope: Scope(r.Scope),
				CheckKey: r.CheckKey, Item: r.Item, Metric: r.Metric,
			},
			OpenedAt:          r.OpenedAt,
			Severity:          rules.State(r.Severity),
			Peak:              rules.State(r.PeakSeverity),
			SeverityChangedAt: r.SeverityChangedAt,

			ClosedAt:     db.TimePtr(r.ClosedAt),
			ClosedReason: CloseReason(derefOr(r.ClosedReason, "")),

			AckAt:       db.TimePtr(r.AcknowledgedAt),
			AckBy:       db.UUIDPtr(r.AcknowledgedBy),
			AckSeverity: rules.State(derefOr(r.AcknowledgedSeverity, "")),
			AckNote:     r.AckNote,

			SnoozedUntil:      db.TimePtr(r.SnoozedUntil),
			SnoozeUntilChange: r.SnoozeUntilChange,

			WasFlapping: r.WasFlapping,
		},
		HostName: r.HostName,
	}
}

func toAlertFromListRow(r gen.ListAlertsRow) AlertWithHost {
	return AlertWithHost{
		Alert: Alert{
			ID:    r.ID,
			OrgID: r.OrgID,
			Subject: Subject{
				HostID: r.HostID, Scope: Scope(r.Scope),
				CheckKey: r.CheckKey, Item: r.Item, Metric: r.Metric,
			},
			OpenedAt:          r.OpenedAt,
			Severity:          rules.State(r.Severity),
			Peak:              rules.State(r.PeakSeverity),
			SeverityChangedAt: r.SeverityChangedAt,

			ClosedAt:     db.TimePtr(r.ClosedAt),
			ClosedReason: CloseReason(derefOr(r.ClosedReason, "")),

			AckAt:       db.TimePtr(r.AcknowledgedAt),
			AckBy:       db.UUIDPtr(r.AcknowledgedBy),
			AckSeverity: rules.State(derefOr(r.AcknowledgedSeverity, "")),
			AckNote:     r.AckNote,

			SnoozedUntil:      db.TimePtr(r.SnoozedUntil),
			SnoozeUntilChange: r.SnoozeUntilChange,

			WasFlapping: r.WasFlapping,
		},
		HostName: r.HostName,
	}
}
