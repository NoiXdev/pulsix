package triggers

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
)

// pendingWriteTimeout bounds one pending run's write-back (its savepoint,
// delete and delivery update) and the batch's commit and rollback once
// fire has returned: by then the run may have been enqueued, so the
// bookkeeping must not be abandoned merely because the batch's own
// deadline passed (the schedule ticker's rowWriteTimeout, same reasoning).
const pendingWriteTimeout = 10 * time.Second

// Collect implements PendingStore: the upsert and the delivery row share
// one transaction, so the ticker (which skips the locked row) never starts
// a window whose newest delivery is not recorded yet, and a window it is
// starting is never joined.
func (st *SQLStore) Collect(ctx context.Context, c Collect, d Delivery) (PendingRun, error) {
	var out PendingRun
	err := st.d.WithOrg(ctx, c.OrgID, func(tx pgx.Tx) error {
		q := st.q.WithTx(tx)
		row, err := q.CollectTriggerPendingRun(ctx, gen.CollectTriggerPendingRunParams{
			TriggerID: c.TriggerID, OrgID: c.OrgID, ID: c.ID, At: c.At, Due: c.Due, Deadline: c.Deadline,
			DeliveryID: d.ID, Overrides: c.Overrides,
		})
		if err != nil {
			return fmt.Errorf("collect pending run: %w", err)
		}
		out = toPendingRun(row)
		d.PendingID = &out.ID
		return recordDelivery(ctx, q, d)
	})
	if err != nil {
		return PendingRun{}, err
	}
	return out, nil
}

// Pending implements PendingStore.
func (st *SQLStore) Pending(ctx context.Context, orgID, triggerID uuid.UUID) (*PendingRun, error) {
	row, err := st.q.GetTriggerPendingRun(ctx, gen.GetTriggerPendingRunParams{OrgID: orgID, TriggerID: triggerID})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get pending run: %w", err)
	}
	p := toPendingRun(row)
	return &p, nil
}

// FirePending implements PendingStore on an organization-scoped
// transaction. The lock waits for a ticker that is starting the same run;
// once that one committed, the row is gone and this answers
// ErrNothingPending.
func (st *SQLStore) FirePending(ctx context.Context, orgID, triggerID uuid.UUID, fire FireFunc) (Firing, error) {
	var f Firing
	err := st.d.WithOrg(ctx, orgID, func(tx pgx.Tx) error {
		q := st.q.WithTx(tx)
		row, err := q.LockTriggerPendingRun(ctx, gen.LockTriggerPendingRunParams{OrgID: orgID, TriggerID: triggerID})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNothingPending
		}
		if err != nil {
			return fmt.Errorf("lock pending run: %w", err)
		}
		run, err := toDueRun(row.TriggerPendingRun, row.JobTrigger)
		if err != nil {
			return err
		}
		if f = fire(ctx, run); f.Keep {
			return nil
		}
		wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), pendingWriteTimeout)
		defer cancel()
		return applyFiring(wctx, q, run.Pending, f)
	})
	return f, err
}

// DiscardPending implements PendingStore. The delete waits for a ticker
// that is starting the run; once that one committed there is nothing left
// to discard.
func (st *SQLStore) DiscardPending(ctx context.Context, orgID, triggerID uuid.UUID, note string) error {
	return st.d.WithOrg(ctx, orgID, func(tx pgx.Tx) error {
		q := st.q.WithTx(tx)
		row, err := q.DiscardTriggerPendingRun(ctx, gen.DiscardTriggerPendingRunParams{OrgID: orgID, TriggerID: triggerID})
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNothingPending
		}
		if err != nil {
			return fmt.Errorf("discard pending run: %w", err)
		}
		return settleDeliveries(ctx, q, toPendingRun(row), Firing{Outcome: OutcomeCollected, Note: note})
	})
}

// ProcessDue implements PendingStore: one raw-pool transaction per batch,
// with no pulsix.org_id bound — the bypass policy applies, so the claim
// sees every organization, and every other statement names its org_id.
// fire runs while the rows are locked; its enqueue uses the jobs service's
// own org-scoped transactions on other connections and commits before the
// row's write-back, so a failure in between leaves the row to be started
// again, which the run's idempotency key (settleIdempotencyKey) turns into
// a replay of the same run. Each row is written back in its own savepoint:
// a bad row never takes the batch's other rows with it.
//
// ctx bounds the claim, each fire and the start of every row; the write-
// backs and the commit run detached from it under pendingWriteTimeout, so
// a deadline hit while firing a later row never discards one already
// started. The count is how many rows were started, dropped or recorded as
// failed — not how many were locked.
func (st *SQLStore) ProcessDue(ctx context.Context, now time.Time, limit int, fire FireFunc) (int, error) {
	tx, err := st.d.Pool().Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin settle tick: %w", err)
	}
	defer func() {
		rbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), pendingWriteTimeout)
		defer cancel()
		tx.Rollback(rbCtx) //nolint:errcheck // no-op once committed
	}()
	q := gen.New(tx)
	rows, err := q.LockDueTriggerPendingRuns(ctx, gen.LockDueTriggerPendingRunsParams{Now: now, Lim: int32(limit)}) //nolint:gosec // limit is settleBatchSize
	if err != nil {
		return 0, fmt.Errorf("lock due pending runs: %w", err)
	}
	applied := 0
	for i, r := range rows {
		if ctx.Err() != nil {
			st.logRemaining(ctx, i, len(rows))
			break
		}
		if st.processRow(ctx, tx, r.TriggerPendingRun, r.JobTrigger, fire) {
			applied++
		}
	}
	commitCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), pendingWriteTimeout)
	defer cancel()
	if err := tx.Commit(commitCtx); err != nil {
		return 0, fmt.Errorf("commit settle tick: %w", err)
	}
	return applied, nil
}

