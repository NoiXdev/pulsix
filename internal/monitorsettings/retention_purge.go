package monitorsettings

// The retention purge (backlog 3.15, spec "Per organization"): once per
// interval one manager deletes, for every organization whose effective
// retention is below the instance maximum, the check_samples and
// check_state_changes rows older than now minus that retention.
// TimescaleDB's retention policy (set to the maximum at start, checks.
// RetentionSync) still drops whole chunks past the maximum for everyone;
// this only removes the rows a shorter choice no longer keeps.
//
// Like the item expiry loop (internal/checks/expiry_loop.go) and the auth
// reaper it holds a session-scoped advisory lock for the round, so a
// second manager instance skips instead of racing it; the key is
// RetentionPurgeLockKey, next to the retention sync's 8316.
//
// One round walks the organizations, each one's tables, each table's
// chunks older than the cutoff (oldest first), and deletes in batches of
// RetentionPurgeBatch rows, each batch its own short transaction in the
// organization's scope, until a batch comes back short. Between batches it
// checks RetentionPurgeBudget: once spent the round stops, and the next
// round starts with the organization after the interrupted one, so one
// organization's large backlog cannot starve the others and drains over
// several rounds.

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
	"time"

	"github.com/google/uuid"
)

const (
	// RetentionPurgeInterval is the period between rounds.
	RetentionPurgeInterval = time.Hour
	// RetentionPurgeBatch is the most rows one DELETE removes.
	RetentionPurgeBatch = 10000
	// RetentionPurgeBudget is how long one round keeps starting batches;
	// the rest waits for the next round.
	RetentionPurgeBudget = 2 * time.Minute
	// RetentionPurgeRoundTimeout bounds one round's statements, so a batch
	// stuck on a lock cannot stall the loop for good. Well above the
	// budget: a batch normally takes milliseconds.
	RetentionPurgeRoundTimeout = 10 * time.Minute
	// RetentionPurgeLockKey is the pg_try_advisory_lock key of the purge.
	// Arbitrary but must never change; next to checks.RetentionSyncLockKey
	// (8316).
	RetentionPurgeLockKey = int64(8317)
)

// Table is one of the two hypertables the retention covers.
type Table string

// The tables the retention covers; nothing else is ever purged.
const (
	TableCheckSamples      Table = "check_samples"
	TableCheckStateChanges Table = "check_state_changes"
)

// purgeTables is the order a round purges an organization's tables in.
func purgeTables() []Table { return []Table{TableCheckSamples, TableCheckStateChanges} }

// OrgRetention is an organization whose effective retention, Days, is
// below the instance maximum.
type OrgRetention struct {
	OrgID uuid.UUID
	Days  int
}

// TimeRange is the half-open interval [Start, End).
type TimeRange struct {
	Start, End time.Time
}

// PurgeStore is the purge's database port.
type PurgeStore interface {
	// OrgsWithShorterRetention lists the organizations whose retention
	// choice is below maxDays, ordered by id, across organizations.
	OrgsWithShorterRetention(ctx context.Context, maxDays int) ([]OrgRetention, error)
	// ChunkRanges lists the time ranges of table's chunks that start
	// before before, oldest first; each range is exactly one chunk.
	ChunkRanges(ctx context.Context, table Table, before time.Time) ([]TimeRange, error)
	// DeleteBatch deletes at most limit of org's rows of table with
	// r.Start <= at < r.End and reports how many went.
	DeleteBatch(ctx context.Context, table Table, orgID uuid.UUID, r TimeRange, limit int) (int64, error)
}

// Locker is the advisory-lock port (schedule.AdvisoryLocker satisfies it).
// A round that does not get the lock is skipped; a nil Locker skips
// locking (tests, single instance).
type Locker interface {
	TryLock(ctx context.Context) (bool, error)
	Unlock(ctx context.Context)
}

// PurgeResult is what one round did.
type PurgeResult struct {
	// Orgs is how many organizations had a shorter retention.
	Orgs int
	// Samples and StateChanges are the rows deleted from each table.
	Samples, StateChanges int64
	// Errors counts the failures (the list, or one per organization).
	Errors int
	// Complete is true when every listed organization was purged up to
	// its cutoff without an error and within the budget.
	Complete bool
}

// Purger deletes rows past an organization's shorter retention.
type Purger struct {
	store    PurgeStore
	locker   Locker
	maxDays  int
	interval time.Duration
	now      func() time.Time
	log      *slog.Logger
	// resumeAfter is the organization whose purge the budget interrupted
	// in the previous round (uuid.Nil: none); the next round starts with
	// the organization after it. Only Run's goroutine touches it.
	resumeAfter uuid.UUID
}

