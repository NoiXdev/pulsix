package probes

import (
	"cmp"
	"context"
	"slices"
	"time"

	"github.com/google/uuid"
)

// launchFreed is a launch round after a probe finished: the due probes
// that now fit start at once instead of on the next tick. It never
// launches while the lock is not held (the last tick's verdict; the next
// tick confirms it again) and never refreshes; that stays the tick's job.
// It starts only strictly due probes (no slack): it runs whenever any
// probe finishes, so meeting due times early here would start checks
// early again and again, and a fast probe could restart itself in a loop.
func (r *Runner) launchFreed(ctx context.Context) {
	if ctx.Err() != nil || !r.held {
		return
	}
	r.safely(ctx, "launch", func() { r.launchDue(ctx, r.clock(), 0) })
}

// launchDue starts every due slot that is not in flight, oldest due first,
// until MaxConcurrent probes run, keeping each busy organization's fair
// share of the slots (fairShare). A slot left out stays due. A slot is due
// once now is at most slack before its due time.
func (r *Runner) launchDue(ctx context.Context, now time.Time, slack time.Duration) {
	due, share := r.dueSlots(now, slack)
	slices.SortFunc(due, func(a, b slot) int {
		if c := r.next[a].Compare(r.next[b]); c != 0 {
			return c
		}
		if c := cmp.Compare(a.host.String(), b.host.String()); c != 0 {
			return c
		}
		return cmp.Compare(a.key, b.key)
	})
	for _, s := range due {
		if share.full() {
			return
		}
		t := r.targets[s]
		if !share.take(t.org) {
			continue
		}
		r.next[s] = now.Add(t.check.Interval)
		r.markInFlight(s, t.org)
		r.wg.Add(1)
		go r.probeSlot(ctx, s, t, r.pollerFor(s, t))
	}
}

// dueSlots lists the due slots that are not in flight and sizes the fair
// share from them and from what is in flight.
func (r *Runner) dueSlots(now time.Time, slack time.Duration) ([]slot, *fairShare) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var due []slot
	dueByOrg := map[uuid.UUID]int{}
	for s, t := range r.targets {
		if _, busy := r.inFlight[s]; busy {
			continue
		}
		if n, ok := r.next[s]; ok && now.Before(n.Add(-slack)) {
			continue
		}
		due = append(due, s)
		dueByOrg[t.org]++
	}
	return due, newFairShare(r.maxConcurrent, dueByOrg, r.inFlight)
}

func (r *Runner) markInFlight(s slot, org uuid.UUID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.inFlight[s] = org
}
