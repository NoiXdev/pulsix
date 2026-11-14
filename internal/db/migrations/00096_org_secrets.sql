-- +goose Up
-- Secrets on the manager (backlog 3.13d): named sets of fields an
-- organization stores for the checks the Pulsix server runs itself (the
-- first is snmp, whose community or user credentials live here). A secret
-- of the same name in a host's agent.yaml is a different thing: agent-run
-- checks read that file, never this table.
--
-- fields_sealed is the JSON object of the fields ({"community": "…"}),
-- sealed with the manager's box (crypto.Box.SealAAD) under the additional
-- data org_secrets|<org_id>|<name>, so a row's ciphertext cannot be moved
-- to another organization or another name and still open. field_names is
-- the sorted, clear list of the object's keys, for the GUI; no read path
-- returns a value. Names follow the agent.yaml secret-name pattern
-- (checkplugins.FormatSecretName); the service validates names, fields and
-- values, the CHECKs below only bound what a broken caller could store.
--
-- A backup carries fields_sealed as it is, like every other ciphertext
-- the master key seals.
--
-- The row-level security pair is the standard form (00091):
-- org_isolation admits only the bound organization, org_isolation_bypass
-- applies when none is bound. Every reader binds one: the API binds the
-- request's organization and the server's probe runner the host's, so a
-- probe reads only its own organization's secrets.
CREATE TABLE org_secrets (
  org_id        uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  name          text NOT NULL CHECK (name ~ '^[A-Za-z0-9_.-]{1,64}$'),
  fields_sealed bytea NOT NULL,
  field_names   text[] NOT NULL CHECK (cardinality(field_names) BETWEEN 1 AND 16),
  created_at    timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (org_id, name)
);

ALTER TABLE org_secrets ENABLE ROW LEVEL SECURITY;
ALTER TABLE org_secrets FORCE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON org_secrets
  USING (org_id = NULLIF(current_setting('pulsix.org_id', true), '')::uuid);
CREATE POLICY org_isolation_bypass ON org_secrets
  USING (current_setting('pulsix.org_id', true) IS NULL OR current_setting('pulsix.org_id', true) = '');

-- +goose Down
-- Development databases only (docs/upgrades.md: migrations only go up).
DROP TABLE org_secrets;
