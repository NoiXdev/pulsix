package inventory

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/inventory/schema"
)

// SecretVarNames is the set of variable names the inventory holds a secret
// under for any of hostIDs: the top-level fields of the schemas of every
// group a host belongs to, and of those groups' ancestors (whose vars
// reach the host too), that are or contain a secret, group- or host-scoped
// alike. A run request may not set an extra var of such a name (spec
// 2026-11-25 A, final review I2): extra vars outrank inventory vars, so
// the value would replace the secret for the run.
func (s *Service) SecretVarNames(ctx context.Context, orgID uuid.UUID, hostIDs []uuid.UUID) (map[string]bool, error) {
	out := map[string]bool{}
	if len(hostIDs) == 0 {
		return out, nil
	}
	want := make(map[uuid.UUID]bool, len(hostIDs))
	for _, id := range hostIDs {
		want[id] = true
	}
	members, err := s.store.ListMemberships(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("list memberships: %w", err)
	}
	groups, err := s.store.ListGroups(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("list groups: %w", err)
	}
	tree := treeOf(groups)
	reached := map[uuid.UUID]bool{}
	for _, m := range members {
		if !want[m.HostID] {
			continue
		}
		reached[m.GroupID] = true
		for _, a := range tree.Ancestors(m.GroupID) {
			reached[a] = true
		}
	}
	for _, g := range groups {
		if !reached[g.ID] {
			continue
		}
		for _, f := range g.Schema.Fields {
			if schema.HoldsSecret(f) {
				out[f.Name] = true
			}
		}
	}
	return out, nil
}
