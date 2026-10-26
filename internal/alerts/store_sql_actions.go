// The four operator actions — acknowledge, snooze and their reversals —
// and the transaction they share. Split out of store_sql.go under
// backlog 4.26.
//
// Each opens a transaction of ITS own (writeAlertAction), unlike
// store_sql_execute.go's actions, which run inside the evaluation claim
// their caller already holds. That is the whole reason these are a file
// rather than more of that one: an operator action has no claim to ride
// on, and its row update and matching event must commit together.
package alerts

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
)

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

// closedOrMissing explains a zero-row update for the two statements whose
// WHERE carries `closed_at IS NULL` — AcknowledgeAlert and SnoozeAlert
// (backlog 4.31). For those two, no row matched means either that the
// alert does not exist (or belongs to another organization, which is the
// same thing from here) or that it closed; the row count cannot say
// which, and both of them used to guess — Acknowledge always answered
// "alert is closed" and Snooze always ErrNotFound, so each was wrong in
// exactly the case the other got right. An operator acknowledging an id
// that does not exist was told it was closed; one snoozing an alert that
// had just closed was told it never existed.
//
// The read runs only on this path, inside the same transaction as the
// failed update, so it costs nothing in the normal case and cannot
// disagree with it. UnacknowledgeAlert and UnsnoozeAlert carry no
// closed_at predicate and are deliberately left alone: zero rows there
// has only one meaning, and their ErrNotFound is already correct.
func closedOrMissing(ctx context.Context, q *gen.Queries, orgID, id uuid.UUID) error {
	if _, err := q.GetAlert(ctx, gen.GetAlertParams{OrgID: orgID, ID: id}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("explain the refused alert action: %w", err)
	}
	return fmt.Errorf("%w: alert is closed", ErrInvalidInput)
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
			return closedOrMissing(ctx, q, orgID, id)
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
			return closedOrMissing(ctx, q, orgID, id)
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
