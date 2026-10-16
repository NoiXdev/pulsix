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
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

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

// OpenForHost implements Store.
func (s *SQLStore) OpenForHost(ctx context.Context, tx pgx.Tx, orgID, hostID uuid.UUID) ([]Alert, error) {
	rows, err := gen.New(tx).ListOpenAlertsForHost(ctx, gen.ListOpenAlertsForHostParams{OrgID: orgID, HostID: hostID})
	if err != nil {
		return nil, fmt.Errorf("open alerts for host: %w", err)
	}
	out := make([]Alert, len(rows))
	for i, r := range rows {
		out[i] = toAlert(r)
	}
	return out, nil
}

// Execute implements Store.
func (s *SQLStore) Execute(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, actions []Action) error {
	q := gen.New(tx)
	for _, a := range actions {
		var err error
		switch a.Kind {
		case ActionOpen:
			err = s.open(ctx, q, orgID, a)
		case ActionSeverity:
			err = s.raiseSeverity(ctx, q, orgID, a)
		case ActionFlapped:
			err = q.SetAlertFlapping(ctx, gen.SetAlertFlappingParams{ID: a.AlertID, OrgID: orgID})
		case ActionClose:
			err = s.close(ctx, q, orgID, a)
		default:
			err = fmt.Errorf("alerts: unrecognised action kind %q", a.Kind)
		}
		if err != nil {
			return fmt.Errorf("execute %s action: %w", a.Kind, err)
		}
	}
	return nil
}

// open is ActionOpen: a brand new row (InsertAlert's own comment explains
// why this is always an INSERT, never an update of a closed row), plus
// the one event every newly opened alert gets.
func (s *SQLStore) open(ctx context.Context, q *gen.Queries, orgID uuid.UUID, a Action) error {
	id, err := q.InsertAlert(ctx, gen.InsertAlertParams{
		OrgID: orgID, HostID: a.Subject.HostID, Scope: string(a.Subject.Scope),
		CheckKey: a.Subject.CheckKey, Item: a.Subject.Item, Metric: a.Subject.Metric,
		OpenedAt: a.At, Severity: string(a.Severity),
	})
	if err != nil {
		return fmt.Errorf("insert alert: %w", err)
	}
	return q.InsertAlertEvent(ctx, gen.InsertAlertEventParams{OrgID: orgID, AlertID: id, At: a.At, Kind: eventOpened})
}

// raiseAlertSeverityRank is RaiseAlertSeverity's own @severity_rank
// parameter: simply rules.StateRank delegated to, the single source of
// truth on the Go side, converted to the int32 RaiseAlertSeverityParams
// wants. The frozen copy is on the SQL side, not here: queries/alerts.sql's
// CASE (crit=3, warn=2, unknown=1) cannot call rules.StateRank — SQL has
// no way to — so it is a literal that must be kept in sync by hand, not
// one computed at runtime, and it is exactly the brief's own literal CASE
// text (which ranked unknown above warn) that got this wrong the first
// time. TestPeakSeverityRankMatchesRulesStateRank (store_sql_test.go) is
// the test the brief's own review focus asked for, proving the two still
// agree rather than trusting they do — driven across every ordered pair
// of the three severities an alert can carry, not just crit and warn,
// which outranks either way and so cannot expose a warn/unknown
// transposition.
func raiseAlertSeverityRank(s rules.State) int32 { return int32(rules.StateRank(s)) } //nolint:gosec // StateRank is 0..3

