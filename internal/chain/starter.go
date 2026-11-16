package chain

import (
	"context"
	"log/slog"
	"runtime/debug"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/noixdev/pulsix/internal/audit"
	"github.com/noixdev/pulsix/internal/jobs"
)

// DefaultTick is the starter's interval when none is configured; batchSize
// bounds one tick's sweep and one tick's locked batch. A batch's starts are
// bounded to batchDeadlineFactor × tick, at least minBatchDeadline, and its
// write-backs and commit to rowWriteTimeout each, as in the schedule ticker
// (internal/schedules): a starved pool fails a batch fast instead of holding
// its row locks, and a write-back is never abandoned once its start happened.
const (
	DefaultTick         = 5 * time.Second
	batchSize           = 50
	batchDeadlineFactor = 4
	minBatchDeadline    = 10 * time.Second
	rowWriteTimeout     = 10 * time.Second
)

// Runs launches follow-up runs and tells whether a template is running
// (jobs.Service).
type Runs interface {
	EnqueueRunWith(ctx context.Context, orgID, templateID uuid.UUID, req jobs.RunRequest) ([]jobs.Job, error)
	HasActiveRun(ctx context.Context, orgID, templateID uuid.UUID) (bool, error)
}

// TemplateNames names a template; jobs.ErrNotFound once it is deleted.
type TemplateNames interface {
	TemplateName(ctx context.Context, orgID, id uuid.UUID) (string, error)
}

// Maintenance answers which of the hosts are in a maintenance window at an
// instant (maintenance.Service).
type Maintenance interface {
	HostsInMaintenance(ctx context.Context, orgID uuid.UUID, hostIDs []uuid.UUID, at time.Time) (map[uuid.UUID]bool, error)
}

// ScheduleGuards reads the maintenance guard of a chain's root schedule;
// found is false once the schedule is deleted.
type ScheduleGuards interface {
	MaintenanceMode(ctx context.Context, orgID, scheduleID uuid.UUID) (mode string, found bool, err error)
}

// Auditor records the starter's decisions.
type Auditor interface {
	Record(ctx context.Context, e audit.Event) error
}

// Notice categories of a skipped follow-up, as the root schedule's
// notification rules name them.
const (
	NoticeSkipped = "skipped"
	NoticeFailure = "failure"
)

// SkipNotice is a skipped follow-up of a chain whose root run was scheduled.
type SkipNotice struct {
	OrgID, ScheduleID, RowID, TemplateID, ParentTemplateID uuid.UUID
	// Category is NoticeSkipped (maintenance) or NoticeFailure (someone has
	// to act).
	Category  string
	Reason    string
	DecidedAt time.Time
}

// Notifier queues a skip notification inside the row's write transaction.
type Notifier interface {
	FollowUpSkipped(ctx context.Context, tx pgx.Tx, n SkipNotice) error
}

// Outcome is what the starter decided for one due follow-up.
type Outcome struct {
	// State is StateStarted, StateSkipped, or StateDue for a row tried
	// again at NextAttemptAt.
	State         State
	Reason        string
	ChildGroupID  uuid.UUID
	At            time.Time
	NextAttemptAt time.Time
	// CountAttempt counts a failed start towards the attempt limit.
	CountAttempt bool
}

// StarterStore locks due follow-ups and resolves stale pending ones
// (SQLStore).
type StarterStore interface {
	SweepStale(ctx context.Context, now time.Time, limit int) (int, error)
	// LockDue locks up to limit due rows across organizations; the locks
	// hold until the batch commits.
	LockDue(ctx context.Context, now time.Time, limit int) (DueBatch, error)
}

// DueBatch is one locked batch of due follow-ups.
type DueBatch interface {
	Rows() []FollowUp
	// Write records o for f in a savepoint of its own and, when notify is
	// not nil, runs it in a nested savepoint whose failure keeps the outcome.
	Write(ctx context.Context, f FollowUp, o Outcome, notify func(ctx context.Context, tx pgx.Tx) error) error
	Commit(ctx context.Context) error
	Rollback(ctx context.Context) error
}

var _ StarterStore = (*SQLStore)(nil)

// Starter starts due follow-ups. Every manager runs one; SKIP LOCKED hands
// each row to exactly one of them.
type Starter struct {
	store    StarterStore
	runs     Runs
	names    TemplateNames
	maint    Maintenance
	guards   ScheduleGuards
	audit    Auditor
	notifier Notifier
	now      func() time.Time
	tick     time.Duration
	log      *slog.Logger
}

