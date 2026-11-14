-- +goose Up
-- The two script rules (backlog 3.6g, spec
-- docs/superpowers/specs/2026-11-14-script-checks-design.md, "Manager")
-- join the starter set internal/rules.SeedRules() gives every new
-- organization: a script reporting crit is critical, one reporting warn or
-- unknown a warning. They match nothing on a host without a script check.
-- This backfills the same two rows for every organization that already
-- exists, the way 00045 backfilled the first five: plain SQL, because a
-- migration cannot call Go code, so the values below — the ast jsonb is
-- what Form.Compile builds, the source what Format renders for it — must
-- stay in sync with SeedRules() (internal/rules/seed.go) by hand.
-- TestRulesBackfillMatchesSeedRules runs the statement between the markers
-- and compares the rows with SeedRules();
-- TestScriptRulesMigrationMatchesSeedForOrg compares the migrated rows
-- with what SeedForOrg writes, column for column.
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
SELECT gen_random_uuid(), o.id, v.name, true, 'seed', 'org', 'script', 'script.status', v.severity,
  'form', v.ast::jsonb, NULL, v.source, '0'::interval
FROM organizations o
CROSS JOIN (VALUES
  (
    'Script reports critical', 'crit',
    '{"kind":"label","name":"script.status","op":"in","values":["crit"]}',
    'script.status in ("crit")'
  ),
  (
    'Script reports warning', 'warn',
    '{"kind":"label","name":"script.status","op":"in","values":["warn","unknown"]}',
    'script.status in ("warn", "unknown")'
  )
) AS v(name, severity, ast, source)
WHERE NOT EXISTS (SELECT 1 FROM check_rules r WHERE r.org_id = o.id AND r.name = v.name);
-- rules-seed-backfill:end

-- +goose Down
-- Development databases only (docs/upgrades.md: migrations only go up).
-- Only the seed rows of the two names, whether this migration or
-- SeedForOrg wrote them; an operator's rule of either name stays.
DELETE FROM check_rules
WHERE origin = 'seed' AND name IN ('Script reports critical', 'Script reports warning');
