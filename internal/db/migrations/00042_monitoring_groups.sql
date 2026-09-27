-- +goose Up
-- Monitoring groups (v0.3b-1, spec docs/superpowers/specs/2026-09-27-v0.3b-
-- checks-design.md, "Data model"): a named, saved host-tag selector with a
-- priority, used both to bind check definitions to a set of hosts (Task 2)
-- and to break ties when more than one group's definitions overlap on a
-- host (the resolver, Task 3, "Resolution": higher priority wins, a tie
-- goes to the older group). The selector expression itself is validated at
-- write time by internal/checks (internal/hosts/selector.Parse) — this
-- migration stores whatever text was accepted, unparsed and unindexed.
--
-- UNIQUE (org_id, id) exists for the same reason migration 00032 added it
-- to maintenance_windows: Task 2's check_definitions.group_id needs a
-- composite foreign key (org_id, group_id) REFERENCES monitoring_groups
-- (org_id, id), so a definition can never reference a group belonging to
-- another organization.
CREATE TABLE monitoring_groups (
  id         uuid PRIMARY KEY,
  org_id     uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  name       text NOT NULL,
  selector   text NOT NULL,
  priority   integer NOT NULL DEFAULT 0,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (org_id, name),
  UNIQUE (org_id, id)
);
CREATE INDEX monitoring_groups_org_idx ON monitoring_groups (org_id);

-- The standard row-level security pair (see e.g. migration 00037): every
-- org-owned table admits only the bound organization, or every row when no
-- organization is bound at all (the fail-closed default for a connection
-- that never scopes itself, not a way to skip the check for one that did).
ALTER TABLE monitoring_groups ENABLE ROW LEVEL SECURITY;
ALTER TABLE monitoring_groups FORCE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON monitoring_groups
  USING (org_id = NULLIF(current_setting('pulsix.org_id', true), '')::uuid);
CREATE POLICY org_isolation_bypass ON monitoring_groups
  USING (current_setting('pulsix.org_id', true) IS NULL OR current_setting('pulsix.org_id', true) = '');

-- +goose Down
DROP TABLE monitoring_groups;
