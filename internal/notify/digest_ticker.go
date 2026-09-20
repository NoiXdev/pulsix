package notify

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

// Digest ticker tuning: it looks for due digests every minute; a digest
// more than maxDigestLate overdue (the manager was down) is skipped, not
// sent days late, and the next week's is scheduled.
//
// batchDeadlineFactor and minBatchDeadline bound one ProcessDue call the
// way schedules.Ticker bounds its own (final review, Important #5). The
// batch holds a transaction and FOR NO KEY UPDATE row locks while the
// builder issues its statements on *other* pool connections, so a starved
// pool would otherwise pin both indefinitely — the v0.2b pool-exhaustion
// shape the schedule ticker was explicitly fixed for, of which the digest
// ticker inherited only the SKIP LOCKED half. The floor keeps a short tick
// (a test's, a dev instance's) from failing an ordinary slow build.
// digestWriteTimeout bounds each row's own write-back, which is detached
// from the batch deadline so a deadline hit while building a *later*
// digest cannot discard a write an earlier one already earned.
const (
	DefaultDigestTick   = time.Minute
	maxDigestLate       = 24 * time.Hour
	digestBatch         = 20
	batchDeadlineFactor = 4
	minBatchDeadline    = 10 * time.Second
	digestWriteTimeout  = 10 * time.Second
)

// DueDigest is one organization's digest occurrence that is due.
type DueDigest struct {
	OrgID      uuid.UUID
	Occurrence time.Time
	Timezone   string
}

// Key is the digest's idempotency key: one digest per organization and
// occurrence however often the ticker retries.
func (d DueDigest) Key() string { return "digest:" + d.Occurrence.UTC().Format(time.RFC3339) }

// DigestBuilder computes a week's summary (schedules.Service).
type DigestBuilder interface {
	Digest(ctx context.Context, orgID uuid.UUID, from, to time.Time) (DigestSummary, error)
}

// DigestEnqueuer queues a digest's deliveries inside tx (Outbox).
type DigestEnqueuer interface {
	EnqueueDigest(ctx context.Context, tx pgx.Tx, d DueDigest, sum DigestSummary) error
}

// DigestQueue locks due digests and advances each one whose handle
// succeeds (DigestSQLStore).
type DigestQueue interface {
	ProcessDue(ctx context.Context, now time.Time, limit int, handle func(ctx context.Context, tx pgx.Tx, d DueDigest) error) (int, error)
}

// DigestTicker sends weekly digests. Several managers may run one each:
// the due scan is FOR NO KEY UPDATE SKIP LOCKED, like the schedule
// ticker's, and the digest key dedupes what slips through.
type DigestTicker struct {
	queue   DigestQueue
	builder DigestBuilder
	outbox  DigestEnqueuer
	tick    time.Duration
	now     func() time.Time
	sleep   func(ctx context.Context, d time.Duration) error
	log     *slog.Logger
}

// NewDigestTicker wires the ticker; zero tick, nil now, sleep and log take
// defaults.
func NewDigestTicker(queue DigestQueue, builder DigestBuilder, outbox DigestEnqueuer, tick time.Duration, now func() time.Time, sleep func(ctx context.Context, d time.Duration) error, log *slog.Logger) *DigestTicker {
	if tick <= 0 {
		tick = DefaultDigestTick
	}
	if now == nil {
		now = time.Now
	}
	if sleep == nil {
		sleep = func(ctx context.Context, d time.Duration) error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(d):
				return nil
			}
		}
	}
	if log == nil {
		log = slog.Default()
	}
	return &DigestTicker{queue: queue, builder: builder, outbox: outbox, tick: tick, now: now, sleep: sleep, log: log}
}

// Run ticks until ctx is cancelled.
func (t *DigestTicker) Run(ctx context.Context) {
	for {
		t.tickSafely(ctx)
		if err := t.sleep(ctx, t.tick); err != nil {
			return
		}
	}
}

func (t *DigestTicker) tickSafely(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			t.log.ErrorContext(ctx, "digest tick panicked", "panic", r, "stack", string(debug.Stack()))
		}
	}()
	if _, err := t.Tick(ctx); err != nil && ctx.Err() == nil {
		t.log.ErrorContext(ctx, "digest tick failed", "err", err)
	}
}

