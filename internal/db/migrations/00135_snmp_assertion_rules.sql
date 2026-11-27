-- +goose Up
-- Assertion rules for snmp (backlog 3.134, plan
-- docs/superpowers/plans/2026-11-26-snmp-assertions-3.134.md, R10).
--
-- Two starter rules join internal/rules.SeedRules(): a failed snmp
-- assertion is a warning or critical (the two discrete snmp.assertion
-- labels), as 00134 seeded for tcp, dns and tls. They match nothing in an
-- organization without an snmp check that has assertions. This backfills
-- the same two rows for every organization that already exists, the way
-- 00134 did: plain SQL, because a migration cannot call Go code, so the
-- values below — the ast jsonb is what Form.Compile builds, the source what
-- Format renders for it — must stay in sync with SeedRules()
-- (internal/rules/seed.go) by hand. TestRulesBackfillMatchesSeedRules runs
-- the statement between the markers and compares the rows with SeedRules();
-- TestSNMPAssertionRulesMigrationMatchesSeedForOrg compares the migrated
-- rows with what SeedForOrg writes, column for column.
--
-- check_rules has no unique key on a rule's name, so an organization that
-- already holds a rule of one of the names (an operator's own, or a seed
-- row it changed) is skipped for that name by the NOT EXISTS below.
-- Additive only.

-- rules-seed-backfill:begin
INSERT INTO check_rules (
  id, org_id, name, enabled, origin, scope, check_key, metric, severity,
  mode, ast, clear_ast, source, for_duration
)
SELECT gen_random_uuid(), o.id, v.name, true, 'seed', 'org', v.check_key, v.metric, v.severity,
  'form', v.ast::jsonb, NULL, v.source, '0'::interval
FROM organizations o
CROSS JOIN (VALUES
  (
    'SNMP assertion failed', 'snmp', 'snmp.assertion', 'warn',
    '{"kind":"label","name":"snmp.assertion","op":"in","values":["warn"]}',
    'snmp.assertion in ("warn")'
  ),
  (
    'SNMP assertion failed (critical)', 'snmp', 'snmp.assertion', 'crit',
    '{"kind":"label","name":"snmp.assertion","op":"in","values":["crit"]}',
    'snmp.assertion in ("crit")'
  )
) AS v(name, check_key, metric, severity, ast, source)
WHERE NOT EXISTS (SELECT 1 FROM check_rules r WHERE r.org_id = o.id AND r.name = v.name);
-- rules-seed-backfill:end

-- +goose Down
-- Development databases only (docs/upgrades.md: migrations only go up).
-- Only the seed rows of the two names, whether this migration or
-- SeedForOrg wrote them; an operator's rule of either name stays.
DELETE FROM check_rules
WHERE origin = 'seed' AND name IN ('SNMP assertion failed', 'SNMP assertion failed (critical)');
