// Resolution turns the bindings definitions.go and groups.go persist into
// what actually runs on one host (v0.3b-1 spec, "Resolution"): start from
// the organization's definitions, overlay every matching monitoring
// group's, then overlay the host's own — narrower scope wins per key,
// among groups the higher priority wins, and a version over the result is
// what tells an agent its configuration changed ("Configuration
// delivery"). This is the one place in the milestone where a silent
// mistake is invisible until a check runs at the wrong interval on the
// wrong host, which is why every precedence and tie-break rule below is
// pinned by a table-driven test in resolver_test.go rather than trusted
// to read correctly.
package checks

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/google/uuid"
)

// Resolved is one check as it applies to a specific host, after
// precedence: the winning definition's shape plus where it came from.
type Resolved struct {
	Key, Plugin string
	Params      map[string]any
	Interval    time.Duration
	// Source is the scope the winning definition was bound at.
	Source BindingKind
	// SourceID is the group or host the winning definition belongs to;
	// nil when Source is BindingOrg (an organization-bound definition
	// belongs to no single group or host).
	SourceID *uuid.UUID
}

// ResolvedSet is one host's complete, resolved configuration: what an
// agent should be running, and the version it reports back to prove it is.
type ResolvedSet struct {
	HostID uuid.UUID
	// Checks is sorted by Key — both because that is a stable, readable
	// order and because Version is computed over exactly this order.
	Checks []Resolved
	// Version is the hex SHA-256 of the canonical encoding (see
	// canonicalEncoding) — the configuration version an agent reports in
	// its heartbeat and the manager compares against to decide whether to
	// push.
	Version string
}

// resolveBundle is every input the resolver needs for one organization,
// loaded once and shared across every host ResolveOrg computes a set for
// (and used, as a bundle of one host's worth of relevance, by ResolveHost
// too) — so a single ResolveOrg call expands each group's selector
// exactly once rather than once per host.
type resolveBundle struct {
	// orgDefs is keyed by Key; the (org_id, binding_kind, group_id,
	// host_id, key) NULLS NOT DISTINCT constraint guarantees at most one
	// row per key at the organization scope, so this map can never
	// silently drop a collision the database would have refused to store.
	orgDefs map[string]Definition
	// groupDefs[groupID][key] — same uniqueness guarantee, scoped to one
	// group.
	groupDefs map[uuid.UUID]map[string]Definition
	// hostDefs[hostID][key] — same uniqueness guarantee, scoped to one
	// host.
	hostDefs map[uuid.UUID]map[string]Definition
	// groups is every monitoring group in the organization, carrying the
	// Priority/CreatedAt/ID the among-groups tie-break needs.
	groups []Group
	// groupHosts[groupID] is the set of host ids that group's selector
	// currently matches, computed once per group per bundle so that
	// resolving many hosts in one ResolveOrg call sees one consistent
	// answer to "does this group match this host" for all of them.
	groupHosts map[uuid.UUID]map[uuid.UUID]struct{}
}

// loadResolveBundle fetches every definition and group for orgID and
// expands each group's selector into the set of host ids it currently
// matches. Expansion is skipped entirely (and s.matcher is never
// dereferenced) when the organization has no groups — a Service built
// without WithMatcher can still resolve an organization that has none.
func (s *Service) loadResolveBundle(ctx context.Context, orgID uuid.UUID) (*resolveBundle, error) {
	defs, err := s.store.ListDefinitions(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("resolve: list definitions: %w", err)
	}
	groups, err := s.store.List(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("resolve: list groups: %w", err)
	}

	b := &resolveBundle{
		orgDefs:   map[string]Definition{},
		groupDefs: map[uuid.UUID]map[string]Definition{},
		hostDefs:  map[uuid.UUID]map[string]Definition{},
		groups:    groups,
	}
	for _, d := range defs {
		switch d.BindingKind {
		case BindingOrg:
			b.orgDefs[d.Key] = d
		case BindingGroup:
			if d.GroupID == nil {
				continue // unreachable given normalizeDefinition, guarded rather than trusted
			}
			m := b.groupDefs[*d.GroupID]
			if m == nil {
				m = map[string]Definition{}
				b.groupDefs[*d.GroupID] = m
			}
			m[d.Key] = d
		case BindingHost:
			if d.HostID == nil {
				continue
			}
			m := b.hostDefs[*d.HostID]
			if m == nil {
				m = map[string]Definition{}
				b.hostDefs[*d.HostID] = m
			}
			m[d.Key] = d
		}
	}

	b.groupHosts = map[uuid.UUID]map[uuid.UUID]struct{}{}
	if len(groups) > 0 {
		if s.matcher == nil {
			return nil, fmt.Errorf("checks: resolve: service was built without WithMatcher")
		}
		for _, g := range groups {
			ids, err := s.matcher.MatchingHosts(ctx, orgID, g.Selector)
			if err != nil {
				return nil, fmt.Errorf("resolve: expand group %s: %w", g.ID, err)
			}
			set := make(map[uuid.UUID]struct{}, len(ids))
			for _, id := range ids {
				set[id] = struct{}{}
			}
			b.groupHosts[g.ID] = set
		}
	}
	return b, nil
}

