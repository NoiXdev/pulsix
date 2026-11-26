// Which templates' runs an inventory change reaches (spec 2026-11-25
// assistant API gaps, decisions D6 and D7): an assistant's change of a
// group's or a membership's values, of who is in a group, or of where a
// group sits pauses what runs unattended on the hosts it reaches, and the
// reviewer asks this which templates those are.
package jobs

import (
	"context"
	"slices"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/inventory/render"
)

// ScopeHostSource lists the hosts a scope selects from without their
// facts (hosts.Service).
type ScopeHostSource interface {
	ListScopeHosts(ctx context.Context, orgID uuid.UUID) ([]hosts.Host, error)
}

// ScopeReach answers, for an inventory change, which of its hosts each
// template's scope selects. It reads groups, memberships and hosts once
// per call and selects by the scope resolver's rules.
type ScopeReach struct {
	groups      GroupSource
	memberships MembershipSource
	hosts       ScopeHostSource
}

// NewScopeReach wires the three sources.
func NewScopeReach(groups GroupSource, memberships MembershipSource, hosts ScopeHostSource) *ScopeReach {
	return &ScopeReach{groups: groups, memberships: memberships, hosts: hosts}
}

// Overlap returns, by key of scopes, the sorted names of the managed hosts
// one of changed selects (a group: its members and its descendant groups'
// members; host ids) that the key's scope selects too; a key whose scope
// selects none of them is absent. A scope naming a group that does not
// exist counts as selecting every changed host: what cannot be decided
// counts as reached (fail closed). A changed group that does not exist
// reaches nothing.
func (r *ScopeReach) Overlap(ctx context.Context, orgID uuid.UUID, changed []render.Scope, scopes map[uuid.UUID]render.Scope) (map[uuid.UUID][]string, error) {
	ix, err := loadScopeIndex(ctx, orgID, r.groups, r.memberships, r.hosts.ListScopeHosts)
	if err != nil {
		return nil, err
	}
	var inChange []func(hosts.Host) bool
	for _, c := range changed {
		if selects, known := ix.selector(c); known {
			inChange = append(inChange, selects)
		}
	}
	var reached []hosts.Host
	for _, h := range ix.hosts {
		for _, sel := range inChange {
			if sel(h) {
				reached = append(reached, h)
				break
			}
		}
	}
	out := map[uuid.UUID][]string{}
	if len(reached) == 0 {
		return out, nil
	}
	for key, scope := range scopes {
		selects, known := ix.selector(scope)
		var names []string
		for _, h := range reached {
			if !known || selects(h) {
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
