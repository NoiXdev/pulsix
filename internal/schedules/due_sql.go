package schedules

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
)

// DueSQLStore is the ticker's store: one raw-pool transaction per batch,
// with no pulsix.org_id bound — the bypass policy applies, so the due scan
// sees every organization, and every other statement names its org_id.
// decide runs while the rows are locked; its enqueue uses the normal
// org-scoped services on their own connections.
//
// Each locked row is written back inside its own savepoint (processRow),
// so a bad row — one that fails to decode, panics inside decide, or fails
// a write — rolls back only its own savepoint. Rows already committed to a
// savepoint earlier in the same batch are unaffected, and are still part
// of the outer transaction's one commit at the end. Before fix round 1
// (Important #2), a single bad row aborted the whole batch after earlier
// rows had already been enqueued: their schedule_runs rows and
// next_run_at advances were rolled back too, so the next tick re-fired
// every one of them, forever, if the bad row's failure was persistent
// (e.g. corrupt stored JSON) rather than transient.
type DueSQLStore struct{ d *db.DB }

// NewDueSQLStore builds the store.
func NewDueSQLStore(d *db.DB) *DueSQLStore { return &DueSQLStore{d: d} }

// ProcessDue implements DueStore. ctx normally carries the batch's own
// deadline (Ticker.Tick bounds it to a small multiple of the tick — fix
// round 1, Important #3): a starved pool connection or a stuck decide()
// then fails fast, releasing the locked rows (and the pool connection an
// HTTP handler is waiting on) instead of holding them forever.
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
		processRow(ctx, tx, r, now, decide)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit schedule tick: %w", err)
	}
	return len(rows), nil
}

// processRow decides and records one locked row inside its own savepoint.
// A panic anywhere in this function — decoding the row, deciding it, or
// writing its run and advance — is recovered: Service.Decide already
// recovers a panic inside deciding/enqueueing itself (fix round 1,
// Important #1) and returns a normal failed Decision, but toSchedule and
// the write helpers below run outside that recover, so this is the belt
// to Decide's suspenders, and it is what makes one row's panic leave the
// other rows in the batch, and the batch's own commit, unaffected.
//
// Any failure — decode, decide, or a write — simply leaves the row as it
// was: neither its run nor its next_run_at advance, so the row stays due
// and the next tick's SKIP LOCKED retries it, the same at-least-once
// guarantee a failed commit already gives (global-constraints.md, "Ticker
// atomicity is at-least-once"). The one exception is a row that never
// decodes at all (see decideOne): that failure is deterministic, not
// transient, so the row is stopped instead of retried forever.
func processRow(ctx context.Context, tx pgx.Tx, r gen.Schedule, now time.Time, decide func(ctx context.Context, s Schedule) Decision) {
	defer func() {
		if rec := recover(); rec != nil {
			slog.Default().ErrorContext(ctx, "schedule row panicked outside Decide; leaving it due for the next tick",
				"schedule", r.ID, "org", r.OrgID, "panic", rec, "stack", string(debug.Stack()))
		}
	}()
	sp, err := tx.Begin(ctx)
	if err != nil {
		slog.Default().ErrorContext(ctx, "begin schedule row savepoint; leaving it due for the next tick", "schedule", r.ID, "org", r.OrgID, "err", err)
		return
	}
	committed := false
	defer func() {
		if !committed {
			sp.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck // no-op once committed
		}
	}()
	sq := gen.New(sp)

	run, next := decideOne(ctx, r, now, decide)

	if err := insertRun(ctx, sq, run); err != nil {
		slog.Default().ErrorContext(ctx, "record schedule run; leaving it due for the next tick", "schedule", r.ID, "org", r.OrgID, "err", err)
		return
	}
	if err := sq.AdvanceSchedule(ctx, gen.AdvanceScheduleParams{
		OrgID: r.OrgID, ID: r.ID, LastRunAt: db.NullTime(&run.DecidedAt), NextRunAt: db.NullTime(next),
	}); err != nil {
		slog.Default().ErrorContext(ctx, "advance schedule; leaving it due for the next tick", "schedule", r.ID, "org", r.OrgID, "err", err)
		return
	}
	if err := sq.TrimScheduleRuns(ctx, gen.TrimScheduleRunsParams{OrgID: r.OrgID, ScheduleID: r.ID, Keep: keepRuns}); err != nil {
		slog.Default().ErrorContext(ctx, "trim schedule runs; leaving it due for the next tick", "schedule", r.ID, "org", r.OrgID, "err", err)
		return
	}
	if err := sp.Commit(ctx); err != nil {
		slog.Default().ErrorContext(ctx, "commit schedule row savepoint; leaving it due for the next tick", "schedule", r.ID, "org", r.OrgID, "err", err)
		return
	}
	committed = true
}

// decideOne decodes one locked row and decides it, or — when the row's
// stored guards/simple/extra_vars no longer decode (a hand-edited row, or
// a struct laid out differently by a later version) — returns a failed
// run with next_run_at cleared, stopping the schedule the same way Decide
// stops one with an unparsable cron expression. Unlike every other
// failure processRow can hit, a decode failure is deterministic: retrying
// it every tick would only re-fail forever, so the schedule is stopped
// instead of left due (fix round 1, Important #2).
func decideOne(ctx context.Context, r gen.Schedule, now time.Time, decide func(ctx context.Context, s Schedule) Decision) (Run, *time.Time) {
	sch, err := toSchedule(r)
	if err != nil {
		slog.Default().ErrorContext(ctx, "schedule row does not decode; stopping it", "schedule", r.ID, "org", r.OrgID, "err", err)
		dueAt := now
		if t := db.TimePtr(r.NextRunAt); t != nil {
			dueAt = *t
		}
		return Run{
			ID: uuid.Must(uuid.NewV7()), OrgID: r.OrgID, ScheduleID: r.ID, DueAt: dueAt, DecidedAt: now,
			Outcome: OutcomeFailed, Reason: "the schedule's stored data is corrupt; edit and save it",
		}, nil
	}
	d := decide(ctx, sch)
	return d.Run, d.NextRunAt
}