// groupWinner is a candidate contributing a key at group scope: the group
// it belongs to (for the priority/created_at/id tie-break) alongside the
// definition itself.
type groupWinner struct {
	group Group
	def   Definition
}

// beats reports whether candidate g/d should replace the current winner
// cur for the same key: higher Group.Priority wins; a tie goes to the
// older group (earlier CreatedAt); a further tie (equal priority, equal
// timestamp) is broken by the smaller id, byte for byte. This is a strict
// total order over Group — no two distinct groups ever compare equal,
// since IDs are unique — which is what makes the reduction below safe to
// run over a map in any iteration order: a maximum under a strict total
// order does not depend on the order visited.
func (g Group) beats(other Group) bool {
	if g.Priority != other.Priority {
		return g.Priority > other.Priority
	}
	if !g.CreatedAt.Equal(other.CreatedAt) {
		return g.CreatedAt.Before(other.CreatedAt)
	}
	return bytes.Compare(g.ID[:], other.ID[:]) < 0
}

// resolve computes hostID's Resolved list (unsorted) from bundle b:
// organization first, then every group whose expansion contains hostID
// (the among-groups tie-break applied independently per key), then the
// host's own — each later layer overriding on Key. A definition whose
// final, winning layer has Enabled = false drops the key entirely, rather
// than appearing disabled: that richer "still visible, disabled, with its
// source" view is the effective-checks view a later task builds on top of
// this, not this resolver's own output (see the resolver_test.go doc
// comment for where that split is tested).
func (b *resolveBundle) resolve(hostID uuid.UUID) []Resolved {
	winners := make(map[string]Definition, len(b.orgDefs))
	for k, d := range b.orgDefs {
		winners[k] = d
	}

	groupKeyWinner := map[string]groupWinner{}
	for _, g := range b.groups {
		if _, matched := b.groupHosts[g.ID][hostID]; !matched {
			continue
		}
		for k, d := range b.groupDefs[g.ID] {
			cur, exists := groupKeyWinner[k]
			if !exists || g.beats(cur.group) {
				groupKeyWinner[k] = groupWinner{group: g, def: d}
			}
		}
	}
	for k, w := range groupKeyWinner {
		winners[k] = w.def
	}

	for k, d := range b.hostDefs[hostID] {
		winners[k] = d
	}

	out := make([]Resolved, 0, len(winners))
	for k, d := range winners {
		if !d.Enabled {
			continue
		}
		r := Resolved{Key: k, Plugin: d.Plugin, Params: d.Params, Interval: d.Interval, Source: d.BindingKind}
		switch d.BindingKind {
		case BindingGroup:
			r.SourceID = d.GroupID
		case BindingHost:
			r.SourceID = d.HostID
		case BindingOrg:
			// SourceID stays nil.
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// canonicalParams renders params the way Version's encoding requires:
// "{}" for nil or empty (never the bare string "null" json.Marshal(nil
// map) would otherwise produce — two definitions that both take no
// parameters must hash identically regardless of whether the map that
// happened to reach here was nil or merely empty), sorted-key JSON
// otherwise — encoding/json sorts map keys at every nesting level, so
// marshalling map[string]any is already canonical.
func canonicalParams(params map[string]any) ([]byte, error) {
	if len(params) == 0 {
		return []byte("{}"), nil
	}
	return json.Marshal(params)
}

// canonicalEncoding renders checks (already sorted by Key) into the exact
// byte sequence Version is hashed over: each check as
// "key\x00plugin\x00interval_seconds\x00canonical-json(params)\x00",
// concatenated in order. Pinned byte-for-byte by
// TestVersionCanonicalEncoding, not merely by "the hash changes" — the
// version is a hash over this encoding, so the encoding's stability is the
// whole contract.
func canonicalEncoding(checks []Resolved) ([]byte, error) {
	var buf bytes.Buffer
	for _, c := range checks {
		paramsJSON, err := canonicalParams(c.Params)
		if err != nil {
			return nil, fmt.Errorf("encode params for key %q: %w", c.Key, err)
		}
		buf.WriteString(c.Key)
		buf.WriteByte(0)
		buf.WriteString(c.Plugin)
		buf.WriteByte(0)
		buf.WriteString(strconv.FormatInt(int64(c.Interval/time.Second), 10))
		buf.WriteByte(0)
		buf.Write(paramsJSON)
		buf.WriteByte(0)
	}
	return buf.Bytes(), nil
}

// version hashes checks's canonical encoding into the hex SHA-256 that is
// ResolvedSet.Version.
func version(checks []Resolved) (string, error) {
	enc, err := canonicalEncoding(checks)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(enc)
	return hex.EncodeToString(sum[:]), nil
}

// toResolvedSet sorts checks by Key (resolve already does this, but a
// ResolvedSet is never handed out with unsorted Checks even if a future
// caller of resolve forgets to) and computes Version.
func toResolvedSet(hostID uuid.UUID, checks []Resolved) (ResolvedSet, error) {
	sort.Slice(checks, func(i, j int) bool { return checks[i].Key < checks[j].Key })
	v, err := version(checks)
	if err != nil {
		return ResolvedSet{}, fmt.Errorf("resolve: compute version: %w", err)
	}
	return ResolvedSet{HostID: hostID, Checks: checks, Version: v}, nil
}

// ResolveHost computes hostID's resolved set: organization, then every
// matching group, then the host's own, per "Resolution". A host that is
// not monitored (or does not exist in orgID at all — the two read the
// same, the same "invisible rather than forbidden" contract ErrNotFound
// gives every other lookup in this codebase) resolves to an empty set,
// never an error.
func (s *Service) ResolveHost(ctx context.Context, orgID, hostID uuid.UUID) (ResolvedSet, error) {
	if s.hostRoster == nil {
		return ResolvedSet{}, fmt.Errorf("checks: resolve: service was built without WithHostRoster")
	}
	monitoredIDs, err := s.hostRoster.MonitoredHosts(ctx, orgID)
	if err != nil {
		return ResolvedSet{}, fmt.Errorf("resolve: monitored hosts: %w", err)
	}
	if !containsID(monitoredIDs, hostID) {
		return toResolvedSet(hostID, nil)
	}

	bundle, err := s.loadResolveBundle(ctx, orgID)
	if err != nil {
		return ResolvedSet{}, err
	}
	return toResolvedSet(hostID, bundle.resolve(hostID))
}

// ResolveOrg computes every monitored host's resolved set for orgID in one
// pass, sharing one loadResolveBundle (so each group's selector is
// expanded exactly once) across every host. The returned map has an entry
// for exactly the hosts HostRoster.MonitoredHosts reports monitored;
// nothing else is ever considered, per ResolveHost's own contract, and
// ResolveOrg's Version for each host is computed by the exact same
// resolve/toResolvedSet path ResolveHost uses, so the two can never
// disagree about the same host.
func (s *Service) ResolveOrg(ctx context.Context, orgID uuid.UUID) (map[uuid.UUID]ResolvedSet, error) {
	if s.hostRoster == nil {
		return nil, fmt.Errorf("checks: resolve: service was built without WithHostRoster")
	}
	monitoredIDs, err := s.hostRoster.MonitoredHosts(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("resolve: monitored hosts: %w", err)
	}

	bundle, err := s.loadResolveBundle(ctx, orgID)
	if err != nil {
		return nil, err
	}

	out := make(map[uuid.UUID]ResolvedSet, len(monitoredIDs))
	for _, id := range monitoredIDs {
		rs, err := toResolvedSet(id, bundle.resolve(id))
		if err != nil {
			return nil, err
		}
		out[id] = rs
	}
	return out, nil
}

func containsID(ids []uuid.UUID, id uuid.UUID) bool {
	for _, existing := range ids {
		if existing == id {
			return true
		}
	}
	return false
}
