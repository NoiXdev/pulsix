// Which hosts an inventory change reaches in each template's run (spec
// 2026-11-25 assistant API gaps, decision D6): an assistant's change of a
// group's or a membership's values pauses what runs unattended on the
// hosts that receive them, and the reviewer asks this which templates
// those are.
package jobs

import (
	"context"
	"slices"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/inventory/render"
)

// ScopeReach answers, for an inventory change, which of its hosts each
// template's scope selects. It reads the same sources as the scope
// resolver, once per call, and selects by the same rules.
type ScopeReach struct {
	groups      GroupSource
	memberships MembershipSource
	hosts       HostSource
}

// NewScopeReach wires the three sources.
func NewScopeReach(groups GroupSource, memberships MembershipSource, hosts HostSource) *ScopeReach {
	return &ScopeReach{groups: groups, memberships: memberships, hosts: hosts}
}

// Overlap returns, by key of scopes, the sorted names of the managed hosts
// that both changed and that scope select; a key whose scope selects none
// of them is absent. changed is a group (its members and its descendant
// groups' members) or host ids. A scope naming a group that does not exist
// counts as selecting every changed host: what cannot be decided counts as
// reached (fail closed). A changed group that does not exist reaches
// nothing.
func (r *ScopeReach) Overlap(ctx context.Context, orgID uuid.UUID, changed render.Scope, scopes map[uuid.UUID]render.Scope) (map[uuid.UUID][]string, error) {
	ix, err := loadScopeIndex(ctx, orgID, r.groups, r.memberships, r.hosts)
	if err != nil {
		return nil, err
	}
	out := map[uuid.UUID][]string{}
	inChange, known := ix.selector(changed)
	if !known {
		return out, nil
	}
	for key, scope := range scopes {
		selects, known := ix.selector(scope)
		var names []string
		for _, h := range ix.hosts {
			if inChange(h) && (!known || selects(h)) {
				names = append(names, h.Name)
			}
		}
		if len(names) > 0 {
			slices.Sort(names)
			out[key] = names
		}
	}
	return out, nil
}
