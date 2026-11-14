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
// value"). All seven bind at scope org, in form mode.
//
// The two script rules (backlog 3.6g) judge the verdict a host-defined
// script reports: crit is critical, warn and unknown are warnings (a script
// that cannot run should be seen; the operator can edit the rule). They
// match nothing on a host without a script check, which is why the script
// plugin itself is not seeded as a check while its rules are.
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
// SeedForOrg calls it directly for a newly created organization, and the
// backfill migrations encode the same rows in SQL for organizations that
// already existed — 00045 the first five, 00094 the two script rules. A
// migration cannot call Go code, so keep them in sync by hand when either
// changes (TestRulesBackfillMatchesSeedRules and
// TestScriptRulesMigrationMatchesSeedForOrg compare them).
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
		{
			Name: "Script reports critical", Enabled: true, Scope: ScopeOrg,
			Metric: "script.status", Severity: SeverityCrit, Mode: modeForm,
			Form: &Form{Metric: "script.status", Op: "in", Labels: []string{"crit"}},
		},
		{
			Name: "Script reports warning", Enabled: true, Scope: ScopeOrg,
			Metric: "script.status", Severity: SeverityWarn, Mode: modeForm,
			Form: &Form{Metric: "script.status", Op: "in", Labels: []string{"warn", "unknown"}},
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
