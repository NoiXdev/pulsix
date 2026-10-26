// The evaluator's own write path: the alerts a host currently has open,
// and the actions Reconcile decided for them. Split out of store_sql.go
// under backlog 4.26 — everything here runs inside the CALLER's
// transaction (the per-host evaluation claim), which is what sets it
// apart from the operator actions in store_sql_actions.go, each of which
// opens one of its own.
//
// OpenForHost is a read and still lives here rather than in
// store_sql_read.go: it is the input Execute's actions are computed
// from, on the same transaction, and separating the pair would hide
// that they are one exchange.
package alerts

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/rules"
)

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
			err = changedOne(q.SetAlertFlapping(ctx, gen.SetAlertFlappingParams{ID: a.AlertID, OrgID: orgID}))
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

// changedOne turns an :execrows result into an error when the statement
// matched nothing (backlog 4.24). Execute's three UPDATEs key on
// (id, org_id) and carry no state predicate, so no row matched means one
// thing only: this organization has no such alert.
//
// They were :exec, and the event write that follows each was the only
// thing standing between a wrong id and a silent no-op — which is not a
// guard at all for two of the three. alert_events.alert_id references
// alerts(id) ALONE, not (org_id, id), so an alert belonging to ANOTHER
// organization leaves the UPDATE empty while the event insert succeeds:
// a history row filed under the caller's organization, about an alert it
// did not touch and cannot see. ActionFlapped writes no event at all
// (there is no "flapped" kind), so nothing caught its no-op even in
// principle.
//
// Call sites pass ids they just read from this same organization, so
// none of this is reachable today. That is exactly the reliance the
// review asked to remove: the guarantee now comes from the statement
// rather than from every future caller remembering.
func changedOne(n int64, err error) error {
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
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
//
// The severity this replaced comes back from the UPDATE itself (backlog
// 4.25). It used to be a GetAlertSeverity SELECT issued immediately
// before — two statements, so two readings of a row another transaction
// could change in between, with the event decision hanging on the older
// one. queries/alerts.sql's own comment on RaiseAlertSeverity has the
// mechanism.
func (s *SQLStore) raiseSeverity(ctx context.Context, q *gen.Queries, orgID uuid.UUID, a Action) error {
	before, err := q.RaiseAlertSeverity(ctx, gen.RaiseAlertSeverityParams{
		Severity: string(a.Severity), SeverityRank: raiseAlertSeverityRank(a.Severity),
		ClearsSnooze: a.ClearsSnoozeUntilChange, VoidsAck: a.VoidsAck, At: a.At, ID: a.AlertID, OrgID: orgID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		// This organization has no such alert. The same ANSWER the other
		// two actions give through changedOne (backlog 4.24), reported
		// here by a RETURNING statement that matched nothing — a caller
		// matching on ErrNotFound must not have to know which action it
		// asked for.
		return ErrNotFound
	}
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
	if err := changedOne(q.CloseAlert(ctx, gen.CloseAlertParams{
		ClosedAt: pgtype.Timestamptz{Time: a.At, Valid: true}, Reason: &reason, ID: a.AlertID, OrgID: orgID,
	})); err != nil {
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
