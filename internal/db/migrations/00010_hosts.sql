-- +goose Up
-- zones group hosts within an organization (a datacenter, a region, an
-- environment — the operator's own vocabulary). Every organization gets a
-- "default" zone the moment it is created (internal/hosts.Service.
-- EnsureDefaultZone, invoked via the tenancy.OrgCreatedHook — see
-- internal/tenancy.Service.CreateOrg and internal/setup.Service.Complete),
-- so a host can always be created without the caller first having to think
-- about zones.
CREATE TABLE zones (
  id         uuid PRIMARY KEY,
  org_id     uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  slug       text NOT NULL,
  name       text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (org_id, slug)
);
-- hosts carry two independent capability flags: managed (Ansible may act on
-- it over SSH) and monitored (checks run on or against it). Neither implies
-- the other — a Kubernetes node may be managed only, a network switch
-- monitored only, a web server both — see internal/hosts.Service.
-- SetCapabilities, which updates either flag without ever touching the
-- other.
CREATE TABLE hosts (
  id           uuid PRIMARY KEY,
  org_id       uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  zone_id      uuid NOT NULL REFERENCES zones(id),
  name         text NOT NULL,
  address      text NOT NULL DEFAULT '',
  managed      boolean NOT NULL DEFAULT false,
  monitored    boolean NOT NULL DEFAULT false,
  facts        jsonb NOT NULL DEFAULT '{}',
  last_seen_at timestamptz,
  created_at   timestamptz NOT NULL DEFAULT now(),
  updated_at   timestamptz NOT NULL DEFAULT now(),
  UNIQUE (org_id, name)
);
CREATE INDEX hosts_org_zone ON hosts(org_id, zone_id);
-- RLS for both tables, the same org_isolation / org_isolation_bypass pair
-- every other org-owned table in this schema carries (see api_tokens'
-- migration for the pattern this mirrors).
ALTER TABLE zones ENABLE ROW LEVEL SECURITY;
ALTER TABLE zones FORCE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON zones
  USING (org_id = current_setting('pulsix.org_id', true)::uuid);
CREATE POLICY org_isolation_bypass ON zones
  USING (current_setting('pulsix.org_id', true) IS NULL OR current_setting('pulsix.org_id', true) = '');
ALTER TABLE hosts ENABLE ROW LEVEL SECURITY;
ALTER TABLE hosts FORCE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON hosts
  USING (org_id = current_setting('pulsix.org_id', true)::uuid);
CREATE POLICY org_isolation_bypass ON hosts
  USING (current_setting('pulsix.org_id', true) IS NULL OR current_setting('pulsix.org_id', true) = '');

-- +goose Down
DROP TABLE hosts;
DROP TABLE zones;