// digestPeriod resolves timezone once and returns both ends of the
// digest's week in that same Location: occurrence itself, and the same
// wall-clock time seven days earlier. Both ends must share the org's
// Location, not just from — the renderer (render.go's EventDigest case,
// placeholders.go's digest.period) formats each end with a bare
// Time.Format and labels the pair with the org's timezone, so an end
// still carrying occurrence's own UTC Location (as read back from
// timestamptz) would print the wrong local date whenever the org's date
// differs from the UTC date at the occurrence — e.g. Pacific/Auckland at
// a Monday 08:00 due time is still Sunday in UTC — making the printed
// period six or eight days instead of seven. Converting to the org's
// zone before subtracting also keeps a week that crosses a DST change
// starting at the same local wall-clock time it ends at, rather than
// subtracting exactly 168 hours the way arithmetic on a UTC-located value
// would.
func digestPeriod(occurrence time.Time, timezone string) (from, to time.Time, err error) {
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("%w: unknown timezone %q", ErrInvalidInput, timezone)
	}
	to = occurrence.In(loc)
	return to.AddDate(0, 0, -7), to, nil
}

// Tick handles every due digest once. The batch runs under its own
// deadline (final review, Important #5): a batch that cannot complete in
// that time — most likely a starved pool connection — fails fast instead
// of holding its row locks, and the pool connection an HTTP handler is
// waiting on, indefinitely. The same digests are still due on the next
// tick, the same at-least-once guarantee a failed commit already gives.
func (t *DigestTicker) Tick(ctx context.Context) (int, error) {
	now := t.now()
	batchCtx, cancel := context.WithTimeout(ctx, t.batchDeadline())
	defer cancel()
	return t.queue.ProcessDue(batchCtx, now, digestBatch, func(ctx context.Context, tx pgx.Tx, d DueDigest) error {
		if now.Sub(d.Occurrence) > maxDigestLate {
			t.log.WarnContext(ctx, "weekly digest more than a day late; skipping it", "org", d.OrgID, "due", d.Occurrence)
			return nil
		}
		from, to, err := digestPeriod(d.Occurrence, d.Timezone)
		if err != nil {
			// processOne validates the stored timezone (via NextDigestRun)
			// before ever calling handle, so this is unreachable in
			// practice; kept as a safe failure rather than a panic.
			return fmt.Errorf("digest period: %w", err)
		}
		sum, err := t.builder.Digest(db.WithOrgScope(ctx, d.OrgID), d.OrgID, from, to)
		if err != nil {
			return fmt.Errorf("build digest: %w", err)
		}
		sum.Timezone = d.Timezone
		return t.outbox.EnqueueDigest(ctx, tx, d, sum)
	})
}

// batchDeadline bounds one batch: batchDeadlineFactor x tick, never less
// than minBatchDeadline.
func (t *DigestTicker) batchDeadline() time.Duration {
	return max(batchDeadlineFactor*t.tick, minBatchDeadline)
}

// DigestSQLStore is the digest ticker's store: the raw pool, bypass
// policy, every statement names its org_id.
type DigestSQLStore struct {
	d   *db.DB
	log *slog.Logger
}

// NewDigestSQLStore builds the store; nil log is slog.Default.
func NewDigestSQLStore(d *db.DB, log *slog.Logger) *DigestSQLStore {
	if log == nil {
		log = slog.Default()
	}
	return &DigestSQLStore{d: d, log: log}
}

