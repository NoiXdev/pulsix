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
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
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
)

// Store is what Task 7's evaluator wiring calls to carry out one host's
// Reconcile verdict, and what the ticker calls once per tick for the
// sweep (design doc: "Ticker.Tick calls AlertSink.Sweep once per tick").
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
}

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

		OpenedAt: r.OpenedAt,
		Severity: rules.State(r.Severity),
		Peak:     rules.State(r.PeakSeverity),

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

// raiseAlertSeverityRank is the rank RaiseAlertSeverity's own CASE must
// agree with — rules.StateRank, not the brief's literal CASE text (which
// ranked unknown above warn): the brief's own review focus calls this
// duplication out by name and asks for a test proving the two agree
// rather than trusting that they do, and
// TestPeakSeverityRankMatchesRulesStateRank (store_sql_test.go) is that
// test, driven across every ordered pair of the three severities an alert
// can carry — not just crit and warn, which outranks either way and so
// cannot expose a warn/unknown transposition. queries/alerts.sql carries
// the fixed ordering (crit=3, warn=2, unknown=1); this is the Go side of
// the same number, never recomputed from rules.StateRank at runtime so
// that a change to one without the other fails loudly in that test
// instead of silently agreeing by accident.
func raiseAlertSeverityRank(s rules.State) int32 { return int32(rules.StateRank(s)) } //nolint:gosec // StateRank is 0..3

// raiseSeverity is ActionSeverity: the severity column always moves (the
// only reason Reconcile emits this action at all), the peak only rises
// (RaiseAlertSeverity's own CASE), and a severity_raised event is written
// only when it actually rose — a falling severity is a real column
// change with no event, "the column moves, the history does not gain
// noise" (the brief's own wording). ack_voided is independent of all of
// that: it is written whenever the action says so, regardless of which
// way the severity moved.
func (s *SQLStore) raiseSeverity(ctx context.Context, q *gen.Queries, orgID uuid.UUID, a Action) error {
	before, err := q.GetAlertSeverity(ctx, gen.GetAlertSeverityParams{ID: a.AlertID, OrgID: orgID})
	if err != nil {
		return fmt.Errorf("read current severity: %w", err)
	}

	err = q.RaiseAlertSeverity(ctx, gen.RaiseAlertSeverityParams{
		Severity: string(a.Severity), SeverityRank: raiseAlertSeverityRank(a.Severity),
		ClearsSnooze: a.ClearsSnoozeUntilChange, ID: a.AlertID, OrgID: orgID,
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
// not scoped to one host or one organization (design doc "The sweep"):
// driven by open alerts rather than by check_states, so a host whose last
// check was removed and therefore never becomes due again still gets its
// alert closed.
func (s *SQLStore) Sweep(ctx context.Context, now time.Time) (int, error) {
	tx, err := s.d.Pool().Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin sweep: %w", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck // no-op once committed

	n, err := gen.New(tx).SweepStaleAlerts(ctx, pgtype.Timestamptz{Time: now, Valid: true})
	if err != nil {
		return 0, fmt.Errorf("sweep stale alerts: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit sweep: %w", err)
	}
	return int(n), nil
}