// raiseSeverity is ActionSeverity: the severity column always moves (the
// only reason Reconcile emits this action at all), the peak only rises
// (RaiseAlertSeverity's own CASE), and a severity_raised event is written
// only when it actually rose — a falling severity is a real column
// change with no event, "the column moves, the history does not gain
// noise" (the brief's own wording). ack_voided is independent of all of
// that: it is written whenever the action says so, regardless of which
// way the severity moved — and RaiseAlertSeverity's own CASE clears
// acknowledged_severity in that same UPDATE whenever a.VoidsAck is set, so
// the column cannot later read as acknowledged again (backlog 4.36) while
// disagreeing with the ack_voided event this function writes right below
// it for the very same action.
func (s *SQLStore) raiseSeverity(ctx context.Context, q *gen.Queries, orgID uuid.UUID, a Action) error {
	before, err := q.GetAlertSeverity(ctx, gen.GetAlertSeverityParams{ID: a.AlertID, OrgID: orgID})
	if err != nil {
		return fmt.Errorf("read current severity: %w", err)
	}

	err = q.RaiseAlertSeverity(ctx, gen.RaiseAlertSeverityParams{
		Severity: string(a.Severity), SeverityRank: raiseAlertSeverityRank(a.Severity),
		ClearsSnooze: a.ClearsSnoozeUntilChange, VoidsAck: a.VoidsAck, At: a.At, ID: a.AlertID, OrgID: orgID,
	})
	if err != nil {
		return fmt.Errorf("raise severity: %w", err)
	}

	if rules.StateRank(a.Severity) > rules.StateRank(rules.State(before)) {
		if err := q.InsertAlertEvent(ctx, gen.InsertAlertEventParams{
			OrgID: orgID, AlertID: a.AlertID, At: a.At, Kind: eventSeverityRaised,
		}); err != nil {
			return fmt.Errorf("insert severity_raised event: %w", err)
		}
	}
	if a.VoidsAck {
		if err := q.InsertAlertEvent(ctx, gen.InsertAlertEventParams{
			OrgID: orgID, AlertID: a.AlertID, At: a.At, Kind: eventAckVoided,
		}); err != nil {
			return fmt.Errorf("insert ack_voided event: %w", err)
		}
	}
	return nil
}

// close is ActionClose: the row closes with the action's own reason and
// timestamp, and always gets exactly one closed event — unlike
// severity_raised, closing is never conditional on anything but having
// been asked to.
func (s *SQLStore) close(ctx context.Context, q *gen.Queries, orgID uuid.UUID, a Action) error {
	reason := string(a.Reason)
	err := q.CloseAlert(ctx, gen.CloseAlertParams{
		ClosedAt: pgtype.Timestamptz{Time: a.At, Valid: true}, Reason: &reason, ID: a.AlertID, OrgID: orgID,
	})
	if err != nil {
		return fmt.Errorf("close alert: %w", err)
	}
	return q.InsertAlertEvent(ctx, gen.InsertAlertEventParams{OrgID: orgID, AlertID: a.AlertID, At: a.At, Kind: eventClosed})
}

// Sweep implements Store. It runs in its own transaction on the bare
// pool — never the pgx.Tx OpenForHost and Execute take — because it is
// not scoped to one host or one organization (design doc "The sweep"): it
// is driven by open alerts rather than by check_states, so it answers for
// subjects no host's evaluation reaches on a given tick, across every
// organization, with no org loop. What it catches and what it cannot is
// in queries/alerts.sql's own comment on SweepStaleAlerts; in short, a
// tuple whose check_states row is GONE. A check taken off a host leaves
// that row standing and is closed by Reconcile instead.
// SweepStaleAlerts itself writes the matching 'closed' event
// for every alert it sweeps, in the same statement — a post-review
// correction to the design doc, which specified only the UPDATE, from
// before alert_events existed; without it a swept alert would read on
// Task 10's own timeline as an episode that opened and never ended.
func (s *SQLStore) Sweep(ctx context.Context, now time.Time) (int, error) {
	tx, err := s.d.Pool().Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin sweep: %w", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck // no-op once committed

	n, err := gen.New(tx).SweepStaleAlerts(ctx, now)
	if err != nil {
		return 0, fmt.Errorf("sweep stale alerts: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit sweep: %w", err)
	}
	return int(n), nil
}

