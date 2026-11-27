package probes

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/agents"
	"github.com/noixdev/pulsix/internal/checkplugins"
	"github.com/noixdev/pulsix/internal/checks"
	"github.com/noixdev/pulsix/internal/probe"
	"github.com/noixdev/pulsix/internal/probe/judge"
	"github.com/noixdev/pulsix/internal/probeconfig"
)

// Agent vantage points (backlog 3.13a, spec
// 2026-11-16-probe-vantages-design.md): a synthetic check naming vantage
// groups is probed by agents of those groups as well as (or instead of)
// the manager. The runner assigns the agents on its refresh, stores the
// manager's own probe as the vantage "manager", and at each due time
// combines every expected vantage's latest result per the check's fail
// policy into the check's own observations. A check without vantage
// groups never comes here.

// agentRef is one agent vantage of a slot: the agent's host and its name.
type agentRef struct {
	id   uuid.UUID
	name string
}

// expectedSet is a vantage slot's expected vantages, sorted, as last
// reconciled with its per-vantage rows.
type expectedSet struct {
	org  uuid.UUID
	keep []string
}

// vantagesOn reports whether agent vantages are wired (Deps).
func (r *Runner) vantagesOn() bool {
	return r.agents != nil && r.groups != nil && r.vantages != nil
}

// vantaged reports whether t's check is probed from agent vantages.
func (t target) vantaged() bool { return len(t.vantage.Groups) > 0 }

// expectedVantages is t's vantages in combining order: the manager when
// it probes, then the assigned agents by host name, then host id.
func (t target) expectedVantages() []string {
	out := make([]string, 0, len(t.agents)+1)
	if t.vantage.Manager {
		out = append(out, VantageManager)
	}
	for _, a := range t.agents {
		out = append(out, a.id.String())
	}
	return out
}

// vantageCandidates expands the vantage groups the organization's checks
// name, each group once: per group its members whose agent is online and
// probe capable, and all of those agents once. A group that no longer
// exists (deleted since the check was validated) has no members; any
// other read error fails the organization's resolution. An organization
// without vantage checks reads nothing.
func (r *Runner) vantageCandidates(ctx context.Context, org uuid.UUID, sets map[uuid.UUID]checks.ResolvedSet) (map[uuid.UUID][]uuid.UUID, []uuid.UUID, error) {
	if !r.vantagesOn() {
		return nil, nil, nil
	}
	var groups []uuid.UUID
	for _, rs := range sets {
		for _, c := range rs.Synthetic {
			if r.secretName(c) != "" {
				continue
			}
			for _, g := range checkplugins.VantageOf(c.Params).Groups {
				if !slices.Contains(groups, g) {
					groups = append(groups, g)
				}
			}
		}
	}
	if len(groups) == 0 {
		return nil, nil, nil
	}
	online, err := r.agents.OnlineProbeAgents(ctx, org)
	if err != nil {
		return nil, nil, fmt.Errorf("list probe agents: %w", err)
	}
	isOnline := make(map[uuid.UUID]bool, len(online))
	for _, a := range online {
		isOnline[a] = true
	}
	members := make(map[uuid.UUID][]uuid.UUID, len(groups))
	seen := map[uuid.UUID]bool{}
	var agents []uuid.UUID
	for _, g := range groups {
		grp, err := r.groups.GetGroup(ctx, org, g)
		if errors.Is(err, checks.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, nil, fmt.Errorf("read vantage group %s: %w", g, err)
		}
		hostIDs, err := r.groups.Preview(ctx, org, grp.Selector)
		if err != nil {
			return nil, nil, fmt.Errorf("expand vantage group %s: %w", g, err)
		}
		for _, id := range hostIDs {
			if !isOnline[id] {
				continue
			}
			members[g] = append(members[g], id)
			if !seen[id] {
				seen[id] = true
				agents = append(agents, id)
			}
		}
	}
	return members, agents, nil
}

