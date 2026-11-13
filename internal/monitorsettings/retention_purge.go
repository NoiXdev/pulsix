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
// One round lists both tables' chunks once, then walks the organizations,
// each one's tables, each table's chunks older than the cutoff (oldest
// first), and deletes in batches of RetentionPurgeBatch rows, each batch
// its own short transaction in the organization's scope, until a batch
// comes back short, pausing RetentionPurgeBatchPause after every batch
// that deleted rows. Between batches it checks RetentionPurgeBudget: once
// spent the round stops, and the next round starts with the organization
// after the interrupted one, so one organization's large backlog cannot
// starve the others and drains over several rounds.
//
// Steady state: the purger remembers, per organization and table, up to
// which time it last drained (drained). A later round only visits the
// part of the chunks after that mark, so an hour later it issues one
// DELETE per table (two when the hour crosses a chunk boundary) instead
// of one per old chunk. The memory is the manager's own: it is empty after
// a start and cleared every RetentionPurgeFullSweepInterval, when a round
// visits every old chunk again — that catches rows written late with old
// timestamps and anything another manager left. Forgetting only costs
// statements, never a missed row past the next full sweep.

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
	// RetentionPurgeBatchPause is the pause after a batch that deleted
	// rows, spreading a large backlog's write load (WAL, IO) so ingest on a
	// small database keeps up. It counts against the budget.
	RetentionPurgeBatchPause = 100 * time.Millisecond
	// RetentionPurgeFullSweepInterval is how often a round forgets how far
	// each organization was drained and visits every old chunk again.
	RetentionPurgeFullSweepInterval = 24 * time.Hour
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
	pause    time.Duration
	// resumeAfter is the organization whose purge the budget interrupted
	// in the previous round (uuid.Nil: none); the next round starts with
	// the organization after it. drained is, per organization and table,
	// the time before which the purger last found nothing left to delete;
	// sweptAt is when drained was last cleared. Only Run's goroutine (one
	// round at a time) touches the three.
	resumeAfter uuid.UUID
	drained     map[drainKey]time.Time
	sweptAt     time.Time
}

// drainKey is one organization's table.
type drainKey struct {
	org   uuid.UUID
	table Table
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
	return &Purger{
		store: store, locker: locker, maxDays: maxDays, interval: interval, now: now, log: log,
		pause: RetentionPurgeBatchPause, drained: map[drainKey]time.Time{},
	}
}

// SetBatchPause replaces RetentionPurgeBatchPause; tests set 0. Call it
// before Run.
func (p *Purger) SetBatchPause(d time.Duration) { p.pause = d }

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
		if ctx.Err() != nil {
			return PurgeResult{}
		}
		p.log.ErrorContext(ctx, "measurement retention purge: list organizations", "err", err)
		return PurgeResult{Errors: 1}
	}
	if p.sweptAt.IsZero() || now.Sub(p.sweptAt) >= RetentionPurgeFullSweepInterval {
		clear(p.drained)
		p.sweptAt = now
	}
	p.forgetUnlisted(orgs)
	res := PurgeResult{Orgs: len(orgs), Complete: true}
	if len(orgs) == 0 {
		return res
	}
	// Every organization's cutoff is at or before now minus the shortest
	// choice (30 days), so listing up to now covers them all; each org
	// then skips the chunks past its own cutoff.
	ranges := map[Table][]TimeRange{}
	for _, table := range purgeTables() {
		r, err := p.store.ChunkRanges(ctx, table, now)
		if err != nil {
			if ctx.Err() != nil {
				return PurgeResult{Orgs: len(orgs)}
			}
			p.log.ErrorContext(ctx, "measurement retention purge: list chunks", "table", table, "err", err)
			return PurgeResult{Orgs: len(orgs), Errors: 1}
		}
		ranges[table] = r
	}
	for _, o := range p.rotate(orgs) {
		if ctx.Err() != nil { // shutting down: stop quietly
			res.Complete = false
			return res
		}
		// One rule for what is kept: the settings API shows the same value.
		days := EffectiveRetentionDays(o.Days, p.maxDays)
		if days >= p.maxDays {
			continue // TimescaleDB's policy already enforces the maximum
		}
		cutoff := now.Add(-time.Duration(days) * 24 * time.Hour)
		done, err := p.purgeOrg(ctx, o.OrgID, cutoff, deadline, ranges, &res)
		if err != nil && ctx.Err() == nil {
			res.Errors++
			res.Complete = false
			p.log.ErrorContext(ctx, "measurement retention purge failed for an organization", "org_id", o.OrgID, "err", err)
			continue
		}
		if !done || err != nil {
			res.Complete = false
			p.resumeAfter = o.OrgID
			return res
		}
	}
	p.resumeAfter = uuid.Nil
	return res
}

// forgetUnlisted drops the drained marks of organizations no longer
// listed, so the memory stays bounded by the current list.
func (p *Purger) forgetUnlisted(orgs []OrgRetention) {
	listed := make(map[uuid.UUID]bool, len(orgs))
	for _, o := range orgs {
		listed[o.OrgID] = true
	}
	for k := range p.drained {
		if !listed[k.org] {
			delete(p.drained, k)
		}
	}
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

// purgeOrg deletes one organization's rows older than cutoff from both
// tables, skipping what an earlier round already drained. done is false
// when the budget ran out or ctx ended first.
func (p *Purger) purgeOrg(ctx context.Context, org uuid.UUID, cutoff, deadline time.Time, ranges map[Table][]TimeRange, res *PurgeResult) (done bool, err error) {
	for _, table := range purgeTables() {
		key := drainKey{org: org, table: table}
		from := p.drained[key]
		for _, r := range ranges[table] { // oldest first
			if !r.Start.Before(cutoff) {
				break
			}
			if r.End.After(cutoff) {
				r.End = cutoff
			}
			if !r.End.After(from) {
				continue // drained by an earlier round
			}
			if r.Start.Before(from) {
				r.Start = from // still inside the one chunk
			}
			finished, err := p.drainRange(ctx, table, org, r, deadline, res)
			if err != nil || !finished {
				return false, err
			}
			p.drained[key] = r.End
		}
	}
	return true, nil
}

// drainRange deletes batches of org's rows in r until one comes back
// short. finished is false when the budget ran out or ctx ended first.
func (p *Purger) drainRange(ctx context.Context, table Table, org uuid.UUID, r TimeRange, deadline time.Time, res *PurgeResult) (finished bool, err error) {
	for {
		if ctx.Err() != nil || !p.now().Before(deadline) {
			return false, nil
		}
		n, err := p.store.DeleteBatch(ctx, table, org, r, RetentionPurgeBatch)
		if err != nil {
			return false, fmt.Errorf("delete from %s: %w", table, err)
		}
		if table == TableCheckSamples {
			res.Samples += n
		} else {
			res.StateChanges += n
		}
		if n > 0 {
			sleep(ctx, p.pause)
		}
		if n < RetentionPurgeBatch {
			return true, nil
		}
	}
}

// sleep waits d or until ctx ends.
func sleep(ctx context.Context, d time.Duration) {
	if d <= 0 {
		return
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
