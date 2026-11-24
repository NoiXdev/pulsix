package render

import (
	"cmp"
	"fmt"
	"slices"

	"github.com/google/uuid"
)

// MembershipKey names one membership: a host in a group.
type MembershipKey struct {
	GroupID uuid.UUID
	HostID  uuid.UUID
}

// Carried names the rows whose vars a render puts into its document: the
// groups whose entry it writes (their vars land under "vars") and the
// memberships it merges into a selected host's hostvars.
type Carried struct {
	Groups      map[uuid.UUID]bool
	Memberships map[MembershipKey]bool
}

// Carries reports which groups' and memberships' vars Render(in) carries,
// from the very selection Render itself makes, so a caller can prepare
// only those rows' vars (a snapshot opens only the secrets the run
// carries). The vars of in are not looked at. It fails as Render does
// when the group tree has a cycle or Scope.GroupID is unknown.
func Carries(in Input) (Carried, error) {
	r, err := newRenderer(in)
	if err != nil {
		return Carried{}, err
	}
	sel, err := r.selection()
	if err != nil {
		return Carried{}, err
	}
	out := Carried{Groups: sel.included, Memberships: map[MembershipKey]bool{}}
	for _, ms := range sel.byHost {
		for _, m := range ms {
			out.Memberships[MembershipKey{GroupID: m.GroupID, HostID: m.HostID}] = true
		}
	}
	return out, nil
}

// selection is what a scope selects: the rendered hosts, each one's
// memberships in merge order, and the groups the document writes.
type selection struct {
	hosts    map[uuid.UUID]bool
	byHost   map[uuid.UUID][]Membership
	included map[uuid.UUID]bool
}

// selection is the one place Render and Carries decide what a scope
// renders.
func (r *renderer) selection() (selection, error) {
	selected, err := r.selectHosts()
	if err != nil {
		return selection{}, err
	}
	byHost := r.membershipsByHost(selected)
	return selection{hosts: selected, byHost: byHost, included: r.includedGroups(byHost)}, nil
}

func (r *renderer) selectHosts() (map[uuid.UUID]bool, error) {
	sel := map[uuid.UUID]bool{}
	for _, h := range r.in.Hosts {
		if h.Managed {
			sel[h.ID] = true
		}
	}
	if g := r.in.Scope.GroupID; g != nil {
		if !r.tree.Has(*g) {
			return nil, fmt.Errorf("%w: %s", ErrUnknownGroup, *g)
		}
		subtree := map[uuid.UUID]bool{*g: true}
		for _, d := range r.tree.Descendants(*g) {
			subtree[d] = true
		}
		inSubtree := map[uuid.UUID]bool{}
		for _, m := range r.in.Memberships {
			if subtree[m.GroupID] {
				inSubtree[m.HostID] = true
			}
		}
		for id := range sel {
			if !inSubtree[id] {
				delete(sel, id)
			}
		}
	}
	if len(r.in.Scope.HostIDs) > 0 {
		want := map[uuid.UUID]bool{}
		for _, id := range r.in.Scope.HostIDs {
			want[id] = true
		}
		for id := range sel {
			if !want[id] {
				delete(sel, id)
			}
		}
	}
	return sel, nil
}

// membershipsByHost returns each selected host's memberships ordered by group
// depth (shallow first), then group name: the merge order of hostvars.
func (r *renderer) membershipsByHost(sel map[uuid.UUID]bool) map[uuid.UUID][]Membership {
	out := map[uuid.UUID][]Membership{}
	warned := map[uuid.UUID]bool{}

	// subtree, when Scope.GroupID is set, is that group and its
	// descendants — the same set selectHosts intersects managed hosts
	// against. hostWant, when Scope.HostIDs is set, is that set. A
	// membership is "in scope" — and so worth warning about, even though
	// its unmanaged host is never rendered — exactly when it would have
	// survived selectHosts's filtering had its host been managed.
	var subtree map[uuid.UUID]bool
	if g := r.in.Scope.GroupID; g != nil {
		subtree = map[uuid.UUID]bool{*g: true}
		for _, d := range r.tree.Descendants(*g) {
			subtree[d] = true
		}
	}
	hostWant := map[uuid.UUID]bool{}
	for _, id := range r.in.Scope.HostIDs {
		hostWant[id] = true
	}
	inScope := func(m Membership) bool {
		if subtree != nil && !subtree[m.GroupID] {
			return false
		}
		if len(hostWant) > 0 && !hostWant[m.HostID] {
			return false
		}
		return true
	}

	for _, m := range r.in.Memberships {
		h, ok := r.hosts[m.HostID]
		if _, known := r.groups[m.GroupID]; !ok || !known {
			continue
		}
		if !h.Managed {
			if inScope(m) && !warned[h.ID] {
				warned[h.ID] = true
				r.warnings = append(r.warnings, Warning{Host: h.Name, Message: "is a member of inventory groups but not managed; skipped"})
			}
			continue
		}
		if sel[m.HostID] {
			out[m.HostID] = append(out[m.HostID], m)
		}
	}
	for _, ms := range out {
		slices.SortFunc(ms, func(a, b Membership) int {
			return cmp.Or(cmp.Compare(r.tree.Depth(a.GroupID), r.tree.Depth(b.GroupID)), cmp.Compare(r.groups[a.GroupID].Name, r.groups[b.GroupID].Name))
		})
	}
	return out
}

func (r *renderer) includedGroups(byHost map[uuid.UUID][]Membership) map[uuid.UUID]bool {
	inc := map[uuid.UUID]bool{}
	if r.wholeOrg() {
		for id := range r.groups {
			inc[id] = true
		}
		return inc
	}
	for _, ms := range byHost {
		for _, m := range ms {
			inc[m.GroupID] = true
			for _, a := range r.tree.Ancestors(m.GroupID) {
				inc[a] = true
			}
		}
	}
	return inc
}
