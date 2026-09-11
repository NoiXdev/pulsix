-- +goose Up
-- Inventory groups and memberships (spec §6, backlog 2.1-2.5).
--
-- schema is the group's variable schema (internal/inventory/schema JSON);
-- vars holds the group-scoped values ("values" is an SQL keyword). Secret
-- values inside vars are stored as {"$sealed": "<base64>"}, sealed with
-- "inventory-group:<org>:<group>" as additional data. revision backs the
-- If-Match concurrency check on every group write.
--
-- Group names are unique per organization because Ansible's group namespace
-- is flat. A group with children cannot be deleted (the parent foreign key
-- has no cascade); deleting a group removes its memberships.
CREATE TABLE inventory_groups (
  id          uuid PRIMARY KEY,
  org_id      uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  parent_id   uuid,
  name        text NOT NULL,
  description text NOT NULL DEFAULT '',
  schema      jsonb NOT NULL DEFAULT '{"fields": []}',
  vars        jsonb NOT NULL DEFAULT '{}',
  bastion_id  uuid,
  revision    bigint NOT NULL DEFAULT 1,
  created_at  timestamptz NOT NULL DEFAULT now(),
  updated_at  timestamptz NOT NULL DEFAULT now(),
  UNIQUE (org_id, name),
  UNIQUE (org_id, id),
  CHECK (parent_id IS DISTINCT FROM id),
  FOREIGN KEY (org_id, parent_id) REFERENCES inventory_groups (org_id, id),
  FOREIGN KEY (org_id, bastion_id) REFERENCES bastions (org_id, id)
);
CREATE INDEX inventory_groups_parent ON inventory_groups (org_id, parent_id);

-- vars holds the host-scoped values and the overrides of overridable
-- group-scoped fields; secrets are sealed with
-- "inventory-member:<org>:<group>:<host>". Deleting the group or the host
-- removes the membership.
CREATE TABLE inventory_memberships (
  group_id   uuid NOT NULL,
  host_id    uuid NOT NULL,
  org_id     uuid NOT NULL,
  vars       jsonb NOT NULL DEFAULT '{}',
  revision   bigint NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (group_id, host_id),
  FOREIGN KEY (org_id, group_id) REFERENCES inventory_groups (org_id, id) ON DELETE CASCADE,
  FOREIGN KEY (org_id, host_id) REFERENCES hosts (org_id, id) ON DELETE CASCADE
);
CREATE INDEX inventory_memberships_host ON inventory_memberships (org_id, host_id);

ALTER TABLE inventory_groups ENABLE ROW LEVEL SECURITY;
ALTER TABLE inventory_groups FORCE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON inventory_groups
  USING (org_id = NULLIF(current_setting('pulsix.org_id', true), '')::uuid);
CREATE POLICY org_isolation_bypass ON inventory_groups
  USING (current_setting('pulsix.org_id', true) IS NULL OR current_setting('pulsix.org_id', true) = '');
ALTER TABLE inventory_memberships ENABLE ROW LEVEL SECURITY;
ALTER TABLE inventory_memberships FORCE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON inventory_memberships
  USING (org_id = NULLIF(current_setting('pulsix.org_id', true), '')::uuid);
CREATE POLICY org_isolation_bypass ON inventory_memberships
  USING (current_setting('pulsix.org_id', true) IS NULL OR current_setting('pulsix.org_id', true) = '');

-- +goose Down
DROP TABLE inventory_memberships;
DROP TABLE inventory_groups;
