package rules

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// SeedRules is the starter set every organization gets: visible, editable
// rows, never a default hidden in Go code (the design's own position — an
// operator must be able to see a threshold, change it and delete it, and
// deleting it must mean "no rule", not "silently fall back to a hardcoded
// value"). All five bind at scope org, in form mode.
//
// service.state deliberately has no row here, and that is not an
// oversight: backlog 4.3's standing ruling is that the seeded sshd check
// (checks.SeedDefinitions) exists on nearly every host Pulsix manages, but
// on the few where it does not, a default rule here would turn a harmless
// oddity into a 3 a.m. page. An operator who wants to watch a particular
// service adds that rule themselves, deliberately, for the services that
// actually matter to their fleet. Do not "complete" this set by adding
// service.state back in.
//
// This function's return value is used two ways that must never disagree:
// SeedForOrg calls it directly for a newly created organization, and
// migration 00045's backfill INSERT encodes the same five rows in SQL for
// organizations that already existed — the migration cannot call Go code,
// so keep the two in sync by hand when either changes.
func SeedRules() []RuleInput {
	return []RuleInput{
		{
			Name: "Disk usage warning", Enabled: true, Scope: ScopeOrg,
			Metric: "disk.used_pct", Severity: SeverityWarn, Mode: modeForm,
			Form: &Form{Metric: "disk.used_pct", Op: ">=", Value: 85, ClearOp: "<", ClearValue: 80},
		},
		{
			Name: "Disk usage critical", Enabled: true, Scope: ScopeOrg,
			Metric: "disk.used_pct", Severity: SeverityCrit, Mode: modeForm,
			Form: &Form{Metric: "disk.used_pct", Op: ">=", Value: 95, ClearOp: "<", ClearValue: 90},
		},
		{
			Name: "Memory usage warning", Enabled: true, Scope: ScopeOrg,
			Metric: "mem.used_pct", Severity: SeverityWarn, Mode: modeForm,
			Form: &Form{Metric: "mem.used_pct", Op: ">=", Value: 90},
			For:  10 * time.Minute,
		},
		{
			Name: "Memory usage critical", Enabled: true, Scope: ScopeOrg,
			Metric: "mem.used_pct", Severity: SeverityCrit, Mode: modeForm,
			Form: &Form{Metric: "mem.used_pct", Op: ">=", Value: 97},
			For:  5 * time.Minute,
		},
		{
			Name: "CPU usage warning", Enabled: true, Scope: ScopeOrg,
			Metric: "cpu.used_pct", Severity: SeverityWarn, Mode: modeForm,
			Form: &Form{Metric: "cpu.used_pct", Op: ">=", Value: 90},
			For:  15 * time.Minute,
		},
	}
}

// SeedForOrg writes orgID's seed set (SeedRules), each row stamped origin =
// "seed". It is wired as tenancy's OrgCreatedHook by internal/app
// (app.ChainOrgCreatedHooks), run right after checks.Service.SeedForOrg —
// a later, sequential step in the same chained hook, not inside the same
// transaction (see tenancy.OrgCreatedHook's own doc comment for why a
// hook runs post-commit rather than inside CreateOrg's transaction).
//
// A failure partway through (the store rejecting one row, a lost
// connection) is reported rather than silently leaving a partial seed set:
// CreateOrg propagates the error like any other hook failure, and the
// organization is left for an operator or a retry to notice and repair,
// rather than this call quietly leaving whatever it managed to write in
// place.
func (s *Service) SeedForOrg(ctx context.Context, orgID uuid.UUID) error {
	for _, in := range SeedRules() {
		if _, err := s.createRule(ctx, orgID, in, originSeed); err != nil {
			return fmt.Errorf("seed rule %q: %w", in.Name, err)
		}
	}
	return nil
}
