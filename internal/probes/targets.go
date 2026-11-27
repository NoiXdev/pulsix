package probes

import (
	"context"
	"fmt"
	"reflect"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/checkplugins"
	"github.com/noixdev/pulsix/internal/checks"
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
//
// With agent vantages on, each resolved organization's probe assignments
// are written too (writeAssignments); one whose write fails keeps its
// previous targets like one that cannot be read, so the in-memory
// expected vantages stay those the agents were told. At the end the
// per-vantage rows of vantages no longer expected are deleted
// (pruneVantages). A refresh that hits the tick's deadline after writing
// some organizations' assignments returns with the in-memory targets
// unchanged, so for those organizations the database (and the agents it
// answers) is ahead of what the runner expects: an agent that has just
// been assigned is not yet an expected vantage, and one just unassigned
// still is while its results are no longer accepted. This heals itself:
// the next successful refresh writes the assignments again and adopts
// them as its targets.
func (r *Runner) refresh(ctx context.Context, now time.Time) error {
	orgs, err := r.orgs.ListOrgIDs(ctx)
	if err != nil {
		return fmt.Errorf("list organizations: %w", err)
	}
	next := make(map[slot]target, len(r.targets))
	short := 0
	for _, org := range orgs {
		octx := orgScope(ctx, org)
		plan, err := r.orgTargets(octx, org)
		if err == nil && ctx.Err() == nil {
			err = r.writeAssignments(octx, org, plan.assignments)
		}
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
		for s, t := range plan.targets {
			next[s] = t
		}
		short += plan.short
	}
	if short > 0 {
		r.log.WarnContext(ctx, "probe runner: slots got fewer agents than asked; agents are at their assignment limit",
			"slots", short, "limit", MaxAssignmentsPerAgent)
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
	r.adoptAssertions(next, now)
	r.targets = next
	r.refreshed = now
	r.pruneVantages(ctx)
	return nil
}

// changed reports whether a slot's target differs in anything a probe
// depends on: plugin, params, interval, vantage settings or the host's
// address. The assigned agents are not among them: a moved agent does
// not reset the slot's schedule.
func changed(old, cur target) bool {
	return old.address != cur.address ||
		old.check.Plugin != cur.check.Plugin ||
		old.check.Interval != cur.check.Interval ||
		!reflect.DeepEqual(old.check.Params, cur.check.Params) ||
		!reflect.DeepEqual(old.vantage, cur.vantage)
}

// orgPlan is one organization's resolution: its slots, and with agent
// vantages on its complete assignment set and how many of its slots the
// assignment limit cut short.
type orgPlan struct {
	targets     map[slot]target
	assignments []Assignment
	short       int
}

// orgTargets resolves one organization: every monitored host's synthetic
// checks, with the host's current address, and the agents of the checks
// with vantage groups (planVantages). The agent hosts are listed with the
// targets, in one read, for their names and to make sure they exist.
func (r *Runner) orgTargets(ctx context.Context, org uuid.UUID) (orgPlan, error) {
	sets, err := r.resolver.ResolveOrg(ctx, org)
	if err != nil {
		return orgPlan{}, fmt.Errorf("resolve checks: %w", err)
	}
	ids := make([]uuid.UUID, 0, len(sets))
	for id, rs := range sets {
		if len(rs.Synthetic) > 0 {
			ids = append(ids, id)
		}
	}
	plan := orgPlan{targets: map[slot]target{}}
	if len(ids) == 0 {
		return plan, nil
	}
	members, agents, err := r.vantageCandidates(ctx, org, sets)
	if err != nil {
		return orgPlan{}, err
	}
	hs, err := r.hosts.ListHostsByIDs(ctx, org, append(ids, agents...))
	if err != nil {
		return orgPlan{}, fmt.Errorf("list hosts: %w", err)
	}
	names := make(map[uuid.UUID]string, len(hs))
	for _, h := range hs {
		names[h.ID] = h.Name
		if !h.Monitored {
			continue
		}
		for _, c := range sets[h.ID].Synthetic {
			plan.targets[slot{host: h.ID, key: c.Key}] = r.newTarget(org, h.Address, c)
		}
	}
	r.planVantages(org, &plan, members, names)
	return plan, nil
}

// newTarget is the target of check c on a host at address: its params
// without the vantage params, its vantage settings read once. A check
// probed only from the manager never gets agent vantages: one naming a
// secret (its credentials stay on the manager) or an http check with a
// custom request (an agent of an earlier release would send a plain GET,
// backlog 3.84); the descriptors refuse the combination, this guards rows
// written past them. Neither does any check while agent vantages are off.
func (r *Runner) newTarget(org uuid.UUID, address string, c checks.Resolved) target {
	t := target{org: org, address: address, check: c, secret: r.secretName(c), vantage: checkplugins.VantageOf(c.Params)}
	t.check.Params = checkplugins.StripVantage(c.Params)
	if r.managerOnly(c) || !r.vantagesOn() {
		t.vantage.Groups = nil
	}
	return t
}

// adoptAssertions sets each slot's assertSince: kept while the check's
// assertions are what the slot had, now for a new slot, changed
// assertions and every slot on the first refresh after regaining the
// lock (what happened while another manager held it is unknown). A slot
// gone forgets its run of unevaluated rounds.
func (r *Runner) adoptAssertions(next map[slot]target, now time.Time) {
	fresh := r.regained
	r.regained = false
	for s, t := range next {
		if old, ok := r.targets[s]; ok && !fresh && !old.assertSince.IsZero() &&
			reflect.DeepEqual(old.check.Params["assertions"], t.check.Params["assertions"]) {
			t.assertSince = old.assertSince
		} else {
			t.assertSince = now
		}
		next[s] = t
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for s := range r.missing {
		if _, ok := next[s]; !ok {
			delete(r.missing, s)
		}
	}
}
