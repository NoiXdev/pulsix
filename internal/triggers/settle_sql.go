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

// pendingWriteTimeout bounds one pending run's write-back (its delete and
// delivery update), commit and rollback once fire has returned: by then
// the run may have been enqueued, so the bookkeeping must not be abandoned
// merely because the pass's own deadline passed (the schedule ticker's
// rowWriteTimeout, same reasoning).
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

// Supersede implements PendingStore.
func (st *SQLStore) Supersede(ctx context.Context, orgID, triggerID uuid.UUID) (*uuid.UUID, error) {
	id, err := st.q.SupersedeTriggerPendingRun(ctx, gen.SupersedeTriggerPendingRunParams{OrgID: orgID, TriggerID: triggerID})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("supersede pending run: %w", err)
	}
	return &id, nil
}

// NoteWindow implements PendingStore.
func (st *SQLStore) NoteWindow(ctx context.Context, orgID, triggerID, windowID uuid.UUID, note string) error {
	return settleDeliveries(ctx, st.q, PendingRun{ID: windowID, OrgID: orgID, TriggerID: triggerID}, Firing{Outcome: OutcomeCollected, Note: note})
}

// ProcessDue implements PendingStore: every pending run is claimed and
// written back in a raw-pool transaction of its own, with no pulsix.org_id
// bound — the bypass policy applies, so the claim sees every organization,
// and every other statement names its org_id. One row per transaction
// keeps the row lock as short as one start: a delivery to a trigger never
// waits behind other triggers' starts (review fix 5).
//
// fire runs while the row is locked; its enqueue uses the jobs service's
// own org-scoped transactions on other connections and commits before the
// row's write-back, so a failure in between leaves the row to be started
// again — which fire turns into a replay of the same run (see fire).
//
// ctx bounds the claims and each fire; the write-back and the commit run
// detached from it under pendingWriteTimeout, so a deadline hit while
// firing never discards a start already made. A row that is kept, or whose
// write-back failed, is skipped for the rest of this pass. The count is how
// many rows were started, dropped or recorded as failed.
func (st *SQLStore) ProcessDue(ctx context.Context, now time.Time, limit int, fire FireFunc) (int, error) {
	skip := []uuid.UUID{}
	applied := 0
	for applied+len(skip) < limit {
		if ctx.Err() != nil {
			st.log.WarnContext(context.WithoutCancel(ctx), "settle pass deadline reached; leaving the remaining pending runs for the next tick", "started", applied)
			return applied, nil
		}
		id, claimed, ok, err := st.processOne(ctx, now, skip, fire)
		switch {
		case err != nil:
			return applied, err
		case !claimed:
			return applied, nil
		case ok:
			applied++
		default:
			skip = append(skip, id)
		}
	}
	return applied, nil
}

// processOne claims one due pending run in its own transaction, fires it
// and writes the outcome back. claimed is false when none was due; ok is
// false when the row was kept or its write-back failed (it stays for the
// next tick).
func (st *SQLStore) processOne(ctx context.Context, now time.Time, skip []uuid.UUID, fire FireFunc) (id uuid.UUID, claimed, ok bool, err error) {
	tx, err := st.d.Pool().Begin(ctx)
	if err != nil {
		return uuid.Nil, false, false, fmt.Errorf("begin settle claim: %w", err)
	}
	defer func() {
		rbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), pendingWriteTimeout)
		defer cancel()
		tx.Rollback(rbCtx) //nolint:errcheck // no-op once committed
	}()
	r, err := gen.New(tx).LockDueTriggerPendingRun(ctx, gen.LockDueTriggerPendingRunParams{Skip: skip, Now: now})
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, false, false, nil
	}
	if err != nil {
		return uuid.Nil, false, false, fmt.Errorf("lock due pending run: %w", err)
	}
	id = r.TriggerPendingRun.TriggerID
	if !st.processRow(ctx, tx, r.TriggerPendingRun, r.JobTrigger, fire) {
		return id, true, false, nil
	}
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), pendingWriteTimeout)
	defer cancel()
	if err := tx.Commit(wctx); err != nil {
		st.log.ErrorContext(wctx, "commit pending run; leaving it for the next tick", "trigger", id, "org", r.TriggerPendingRun.OrgID, "err", err)
		return id, true, false, nil
	}
	return id, true, true, nil
}

// processRow fires one locked row and writes the outcome back on tx;
// false when the row was kept or its write-back failed.
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
		run = DueRun{Pending: toPendingRun(p)}
	} else if f = fire(ctx, run); f.Keep {
		return false
	}
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), pendingWriteTimeout)
	defer cancel()
	if err := applyFiring(wctx, gen.New(tx), run.Pending, f); err != nil {
		st.log.ErrorContext(wctx, "write back pending run; leaving it for the next tick", "trigger", p.TriggerID, "org", p.OrgID, "err", err)
		return false
	}
	return true
}

// applyFiring deletes the started (or dropped) window and tells its
// deliveries what became of them. On a replay (f.reopen) the deliveries
// recorded after the run's first start stay pending as a new window of the
// same row — whose collected values are the latest delivery's — and only
// the earlier ones are counted into the run.
func applyFiring(ctx context.Context, q *gen.Queries, p PendingRun, f Firing) error {
	if f.reopen != nil {
		reopened, err := reopenLater(ctx, q, p, *f.reopen)
		if err != nil {
			return err
		}
		if reopened {
			return settleDeliveries(ctx, q, p, f)
		}
	}
	if err := q.DeleteTriggerPendingRun(ctx, gen.DeleteTriggerPendingRunParams{OrgID: p.OrgID, TriggerID: p.TriggerID, ID: p.ID}); err != nil {
		return fmt.Errorf("delete pending run: %w", err)
	}
	return settleDeliveries(ctx, q, p, f)
}

// reopenLater moves the window's deliveries after r.after into the new
// window r.id; false when there are none (the row is then deleted).
func reopenLater(ctx context.Context, q *gen.Queries, p PendingRun, r reopen) (bool, error) {
	later, err := q.LaterTriggerDeliveries(ctx, gen.LaterTriggerDeliveriesParams{After: r.after, OrgID: p.OrgID, TriggerID: p.TriggerID, PendingID: p.ID})
	if err != nil {
		return false, fmt.Errorf("count later deliveries: %w", err)
	}
	if later.N == 0 {
		return false, nil
	}
	deadline := later.FirstAt.Add(r.maxWait)
	if err := q.ReopenTriggerPendingRun(ctx, gen.ReopenTriggerPendingRunParams{
		NewID: r.id, FirstAt: later.FirstAt, LastAt: later.LastAt, Deliveries: later.N, Deadline: deadline,
		Due: later.LastAt.Add(r.settle), OrgID: p.OrgID, TriggerID: p.TriggerID, ID: p.ID,
	}); err != nil {
		return false, fmt.Errorf("reopen pending run: %w", err)
	}
	if err := q.MoveLaterTriggerDeliveries(ctx, gen.MoveLaterTriggerDeliveriesParams{
		NewID: r.id, OrgID: p.OrgID, TriggerID: p.TriggerID, PendingID: p.ID, After: r.after,
	}); err != nil {
		return false, fmt.Errorf("move later deliveries: %w", err)
	}
	return true, nil
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