func (st *SQLStore) logRemaining(ctx context.Context, done, locked int) {
	st.log.WarnContext(context.WithoutCancel(ctx), "settle batch deadline reached; leaving the remaining pending runs for the next tick",
		"started", done, "remaining", locked-done)
}

// processRow fires one locked row and writes the outcome back in its own
// savepoint; false when the row was kept or its write-back failed (it
// stays, and the next tick tries it again).
func (st *SQLStore) processRow(ctx context.Context, tx pgx.Tx, p gen.TriggerPendingRun, t gen.JobTrigger, fire FireFunc) (applied bool) {
	defer func() {
		if rec := recover(); rec != nil {
			st.log.ErrorContext(context.WithoutCancel(ctx), "pending run panicked outside its start; leaving it for the next tick",
				"trigger", p.TriggerID, "org", p.OrgID, "panic", rec, "stack", string(debug.Stack()))
			applied = false
		}
	}()
	run, err := toDueRun(p, t)
	f := Firing{Outcome: OutcomeFailed, Note: "the trigger could not be read (see the manager log)"}
	if err != nil {
		st.log.ErrorContext(ctx, "decode trigger of a pending run", "trigger", p.TriggerID, "org", p.OrgID, "err", err)
	} else if f = fire(ctx, run); f.Keep {
		return false
	}
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), pendingWriteTimeout)
	defer cancel()
	sp, err := tx.Begin(wctx)
	if err != nil {
		st.log.ErrorContext(wctx, "begin pending run savepoint; leaving it for the next tick", "trigger", p.TriggerID, "org", p.OrgID, "err", err)
		return false
	}
	defer sp.Rollback(wctx) //nolint:errcheck // no-op once committed
	if err := applyFiring(wctx, gen.New(sp), toPendingRun(p), f); err != nil {
		st.log.ErrorContext(wctx, "write back pending run; leaving it for the next tick", "trigger", p.TriggerID, "org", p.OrgID, "err", err)
		return false
	}
	if err := sp.Commit(wctx); err != nil {
		st.log.ErrorContext(wctx, "commit pending run savepoint; leaving it for the next tick", "trigger", p.TriggerID, "org", p.OrgID, "err", err)
		return false
	}
	return true
}

// applyFiring deletes the started (or dropped) window and tells its
// deliveries what became of them.
func applyFiring(ctx context.Context, q *gen.Queries, p PendingRun, f Firing) error {
	if err := q.DeleteTriggerPendingRun(ctx, gen.DeleteTriggerPendingRunParams{OrgID: p.OrgID, TriggerID: p.TriggerID, ID: p.ID}); err != nil {
		return fmt.Errorf("delete pending run: %w", err)
	}
	return settleDeliveries(ctx, q, p, f)
}

func settleDeliveries(ctx context.Context, q *gen.Queries, p PendingRun, f Firing) error {
	if err := q.SettleTriggerDeliveries(ctx, gen.SettleTriggerDeliveriesParams{
		Outcome: string(f.Outcome), JobGroupID: db.NullUUID(f.JobGroupID), Note: f.Note,
		OrgID: p.OrgID, TriggerID: p.TriggerID, PendingID: p.ID,
	}); err != nil {
		return fmt.Errorf("settle collected deliveries: %w", err)
	}
	return nil
}

func toPendingRun(r gen.TriggerPendingRun) PendingRun {
	return PendingRun{
		ID: r.ID, OrgID: r.OrgID, TriggerID: r.TriggerID, FirstAt: r.FirstAt, LastAt: r.LastAt,
		DueAt: r.DueAt, DeadlineAt: r.DeadlineAt, Deliveries: int(r.Deliveries), LastDeliveryID: r.LastDeliveryID,
	}
}

func toDueRun(p gen.TriggerPendingRun, t gen.JobTrigger) (DueRun, error) {
	trig, err := toTrigger(t)
	if err != nil {
		return DueRun{}, err
	}
	return DueRun{Pending: toPendingRun(p), Overrides: p.Overrides, Trigger: trig}, nil
}
