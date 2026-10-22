package schedules

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"runtime/debug"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/db"
	"github.com/noixdev/pulsix/internal/db/gen"
	"github.com/noixdev/pulsix/internal/notify"
)

// rowWriteTimeout bounds one row's write-back (its savepoint, run insert,
// schedule advance and trim) once decide has already returned, and —
// since backlog 2.86 — the batch's own final commit and deferred rollback
// too. These are a handful of statements on a connection the batch's own
// transaction already holds, not a fresh pool acquisition, so a bound
// this generous only guards against a genuinely stuck driver or network —
// never against ordinary pool contention, which is what the batch's own
// decide/enqueue deadline is for (see ProcessDue). Fix round 2, New
// Important: this write-back must never be abandoned merely because that
// deadline has passed — by the time decide has returned, the row's
// enqueue may already have happened.
const rowWriteTimeout = 10 * time.Second

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
type DueSQLStore struct {
	d      *db.DB
	rand   io.Reader
	log    *slog.Logger
	outbox DecisionOutbox
}

// DecisionOutbox queues the notifications of a recorded decision inside
// the row's savepoint (notify.Outbox), so the notification commits with
// the decision or not at all.
type DecisionOutbox interface {
	EnqueueDecision(ctx context.Context, tx pgx.Tx, d notify.Decision) error
}

// SetOutbox injects the decision outbox (spec 2026-09-18 §7); nil sends
// no decision notifications.
func (st *DueSQLStore) SetOutbox(o DecisionOutbox) { st.outbox = o }

// NewDueSQLStore builds the store. log defaults to slog.Default() when
// nil, matching Service and Ticker; rand is the entropy source for a
// corrupt row's failed-run id (fix round 2: this used to call
// uuid.NewV7() directly, bypassing the app's injected randomness, like
// every other id in this package).
func NewDueSQLStore(d *db.DB, rand io.Reader, log *slog.Logger) *DueSQLStore {
	if log == nil {
		log = slog.Default()
	}
	return &DueSQLStore{d: d, rand: rand, log: log}
}

// ProcessDue implements DueStore. ctx normally carries the batch's own
// decide/enqueue deadline (Ticker.Tick bounds it to a small multiple of
// the tick — fix round 1, Important #3): a starved pool connection or a
// stuck decide() then fails fast instead of blocking the batch forever.
//
// That deadline applies only to deciding each row (the call to decide,
// passed ctx directly below) and to starting a new row at all — the loop
// stops as soon as ctx is done, leaving every row it had not yet reached
// untouched, still due. It never reaches a row's write-back: fix round 1
// used ctx for every savepoint and for the final commit too, so a batch
// that overran its deadline mid-row rolled back every row it had already
// decided and enqueued — including rows whose savepoint had already
// committed — and they fired again on the very next tick (fix round 2,
// New Important). processRow now writes each decided row back on
// context.WithoutCancel(ctx), under its own short rowWriteTimeout, and
// the final commit below does the same.
//
// Both the deferred rollback and the final commit bound that detached
// context with rowWriteTimeout too (backlog 2.86), the same pairing the
// digest ticker's own ProcessDue uses for its equivalent pair
// (digest_ticker.go): a plain context.WithoutCancel(ctx) with no timeout
// at all waits forever on an unresponsive database, which is exactly what
// makes App.Close hang rather than time out.
func (st *DueSQLStore) ProcessDue(ctx context.Context, now time.Time, limit int, decide func(ctx context.Context, s Schedule) Decision) (int, error) {
	tx, err := st.d.Pool().Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin schedule tick: %w", err)
	}
	defer func() {
		rbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), rowWriteTimeout)
		defer cancel()
		tx.Rollback(rbCtx) //nolint:errcheck // no-op once committed
	}()
	q := gen.New(tx)
	rows, err := q.LockDueSchedules(ctx, gen.LockDueSchedulesParams{Now: now, Lim: int32(limit)}) //nolint:gosec // limit is batchSize
	if err != nil {
		return 0, fmt.Errorf("lock due schedules: %w", err)
	}
	for i, r := range rows {
		if ctx.Err() != nil {
			// The batch's own deadline (or the manager's shutdown) is
			// gone: every row from here on is left exactly as it is,
			// still locked until the commit below releases it, rather
			// than started and each logging its own failure to begin —
			// one line here beats one per remaining row.
			st.log.WarnContext(context.WithoutCancel(ctx), "schedule batch deadline reached; leaving the remaining rows due for the next tick",
				"decided", i, "remaining", len(rows)-i)
			break
		}
		st.processRow(ctx, tx, r, now, decide)
	}
	commitCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), rowWriteTimeout)
	defer cancel()
	if err := tx.Commit(commitCtx); err != nil {
		return 0, fmt.Errorf("commit schedule tick: %w", err)
	}
	return len(rows), nil
}

