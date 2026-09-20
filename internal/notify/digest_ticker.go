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
const (
	DefaultDigestTick = time.Minute
	maxDigestLate     = 24 * time.Hour
	digestBatch       = 20
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

// Tick handles every due digest once.
func (t *DigestTicker) Tick(ctx context.Context) (int, error) {
	now := t.now()
	return t.queue.ProcessDue(ctx, now, digestBatch, func(ctx context.Context, tx pgx.Tx, d DueDigest) error {
		if now.Sub(d.Occurrence) > maxDigestLate {
			t.log.WarnContext(ctx, "weekly digest more than a day late; skipping it", "org", d.OrgID, "due", d.Occurrence)
			return nil
		}
		sum, err := t.builder.Digest(db.WithOrgScope(ctx, d.OrgID), d.OrgID, d.Occurrence.AddDate(0, 0, -7), d.Occurrence)
		if err != nil {
			return fmt.Errorf("build digest: %w", err)
		}
		sum.Timezone = d.Timezone
		return t.outbox.EnqueueDigest(ctx, tx, d, sum)
	})
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
	defer tx.Rollback(context.WithoutCancel(ctx))                                                       //nolint:errcheck // no-op once committed
	rows, err := gen.New(tx).LockDueDigests(ctx, gen.LockDueDigestsParams{Now: now, Lim: int32(limit)}) //nolint:gosec // limit is digestBatch
	if err != nil {
		return 0, fmt.Errorf("lock due digests: %w", err)
	}
	for _, r := range rows {
		st.processOne(ctx, tx, r, now, handle)
	}
	if err := tx.Commit(context.WithoutCancel(ctx)); err != nil {
		return 0, fmt.Errorf("commit digest tick: %w", err)
	}
	return len(rows), nil
}

func (st *DigestSQLStore) processOne(ctx context.Context, tx pgx.Tx, r gen.NotificationDigest, now time.Time, handle func(ctx context.Context, tx pgx.Tx, d DueDigest) error) {
	due := DueDigest{OrgID: r.OrgID, Timezone: r.Timezone}
	if t := db.TimePtr(r.NextRunAt); t != nil {
		due.Occurrence = *t
	}
	sp, err := tx.Begin(ctx)
	if err != nil {
		st.log.ErrorContext(ctx, "begin digest savepoint", "org", r.OrgID, "err", err)
		return
	}
	if err := handle(ctx, sp, due); err != nil {
		_ = sp.Rollback(context.WithoutCancel(ctx))
		st.log.ErrorContext(ctx, "weekly digest failed; retrying next tick", "org", r.OrgID, "err", err)
		return
	}
	next, err := NextDigestRun(int(r.Weekday), r.AtTime, r.Timezone, now)
	if err != nil {
		// Stored settings that no longer evaluate stop the digest rather
		// than retry forever (the CHECK needs next_run_at while enabled, so
		// disable it).
		st.log.ErrorContext(ctx, "weekly digest settings do not evaluate; disabling the digest", "org", r.OrgID, "err", err)
		_, err = sp.Exec(ctx, `UPDATE notification_digests SET enabled = false, next_run_at = NULL WHERE org_id = $1`, r.OrgID)
	} else {
		err = gen.New(sp).AdvanceNotificationDigest(ctx, gen.AdvanceNotificationDigestParams{NextRunAt: db.NullTime(&next), LastPeriod: due.Key(), OrgID: r.OrgID})
	}
	if err != nil {
		_ = sp.Rollback(context.WithoutCancel(ctx))
		st.log.ErrorContext(ctx, "advance weekly digest", "org", r.OrgID, "err", err)
		return
	}
	if err := sp.Commit(ctx); err != nil {
		st.log.ErrorContext(ctx, "commit digest savepoint", "org", r.OrgID, "err", err)
	}
}
