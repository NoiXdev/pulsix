package checks

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/db/gen"
)

// errForgetDeclined rolls a forget transaction back when there turned out
// to be nothing to forget; ForgetItem reports it as false, never as an
// error.
var errForgetDeclined = errors.New("forget declined")

// ForgetItem implements Store (backlog 3.12b). One transaction per item,
// bound to the item's organization (WithOrg), whatever the caller's
// context is bound to: the hourly expiry calls it with no organization at
// all.
//
// The order is what makes it safe to run beside the rule evaluator and
// ingest:
//
//  1. LockHostEvalCursor — the row lock the evaluator holds for the whole
//     of one host's evaluation (rules' LockOneEvalCursor). Taken first and
//     waited for, so an evaluation in progress finishes before anything
//     here is read or deleted, and none starts until this commits (the
//     evaluator skips a locked cursor). That is what stops an evaluation
//     from writing the item's states or alerts between the deletes below,
//     or closing an alert this is about to close as forgotten.
//  2. steps.Proceed, with the item's newest readings read under the lock.
//  3. DeleteItemCurrent, skipping rows an in-flight ingest holds. Ingest
//     takes its check_current rows first and the cursor last
//     (MarkHostDirty), so such an ingest is waiting for this transaction
//     and lands after it, re-creating the item; waiting for its rows here
//     would be a deadlock. Nothing deleted means nothing to forget.
//  4. The item's states and state-change history: only the evaluator
//     writes them, and only under the lock held since step 1, so no
//     orphan state can appear behind this. Then its check_items row,
//     which only the evaluator's discovery writes, under the same lock.
//  5. MarkHostDirtyNow, so the host's roll-up is re-evaluated without the
//     forgotten item.
//  6. steps.Within — the alert close — on the same transaction.
func (s *SQLStore) ForgetItem(ctx context.Context, ref ItemRef, steps ForgetSteps) (bool, error) {
	err := s.d.WithOrg(ctx, ref.OrgID, func(tx pgx.Tx) error {
		return forgetItem(ctx, tx, ref, steps)
	})
	if errors.Is(err, errForgetDeclined) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// forgetItem is ForgetItem's body on its transaction; errForgetDeclined
// rolls it back with nothing changed.
func forgetItem(ctx context.Context, tx pgx.Tx, ref ItemRef, steps ForgetSteps) error {
	q := gen.New(tx)
	if _, err := q.LockHostEvalCursor(ctx, gen.LockHostEvalCursorParams{OrgID: ref.OrgID, HostID: ref.HostID}); err != nil {
		return fmt.Errorf("lock host evaluation: %w", err)
	}
	if steps.Proceed != nil {
		n, err := q.ItemNewest(ctx, gen.ItemNewestParams{
			OrgID: ref.OrgID, HostID: ref.HostID, CheckKey: ref.CheckKey, Item: ref.Item,
		})
		if err != nil {
			return fmt.Errorf("read item newest: %w", err)
		}
		if !steps.Proceed(ItemNewest{
			Rows: int(n.ItemRows), Item: n.ItemNewest, Key: n.KeyNewest, Host: n.HostNewest,
			HasAgent: n.HasAgent, AgentSeen: n.AgentSeen, AgentConnected: n.AgentConnected,
		}) {
			return errForgetDeclined
		}
	}
	deleted, err := q.DeleteItemCurrent(ctx, gen.DeleteItemCurrentParams{
		OrgID: ref.OrgID, HostID: ref.HostID, CheckKey: ref.CheckKey, Item: ref.Item,
	})
	if err != nil {
		return fmt.Errorf("delete item current: %w", err)
	}
	if deleted == 0 {
		return errForgetDeclined
	}
	if err := q.DeleteItemStates(ctx, gen.DeleteItemStatesParams{
		OrgID: ref.OrgID, HostID: ref.HostID, CheckKey: ref.CheckKey, Item: ref.Item,
	}); err != nil {
		return fmt.Errorf("delete item states: %w", err)
	}
	if err := q.DeleteItemStateChanges(ctx, gen.DeleteItemStateChangesParams{
		OrgID: ref.OrgID, HostID: ref.HostID, CheckKey: ref.CheckKey, Item: ref.Item,
	}); err != nil {
		return fmt.Errorf("delete item state changes: %w", err)
	}
	if err := q.DeleteItemCheckItem(ctx, gen.DeleteItemCheckItemParams{
		OrgID: ref.OrgID, HostID: ref.HostID, CheckKey: ref.CheckKey, Item: ref.Item,
	}); err != nil {
		return fmt.Errorf("delete discovered item: %w", err)
	}
	if err := q.MarkHostDirtyNow(ctx, gen.MarkHostDirtyNowParams{OrgID: ref.OrgID, HostID: ref.HostID}); err != nil {
		return fmt.Errorf("mark host dirty: %w", err)
	}
	if steps.Within != nil {
		if err := steps.Within(tx); err != nil {
			return err
		}
	}
	return nil
}

// ExpiredItems implements Store. It reads every organization at once, so
// it runs on the bare pool with no organization bound — where the
// org_isolation_bypass policy applies — the posture alerts' Sweep and the
// evaluator's claim take for the same reason. Each item it returns names
// its own organization, which ForgetItem then binds. An agent counts as
// the host's sign of life only after agentCountsAfter connected, the same
// bound itemExpired's agentCounts applies.
func (s *SQLStore) ExpiredItems(ctx context.Context, cutoff time.Time, period time.Duration, limit int) ([]ItemRef, error) {
	rows, err := gen.New(s.d.Pool()).ListExpiredItems(ctx, gen.ListExpiredItemsParams{
		Cutoff: cutoff, PeriodSeconds: period.Seconds(), AgentCountsAfterSeconds: agentCountsAfter.Seconds(),
		Lim: int32(min(limit, maxExpiredBatch)), //nolint:gosec // bounded by maxExpiredBatch
	})
	if err != nil {
		return nil, fmt.Errorf("list expired items: %w", err)
	}
	out := make([]ItemRef, len(rows))
	for i, r := range rows {
		out[i] = ItemRef{OrgID: r.OrgID, HostID: r.HostID, CheckKey: r.CheckKey, Item: r.Item}
	}
	return out, nil
}

// maxExpiredBatch caps one selection whatever limit a caller passes, so
// the conversion to the statement's int32 can never wrap. The expiry loop
// asks for far fewer per round (the spec's "a few thousand").
const maxExpiredBatch = 100_000
