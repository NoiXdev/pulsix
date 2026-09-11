-- +goose Up
-- credentials is each organization's encrypted secret store (spec §4,
-- backlog 2.7): SSH keys and passwords for managed hosts and jump hosts, Git
-- deploy keys and tokens. secret holds the sealed payload
-- (crypto.Box.SealAAD with "credential:<org_id>:<id>" as additional data), so
-- a ciphertext copied into another row or organization no longer opens.
-- public_key is stored without a comment; the API composes
-- "pulsix:<org-slug>:<name>" on read, so a rename updates it. UNIQUE (org_id,
-- id) is the target of the composite foreign keys that bastions and host_ssh
-- use to reference a credential of their own organization only.
CREATE TABLE credentials (
  id          uuid PRIMARY KEY,
  org_id      uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  name        text NOT NULL,
  kind        text NOT NULL CHECK (kind IN ('ssh_key', 'ssh_password', 'git_key', 'git_token')),
  username    text NOT NULL DEFAULT '',
  public_key  text NOT NULL DEFAULT '',
  fingerprint text NOT NULL DEFAULT '',
  key_type    text NOT NULL DEFAULT '',
  secret      bytea NOT NULL,
  expires_at  timestamptz,
  created_by  uuid REFERENCES users(id) ON DELETE SET NULL,
  created_at  timestamptz NOT NULL DEFAULT now(),
  updated_at  timestamptz NOT NULL DEFAULT now(),
  UNIQUE (org_id, name),
  UNIQUE (org_id, id)
);
ALTER TABLE credentials ENABLE ROW LEVEL SECURITY;
ALTER TABLE credentials FORCE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON credentials
  USING (org_id = NULLIF(current_setting('pulsix.org_id', true), '')::uuid);
CREATE POLICY org_isolation_bypass ON credentials
  USING (current_setting('pulsix.org_id', true) IS NULL OR current_setting('pulsix.org_id', true) = '');

-- +goose Down
DROP TABLE credentials;
