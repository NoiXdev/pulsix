-- +goose Up
ALTER TABLE users ADD COLUMN disabled_at timestamptz;
CREATE TABLE invites (
  id          uuid PRIMARY KEY,
  org_id      uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  email       citext NOT NULL,
  role        org_role NOT NULL,
  token_hash  bytea NOT NULL UNIQUE,
  invited_by  uuid NOT NULL REFERENCES users(id),
  expires_at  timestamptz NOT NULL,
  accepted_at timestamptz,
  created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX invites_org_idx ON invites(org_id);
ALTER TABLE invites ENABLE ROW LEVEL SECURITY;
ALTER TABLE invites FORCE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON invites USING (org_id = current_setting('pulsix.org_id', true)::uuid);
CREATE POLICY org_isolation_bypass ON invites USING (current_setting('pulsix.org_id', true) IS NULL OR current_setting('pulsix.org_id', true) = '');
-- +goose Down
DROP TABLE invites;
ALTER TABLE users DROP COLUMN disabled_at;
