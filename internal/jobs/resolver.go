// The concrete ScopeResolver: scope semantics on plain inventory reads.
package jobs

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/inventory"
	"github.com/noixdev/pulsix/internal/inventory/grouptree"
	"github.com/noixdev/pulsix/internal/inventory/render"
)

// HostSource is the slice of hosts.Service the resolver needs.
type HostSource interface {
	ListAllHosts(ctx context.Context, orgID uuid.UUID) ([]hosts.Host, error)
}

// scopeResolver implements ScopeResolver on plain reads: the group tree
// and memberships from inventory, the hosts from hosts.Service. It mirrors
// render.selectHosts's selection semantics (managed hosts, the scope
// group's subtree, the scope's host-id intersection) without rendering
// anything — enqueue must not fail because a jump host chain is broken,
// and it must never touch secrets. The claim-time snapshot re-renders with
// the full renderer and fails closed there.
type scopeResolver struct {
	groups      GroupSource
	memberships MembershipSource
	hosts       HostSource
}

// NewScopeResolver wires the three sources.
func NewScopeResolver(groups GroupSource, memberships MembershipSource, hosts HostSource) ScopeResolver {
	return &scopeResolver{groups: groups, memberships: memberships, hosts: hosts}
}

// Resolve returns the selected managed hosts grouped by their zone, sorted
// by zone id and host id for deterministic job creation. An unknown scope
// group is ErrNotFound, exactly like any other missing row.
//
// limit is the run's effective limit pattern ("" = none): a zone none of
// whose selected host names match it gets no job, and a limit that matches
// no zone at all is ErrEmptyScope (backlog 2.45). The gate NEVER narrows a
// zone's host list — the job keeps the zone's full set and the limit string
// still ships on the claim for ansible to apply on top of it (defense in
// depth). Callers without a limit (RunTargets, the maintenance guard's
// group look-ahead) resolve the full scope.
func (r *scopeResolver) Resolve(ctx context.Context, orgID uuid.UUID, scope render.Scope, limit string) ([]ZoneTargets, error) {
	groupList, err := r.groups.ListGroups(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("list inventory groups: %w", err)
	}
	nodes := make([]grouptree.Node, len(groupList))
	byID := make(map[uuid.UUID]bool, len(groupList))
	for i, g := range groupList {
		nodes[i] = grouptree.Node{ID: g.ID, ParentID: g.ParentID, Name: g.Name}
		byID[g.ID] = true
	}
	tree := grouptree.New(nodes)

	var subtree map[uuid.UUID]bool
	if scope.GroupID != nil {
		if !byID[*scope.GroupID] {
			return nil, ErrNotFound
		}
		subtree = map[uuid.UUID]bool{*scope.GroupID: true}
		for _, d := range tree.Descendants(*scope.GroupID) {
			subtree[d] = true
		}
	}
	inScopeGroup := func(hostID uuid.UUID, memberList []inventory.Membership) bool {
		if subtree == nil {
			return true
		}
		for _, m := range memberList {
			if m.HostID == hostID && subtree[m.GroupID] {
				return true
			}
		}
		return false
	}

	memberList, err := r.memberships.ListMemberships(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("list inventory memberships: %w", err)
	}
	hostWant := map[uuid.UUID]bool{}
	for _, id := range scope.HostIDs {
		hostWant[id] = true
	}

	all, err := r.hosts.ListAllHosts(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("list hosts: %w", err)
	}
	// The limit gates zones by host NAME; an undecidable pattern keeps
	// every zone (fail-open — limit.go's contract). The verdict is per
	// host: the zone survives when any of its selected hosts matches.
	var gate func(name string) (matched, decided bool)
	if limit != "" {
		if p := parseLimit(limit); p.decided {
			gate = p.matches
		}
	}
	zoneHit := map[uuid.UUID]bool{}
	byZone := map[uuid.UUID][]uuid.UUID{}
	for _, h := range all {
		if !h.Managed {
			continue
		}
		if len(hostWant) > 0 && !hostWant[h.ID] {
			continue
		}
		if !inScopeGroup(h.ID, memberList) {
			continue
		}
		byZone[h.ZoneID] = append(byZone[h.ZoneID], h.ID)
		if gate != nil {
			if m, d := gate(h.Name); d && m {
				zoneHit[h.ZoneID] = true
			}
		}
	}
	for z := range byZone {
		if gate != nil && !zoneHit[z] {
			delete(byZone, z)
		}
	}
	if len(byZone) == 0 {
		return nil, ErrEmptyScope
	}
	out := make([]ZoneTargets, 0, len(byZone))
	for z, ids := range byZone {
		slices.SortFunc(ids, func(a, b uuid.UUID) int { return strings.Compare(a.String(), b.String()) })
		out = append(out, ZoneTargets{ZoneID: z, HostIDs: ids})
	}
	slices.SortFunc(out, func(a, b ZoneTargets) int { return strings.Compare(a.ZoneID.String(), b.ZoneID.String()) })
	return out, nil
}