// processRow decides one locked row on ctx (the batch's decide/enqueue
// deadline) and records it inside its own savepoint. Every write below —
// the savepoint itself, insertRun, AdvanceSchedule, TrimScheduleRuns and
// the savepoint's own commit — runs on a fresh context.WithoutCancel(ctx)
// bounded only by rowWriteTimeout, detached from ctx's own deadline, so a
// deadline hit while deciding a *later* row can never discard a write
// this row already earned (fix round 2, New Important).
//
// A panic anywhere in this function — decoding the row, deciding it, or
// writing its run and advance — is recovered: Service.Decide already
// recovers a panic inside deciding/enqueueing itself, including inside
// cronexpr and the audit call (fix round 1, Important #1; fix round 2
// closed the two gaps a re-review found in that recover's own coverage),
// but toSchedule and the write helpers below run outside it, so this is
// the belt to Decide's suspenders, and it is what makes one row's panic
// leave the other rows in the batch, and the batch's own commit,
// unaffected.
//
// Any failure — decode, decide, or a write — simply leaves the row as it
// was: neither its run nor its next_run_at advance, so the row stays due
// and the next tick's SKIP LOCKED retries it, the same at-least-once
// guarantee a failed commit already gives (global-constraints.md, "Ticker
// atomicity is at-least-once"). The one exception is a row that never
// decodes at all (see decideOne): that failure is deterministic, not
// transient, so the row is stopped instead of retried forever.
func (st *DueSQLStore) processRow(ctx context.Context, tx pgx.Tx, r gen.Schedule, now time.Time, decide func(ctx context.Context, s Schedule) Decision) {
	defer func() {
		if rec := recover(); rec != nil {
			st.log.ErrorContext(context.WithoutCancel(ctx), "schedule row panicked outside Decide; leaving it due for the next tick",
				"schedule", r.ID, "org", r.OrgID, "panic", rec, "stack", string(debug.Stack()))
		}
	}()

	run, next := st.decideOne(ctx, r, now, decide)

	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), rowWriteTimeout)
	defer cancel()

	sp, err := tx.Begin(wctx)
	if err != nil {
		st.log.ErrorContext(wctx, "begin schedule row savepoint; leaving it due for the next tick", "schedule", r.ID, "org", r.OrgID, "err", err)
		return
	}
	committed := false
	defer func() {
		if !committed {
			sp.Rollback(context.WithoutCancel(ctx)) //nolint:errcheck // no-op once committed
		}
	}()
	sq := gen.New(sp)

	if err := insertRun(wctx, sq, run); err != nil {
		st.log.ErrorContext(wctx, "record schedule run; leaving it due for the next tick", "schedule", r.ID, "org", r.OrgID, "err", err)
		return
	}
	if err := sq.AdvanceSchedule(wctx, gen.AdvanceScheduleParams{
		OrgID: r.OrgID, ID: r.ID, LastRunAt: db.NullTime(&run.DecidedAt), NextRunAt: db.NullTime(next),
	}); err != nil {
		st.log.ErrorContext(wctx, "advance schedule; leaving it due for the next tick", "schedule", r.ID, "org", r.OrgID, "err", err)
		return
	}
	if err := sq.TrimScheduleRuns(wctx, gen.TrimScheduleRunsParams{OrgID: r.OrgID, ScheduleID: r.ID, Keep: keepRuns}); err != nil {
		st.log.ErrorContext(wctx, "trim schedule runs; leaving it due for the next tick", "schedule", r.ID, "org", r.OrgID, "err", err)
		return
	}
	if st.outbox != nil {
		// The outbox runs in its own nested savepoint: a failure loses the
		// notification, never the decision (re-deciding would re-run it).
		if err := st.outbox.EnqueueDecision(wctx, sp, notify.Decision{
			OrgID: r.OrgID, ScheduleID: r.ID, RunID: run.ID, Outcome: string(run.Outcome), Reason: run.Reason,
			DueAt: run.DueAt, DecidedAt: run.DecidedAt,
		}); err != nil {
			st.log.ErrorContext(wctx, "queue schedule notification; the decision stands, its notification is lost", "schedule", r.ID, "org", r.OrgID, "err", err)
		}
	}
	if err := sp.Commit(wctx); err != nil {
		st.log.ErrorContext(wctx, "commit schedule row savepoint; leaving it due for the next tick", "schedule", r.ID, "org", r.OrgID, "err", err)
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
func (st *DueSQLStore) decideOne(ctx context.Context, r gen.Schedule, now time.Time, decide func(ctx context.Context, s Schedule) Decision) (Run, *time.Time) {
	sch, err := toSchedule(r)
	if err != nil {
		st.log.ErrorContext(ctx, "schedule row does not decode; stopping it", "schedule", r.ID, "org", r.OrgID, "err", err)
		dueAt := now
		if t := db.TimePtr(r.NextRunAt); t != nil {
			dueAt = *t
		}
		id, idErr := uuid.NewV7FromReader(st.rand)
		if idErr != nil {
			panic(idErr) //nolint:forbidigo // entropy failure is unrecoverable, like uuid.Must (Service.newID does the same); processRow's own recover still protects the batch
		}
		return Run{
			ID: id, OrgID: r.OrgID, ScheduleID: r.ID, DueAt: dueAt, DecidedAt: now,
			Outcome: OutcomeFailed, Reason: "the schedule's stored data is corrupt; edit and save it",
		}, nil
	}
	d := decide(ctx, sch)
	return d.Run, d.NextRunAt
}