// StarterOption tunes a Starter.
type StarterOption func(*Starter)

// WithTick sets the interval; zero or less keeps DefaultTick.
func WithTick(d time.Duration) StarterOption {
	return func(s *Starter) {
		if d > 0 {
			s.tick = d
		}
	}
}

// NewStarter wires the starter; nil now and log take defaults.
func NewStarter(store StarterStore, runs Runs, names TemplateNames, maint Maintenance, guards ScheduleGuards, audit Auditor, now func() time.Time, log *slog.Logger, opts ...StarterOption) *Starter {
	if now == nil {
		now = time.Now
	}
	if log == nil {
		log = slog.Default()
	}
	s := &Starter{store: store, runs: runs, names: names, maint: maint, guards: guards, audit: audit, now: now, tick: DefaultTick, log: log}
	for _, o := range opts {
		o(s)
	}
	return s
}

// SetNotifier wires the skip notifications; nil sends none.
func (s *Starter) SetNotifier(n Notifier) { s.notifier = n }

// Run ticks until ctx is cancelled; a failed tick is logged and retried.
func (s *Starter) Run(ctx context.Context) {
	t := time.NewTicker(s.tick)
	defer t.Stop()
	for {
		s.tickSafely(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *Starter) tickSafely(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			s.log.ErrorContext(ctx, "follow-up tick panicked", "panic", r, "stack", string(debug.Stack()))
		}
	}()
	if err := s.Tick(ctx); err != nil && ctx.Err() == nil {
		s.log.ErrorContext(ctx, "follow-up tick failed", "err", err)
	}
}

// Tick sweeps stale pending rows, then starts one batch of due rows. A row
// left alone (the deadline, a panic, a failed write) stays due for the next
// tick; a repeated start replays the first one's run group through the
// idempotency key.
func (s *Starter) Tick(ctx context.Context) error {
	now := s.now()
	if _, err := s.store.SweepStale(ctx, now, batchSize); err != nil {
		// The sweep only resolves leftovers; it never blocks starting.
		s.log.ErrorContext(ctx, "follow-up sweep failed", "err", err)
	}
	batchCtx, cancel := context.WithTimeout(ctx, max(batchDeadlineFactor*s.tick, minBatchDeadline))
	defer cancel()
	batch, err := s.store.LockDue(batchCtx, now, batchSize)
	if err != nil {
		return err
	}
	defer func() {
		rbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), rowWriteTimeout)
		defer cancel()
		batch.Rollback(rbCtx) //nolint:errcheck // no-op once committed
	}()
	rows := batch.Rows()
	for i, f := range rows {
		if batchCtx.Err() != nil {
			s.log.WarnContext(ctx, "follow-up batch deadline reached; leaving the remaining rows due", "started", i, "remaining", len(rows)-i)
			break
		}
		s.processRow(batchCtx, batch, f, now)
	}
	commitCtx, cancelCommit := context.WithTimeout(context.WithoutCancel(ctx), rowWriteTimeout)
	defer cancelCommit()
	return batch.Commit(commitCtx)
}

// processRow decides one row, writes the outcome back and audits a final
// one. A panic leaves the row due for the next tick.
func (s *Starter) processRow(ctx context.Context, batch DueBatch, f FollowUp, now time.Time) {
	defer func() {
		if r := recover(); r != nil {
			s.log.ErrorContext(context.WithoutCancel(ctx), "follow-up row panicked; leaving it due", "follow_up", f.ID, "org", f.OrgID, "panic", r, "stack", string(debug.Stack()))
		}
	}()
	d := s.decide(ctx, f, now)
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), rowWriteTimeout)
	defer cancel()
	var notify func(context.Context, pgx.Tx) error
	if d.notice != nil {
		notify = func(ctx context.Context, tx pgx.Tx) error {
			err := s.notifier.FollowUpSkipped(ctx, tx, *d.notice)
			if err != nil {
				s.log.ErrorContext(ctx, "queue follow-up notification; the decision stands, its notification is lost", "follow_up", f.ID, "org", f.OrgID, "err", err)
			}
			return err
		}
	}
	if err := batch.Write(wctx, f, d.Outcome, notify); err != nil {
		s.log.ErrorContext(wctx, "record follow-up outcome; leaving it due", "follow_up", f.ID, "org", f.OrgID, "err", err)
		return
	}
	if d.State != StateDue {
		s.auditOutcome(wctx, f, d)
	}
}
