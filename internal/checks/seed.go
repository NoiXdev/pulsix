package checks

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// SeedDefinitions is the "Linux base" set (spec's Data model, "Seed set"):
// every plugin the agent runs, each bound to the whole organization under
// its own plugin name as key, so a fresh organization
// already shows current values and graphs for every managed host without
// an operator configuring anything first.
//
// The service plugin's unit list has no organization-wide default that
// makes sense for every fleet — which services matter is exactly what
// differs between organizations — except one: sshd. Pulsix reaches every
// managed host over SSH, so on any host it manages, sshd is both running
// and meaningful to watch; an operator adds the units that matter to them
// alongside it, or edits this row's list outright.
//
// This function's return value is used two ways that must never disagree:
// SeedForOrg calls it directly for a newly created organization, and
// migrations encode the same rows in SQL for organizations that already
// existed — 00043 the first four, 00084 network and uptime (backlog 3.7).
// A migration cannot call Go code, so keep them in sync by hand when either
// changes; TestNetworkUptimeSeedMigrationMatchesSeedForOrg compares 00084's
// rows with this function's.
func SeedDefinitions() []DefinitionInput {
	return []DefinitionInput{
		{Key: PluginCPU, Plugin: PluginCPU, Interval: 60 * time.Second, Enabled: true, BindingKind: BindingOrg},
		{Key: PluginMemory, Plugin: PluginMemory, Interval: 60 * time.Second, Enabled: true, BindingKind: BindingOrg},
		{Key: PluginDisk, Plugin: PluginDisk, Interval: 5 * time.Minute, Enabled: true, BindingKind: BindingOrg},
		{
			Key: PluginService, Plugin: PluginService, Interval: 60 * time.Second, Enabled: true, BindingKind: BindingOrg,
			Params: map[string]any{"units": []string{"sshd"}},
		},
		{Key: PluginNetwork, Plugin: PluginNetwork, Interval: 60 * time.Second, Enabled: true, BindingKind: BindingOrg},
		{Key: PluginUptime, Plugin: PluginUptime, Interval: 5 * time.Minute, Enabled: true, BindingKind: BindingOrg},
	}
}

// SeedForOrg writes orgID's seed set (SeedDefinitions), each row stamped
// origin = "seed". It is wired as tenancy's OrgCreatedHook (see
// tenancy.OrgCreatedHook's own doc comment) by internal/app, run as one
// more sequential step after CreateOrg's own writes have committed —
// exactly how hosts.Service.EnsureDefaultZone is already wired there, and
// combined with it (internal/app cannot install two separate hooks on the
// same tenancy.Service, so it composes both into one).
//
// Every SeedDefinitions entry binds to the organization under a distinct
// key, so nothing here can collide with anything else this same call
// writes; a key already taken by an unrelated row (in the pathological
// case of this hook somehow running twice for the same organization) is
// reported rather than silently skipped — CreateOrg propagates the error,
// the same as any other step's failure, and the organization is left
// without a full seed set for an operator or a retry to notice and repair,
// rather than this call quietly leaving stale seed rows in place.
func (s *Service) SeedForOrg(ctx context.Context, orgID uuid.UUID) error {
	for _, in := range SeedDefinitions() {
		if _, err := s.createDefinition(ctx, orgID, in, originSeed); err != nil {
			return fmt.Errorf("seed check definition %q: %w", in.Key, err)
		}
	}
	return nil
}