// planVantages assigns plan's vantage slots their agents (assign) among
// members that exist as hosts (names), and fills the organization's
// assignment set and each slot's agents in combining order.
func (r *Runner) planVantages(org uuid.UUID, plan *orgPlan, members map[uuid.UUID][]uuid.UUID, names map[uuid.UUID]string) {
	var demands []demand
	for s, t := range plan.targets {
		if !t.vantaged() {
			continue
		}
		d := demand{slot: s, perGroup: min(max(t.vantage.PerGroup, 1), checkplugins.MaxAgentsPerGroup)}
		for _, g := range t.vantage.Groups {
			var eligible []uuid.UUID
			for _, a := range members[g] {
				if _, ok := names[a]; ok {
					eligible = append(eligible, a)
				}
			}
			d.groups = append(d.groups, eligible)
		}
		demands = append(demands, d)
	}
	if len(demands) == 0 {
		return
	}
	picks, short := assign(demands, MaxAssignmentsPerAgent)
	plan.short = short
	for s, agents := range picks {
		t := plan.targets[s]
		t.agents = make([]agentRef, len(agents))
		for i, a := range agents {
			t.agents[i] = agentRef{id: a, name: names[a]}
			plan.assignments = append(plan.assignments, Assignment{
				OrgID: org, AgentHostID: a, TargetHostID: s.host, CheckKey: s.key,
				Plugin: t.check.Plugin, Params: t.check.Params, Interval: t.check.Interval, Address: t.address,
			})
		}
		slices.SortFunc(t.agents, func(a, b agentRef) int {
			return cmp.Or(cmp.Compare(a.name, b.name), bytes.Compare(a.id[:], b.id[:]))
		})
		plan.targets[s] = t
	}
	slices.SortFunc(plan.assignments, func(a, b Assignment) int {
		return cmp.Or(bytes.Compare(a.TargetHostID[:], b.TargetHostID[:]), cmp.Compare(a.CheckKey, b.CheckKey),
			bytes.Compare(a.AgentHostID[:], b.AgentHostID[:]))
	})
}

// writeAssignments makes want the organization's assignment set. An
// organization whose last written set was empty and still is, is not
// written again (the first refresh after taking the lock always writes,
// clearing what an earlier holder left).
func (r *Runner) writeAssignments(ctx context.Context, org uuid.UUID, want []Assignment) error {
	if !r.vantagesOn() || (len(want) == 0 && r.assignedEmpty[org]) {
		return nil
	}
	if err := r.vantages.ReplaceOrgAssignments(ctx, org, want); err != nil {
		delete(r.assignedEmpty, org)
		return fmt.Errorf("write probe assignments: %w", err)
	}
	r.assignedEmpty[org] = len(want) == 0
	return nil
}

// pruneVantages deletes the per-vantage rows of vantages no longer
// expected: for a vantage slot whose expected set changed since the last
// refresh (or is not known yet, as after taking the lock), every vantage
// outside it; for a slot that is no longer a vantage slot (its groups
// removed, or gone altogether), all of them. A failed delete is logged and
// retried on the next refresh. Samples stay until retention.
func (r *Runner) pruneVantages(ctx context.Context) {
	if !r.vantagesOn() {
		return
	}
	want := map[slot]expectedSet{}
	for s, t := range r.targets {
		if !t.vantaged() {
			continue
		}
		keep := t.expectedVantages()
		slices.Sort(keep)
		want[s] = expectedSet{org: t.org, keep: keep}
	}
	for s, e := range want {
		if old, ok := r.expected[s]; ok && slices.Equal(old.keep, e.keep) {
			continue
		}
		if r.deleteVantages(ctx, s, e) {
			r.expected[s] = e
		}
	}
	for s, old := range r.expected {
		if _, ok := want[s]; ok {
			continue
		}
		if r.deleteVantages(ctx, s, expectedSet{org: old.org}) {
			delete(r.expected, s)
		}
	}
}

func (r *Runner) deleteVantages(ctx context.Context, s slot, e expectedSet) bool {
	if err := r.vantages.DeleteVantages(orgScope(ctx, e.org), e.org, s.host, s.key, e.keep); err != nil {
		if ctx.Err() == nil {
			r.log.ErrorContext(ctx, "probe runner: delete unexpected vantages", "org", e.org, "host", s.host, "check", s.key, "err", err)
		}
		return false
	}
	return true
}

// probeVantages is a vantage slot's due time: the manager probes when it
// is a vantage and stores its result as the vantage "manager" (not as the
// check's own), then every vantage is combined into the check's result.
func (r *Runner) probeVantages(ctx context.Context, s slot, t target, p Poller) {
	if t.vantage.Manager {
		obs := r.runProbe(ctx, s, t, p)
		if ctx.Err() != nil {
			return
		}
		if len(obs) > 0 {
			err := r.vantages.RecordVantage(orgScope(ctx, t.org), t.org, VantageManager, observations(s, obs, r.clock()))
			if err != nil && ctx.Err() == nil {
				r.log.ErrorContext(ctx, "probe runner: record manager vantage", "org", t.org, "host", s.host, "check", s.key, "err", err)
			}
		}
	}
	r.combine(ctx, s, t)
}

