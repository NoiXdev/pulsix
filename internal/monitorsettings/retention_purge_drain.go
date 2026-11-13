package monitorsettings

// The per-organization half of a retention purge round (retention_purge.go):
// walking one organization's chunk ranges past its drained marks and
// draining each in batches within the budget.

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

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
