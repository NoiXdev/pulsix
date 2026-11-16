-- +goose Up
-- Web assertions and synthetic check rules (backlog 3.13b, spec
-- docs/superpowers/specs/2026-11-16-web-assertions-design.md, "Rules" and
-- "Alert detail").
--
-- 1. alerts.detail and alert_events.detail: a short, nullable text the
--    alerts executor fills when it opens an alert or changes its severity
--    (for example the failure reason of a synthetic check). Additive.
--
-- 2. Seven starter rules join internal/rules.SeedRules(): a failed web
--    assertion is a warning or critical (the two discrete http.assertion
--    labels), a synthetic check that does not succeed (<plugin>.up below 1)
--    is critical. They match nothing in an organization without a check of
--    that plugin. This backfills the same seven rows for every organization
--    that already exists, the way 00097 did: plain SQL, because a migration
--    cannot call Go code, so the values below — the ast jsonb is what
--    Form.Compile builds, the source what Format renders for it — must stay
--    in sync with SeedRules() (internal/rules/seed.go) by hand.
--    TestRulesBackfillMatchesSeedRules runs the statement between the
--    markers and compares the rows with SeedRules();
--    TestWebRulesMigrationMatchesSeedForOrg compares the migrated rows with
--    what SeedForOrg writes, column for column.
--
--    check_rules has no unique key on a rule's name, so an organization
--    that already holds a rule of one of the names (an operator's own, or a
--    seed row) is skipped for that name by the NOT EXISTS below: the
--    operator's rule stays, and no second rule of the same name appears.
--    Upgrading turns a currently failing synthetic check into a critical
--    alert on the first evaluation after the migration (docs/upgrades.md).
ALTER TABLE alerts ADD COLUMN detail text;
ALTER TABLE alert_events ADD COLUMN detail text;

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
    'Web assertion failed', 'http', 'http.assertion', 'warn',
    '{"kind":"label","name":"http.assertion","op":"in","values":["warn"]}',
    'http.assertion in ("warn")'
  ),
  (
    'Web assertion failed (critical)', 'http', 'http.assertion', 'crit',
    '{"kind":"label","name":"http.assertion","op":"in","values":["crit"]}',
    'http.assertion in ("crit")'
  ),
  (
    'Ping check down', 'icmp', 'icmp.up', 'crit',
    '{"kind":"compare","op":"<","args":[{"kind":"metric","name":"icmp.up"},{"kind":"number","num":1}]}',
    'icmp.up < 1'
  ),
  (
    'TCP check down', 'tcp', 'tcp.up', 'crit',
    '{"kind":"compare","op":"<","args":[{"kind":"metric","name":"tcp.up"},{"kind":"number","num":1}]}',
    'tcp.up < 1'
  ),
  (
    'HTTP check down', 'http', 'http.up', 'crit',
    '{"kind":"compare","op":"<","args":[{"kind":"metric","name":"http.up"},{"kind":"number","num":1}]}',
    'http.up < 1'
  ),
  (
    'TLS check down', 'tls', 'tls.up', 'crit',
    '{"kind":"compare","op":"<","args":[{"kind":"metric","name":"tls.up"},{"kind":"number","num":1}]}',
    'tls.up < 1'
  ),
  (
    'DNS check down', 'dns', 'dns.up', 'crit',
    '{"kind":"compare","op":"<","args":[{"kind":"metric","name":"dns.up"},{"kind":"number","num":1}]}',
    'dns.up < 1'
  )
) AS v(name, check_key, metric, severity, ast, source)
WHERE NOT EXISTS (SELECT 1 FROM check_rules r WHERE r.org_id = o.id AND r.name = v.name);
-- rules-seed-backfill:end

-- +goose Down
-- Development databases only (docs/upgrades.md: migrations only go up).
-- Only the seed rows of the seven names, whether this migration or
-- SeedForOrg wrote them; an operator's rule of any name stays.
DELETE FROM check_rules
WHERE origin = 'seed' AND name IN (
  'Web assertion failed', 'Web assertion failed (critical)', 'Ping check down',
  'TCP check down', 'HTTP check down', 'TLS check down', 'DNS check down'
);
ALTER TABLE alert_events DROP COLUMN detail;
ALTER TABLE alerts DROP COLUMN detail;