// combine records the check's result from its expected vantages' latest
// rounds (combineVantages): the representative's round re-stamped with
// the combining time, or, with no vantage up or down, every metric failed
// with errNoVantage (unknown, like a check that cannot be probed); the
// assertion entries an up representative round left out may be
// judge.ErrUnevaluated (unevaluated). When
// the rows cannot be read, nothing is recorded; the next due time
// retries.
func (r *Runner) combine(ctx context.Context, s slot, t target) {
	rctx, cancel := context.WithTimeout(ctx, probeTimeout(t.check.Interval))
	defer cancel()
	rows, err := r.vantages.VantageCurrent(orgScope(rctx, t.org), t.org, s.host, s.key)
	if err != nil {
		if ctx.Err() == nil {
			r.log.ErrorContext(ctx, "probe runner: read vantages", "org", t.org, "host", s.host, "check", s.key, "err", err)
		}
		return
	}
	now := r.clock()
	v := combineVantages(t.check.Plugin, t.vantage.Policy, t.expectedVantages(), rows, now, t.check.Interval)
	var obs []probe.Observation
	if v.n == 0 {
		obs = r.unattempted(t, errNoVantage)
	} else {
		obs = make([]probe.Observation, len(v.rows))
		for i, row := range v.rows {
			obs[i] = probe.Observation{Item: row.Item, Metric: row.Metric, Value: row.Value, Label: row.Label, Err: row.Err}
		}
		obs = append(obs, r.unevaluated(s, t, v.rep, v.rows[0].At, obs)...)
	}
	if ctx.Err() != nil || len(obs) == 0 {
		return
	}
	r.record(ctx, s, t, observations(s, obs, now))
}

// pushHorizon is the longest an agent can take, after the refresh that
// wrote a changed assignment, to run the new params while it stays
// assigned: the manager hands out a changed probe config only on a
// heartbeat, and an agent that misses heartbeats stays online, and so
// assigned, for agents.HeartbeatsPerOnlineWindow of its interval — with
// the longest interval an agent accepts, the rounds it buffered and
// replays after an outage of that length are still old-param rounds.
// Then its slot's first-run offset, and pushSlack for the probe itself
// and the result's delivery. A round stamped before the change plus
// pushHorizon may have been probed with the old params and is never
// blamed.
const pushHorizon = agents.HeartbeatsPerOnlineWindow*probeconfig.MaxHeartbeatInterval +
	probeconfig.FirstRunWindow + pushSlack

// pushSlack: a probe's own run (a synthetic check's timeout is at most
// 60 s) and the result's way to the manager.
const pushSlack = time.Minute

// missingRun is a slot's run of distinct representative rounds of one
// vantage, all probed after the check's assertions last changed, that
// left assertion entries out: the first's and the latest's time.
type missingRun struct {
	vantage     string
	first, last time.Time
}

// unevaluated is judge.Unevaluated's errors for an up representative
// round of vantage rep probed at at, or nothing (backlog 3.87, R12 as
// amended in review). An agent older than the plugin's assertions
// ignores them, so its rounds lack every entry; the entries read as an
// error saying so rather than going stale. But a current agent's round
// lacks an entry too when it was probed before an edit added it (its new
// params not yet applied), and a round stored in two result messages
// lacks its second half until it arrives. Rounds carry nothing that
// names the params they ran with, so an entry is flagged only when at is
// after the check's assertions last changed (t.assertSince) plus
// pushHorizon, the longest a current agent takes to run the new params,
// and the same vantage left entries out of at least two distinct such
// rounds; otherwise it is left unjudged for this combine — no row at
// all. A complete round, or one probed before the horizon, ends the run.
// The cost: an older agent's entries get the error only some 32 minutes
// after a change, a manager start or a lock takeover. The rounds' times
// are the prober's clock: an agent's clock ahead of the manager's can
// still turn one old round into the first of a run.
func (r *Runner) unevaluated(s slot, t target, rep string, at time.Time, obs []probe.Observation) []probe.Observation {
	flags := judge.Unevaluated(t.check.Plugin, t.check.Params, obs)
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(flags) == 0 || !at.After(t.assertSince.Add(pushHorizon)) {
		delete(r.missing, s)
		return nil
	}
	m, ok := r.missing[s]
	if !ok || m.vantage != rep {
		r.missing[s] = missingRun{vantage: rep, first: at, last: at}
		return nil
	}
	if at.After(m.last) {
		m.last = at
		r.missing[s] = m
	}
	if m.last.After(m.first) {
		return flags
	}
	return nil
}
