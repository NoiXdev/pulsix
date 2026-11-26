// What a scope selects from, read once: the group tree, each host's direct
// groups and the hosts. The scope resolver and the inventory change reach
// (scope_reach.go) share it, so both select by the same rules.
package jobs

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/inventory/grouptree"
	"github.com/noixdev/pulsix/internal/inventory/render"
)

// scopeIndex is one read of the organization's groups, memberships and
// hosts.
type scopeIndex struct {
	tree   grouptree.Tree
	groups map[uuid.UUID]bool
	// groupsOf is the groups each host is a direct member of.
	groupsOf map[uuid.UUID][]uuid.UUID
	hosts    []hosts.Host
}

// loadScopeIndex reads the three sources.
func loadScopeIndex(ctx context.Context, orgID uuid.UUID, groups GroupSource, memberships MembershipSource, hostSrc HostSource) (scopeIndex, error) {
	groupList, err := groups.ListGroups(ctx, orgID)
	if err != nil {
		return scopeIndex{}, fmt.Errorf("list inventory groups: %w", err)
	}
	nodes := make([]grouptree.Node, len(groupList))
	ix := scopeIndex{groups: make(map[uuid.UUID]bool, len(groupList)), groupsOf: map[uuid.UUID][]uuid.UUID{}}
	for i, g := range groupList {
		nodes[i] = grouptree.Node{ID: g.ID, ParentID: g.ParentID, Name: g.Name}
		ix.groups[g.ID] = true
	}
	ix.tree = grouptree.New(nodes)
	memberList, err := memberships.ListMemberships(ctx, orgID)
	if err != nil {
		return scopeIndex{}, fmt.Errorf("list inventory memberships: %w", err)
	}
	for _, m := range memberList {
		ix.groupsOf[m.HostID] = append(ix.groupsOf[m.HostID], m.GroupID)
	}
	if ix.hosts, err = hostSrc.ListAllHosts(ctx, orgID); err != nil {
		return scopeIndex{}, fmt.Errorf("list hosts: %w", err)
	}
	return ix, nil
}

// selector returns whether scope selects a host: a managed host, in the
// scope group's subtree (a member of the group or of a descendant group)
// when it names one, and among its host ids when it lists any. known is
// false when the scope names a group that does not exist.
func (ix scopeIndex) selector(scope render.Scope) (selects func(hosts.Host) bool, known bool) {
	var subtree map[uuid.UUID]bool
	if scope.GroupID != nil {
		if !ix.groups[*scope.GroupID] {
			return nil, false
		}
		subtree = map[uuid.UUID]bool{*scope.GroupID: true}
		for _, d := range ix.tree.Descendants(*scope.GroupID) {
			subtree[d] = true
		}
	}
	want := make(map[uuid.UUID]bool, len(scope.HostIDs))
	for _, id := range scope.HostIDs {
		want[id] = true
	}
	return func(h hosts.Host) bool {
		if !h.Managed || (len(want) > 0 && !want[h.ID]) {
			return false
		}
		if subtree == nil {
			return true
		}
		for _, g := range ix.groupsOf[h.ID] {
			if subtree[g] {
				return true
			}
		}
		return false
	}, true
}
