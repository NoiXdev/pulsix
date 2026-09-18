package schedules

import (
	"context"
	"fmt"
	"time"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
)

// DueSQLStore is the ticker's store: one raw-pool transaction per batch,
// with no pulsix.org_id bound — the bypass policy applies, so the due scan
// sees every organization, and every other statement names its org_id.
// decide runs while the rows are locked; its enqueue uses the normal
// org-scoped services on their own connections.
type DueSQLStore struct{ d *db.DB }

// NewDueSQLStore builds the store.
func NewDueSQLStore(d *db.DB) *DueSQLStore { return &DueSQLStore{d: d} }

// ProcessDue implements DueStore.
func (st *DueSQLStore) ProcessDue(ctx context.Context, now time.Time, limit int, decide func(ctx context.Context, s Schedule) Decision) (int, error) {
	tx, err := st.d.Pool().Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin schedule tick: %w", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck // no-op once committed
	q := gen.New(tx)
	rows, err := q.LockDueSchedules(ctx, gen.LockDueSchedulesParams{Now: now, Lim: int32(limit)}) //nolint:gosec // limit is batchSize
	if err != nil {
		return 0, fmt.Errorf("lock due schedules: %w", err)
	}
	for _, r := range rows {
		sch, err := toSchedule(r)
		if err != nil {
			return 0, err
		}
		d := decide(ctx, sch)
		if err := insertRun(ctx, q, d.Run); err != nil {
			return 0, err
		}
		if err := q.AdvanceSchedule(ctx, gen.AdvanceScheduleParams{
			OrgID: sch.OrgID, ID: sch.ID, LastRunAt: db.NullTime(&d.Run.DecidedAt), NextRunAt: db.NullTime(d.NextRunAt),
		}); err != nil {
			return 0, fmt.Errorf("advance schedule: %w", err)
		}
		if err := q.TrimScheduleRuns(ctx, gen.TrimScheduleRunsParams{OrgID: sch.OrgID, ScheduleID: sch.ID, Keep: keepRuns}); err != nil {
			return 0, fmt.Errorf("trim schedule runs: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit schedule tick: %w", err)
	}
	return len(rows), nil
}
