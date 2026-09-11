-- +goose Up
-- Jump hosts (bastions) and per-host SSH settings (spec §5, backlog 2.7a).
--
-- hosts gains UNIQUE (org_id, id) as the target of composite foreign keys:
-- host_ssh here and inventory memberships in the next migration can then
-- only reference a host of their own organization (part of backlog 1.30).
ALTER TABLE hosts ADD CONSTRAINT hosts_org_id_id_key UNIQUE (org_id, id);

-- A bastion may itself be reached through another bastion (via_bastion_id);
-- internal/sshconf rejects cycles and chains longer than four hops before
-- they are written. Deleting a bastion that anything still references fails
-- on the foreign keys below (no cascade), which the service reports as
-- "in use".
CREATE TABLE bastions (
  id             uuid PRIMARY KEY,
  org_id         uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  name           text NOT NULL,
  address        text NOT NULL,
  port           integer NOT NULL DEFAULT 22 CHECK (port BETWEEN 1 AND 65535),
  username       text NOT NULL,
  credential_id  uuid,
  via_bastion_id uuid,
  created_at     timestamptz NOT NULL DEFAULT now(),
  updated_at     timestamptz NOT NULL DEFAULT now(),
  UNIQUE (org_id, name),
  UNIQUE (org_id, id),
  CHECK (via_bastion_id IS DISTINCT FROM id),
  FOREIGN KEY (org_id, credential_id) REFERENCES credentials (org_id, id),
  FOREIGN KEY (org_id, via_bastion_id) REFERENCES bastions (org_id, id)
);

-- A zone's default jump host: the last step of the resolution chain host →
-- inventory group → zone → none.
ALTER TABLE zones ADD COLUMN bastion_id uuid;
ALTER TABLE zones ADD CONSTRAINT zones_bastion_fk FOREIGN KEY (org_id, bastion_id) REFERENCES bastions (org_id, id);

-- SSH settings of a managed host. No row means the defaults: port 22, no
-- explicit user or credential, jump host inherited. bastion_mode 'explicit'
-- names a bastion; 'none' forces a direct connection even when a group or
-- the zone names one.
CREATE TABLE host_ssh (
  host_id       uuid PRIMARY KEY,
  org_id        uuid NOT NULL,
  username      text NOT NULL DEFAULT '',
  port          integer NOT NULL DEFAULT 22 CHECK (port BETWEEN 1 AND 65535),
  credential_id uuid,
  bastion_mode  text NOT NULL DEFAULT 'inherit' CHECK (bastion_mode IN ('inherit', 'none', 'explicit')),
  bastion_id    uuid,
  updated_at    timestamptz NOT NULL DEFAULT now(),
  CHECK ((bastion_mode = 'explicit') = (bastion_id IS NOT NULL)),
  FOREIGN KEY (org_id, host_id) REFERENCES hosts (org_id, id) ON DELETE CASCADE,
  FOREIGN KEY (org_id, credential_id) REFERENCES credentials (org_id, id),
  FOREIGN KEY (org_id, bastion_id) REFERENCES bastions (org_id, id)
);

ALTER TABLE bastions ENABLE ROW LEVEL SECURITY;
ALTER TABLE bastions FORCE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON bastions
  USING (org_id = NULLIF(current_setting('pulsix.org_id', true), '')::uuid);
CREATE POLICY org_isolation_bypass ON bastions
  USING (current_setting('pulsix.org_id', true) IS NULL OR current_setting('pulsix.org_id', true) = '');
ALTER TABLE host_ssh ENABLE ROW LEVEL SECURITY;
ALTER TABLE host_ssh FORCE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON host_ssh
  USING (org_id = NULLIF(current_setting('pulsix.org_id', true), '')::uuid);
CREATE POLICY org_isolation_bypass ON host_ssh
  USING (current_setting('pulsix.org_id', true) IS NULL OR current_setting('pulsix.org_id', true) = '');

-- +goose Down
DROP TABLE host_ssh;
ALTER TABLE zones DROP CONSTRAINT zones_bastion_fk;
ALTER TABLE zones DROP COLUMN bastion_id;
DROP TABLE bastions;
ALTER TABLE hosts DROP CONSTRAINT hosts_org_id_id_key;
