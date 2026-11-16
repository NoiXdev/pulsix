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
// value"). All sixteen bind at scope org, in form mode.
//
// The two script rules (backlog 3.6g) judge the verdict a host-defined
// script reports: crit is critical, warn and unknown are warnings (a script
// that cannot run should be seen; the operator can edit the rule). They
// match nothing on a host without a script check, which is why the script
// plugin itself is not seeded as a check while its rules are.
//
// The two SNMP rules (backlog 3.13d) are the same kind: a device that does
// not answer is critical (snmp.up is 0 only when the device did not
// answer; a refused or misconfigured probe is a collection error, not 0),
// an interface the device's operator switched on but that is not up is a
// warning (admin_down is deliberate and stays quiet). They match nothing
// in an organization without an snmp check.
//
// The seven synthetic-check rules (backlog 3.13b) judge the probes' own
// metrics: http.assertion carries the worst failed web assertion (warn or
// crit, one rule per label), <plugin>.up below 1 means the probe did not
// succeed and is critical. They match nothing in an organization without a
// check of that plugin; upgrading turns an already failing check into an
// alert on the first evaluation (docs/upgrades.md).
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
// already existed — 00045 the first five, 00094 the two script rules,
// 00097 the two SNMP rules. A migration cannot call Go code, so keep them
// in sync by hand when either changes (TestRulesBackfillMatchesSeedRules,
// TestScriptRulesMigrationMatchesSeedForOrg and
// TestSNMPRulesMigrationMatchesSeedForOrg and
// TestWebRulesMigrationMatchesSeedForOrg compare them).
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
		{
			Name: "SNMP device unreachable", Enabled: true, Scope: ScopeOrg,
			Metric: "snmp.up", Severity: SeverityCrit, Mode: modeForm,
			Form: &Form{Metric: "snmp.up", Op: "<", Value: 1},
		},
		{
			Name: "Interface down", Enabled: true, Scope: ScopeOrg,
			Metric: "snmp.if.status", Severity: SeverityWarn, Mode: modeForm,
			Form: &Form{Metric: "snmp.if.status", Op: "in", Labels: []string{"down"}},
		},
		{
			Name: "Web assertion failed", Enabled: true, Scope: ScopeOrg,
			Metric: "http.assertion", Severity: SeverityWarn, Mode: modeForm,
			Form: &Form{Metric: "http.assertion", Op: "in", Labels: []string{"warn"}},
		},
		{
			Name: "Web assertion failed (critical)", Enabled: true, Scope: ScopeOrg,
			Metric: "http.assertion", Severity: SeverityCrit, Mode: modeForm,
			Form: &Form{Metric: "http.assertion", Op: "in", Labels: []string{"crit"}},
		},
		{
			Name: "Ping check down", Enabled: true, Scope: ScopeOrg,
			Metric: "icmp.up", Severity: SeverityCrit, Mode: modeForm,
			Form: &Form{Metric: "icmp.up", Op: "<", Value: 1},
		},
		{
			Name: "TCP check down", Enabled: true, Scope: ScopeOrg,
			Metric: "tcp.up", Severity: SeverityCrit, Mode: modeForm,
			Form: &Form{Metric: "tcp.up", Op: "<", Value: 1},
		},
		{
			Name: "HTTP check down", Enabled: true, Scope: ScopeOrg,
			Metric: "http.up", Severity: SeverityCrit, Mode: modeForm,
			Form: &Form{Metric: "http.up", Op: "<", Value: 1},
		},
		{
			Name: "TLS check down", Enabled: true, Scope: ScopeOrg,
			Metric: "tls.up", Severity: SeverityCrit, Mode: modeForm,
			Form: &Form{Metric: "tls.up", Op: "<", Value: 1},
		},
		{
			Name: "DNS check down", Enabled: true, Scope: ScopeOrg,
			Metric: "dns.up", Severity: SeverityCrit, Mode: modeForm,
			Form: &Form{Metric: "dns.up", Op: "<", Value: 1},
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
