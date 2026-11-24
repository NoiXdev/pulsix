package checks

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/db/gen"
)

// ForgetMetric implements Store (backlog 3.88): ForgetItem for one metric,
// in the same order and for the same reasons (ForgetItem's doc comment):
//
//  1. LockHostEvalCursor, waited for.
//  2. steps.Proceed, with the metric's reading, its item's newest
//     error-free reading and the item's other row count, read under the
//     lock — the expiry's re-check and the manual forget's last-row
//     refusal.
//  3. DeleteMetricCurrent, skipping a row an in-flight ingest holds;
//     nothing deleted means nothing to forget.
//  4. The metric's per-vantage probe rows (backlog 3.117), its states and
//     state-change history. The item's
//     check_items row stays: the item is still there.
//  5. MarkHostDirtyNow.
//  6. steps.Within — the metric's alert close — on the same transaction.
func (s *SQLStore) ForgetMetric(ctx context.Context, ref MetricRef, steps MetricForgetSteps) (bool, error) {
	err := s.d.WithOrg(ctx, ref.OrgID, func(tx pgx.Tx) error {
		return forgetMetric(ctx, tx, ref, steps)
	})
	if errors.Is(err, errForgetDeclined) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// forgetMetric is ForgetMetric's body on its transaction; errForgetDeclined
// rolls it back with nothing changed.
func forgetMetric(ctx context.Context, tx pgx.Tx, ref MetricRef, steps MetricForgetSteps) error {
	q := gen.New(tx)
	if _, err := q.LockHostEvalCursor(ctx, gen.LockHostEvalCursorParams{OrgID: ref.OrgID, HostID: ref.HostID}); err != nil {
		return fmt.Errorf("lock host evaluation: %w", err)
	}
	if steps.Proceed != nil {
		n, err := q.MetricNewest(ctx, gen.MetricNewestParams{
			OrgID: ref.OrgID, HostID: ref.HostID, CheckKey: ref.CheckKey, Item: ref.Item, Metric: ref.Metric,
		})
		if err != nil {
			return fmt.Errorf("read metric newest: %w", err)
		}
		if !steps.Proceed(MetricNewest{
			Found: n.MetricRows > 0, Metric: n.MetricAt, Item: n.ItemNewest, Siblings: int(n.ItemRows - n.MetricRows),
		}) {
			return errForgetDeclined
		}
	}
	deleted, err := q.DeleteMetricCurrent(ctx, gen.DeleteMetricCurrentParams{
		OrgID: ref.OrgID, HostID: ref.HostID, CheckKey: ref.CheckKey, Item: ref.Item, Metric: ref.Metric,
	})
	if err != nil {
		return fmt.Errorf("delete metric current: %w", err)
	}
	if deleted == 0 {
		return errForgetDeclined
	}
	if err := q.DeleteMetricProbeCurrent(ctx, gen.DeleteMetricProbeCurrentParams{
		OrgID: ref.OrgID, HostID: ref.HostID, CheckKey: ref.CheckKey, Item: ref.Item, Metric: ref.Metric,
	}); err != nil {
		return fmt.Errorf("delete metric probe current: %w", err)
	}
	if err := q.DeleteMetricStates(ctx, gen.DeleteMetricStatesParams{
		OrgID: ref.OrgID, HostID: ref.HostID, CheckKey: ref.CheckKey, Item: ref.Item, Metric: ref.Metric,
	}); err != nil {
		return fmt.Errorf("delete metric states: %w", err)
	}
	if err := q.DeleteMetricStateChanges(ctx, gen.DeleteMetricStateChangesParams{
		OrgID: ref.OrgID, HostID: ref.HostID, CheckKey: ref.CheckKey, Item: ref.Item, Metric: ref.Metric,
	}); err != nil {
		return fmt.Errorf("delete metric state changes: %w", err)
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

// ExpiredMetrics implements Store. Like ExpiredItems it reads every
// organization at once on the bare pool, with no organization bound; each
// metric it returns names its own organization, which ForgetMetric binds.
func (s *SQLStore) ExpiredMetrics(ctx context.Context, period time.Duration, limit int) ([]MetricRef, error) {
	rows, err := gen.New(s.d.Pool()).ListExpiredMetrics(ctx, gen.ListExpiredMetricsParams{
		PeriodSeconds: period.Seconds(),
		Lim:           int32(min(limit, maxExpiredBatch)), //nolint:gosec // bounded by maxExpiredBatch
	})
	if err != nil {
		return nil, fmt.Errorf("list expired metrics: %w", err)
	}
	out := make([]MetricRef, len(rows))
	for i, r := range rows {
		out[i] = MetricRef{OrgID: r.OrgID, HostID: r.HostID, CheckKey: r.CheckKey, Item: r.Item, Metric: r.Metric}
	}
	return out, nil
}