// ProcessDue locks due digests and runs handle for each inside its own
// savepoint. A digest whose handle succeeds is advanced to its next
// occurrence (computed from now, so a long outage never replays weeks);
// one whose handle fails stays due for the next tick.
func (st *DigestSQLStore) ProcessDue(ctx context.Context, now time.Time, limit int, handle func(ctx context.Context, tx pgx.Tx, d DueDigest) error) (int, error) {
	tx, err := st.d.Pool().Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin digest tick: %w", err)
	}
	defer func() {
		// Detached and bounded, like every other write-back here: a
		// rollback that waits forever on an unresponsive database is
		// what makes App.Close hang.
		rbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), digestWriteTimeout)
		defer cancel()
		tx.Rollback(rbCtx) //nolint:errcheck // no-op once committed
	}()
	rows, err := gen.New(tx).LockDueDigests(ctx, gen.LockDueDigestsParams{Now: now, Lim: int32(limit)}) //nolint:gosec // limit is digestBatch
	if err != nil {
		return 0, fmt.Errorf("lock due digests: %w", err)
	}
	for i, r := range rows {
		if ctx.Err() != nil {
			// The batch's own deadline (or the manager's shutdown) is
			// gone: every digest from here on is left exactly as it is,
			// still locked until the commit below releases it, and still
			// due for the next tick — one line here beats one per row.
			st.log.WarnContext(context.WithoutCancel(ctx), "digest batch deadline reached; leaving the remaining digests due for the next tick",
				"handled", i, "remaining", len(rows)-i)
			break
		}
		st.processOne(ctx, tx, r, now, handle)
	}
	commitCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), digestWriteTimeout)
	defer cancel()
	if err := tx.Commit(commitCtx); err != nil {
		return 0, fmt.Errorf("commit digest tick: %w", err)
	}
	return len(rows), nil
}

func (st *DigestSQLStore) processOne(ctx context.Context, tx pgx.Tx, r gen.NotificationDigest, now time.Time, handle func(ctx context.Context, tx pgx.Tx, d DueDigest) error) {
	due := DueDigest{OrgID: r.OrgID, Timezone: r.Timezone}
	if t := db.TimePtr(r.NextRunAt); t != nil {
		due.Occurrence = *t
	}
	// Every write below — the savepoint, the disable, the advance and
	// the savepoint's own commit — runs on a fresh detached context
	// bounded only by digestWriteTimeout, so a batch deadline reached
	// while building a *later* digest can never discard a write this row
	// already earned. Only handle itself runs on ctx, the work phase the
	// batch deadline is there to bound.
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), digestWriteTimeout)
	defer cancel()
	sp, err := tx.Begin(wctx)
	if err != nil {
		st.log.ErrorContext(wctx, "begin digest savepoint", "org", r.OrgID, "err", err)
		return
	}
	// The next occurrence is computed — and the stored settings' timezone
	// therefore validated — before handle runs, not after: otherwise a
	// digest whose timezone a direct DB edit or a tzdata update broke
	// would already be built and queued (digestPeriod falling back to a
	// broken period label) by the time this got around to disabling it.
	next, err := NextDigestRun(int(r.Weekday), r.AtTime, r.Timezone, now)
	if err != nil {
		// Stored settings that no longer evaluate stop the digest rather
		// than retry forever (the CHECK needs next_run_at while enabled, so
		// disable it), and skip the send entirely.
		st.log.ErrorContext(wctx, "weekly digest settings do not evaluate; disabling the digest", "org", r.OrgID, "err", err)
		if _, err := sp.Exec(wctx, `UPDATE notification_digests SET enabled = false, next_run_at = NULL WHERE org_id = $1`, r.OrgID); err != nil {
			_ = sp.Rollback(wctx)
			st.log.ErrorContext(wctx, "advance weekly digest", "org", r.OrgID, "err", err)
			return
		}
		if err := sp.Commit(wctx); err != nil {
			st.log.ErrorContext(wctx, "commit digest savepoint", "org", r.OrgID, "err", err)
		}
		return
	}
	if err := handle(ctx, sp, due); err != nil {
		_ = sp.Rollback(wctx)
		st.log.ErrorContext(wctx, "weekly digest failed; retrying next tick", "org", r.OrgID, "err", err)
		return
	}
	if err := gen.New(sp).AdvanceNotificationDigest(wctx, gen.AdvanceNotificationDigestParams{NextRunAt: db.NullTime(&next), LastPeriod: due.Key(), OrgID: r.OrgID}); err != nil {
		_ = sp.Rollback(wctx)
		st.log.ErrorContext(wctx, "advance weekly digest", "org", r.OrgID, "err", err)
		return
	}
	if err := sp.Commit(wctx); err != nil {
		st.log.ErrorContext(wctx, "commit digest savepoint", "org", r.OrgID, "err", err)
	}
}