// GetAlert implements Store.
func (s *SQLStore) GetAlert(ctx context.Context, orgID, id uuid.UUID) (AlertWithHost, error) {
	r, err := gen.New(s.d).GetAlert(ctx, gen.GetAlertParams{OrgID: orgID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return AlertWithHost{}, ErrNotFound
	}
	if err != nil {
		return AlertWithHost{}, fmt.Errorf("get alert: %w", err)
	}
	return toAlertFromGetRow(r), nil
}

// textOrNil turns "" into a nil *string, the narg idiom every optional
// text Filter field below forwards to ListAlerts: an empty State or Scope
// means "match any", never the literal empty string.
func textOrNil[T ~string](v T) *string {
	if v == "" {
		return nil
	}
	s := string(v)
	return &s
}

// ListAlerts implements Store.
// It asks the query for one row more than limit — the standard way to
// learn "is there a next page" without a second COUNT, the same technique
// hosts.SQLStore.ListHosts and audit.SQLStore.List both use — and encodes
// the cursor from the last row it actually returns.
func (s *SQLStore) ListAlerts(ctx context.Context, orgID uuid.UUID, f Filter, cursor string, limit int) ([]AlertWithHost, string, error) {
	// Clamped here as well as in Service.List, through the same function,
	// so the two can never disagree and a direct call (a test, a future
	// caller) cannot reach the slicing below with a limit of zero — which
	// would index out[-1]. Service clamps so that every Store
	// implementation, fakes included, is handed a sane page size; this
	// clamps so that this one cannot be got wrong at all.
	limit = clampLimit(limit)
	cursorAt, cursorID, err := DecodeCursor(cursor)
	if err != nil {
		return nil, "", err
	}
	rows, err := gen.New(s.d).ListAlerts(ctx, gen.ListAlertsParams{
		OrgID: orgID, Open: f.Open, Severity: textOrNil(f.Severity), HostID: db.NullUUID(f.HostID),
		Scope: textOrNil(f.Scope), Acknowledged: f.Acknowledged,
		CursorOpenedAt: cursorTimeToPg(cursor, cursorAt), CursorID: cursorIDToPg(cursor, cursorID),
		RowLimit: int32(limit + 1), //nolint:gosec // limit is clamped to MaxListLimit by Service.List before it ever reaches here
	})
	if err != nil {
		return nil, "", fmt.Errorf("list alerts: %w", err)
	}
	out := make([]AlertWithHost, len(rows))
	for i, r := range rows {
		out[i] = toAlertFromListRow(r)
	}
	next := ""
	if len(out) > limit {
		last := out[limit-1]
		next = EncodeCursor(last.OpenedAt, last.ID)
		out = out[:limit]
	}
	return out, next, nil
}

// cursorTimeToPg and cursorIDToPg return an unset pgtype value for the
// empty cursor — the "first page" case. DecodeCursor already reports the
// zero time and uuid.Nil for it, but the query tests "cursor_opened_at IS
// NULL" to disable the keyset condition, and a zero timestamptz marked
// Valid is not NULL. The same pair hosts.SQLStore and audit.SQLStore each
// carry, for the same reason.
func cursorTimeToPg(cursor string, at time.Time) pgtype.Timestamptz {
	if cursor == "" {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: at, Valid: true}
}

func cursorIDToPg(cursor string, id uuid.UUID) pgtype.UUID {
	if cursor == "" {
		return pgtype.UUID{}
	}
	return pgtype.UUID{Bytes: id, Valid: true}
}

// Events implements Store.
func (s *SQLStore) Events(ctx context.Context, orgID, alertID uuid.UUID) ([]Event, error) {
	rows, err := gen.New(s.d).ListAlertEvents(ctx, gen.ListAlertEventsParams{OrgID: orgID, AlertID: alertID})
	if err != nil {
		return nil, fmt.Errorf("list alert events: %w", err)
	}
	out := make([]Event, len(rows))
	for i, r := range rows {
		out[i] = Event{ID: r.ID, At: r.At, Kind: r.Kind, ActorID: db.UUIDPtr(r.ActorID)}
	}
	return out, nil
}

// StateChanges implements Store.
func (s *SQLStore) StateChanges(ctx context.Context, orgID uuid.UUID, subject Subject, from, to time.Time) ([]StateChange, error) {
	rows, err := gen.New(s.d).ListStateChangesForTuple(ctx, gen.ListStateChangesForTupleParams{
		OrgID: orgID, HostID: subject.HostID, CheckKey: subject.CheckKey, Item: subject.Item, Metric: subject.Metric,
		FromAt: from, ToAt: to,
	})
	if err != nil {
		return nil, fmt.Errorf("list state changes: %w", err)
	}
	out := make([]StateChange, len(rows))
	for i, r := range rows {
		out[i] = StateChange{At: r.At, FromState: rules.State(r.FromState), ToState: rules.State(r.ToState)}
	}
	return out, nil
}

// occurrenceAt decides whether one raw maintenance_windows row (r) covers
// the instant now and, if so, the stretch's own Start/End — the same
// question internal/maintenance.ActiveAt answers, kept here rather than
// exported from that package because WindowsForHost is the only caller
// and needs the stretch's bounds, not just a bool. A one-off window reads
// its own starts_at/ends_at directly; a recurring one delegates to
// maintenance.ActiveAt for the yes/no and maintenance.Occurrences — asked
// for the sliver [now, now+1ns) — for the active stretch's own bounds,
// both already proven against the same cron math (occur_test.go). A
// window that does not evaluate (a hand-edited bad cron or timezone) is
// reported inactive rather than failing the whole call — suppression
// display is best-effort the same way OccurrencesBetween's own calendar
// is, never a safety gate.
func occurrenceAt(r gen.MaintenanceWindow, now time.Time) (maintenance.Occurrence, bool) {
	reason := maintenance.Reason(r.Reason)
	if r.Cron == nil {
		starts, ends := db.TimePtr(r.StartsAt), db.TimePtr(r.EndsAt)
		if starts == nil || ends == nil || now.Before(*starts) || !now.Before(*ends) {
			return maintenance.Occurrence{}, false
		}
		return maintenance.Occurrence{WindowID: r.ID, Reason: reason, Start: *starts, End: *ends}, true
	}
	if r.Timezone == nil || r.DurationSeconds == nil {
		return maintenance.Occurrence{}, false
	}
	w := maintenance.Window{
		ID: r.ID, Reason: reason, Cron: *r.Cron, Timezone: *r.Timezone,
		Duration: time.Duration(*r.DurationSeconds) * time.Second,
	}
	active, err := maintenance.ActiveAt(w, now)
	if err != nil || !active {
		return maintenance.Occurrence{}, false
	}
	occs, _, err := maintenance.Occurrences(w, now, now.Add(time.Nanosecond), 1)
	if err != nil || len(occs) == 0 {
		return maintenance.Occurrence{}, false
	}
	return occs[0], true
}

// WindowsForHost implements Store.
func (s *SQLStore) WindowsForHost(ctx context.Context, orgID, hostID uuid.UUID, now time.Time) ([]maintenance.Occurrence, error) {
	rows, err := gen.New(s.d).ListMaintenanceWindowsForHost(ctx, gen.ListMaintenanceWindowsForHostParams{
		OrgID: orgID, HostID: db.NullUUID(&hostID), Now: now,
	})
	if err != nil {
		return nil, fmt.Errorf("list maintenance windows for host: %w", err)
	}
	var out []maintenance.Occurrence
	for _, r := range rows {
		if occ, active := occurrenceAt(r, now); active {
			out = append(out, occ)
		}
	}
	return out, nil
}

// writeAlertAction runs fn inside a transaction of its own: an operator
// action has no caller-owned tx to run inside, unlike Execute, and the row
// update and its matching event (where one exists) must commit or roll
// back together, never partially.
//
// "Of its own" is true of every caller today but is not an unconditional
// property of db.DB.Begin: inside WithReadTx that helper hands back the
// SHARED transaction, so the Commit below would end the outer read
// transaction early for every other store still reading through it
// (db/scope.go says so itself). Unreachable at present — WithReadTx has
// one user, the inventory renderer, which never writes an alert — but
// stated here rather than left as a claim that happens to hold (backlog
// 4.38).
//
// It opens that transaction through db.DB.Begin, not the bare pool, so it
// is bound to the request's own organization scope and row-level security
// applies to every statement inside it — the same posture every read in
// this file already takes (v0.4a final review, Important). An earlier
// version reached for s.d.Pool().Begin and justified it as "the same
// posture Sweep uses"; that conflated two different things. Sweep is
// genuinely cross-organization and has no scope to bind. These four are
// single-organization requests arriving with one already bound, so
// bypassing it dropped one of the two layers that keep an acknowledgement
// or a snooze inside its own tenant — not a live leak, since the
// hand-written org_id predicates are all present, but the file was
// internally inconsistent about which layer it was relying on.
func (s *SQLStore) writeAlertAction(ctx context.Context, fn func(q *gen.Queries) error) error {
	tx, err := s.d.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck // no-op once committed
	if err := fn(gen.New(tx)); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// Acknowledge implements Store.
func (s *SQLStore) Acknowledge(ctx context.Context, orgID, id, actor uuid.UUID, note string, at time.Time) error {
	return s.writeAlertAction(ctx, func(q *gen.Queries) error {
		n, err := q.AcknowledgeAlert(ctx, gen.AcknowledgeAlertParams{
			At: pgtype.Timestamptz{Time: at, Valid: true}, Actor: db.NullUUID(&actor), Note: note,
			OrgID: orgID, ID: id,
		})
		if err != nil {
			return fmt.Errorf("acknowledge alert: %w", err)
		}
		if n == 0 {
			return fmt.Errorf("%w: alert is closed", ErrInvalidInput)
		}
		return q.InsertAlertActorEvent(ctx, gen.InsertAlertActorEventParams{
			OrgID: orgID, AlertID: id, At: at, Kind: eventAcknowledged, ActorID: db.NullUUID(&actor),
		})
	})
}

// Unacknowledge implements Store. actor and at are unused: no event is
// written (this method's own doc comment on Store explains why), so there
// is nothing here to stamp either against — both stay in the signature
// for symmetry with Snooze/Unsnooze, which do write one.
func (s *SQLStore) Unacknowledge(ctx context.Context, orgID, id, actor uuid.UUID, at time.Time) error {
	_, _ = actor, at
	return s.writeAlertAction(ctx, func(q *gen.Queries) error {
		n, err := q.UnacknowledgeAlert(ctx, gen.UnacknowledgeAlertParams{OrgID: orgID, ID: id})
		if err != nil {
			return fmt.Errorf("unacknowledge alert: %w", err)
		}
		if n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// Snooze implements Store.
func (s *SQLStore) Snooze(ctx context.Context, orgID, id, actor uuid.UUID, until *time.Time, untilChange bool, at time.Time) error {
	return s.writeAlertAction(ctx, func(q *gen.Queries) error {
		n, err := q.SnoozeAlert(ctx, gen.SnoozeAlertParams{
			Until: db.NullTime(until), UntilChange: untilChange, OrgID: orgID, ID: id,
		})
		if err != nil {
			return fmt.Errorf("snooze alert: %w", err)
		}
		if n == 0 {
			return ErrNotFound
		}
		return q.InsertAlertActorEvent(ctx, gen.InsertAlertActorEventParams{
			OrgID: orgID, AlertID: id, At: at, Kind: eventSnoozed, ActorID: db.NullUUID(&actor),
		})
	})
}

// Unsnooze implements Store.
func (s *SQLStore) Unsnooze(ctx context.Context, orgID, id, actor uuid.UUID, at time.Time) error {
	return s.writeAlertAction(ctx, func(q *gen.Queries) error {
		n, err := q.UnsnoozeAlert(ctx, gen.UnsnoozeAlertParams{OrgID: orgID, ID: id})
		if err != nil {
			return fmt.Errorf("unsnooze alert: %w", err)
		}
		if n == 0 {
			return ErrNotFound
		}
		return q.InsertAlertActorEvent(ctx, gen.InsertAlertActorEventParams{
			OrgID: orgID, AlertID: id, At: at, Kind: eventUnsnoozed, ActorID: db.NullUUID(&actor),
		})
	})
}
