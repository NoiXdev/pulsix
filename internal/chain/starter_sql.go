package chain

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
)

// LockDue implements StarterStore: one raw-pool transaction without
// pulsix.org_id (the bypass policy sees every organization; every statement
// names its org_id) that holds the batch's row locks until Commit. The
// starts themselves run through the org-scoped jobs service on other
// connections while the locks are held, so a second manager's SKIP LOCKED
// scan never hands it the same row.
func (s *SQLStore) LockDue(ctx context.Context, now time.Time, limit int) (DueBatch, error) {
	tx, err := s.d.Pool().Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin follow-up batch: %w", err)
	}
	rows, err := gen.New(tx).LockDueFollowUps(ctx, gen.LockDueFollowUpsParams{Now: now, Lim: int32(limit)}) //nolint:gosec // limit is a small batch size
	if err != nil {
		_ = tx.Rollback(context.WithoutCancel(ctx))
		return nil, fmt.Errorf("lock due follow-ups: %w", err)
	}
	out := make([]FollowUp, len(rows))
	for i, r := range rows {
		out[i] = lockedFollowUp(r)
	}
	return &sqlBatch{tx: tx, rows: out, log: s.log}, nil
}

// sqlBatch is one locked batch of due rows.
type sqlBatch struct {
	tx   pgx.Tx
	rows []FollowUp
	log  *slog.Logger
}

func (b *sqlBatch) Rows() []FollowUp { return b.rows }

func (b *sqlBatch) Commit(ctx context.Context) error {
	if err := b.tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit follow-up batch: %w", err)
	}
	return nil
}

func (b *sqlBatch) Rollback(ctx context.Context) error { return b.tx.Rollback(ctx) }

// Write implements DueBatch. A failure or a panic rolls back only this
// row's savepoint (the deferred rollbacks are no-ops once committed); the
// row stays due.
func (b *sqlBatch) Write(ctx context.Context, f FollowUp, o Outcome, notify func(ctx context.Context, tx pgx.Tx) error) error {
	sp, err := b.tx.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin follow-up savepoint: %w", err)
	}
	defer sp.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck // no-op once committed
	if err := writeOutcome(ctx, gen.New(sp), f, o); err != nil {
		return err
	}
	if notify != nil {
		b.notify(ctx, sp, f, notify)
	}
	if err := sp.Commit(ctx); err != nil {
		return fmt.Errorf("commit follow-up savepoint: %w", err)
	}
	return nil
}

// notify runs the notification in a savepoint nested in the row's: its
// failure loses the notification, never the outcome. The hook logs its own
// error.
func (b *sqlBatch) notify(ctx context.Context, sp pgx.Tx, f FollowUp, notify func(ctx context.Context, tx pgx.Tx) error) {
	n, err := sp.Begin(ctx)
	if err != nil {
		b.log.ErrorContext(ctx, "begin follow-up notification savepoint; the notification is lost", "follow_up", f.ID, "org", f.OrgID, "err", err)
		return
	}
	defer n.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck // no-op once committed
	if notify(ctx, n) != nil {
		return
	}
	if err := n.Commit(ctx); err != nil {
		b.log.ErrorContext(ctx, "release follow-up notification savepoint; the notification is lost", "follow_up", f.ID, "org", f.OrgID, "err", err)
	}
}

func writeOutcome(ctx context.Context, q *gen.Queries, f FollowUp, o Outcome) error {
	if o.CountAttempt {
		next := o.NextAttemptAt
		if o.State != StateDue {
			next = o.At
		}
		if _, err := q.BumpFollowUpAttempt(ctx, gen.BumpFollowUpAttemptParams{OrgID: f.OrgID, ID: f.ID, Error: o.Reason, NextAttemptAt: next}); err != nil {
			return fmt.Errorf("count follow-up attempt: %w", err)
		}
		if o.State == StateDue {
			return nil
		}
	}
	var n int64
	var err error
	switch o.State {
	case StateStarted:
		n, err = q.MarkFollowUpStarted(ctx, gen.MarkFollowUpStartedParams{
			OrgID: f.OrgID, ID: f.ID, ChildGroupID: db.NullUUID(&o.ChildGroupID), Reason: o.Reason, Now: o.At,
		})
	case StateSkipped:
		n, err = q.MarkFollowUpSkipped(ctx, gen.MarkFollowUpSkippedParams{OrgID: f.OrgID, ID: f.ID, Reason: o.Reason})
	case StateDue:
		n, err = q.MarkFollowUpWaiting(ctx, gen.MarkFollowUpWaitingParams{OrgID: f.OrgID, ID: f.ID, Reason: o.Reason, NextAttemptAt: o.NextAttemptAt})
	default:
		return fmt.Errorf("follow-up outcome %q is not a starter state", o.State)
	}
	if err != nil {
		return fmt.Errorf("record follow-up %s: %w", o.State, err)
	}
	if n != 1 {
		return fmt.Errorf("follow-up %s is no longer due", f.ID)
	}
	return nil
}
