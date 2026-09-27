-- +goose Up
-- Check definitions (v0.3b-1, spec docs/superpowers/specs/2026-09-27-v0.3b-
-- checks-design.md, "Data model"): what is measured, and where it applies.
-- A definition binds one plugin (cpu, memory, disk, service — the four this
-- milestone ships; internal/checks refuses any other at write time) to
-- exactly one scope — the organization as a whole, one monitoring group, or
-- one host — under a `key` that is what a narrower scope overrides on (see
-- "Resolution": host beats group beats organization, matched by key alone).
--
-- The binding is two nullable foreign keys plus a CHECK that exactly the
-- one column matching binding_kind is set, rather than a single polymorphic
-- id column, so referential integrity (a definition can never dangle on a
-- deleted group or host) stays inside the database instead of becoming an
-- application-level promise. Both FKs are composite on (org_id, ...): the
-- same "can only reference this organization's own row" guard
-- monitoring_groups (00042) and hosts (00014) already carry for their own
-- composite references, extended here so a definition cannot bind to
-- another organization's group or host either. ON DELETE CASCADE on both:
-- deleting a group or host takes its bindings with it, the same as an
-- organization's deletion takes everything in it.
--
-- UNIQUE ... NULLS NOT DISTINCT (org_id, binding_kind, group_id, host_id,
-- key): ordinary UNIQUE treats every NULL as distinct from every other, so
-- two organization-bound definitions (both group_id and host_id NULL) with
-- the same key would never collide — exactly the case this constraint
-- exists to catch. NULLS NOT DISTINCT needs PostgreSQL 15+; 18 is deployed
-- (see deploy/compose/docker-compose.test.yml).
--
-- Seed set: every organization gets the "Linux base" set (cpu, memory,
-- disk, service) the moment it is created — internal/checks.Service.
-- SeedForOrg, wired as tenancy's OrgCreatedHook the same way
-- hosts.EnsureDefaultZone already is (internal/app/app.go) — and this
-- migration backfills the same four rows for every organization that
-- already existed before this migration ran, so no organization is ever
-- missing them. Seeded rows carry origin = 'seed'; internal/checks refuses
-- to change a seeded row's key or plugin (copy it instead), but its
-- interval, parameters and enabled flag are ordinary mutable columns.
CREATE TABLE check_definitions (
  id               uuid PRIMARY KEY,
  org_id           uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  key              text NOT NULL,
  plugin           text NOT NULL,
  params           jsonb NOT NULL DEFAULT '{}'::jsonb,
  interval_seconds integer NOT NULL CHECK (interval_seconds BETWEEN 10 AND 86400),
  enabled          boolean NOT NULL DEFAULT true,
  origin           text NOT NULL DEFAULT 'custom' CHECK (origin IN ('seed', 'custom')),
  binding_kind     text NOT NULL CHECK (binding_kind IN ('org', 'group', 'host')),
  group_id         uuid,
  host_id          uuid,
  created_by       uuid,
  created_at       timestamptz NOT NULL DEFAULT now(),
  updated_at       timestamptz NOT NULL DEFAULT now(),
  CHECK (
    (binding_kind = 'org'   AND group_id IS NULL     AND host_id IS NULL) OR
    (binding_kind = 'group' AND group_id IS NOT NULL AND host_id IS NULL) OR
    (binding_kind = 'host'  AND group_id IS NULL     AND host_id IS NOT NULL)
  ),
  UNIQUE NULLS NOT DISTINCT (org_id, binding_kind, group_id, host_id, key),
  FOREIGN KEY (org_id, group_id) REFERENCES monitoring_groups (org_id, id) ON DELETE CASCADE,
  FOREIGN KEY (org_id, host_id) REFERENCES hosts (org_id, id) ON DELETE CASCADE
);
CREATE INDEX check_definitions_org_idx ON check_definitions (org_id);

-- The standard row-level security pair (see e.g. migration 00037): every
-- org-owned table admits only the bound organization, or every row when no
-- organization is bound at all.
ALTER TABLE check_definitions ENABLE ROW LEVEL SECURITY;
ALTER TABLE check_definitions FORCE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON check_definitions
  USING (org_id = NULLIF(current_setting('pulsix.org_id', true), '')::uuid);
CREATE POLICY org_isolation_bypass ON check_definitions
  USING (current_setting('pulsix.org_id', true) IS NULL OR current_setting('pulsix.org_id', true) = '');

-- Backfill: every organization that existed before this migration gets the
-- same "Linux base" seed set a newly created one gets from
-- internal/checks.Service.SeedForOrg. The values below must stay in sync
-- with SeedDefinitions() (internal/checks/seed.go) — this is plain SQL, not
-- a call into it, because a migration cannot invoke Go code.
INSERT INTO check_definitions (id, org_id, key, plugin, params, interval_seconds, enabled, origin, binding_kind)
SELECT gen_random_uuid(), o.id, v.key, v.plugin, v.params::jsonb, v.interval_seconds, true, 'seed', 'org'
FROM organizations o
CROSS JOIN (VALUES
  ('cpu',     'cpu',     '{}',                  60),
  ('memory',  'memory',  '{}',                  60),
  ('disk',    'disk',    '{}',                  300),
  ('service', 'service', '{"units":["sshd"]}',  60)
) AS v(key, plugin, params, interval_seconds);

-- +goose Down
DROP TABLE check_definitions;
