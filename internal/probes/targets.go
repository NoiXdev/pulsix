package probes

import (
	"context"
	"fmt"
	"reflect"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/db"
)

// orgScope binds ctx to orgID's row-level-security scope, like the other
// background loops do for their per-organization work.
func orgScope(ctx context.Context, orgID uuid.UUID) context.Context {
	return db.WithOrgScope(ctx, orgID)
}

// refresh re-reads every organization's synthetic checks and host
// addresses. A slot that is gone (definition removed or disabled, host no
// longer monitored or deleted) leaves the schedule and drops its poller; a
// new one, and one whose plugin, params, interval or address changed, is
// due at once on a new poller, so an edit takes effect within one refresh
// plus one tick. When an organization cannot be read, its previous targets stand
// until a later refresh succeeds: a database hiccup does not pause its
// probes. When the organization list cannot be read, or ctx ends (the
// tick's deadline) midway, nothing changes and the error is returned; the
// next tick retries.
func (r *Runner) refresh(ctx context.Context, now time.Time) error {
	orgs, err := r.orgs.ListOrgIDs(ctx)
	if err != nil {
		return fmt.Errorf("list organizations: %w", err)
	}
	next := make(map[slot]target, len(r.targets))
	for _, org := range orgs {
		ts, err := r.orgTargets(orgScope(ctx, org), org)
		if ctx.Err() != nil {
			return fmt.Errorf("resolve organization %s: %w", org, ctx.Err())
		}
		if err != nil {
			r.log.ErrorContext(ctx, "probe runner: resolve organization", "org", org, "err", err)
			for s, t := range r.targets {
				if t.org == org {
					next[s] = t
				}
			}
			continue
		}
		for s, t := range ts {
			next[s] = t
		}
	}
	for s := range r.next {
		if t, ok := next[s]; !ok || changed(r.targets[s], t) {
			delete(r.next, s)
		}
	}
	for s := range r.pollers {
		if t, ok := next[s]; !ok || changed(r.targets[s], t) {
			delete(r.pollers, s)
		}
	}
	r.targets = next
	r.refreshed = now
	return nil
}

// changed reports whether a slot's target differs in anything a probe
// depends on: plugin, params, interval or the host's address.
func changed(old, cur target) bool {
	return old.address != cur.address ||
		old.check.Plugin != cur.check.Plugin ||
		old.check.Interval != cur.check.Interval ||
		!reflect.DeepEqual(old.check.Params, cur.check.Params)
}

// orgTargets resolves one organization: every monitored host's synthetic
// checks, with the host's current address.
func (r *Runner) orgTargets(ctx context.Context, org uuid.UUID) (map[slot]target, error) {
	sets, err := r.resolver.ResolveOrg(ctx, org)
	if err != nil {
		return nil, fmt.Errorf("resolve checks: %w", err)
	}
	ids := make([]uuid.UUID, 0, len(sets))
	for id, rs := range sets {
		if len(rs.Synthetic) > 0 {
			ids = append(ids, id)
		}
	}
	out := map[slot]target{}
	if len(ids) == 0 {
		return out, nil
	}
	hs, err := r.hosts.ListHostsByIDs(ctx, org, ids)
	if err != nil {
		return nil, fmt.Errorf("list hosts: %w", err)
	}
	for _, h := range hs {
		if !h.Monitored {
			continue
		}
		for _, c := range sets[h.ID].Synthetic {
			out[slot{host: h.ID, key: c.Key}] = target{org: org, address: h.Address, check: c, secret: r.secretName(c)}
		}
	}
	return out, nil
}
