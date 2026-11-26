-- +goose Up
-- Assertion rules for tcp, dns and tls (backlog 3.87, plan
-- docs/superpowers/plans/2026-11-26-assertions-other-plugins-3.87.md, R10).
--
-- Six starter rules join internal/rules.SeedRules(): a failed tcp, dns or
-- tls assertion is a warning or critical (the two discrete
-- <plugin>.assertion labels), as 00104 seeded for http. They match nothing
-- in an organization without a check of that plugin that has assertions.
-- This backfills the same six rows for every organization that already
-- exists, the way 00104 did: plain SQL, because a migration cannot call Go
-- code, so the values below — the ast jsonb is what Form.Compile builds,
-- the source what Format renders for it — must stay in sync with
-- SeedRules() (internal/rules/seed.go) by hand.
-- TestRulesBackfillMatchesSeedRules runs the statement between the markers
-- and compares the rows with SeedRules();
-- TestAssertionRulesMigrationMatchesSeedForOrg compares the migrated rows
-- with what SeedForOrg writes, column for column.
--
-- check_rules has no unique key on a rule's name, so an organization that
-- already holds a rule of one of the names (an operator's own, or a seed
-- row) is skipped for that name by the NOT EXISTS below. Additive only.

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
    'TCP assertion failed', 'tcp', 'tcp.assertion', 'warn',
    '{"kind":"label","name":"tcp.assertion","op":"in","values":["warn"]}',
    'tcp.assertion in ("warn")'
  ),
  (
    'TCP assertion failed (critical)', 'tcp', 'tcp.assertion', 'crit',
    '{"kind":"label","name":"tcp.assertion","op":"in","values":["crit"]}',
    'tcp.assertion in ("crit")'
  ),
  (
    'DNS assertion failed', 'dns', 'dns.assertion', 'warn',
    '{"kind":"label","name":"dns.assertion","op":"in","values":["warn"]}',
    'dns.assertion in ("warn")'
  ),
  (
    'DNS assertion failed (critical)', 'dns', 'dns.assertion', 'crit',
    '{"kind":"label","name":"dns.assertion","op":"in","values":["crit"]}',
    'dns.assertion in ("crit")'
  ),
  (
    'TLS assertion failed', 'tls', 'tls.assertion', 'warn',
    '{"kind":"label","name":"tls.assertion","op":"in","values":["warn"]}',
    'tls.assertion in ("warn")'
  ),
  (
    'TLS assertion failed (critical)', 'tls', 'tls.assertion', 'crit',
    '{"kind":"label","name":"tls.assertion","op":"in","values":["crit"]}',
    'tls.assertion in ("crit")'
  )
) AS v(name, check_key, metric, severity, ast, source)
WHERE NOT EXISTS (SELECT 1 FROM check_rules r WHERE r.org_id = o.id AND r.name = v.name);
-- rules-seed-backfill:end

-- +goose Down
-- Development databases only (docs/upgrades.md: migrations only go up).
-- Only the seed rows of the six names, whether this migration or
-- SeedForOrg wrote them; an operator's rule of any name stays.
DELETE FROM check_rules
WHERE origin = 'seed' AND name IN (
  'TCP assertion failed', 'TCP assertion failed (critical)',
  'DNS assertion failed', 'DNS assertion failed (critical)',
  'TLS assertion failed', 'TLS assertion failed (critical)'
);
