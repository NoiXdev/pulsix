-- +goose Up
-- Personal API and assistant tokens (backlog 3.100, spec docs/superpowers/
-- specs/2026-11-17-personal-tokens-design.md). A personal token belongs to a
-- user, not to an organization: it acts only in the organizations granted on
-- it, each with its own role cap, and never above the user's current
-- membership role there.
--
-- personal_tokens carries no row-level security: a row belongs to a user,
-- like sessions, and has no organization to be confined to. The bearer
-- lookup by secret hash runs before any organization is known (one indexed
-- read on secret_hash), and an organization's view of personal tokens goes
-- through personal_token_grants, which row-level security confines to that
-- organization's own grants.
CREATE TABLE personal_tokens (
  id           uuid PRIMARY KEY,
  user_id      uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name         text NOT NULL,
  secret_hash  bytea NOT NULL UNIQUE,
  kind         text NOT NULL DEFAULT 'api' CHECK (kind IN ('api', 'assistant')),
  areas        text[],
  expires_at   timestamptz,
  last_used_at timestamptz,
  revoked_at   timestamptz,
  created_at   timestamptz NOT NULL DEFAULT now(),
  -- The grants' composite foreign key needs (id, user_id) to be unique; it
  -- is what keeps a grant's user the token's own user.
  UNIQUE (id, user_id),
  -- The same shapes as api_tokens_kind_areas (00099): an assistant token
  -- always has at least one area, an api token never has any.
  CONSTRAINT personal_tokens_kind_areas CHECK (
    (kind = 'api' AND areas IS NULL) OR
    (kind = 'assistant' AND areas IS NOT NULL AND cardinality(areas) > 0)
  )
);
CREATE INDEX personal_tokens_user_idx ON personal_tokens (user_id) WHERE revoked_at IS NULL;

-- One row per (token, organization): the organization the token may act in
-- and the highest role it may act with there. blocked_at/blocked_by record an
-- organization admin's revocation of the grant, which sticks: the owner
-- cannot add the organization again until an admin lifts the block.
--
-- The foreign key to org_memberships is what deletes a grant when the
-- membership ends (a member removed or leaving), so rejoining never revives
-- one; the foreign key to personal_tokens keeps the grant's user the token's
-- own user.
CREATE TABLE personal_token_grants (
  token_id     uuid NOT NULL,
  user_id      uuid NOT NULL,
  org_id       uuid NOT NULL,
  role         org_role NOT NULL,
  blocked_at   timestamptz,
  blocked_by   uuid REFERENCES users(id) ON DELETE SET NULL,
  last_used_at timestamptz,
  created_at   timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (token_id, org_id),
  FOREIGN KEY (token_id, user_id) REFERENCES personal_tokens (id, user_id) ON DELETE CASCADE,
  FOREIGN KEY (org_id, user_id) REFERENCES org_memberships (org_id, user_id) ON DELETE CASCADE
);
CREATE INDEX personal_token_grants_org_idx ON personal_token_grants (org_id);

ALTER TABLE personal_token_grants ENABLE ROW LEVEL SECURITY;
ALTER TABLE personal_token_grants FORCE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON personal_token_grants
  USING (org_id = NULLIF(current_setting('pulsix.org_id', true), '')::uuid);
CREATE POLICY org_isolation_bypass ON personal_token_grants
  USING (current_setting('pulsix.org_id', true) IS NULL OR current_setting('pulsix.org_id', true) = '');

-- The per-organization switch "Allow personal tokens". No row means allowed:
-- the switch is on by default. Off, existing grants stop working there at
-- once and no new grant can be added.
CREATE TABLE org_token_settings (
  org_id                  uuid PRIMARY KEY REFERENCES organizations(id) ON DELETE CASCADE,
  personal_tokens_allowed boolean NOT NULL DEFAULT true,
  updated_at              timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE org_token_settings ENABLE ROW LEVEL SECURITY;
ALTER TABLE org_token_settings FORCE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON org_token_settings
  USING (org_id = NULLIF(current_setting('pulsix.org_id', true), '')::uuid);
CREATE POLICY org_isolation_bypass ON org_token_settings
  USING (current_setting('pulsix.org_id', true) IS NULL OR current_setting('pulsix.org_id', true) = '');

-- +goose Down
DROP TABLE org_token_settings;
DROP TABLE personal_token_grants;
DROP TABLE personal_tokens;
