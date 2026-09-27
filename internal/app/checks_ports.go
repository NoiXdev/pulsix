package app

import (
	"context"

	"github.com/google/uuid"

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
