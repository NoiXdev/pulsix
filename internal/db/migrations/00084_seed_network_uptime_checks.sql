-- +goose Up
-- The network and uptime checks (backlog 3.7) join the "Linux base" seed
-- set internal/checks.SeedDefinitions() gives every new organization. This
-- backfills the same two rows for every organization that already exists,
-- exactly as 00043 backfilled the first four: plain SQL, because a
-- migration cannot call Go code, so the values below must stay in sync with
-- SeedDefinitions() (internal/checks/seed.go) by hand —
-- TestNetworkUptimeSeedMigrationMatchesSeedForOrg compares the two.
--
-- An organization whose operator already uses the key `network` or `uptime`
-- at the organization-wide scope (a custom row, or a projection of a shared
-- definition) keeps that row: ON CONFLICT on 00043's unique key
-- (org_id, binding_kind, group_id, host_id, key) skips the seed for that
-- key instead of failing the upgrade or replacing the operator's check. A
-- row under either key bound to a group or a host is a different scope and
-- does not stop the seed.
INSERT INTO check_definitions (id, org_id, key, plugin, params, interval_seconds, enabled, origin, binding_kind)
SELECT gen_random_uuid(), o.id, v.key, v.plugin, v.params::jsonb, v.interval_seconds, true, 'seed', 'org'
FROM organizations o
CROSS JOIN (VALUES
  ('network', 'network', '{}', 60),
  ('uptime',  'uptime',  '{}', 300)
) AS v(key, plugin, params, interval_seconds)
ON CONFLICT (org_id, binding_kind, group_id, host_id, key) DO NOTHING;

-- +goose Down
-- Only the seed rows this migration (or SeedForOrg since) wrote under the
-- two keys; an operator's own row under either key stays.
DELETE FROM check_definitions
WHERE origin = 'seed' AND key IN ('network', 'uptime');
