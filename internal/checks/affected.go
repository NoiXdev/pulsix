// Which hosts a check write reaches (v0.3b-1 spec, "Configuration
// delivery": "the manager recomputes the affected hosts and pushes to
// those currently connected"). The question is the resolver's, not the
// HTTP layer's: the answer follows from the same scope rules ResolveHost
// applies, over the same two ports (HostRoster, HostMatcher), and a second
// copy of them in a handler could drift from what an agent actually runs.
package checks

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// Binding is the scope a check definition applies at, on its own — enough
// to say which hosts a definition bound there reaches, without the rest of
// the row. Definition.Binding and DefinitionInput.Binding produce it, so a
// write can ask about the state it is replacing and the state it is
// writing in the same terms.
type Binding struct {
	Kind            BindingKind
	GroupID, HostID *uuid.UUID
}

// Binding is d's own scope.
func (d Definition) Binding() Binding {
	return Binding{Kind: d.BindingKind, GroupID: d.GroupID, HostID: d.HostID}
}

// Binding is the scope in would bind a definition at, available before the
// write that stores it — which is what lets a caller work out the hosts a
// replacement reaches without first performing it.
func (in DefinitionInput) Binding() Binding {
	return Binding{Kind: in.BindingKind, GroupID: in.GroupID, HostID: in.HostID}
}

// Affected is which hosts a check write reaches.
//
// WholeOrg is deliberately not "the list of every monitored host": an
// organization-bound write reaches all of them, and the caller's own
// per-host work (recomputing and pushing one host's configuration) is
// exactly what ResolveOrg exists to do in one shared pass instead of one
// pass per host. Reporting the shape, rather than a list, is what lets the
// caller take that path — and keeps this package from materialising a
// host list nobody needs.
type Affected struct {
	// WholeOrg means every monitored host in the organization; Hosts is
	// then empty.
	WholeOrg bool
	// Hosts is the affected hosts, each listed once, in no particular
	// order. It says nothing about whether they are monitored: a binding
	// names the hosts it names, and an unmonitored one resolves to an
	// empty set rather than being filtered out here (ResolveHost's own
	// gate), so a host that has just been un-monitored is still told.
	Hosts []uuid.UUID
}

// AffectedHosts reports which hosts a write at these bindings reaches —
// several, because a write that moves a definition from one scope to
// another affects both the hosts that lose it and the ones that gain it.
// Each host is reported once; an organization-bound binding anywhere in
// the list makes the whole answer WholeOrg, which already contains every
// per-host answer.
//
// A malformed binding (a kind with no matching id, or an unknown kind) is
// ErrInvalidInput and a binding to a group that does not exist is
// ErrNotFound, rather than an empty answer: "nothing to push" and "I could
// not work out what to push" must not read the same to the caller.
func (s *Service) AffectedHosts(ctx context.Context, orgID uuid.UUID, bindings ...Binding) (Affected, error) {
	seen := map[uuid.UUID]struct{}{}
	var out Affected
	for _, b := range bindings {
		switch b.Kind {
		case BindingOrg:
			// Every monitored host. Answered without reading the roster
			// at all — see Affected.WholeOrg.
			out.WholeOrg = true
		case BindingHost:
			if b.HostID == nil {
				return Affected{}, fmt.Errorf("%w: a host binding must name a host", ErrInvalidInput)
			}
			seen[*b.HostID] = struct{}{}
		case BindingGroup:
			if b.GroupID == nil {
				return Affected{}, fmt.Errorf("%w: a group binding must name a group", ErrInvalidInput)
			}
			g, err := s.GetGroup(ctx, orgID, *b.GroupID)
			if err != nil {
				return Affected{}, fmt.Errorf("affected hosts: group %s: %w", *b.GroupID, err)
			}
			ids, err := s.Preview(ctx, orgID, g.Selector)
			if err != nil {
				return Affected{}, fmt.Errorf("affected hosts: expand group %s: %w", g.ID, err)
			}
			for _, id := range ids {
				seen[id] = struct{}{}
			}
		default:
			return Affected{}, fmt.Errorf("%w: binding_kind must be org, group or host, got %q", ErrInvalidInput, b.Kind)
		}
	}
	if out.WholeOrg {
		// The per-host ids gathered alongside it are already in it.
		return Affected{WholeOrg: true}, nil
	}
	out.Hosts = make([]uuid.UUID, 0, len(seen))
	for id := range seen {
		out.Hosts = append(out.Hosts, id)
	}
	return out, nil
}

// AffectedHostsForGroup reports which hosts a write to the monitoring
// group identified by groupID reaches. selectors is the group's selector
// text on either side of the write (its current text, and the text
// replacing it — the same text, once, for a delete), since a host that
// stops matching loses the group's checks exactly as much as one that
// starts matching gains them.
//
// The answer is empty unless some check definition binds to that group: a
// group carries no configuration of its own, so while nothing is bound to
// it neither its selector nor its priority can change what any host
// resolves to. That rule lives here rather than in a caller because it is
// a statement about how this package resolves, and because a caller that
// got it wrong would either push every host in the organization on a
// rename or — worse — push none after a real change.
//
// No selector is expanded at all when nothing binds, which is what keeps
// renaming a group that matches a large fleet from costing a selector
// query per edit.
func (s *Service) AffectedHostsForGroup(ctx context.Context, orgID, groupID uuid.UUID, selectors ...string) (Affected, error) {
	defs, err := s.store.ListDefinitions(ctx, orgID)
	if err != nil {
		return Affected{}, fmt.Errorf("affected hosts: list definitions: %w", err)
	}
	bound := false
	for _, d := range defs {
		if d.BindingKind == BindingGroup && d.GroupID != nil && *d.GroupID == groupID {
			bound = true
			break
		}
	}
	if !bound {
		return Affected{}, nil
	}

	seen := map[uuid.UUID]struct{}{}
	for _, sel := range selectors {
		ids, err := s.Preview(ctx, orgID, sel)
		if err != nil {
			return Affected{}, fmt.Errorf("affected hosts: expand group %s: %w", groupID, err)
		}
		for _, id := range ids {
			seen[id] = struct{}{}
		}
	}
	out := Affected{Hosts: make([]uuid.UUID, 0, len(seen))}
	for id := range seen {
		out.Hosts = append(out.Hosts, id)
	}
	return out, nil
}
