package app

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/noixdev/pulsix/internal/checks"
	"github.com/noixdev/pulsix/internal/hosts"
	"github.com/noixdev/pulsix/internal/hosts/selector"
	"github.com/noixdev/pulsix/internal/tenancy"
)

// ChainOrgCreatedHooks composes several tenancy.OrgCreatedHook values into
// one, run in order and stopping at the first error — needed because
// tenancy.Service and setup.Service each hold exactly one hook
// (SetOrgCreatedHook overwrites, it does not add), and this task's seed
// set (checks.Service.SeedForOrg) must run alongside the existing
// hostsSvc.EnsureDefaultZone hook at organization creation, not instead of
// it. Order matters only in that a later hook's failure must not be able
// to hide an earlier hook's own — every current caller (Build, apitest.go)
// puts EnsureDefaultZone first, unchanged from before this function
// existed, and SeedForOrg after it. Exported (like NewGroupHosts in
// maintenance_ports.go) so apitest.go's fixture wiring can bind the same
// composition production does, without duplicating it.
//
// One deliberate exception to "wire both in the same commit": v0.3b-2 Task
// 5 added rules.Service.SeedForOrg to Build's chain but not to apitest.go's
// — apitest's Deps has no Rules service yet for a seeded rules hook to be
// useful to any test, so there is nothing there to hang the call off until
// the rules API surface (Task 10) adds one. That task should add
// rulesSvc.SeedForOrg to apitest.go's chain and SeedOrg alongside it.
func ChainOrgCreatedHooks(hooks ...tenancy.OrgCreatedHook) tenancy.OrgCreatedHook {
	return func(ctx context.Context, orgID uuid.UUID) error {
		for _, hook := range hooks {
			if err := hook(ctx, orgID); err != nil {
				return err
			}
		}
		return nil
	}
}

// checksHostMatcher adapts hosts.Service to checks.HostMatcher: a
// monitoring group's selector text, parsed with the same grammar
// hosts.Service.QueryHosts already consumes for the host list's own
// selector queries, resolved to the ids of the hosts it matches.
type checksHostMatcher struct{ hosts *hosts.Service }

// NewChecksHostMatcher wires the adapter Build uses; exported so
// internal/api/apitest can build the identical resolver wiring once its
// own checks routes land (Task 3's handoff: "whichever task wires one
// should almost certainly wire both in the same commit").
func NewChecksHostMatcher(svc *hosts.Service) checks.HostMatcher {
	return checksHostMatcher{hosts: svc}
}

// MatchingHosts implements checks.HostMatcher. An unparseable selector is
// checks.ErrInvalidInput, the same sentinel Preview and the resolver
// already treat a bad selector as — a monitoring group's own selector is
// validated at write time (groups.go), so a parse failure reaching here
// means a group was persisted before the grammar changed underneath it,
// not a caller mistake in this call itself.
func (m checksHostMatcher) MatchingHosts(ctx context.Context, orgID uuid.UUID, sel string) ([]uuid.UUID, error) {
	parsed, err := selector.Parse(sel)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", checks.ErrInvalidInput, err)
	}
	rows, err := m.hosts.QueryHosts(ctx, orgID, parsed)
	if err != nil {
		return nil, err
	}
	ids := make([]uuid.UUID, len(rows))
	for i, h := range rows {
		ids[i] = h.ID
	}
	return ids, nil
}

// checksHostRoster adapts hosts.Service to checks.HostRoster: every
// monitored host in the organization — the resolver's "only monitored
// hosts resolve to a non-empty set" gate.
type checksHostRoster struct{ hosts *hosts.Service }

// NewChecksHostRoster wires the adapter Build uses (see
// NewChecksHostMatcher's own comment on why it is exported).
func NewChecksHostRoster(svc *hosts.Service) checks.HostRoster {
	return checksHostRoster{hosts: svc}
}

// MonitoredHosts implements checks.HostRoster.
func (r checksHostRoster) MonitoredHosts(ctx context.Context, orgID uuid.UUID) ([]uuid.UUID, error) {
	all, err := r.hosts.ListAllHosts(ctx, orgID)
	if err != nil {
		return nil, err
	}
	ids := make([]uuid.UUID, 0, len(all))
	for _, h := range all {
		if h.Monitored {
			ids = append(ids, h.ID)
		}
	}
	return ids, nil
}
