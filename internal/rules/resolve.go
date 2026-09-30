// Resolve turns the rules Task 4's Store persists into what actually
// judges one host today (v0.3b-2 spec, "Precedence and the effective
// view"): for each (check_key, item, metric, severity) tuple, the winning
// rule along item > host > group > org, and the rules it shadowed. This is
// the most consequential correctness surface in the milestone after the
// state machine itself — a resolution that depended on map or slice
// iteration order would flap a tuple's state for reasons no operator could
// diagnose — which is why every tie-break below is a strict total order,
// pinned by a table-driven test (including one that resolves the same
// input a hundred times from a shuffled slice) rather than trusted to read
// correctly. Resolve itself is pure: no IO, no clock, nothing but the
// rules and the host's own context.
package rules

import (
	"bytes"
	"sort"
	"time"

	"github.com/google/uuid"
)

// Tuple is what a state is about: one metric of one item of one check on
// one host.
type Tuple struct {
	CheckKey string
	Item     string
	Metric   string
}

// GroupRank is a monitoring group reduced to what the among-groups
// tie-break needs. internal/rules never imports internal/checks (the two
// packages own different concerns and a dependency edge between them would
// make either untestable in isolation); internal/app adapts
// []checks.Group into []GroupRank at the wiring point.
type GroupRank struct {
	ID        uuid.UUID
	Priority  int
	CreatedAt time.Time
}

// beats is a strict total order over GroupRank — the same one
// checks.Group.beats already implements (higher priority, then the older
// group, then the smaller id byte for byte) — so a maximum computed over a
// set of groups never depends on iteration order. Mirrored here rather
// than shared, since GroupRank and checks.Group are deliberately different
// types in different packages.
func (g GroupRank) beats(other GroupRank) bool {
	if g.Priority != other.Priority {
		return g.Priority > other.Priority
	}
	if !g.CreatedAt.Equal(other.CreatedAt) {
		return g.CreatedAt.Before(other.CreatedAt)
	}
	return bytes.Compare(g.ID[:], other.ID[:]) < 0
}

// HostContext is everything Resolve needs to know about one host to judge
// an organization's rules against it.
type HostContext struct {
	HostID uuid.UUID
	// Groups is the groups whose selector matches this host — the same
	// membership internal/checks's resolver already computes to expand a
	// group into hosts (checks.Service.GroupsForHost), so a host can never
	// receive a group's rules without also receiving that group's checks.
	// A group-scoped rule whose GroupID is not in this slice does not
	// apply to this host at all, the same way a group whose selector no
	// longer matches a host stops contributing its check definitions.
	Groups []GroupRank
	// Tuples is what the host actually reports today.
	Tuples []Tuple
}

// Effective is the outcome for one (tuple, severity): who decided it and
// who was overruled.
type Effective struct {
	Tuple    Tuple
	Severity Severity
	Winner   Rule
	// Shadowed holds the losers in the same order Resolve considered them,
	// nearest scope first — the same order candidate.beats already sorts
	// by, since scope precedence is its primary key.
	Shadowed []Rule
	// Observed is false when an item-scoped rule names an item the host no
	// longer reports. The rule still appears, labelled, rather than
	// vanishing from the view — the same "survives its item" contract
	// check_current gives a vanished mount.
	Observed bool
}

// scopeRank orders Scope for the precedence table item > host > group >
// org: a higher number always outranks a lower one.
func scopeRank(s Scope) int {
	switch s {
	case ScopeItem:
		return 3
	case ScopeHost:
		return 2
	case ScopeGroup:
		return 1
	default: // ScopeOrg
		return 0
	}
}

// candidate is one rule competing for one tuple's severity, carrying the
// GroupRank it needs for the among-groups tie-break (the zero GroupRank
// when the rule is not group-scoped, where it plays no part in beats).
type candidate struct {
	rule  Rule
	group GroupRank
}

// beats is Resolve's own strict total order over candidates: scope first
// (item > host > group > org, the design's own precedence table), then —
// for two candidates scoped to two distinct groups — GroupRank.beats
// exactly as it stands, and otherwise (two rules at the same non-group
// scope, or two rules bound to the very same group, neither of which the
// schema forbids) the older rule and then its own smaller id. No two
// distinct rules ever compare equal under this order — CreatedAt ties
// still resolve on Rule.ID, which is unique — so a maximum computed over
// it never depends on the order its candidates are visited in.
func (c candidate) beats(other candidate) bool {
	if r, o := scopeRank(c.rule.Scope), scopeRank(other.rule.Scope); r != o {
		return r > o
	}
	if c.rule.Scope == ScopeGroup && c.group.ID != other.group.ID {
		return c.group.beats(other.group)
	}
	if !c.rule.CreatedAt.Equal(other.rule.CreatedAt) {
		return c.rule.CreatedAt.Before(other.rule.CreatedAt)
	}
	return bytes.Compare(c.rule.ID[:], other.rule.ID[:]) < 0
}

