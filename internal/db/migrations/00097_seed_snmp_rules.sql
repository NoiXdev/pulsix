-- +goose Up
-- The two SNMP rules (backlog 3.13d, spec
-- docs/superpowers/specs/2026-11-14-snmp-checks-design.md, "Default
-- rules") join the starter set internal/rules.SeedRules() gives every new
-- organization: a device that does not answer (snmp.up below 1) is
-- critical, an interface that is switched on but not up (snmp.if.status
-- down) a warning. They match nothing in an organization without an snmp
-- check. This backfills the same two rows for every organization that
-- already exists, the way 00094 backfilled the script rules: plain SQL,
-- because a migration cannot call Go code, so the values below — the ast
-- jsonb is what Form.Compile builds, the source what Format renders for it
-- — must stay in sync with SeedRules() (internal/rules/seed.go) by hand.
-- TestRulesBackfillMatchesSeedRules runs the statement between the markers
-- and compares the rows with SeedRules();
-- TestSNMPRulesMigrationMatchesSeedForOrg compares the migrated rows with
-- what SeedForOrg writes, column for column.
--
-- check_rules has no unique key on a rule's name, so an organization that
-- already holds a rule of either name (an operator's own, or a seed row)
-- is skipped for that name by the NOT EXISTS below: the operator's rule
-- stays, and no second rule of the same name appears.
-- rules-seed-backfill:begin
INSERT INTO check_rules (
  id, org_id, name, enabled, origin, scope, check_key, metric, severity,
  mode, ast, clear_ast, source, for_duration
)
SELECT gen_random_uuid(), o.id, v.name, true, 'seed', 'org', 'snmp', v.metric, v.severity,
  'form', v.ast::jsonb, NULL, v.source, '0'::interval
FROM organizations o
CROSS JOIN (VALUES
  (
    'SNMP device unreachable', 'snmp.up', 'crit',
    '{"kind":"compare","op":"<","args":[{"kind":"metric","name":"snmp.up"},{"kind":"number","num":1}]}',
    'snmp.up < 1'
  ),
  (
    'Interface down', 'snmp.if.status', 'warn',
    '{"kind":"label","name":"snmp.if.status","op":"in","values":["down"]}',
    'snmp.if.status in ("down")'
  )
) AS v(name, metric, severity, ast, source)
WHERE NOT EXISTS (SELECT 1 FROM check_rules r WHERE r.org_id = o.id AND r.name = v.name);
-- rules-seed-backfill:end

-- +goose Down
-- Development databases only (docs/upgrades.md: migrations only go up).
-- Only the seed rows of the two names, whether this migration or
-- SeedForOrg wrote them; an operator's rule of either name stays.
DELETE FROM check_rules
WHERE origin = 'seed' AND name IN ('SNMP device unreachable', 'Interface down');