// NewPurger wires the purge. maxDays is the instance maximum
// (config.Config.RetentionMaxDays); maxDays <= 0 disables it (Run returns
// at once). interval <= 0 keeps RetentionPurgeInterval; a nil clock
// degrades to time.Now and a nil logger to slog.Default.
func NewPurger(store PurgeStore, locker Locker, maxDays int, interval time.Duration, now func() time.Time, log *slog.Logger) *Purger {
	if interval <= 0 {
		interval = RetentionPurgeInterval
	}
	if now == nil {
		now = time.Now
	}
	if log == nil {
		log = slog.Default()
	}
	return &Purger{store: store, locker: locker, maxDays: maxDays, interval: interval, now: now, log: log}
}

// Run does a round every interval until ctx is done. A failing round is
// logged, never fatal; the next one retries.
func (p *Purger) Run(ctx context.Context) {
	if p.maxDays <= 0 {
		return
	}
	t := time.NewTicker(p.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.Round(ctx)
		}
	}
}

// Round runs one pass: take the lock (skip when another instance holds
// it), purge within the budget, log one INFO summary when there was work.
// A panic is recovered and logged with its stack.
func (p *Purger) Round(ctx context.Context) (res PurgeResult) {
	defer func() {
		if r := recover(); r != nil {
			p.log.ErrorContext(ctx, "measurement retention purge panicked", "panic", r, "stack", string(debug.Stack()))
		}
	}()
	if p.locker != nil {
		locked, err := p.locker.TryLock(ctx)
		if err != nil {
			p.log.ErrorContext(ctx, "measurement retention purge: acquire lock", "err", err)
			return PurgeResult{Errors: 1}
		}
		if !locked {
			return PurgeResult{} // another instance's round is running
		}
		defer p.locker.Unlock(ctx)
	}
	rctx, cancel := context.WithTimeout(ctx, RetentionPurgeRoundTimeout)
	defer cancel()

	start := p.now()
	res = p.purge(rctx, start, start.Add(RetentionPurgeBudget))
	if res.Orgs == 0 && res.Errors == 0 {
		return res
	}
	p.log.InfoContext(ctx, "measurement retention purge",
		"orgs", res.Orgs, "deleted_samples", res.Samples, "deleted_state_changes", res.StateChanges,
		"errors", res.Errors, "complete", res.Complete, "duration", p.now().Sub(start))
	return res
}

// purge is one round's work after the lock: every listed organization in
// turn, starting after resumeAfter, until done or the budget is spent.
func (p *Purger) purge(ctx context.Context, now, deadline time.Time) PurgeResult {
	orgs, err := p.store.OrgsWithShorterRetention(ctx, p.maxDays)
	if err != nil {
		p.log.ErrorContext(ctx, "measurement retention purge: list organizations", "err", err)
		return PurgeResult{Errors: 1}
	}
	res := PurgeResult{Orgs: len(orgs), Complete: true}
	for _, o := range p.rotate(orgs) {
		done, err := p.purgeOrg(ctx, o, now, deadline, &res)
		if err != nil {
			res.Errors++
			res.Complete = false
			p.log.ErrorContext(ctx, "measurement retention purge failed for an organization", "org_id", o.OrgID, "err", err)
			continue
		}
		if !done {
			res.Complete = false
			p.resumeAfter = o.OrgID
			return res
		}
	}
	p.resumeAfter = uuid.Nil
	return res
}

// rotate orders orgs (sorted by id) to start with the first one after
// resumeAfter, wrapping around.
func (p *Purger) rotate(orgs []OrgRetention) []OrgRetention {
	if p.resumeAfter == uuid.Nil {
		return orgs
	}
	for i, o := range orgs {
		if bytes.Compare(o.OrgID[:], p.resumeAfter[:]) > 0 {
			return append(append([]OrgRetention{}, orgs[i:]...), orgs[:i]...)
		}
	}
	return orgs
}

// purgeOrg deletes one organization's rows older than now minus its
// retention from both tables. done is false when the budget ran out first.
func (p *Purger) purgeOrg(ctx context.Context, o OrgRetention, now, deadline time.Time, res *PurgeResult) (done bool, err error) {
	if o.Days <= 0 {
		return true, nil // never delete by a non-positive retention
	}
	cutoff := now.Add(-time.Duration(o.Days) * 24 * time.Hour)
	for _, table := range purgeTables() {
		ranges, err := p.store.ChunkRanges(ctx, table, cutoff)
		if err != nil {
			return false, fmt.Errorf("list %s chunks: %w", table, err)
		}
		for _, r := range ranges {
			if r.End.After(cutoff) {
				r.End = cutoff
			}
			for {
				if !p.now().Before(deadline) {
					return false, nil
				}
				n, err := p.store.DeleteBatch(ctx, table, o.OrgID, r, RetentionPurgeBatch)
				if err != nil {
					return false, fmt.Errorf("delete from %s: %w", table, err)
				}
				if table == TableCheckSamples {
					res.Samples += n
				} else {
					res.StateChanges += n
				}
				if n < RetentionPurgeBatch {
					break
				}
			}
		}
	}
	return true, nil
}