// tupleSeverity keys the per-(tuple, severity) grouping Resolve reduces
// over — one independent decision per severity, per the design's own
// "Rule granularity" (warn and crit are two rows, resolved independently,
// never one row whose two thresholds could disagree about who decided it).
type tupleSeverity struct {
	tuple    Tuple
	severity Severity
}

// Resolve picks a winner per (tuple, severity) along item > host > group >
// org. Disabled rules are dropped before precedence, so disabling a
// host-scoped rule lets the group's rule win rather than leaving the
// metric unjudged. rules is an organization's whole rule set, not
// pre-filtered to one host by the caller: Resolve itself ignores a
// host-scoped or item-scoped rule bound to a different host, and a
// group-scoped rule bound to a group absent from host.Groups (one whose
// selector no longer matches this host).
//
// An item-scoped rule always produces an Effective for the tuple it names,
// even when that tuple is not in host.Tuples — Observed is false in that
// case, but the rule is never silently dropped. A rule at any other scope
// contributes only to tuples host.Tuples actually reports; one whose
// metric no tuple reports yields no Effective at all, since it names no
// single tuple that could survive on its own.
func Resolve(rules []Rule, host HostContext) []Effective {
	groupByID := make(map[uuid.UUID]GroupRank, len(host.Groups))
	for _, g := range host.Groups {
		groupByID[g.ID] = g
	}
	observed := make(map[Tuple]bool, len(host.Tuples))
	for _, t := range host.Tuples {
		observed[t] = true
	}

	candidates := map[tupleSeverity][]candidate{}
	addCandidate := func(t Tuple, r Rule, g GroupRank) {
		k := tupleSeverity{tuple: t, severity: r.Severity}
		candidates[k] = append(candidates[k], candidate{rule: r, group: g})
	}
	addToEveryMatchingTuple := func(r Rule, g GroupRank) {
		for _, t := range host.Tuples {
			if t.CheckKey == r.CheckKey && t.Metric == r.Metric {
				addCandidate(t, r, g)
			}
		}
	}

	for _, r := range rules {
		if !r.Enabled {
			continue
		}
		switch r.Scope {
		case ScopeOrg:
			addToEveryMatchingTuple(r, GroupRank{})
		case ScopeGroup:
			if r.GroupID == nil {
				continue // unreachable given Service.buildRule's own validation, guarded rather than trusted
			}
			g, matched := groupByID[*r.GroupID]
			if !matched {
				continue // the group's selector no longer matches this host
			}
			addToEveryMatchingTuple(r, g)
		case ScopeHost:
			if r.HostID == nil || *r.HostID != host.HostID {
				continue
			}
			addToEveryMatchingTuple(r, GroupRank{})
		case ScopeItem:
			if r.HostID == nil || *r.HostID != host.HostID {
				continue
			}
			addCandidate(Tuple{CheckKey: r.CheckKey, Item: r.Item, Metric: r.Metric}, r, GroupRank{})
		}
	}

	out := make([]Effective, 0, len(candidates))
	for k, cands := range candidates {
		sort.Slice(cands, func(i, j int) bool { return cands[i].beats(cands[j]) })
		var shadowed []Rule
		if len(cands) > 1 {
			shadowed = make([]Rule, 0, len(cands)-1)
			for _, c := range cands[1:] {
				shadowed = append(shadowed, c.rule)
			}
		}
		out = append(out, Effective{
			Tuple:    k.tuple,
			Severity: k.severity,
			Winner:   cands[0].rule,
			Shadowed: shadowed,
			Observed: observed[k.tuple],
		})
	}

	// The map above is built and ranged over in no promised order; sort the
	// result into one fixed, deterministic order (by tuple, then severity)
	// so that two calls with the same input — however differently ordered
	// — always compare equal, not merely contain the same elements.
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i].Tuple, out[j].Tuple
		if a.CheckKey != b.CheckKey {
			return a.CheckKey < b.CheckKey
		}
		if a.Item != b.Item {
			return a.Item < b.Item
		}
		if a.Metric != b.Metric {
			return a.Metric < b.Metric
		}
		return out[i].Severity < out[j].Severity
	})
	return out
}
