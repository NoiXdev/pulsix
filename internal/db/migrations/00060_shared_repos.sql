-- +goose Up
-- shared_repos is a playbook repository the INSTALLATION owns, projected
-- into every organization as an ordinary git_repos row (spec decision 1).
-- No org_id, no RLS and no policy pair: this is installation-level data,
-- the posture instance_settings has had since 00009, and access is a
-- service decision on Principal.InstanceAdmin rather than a database one.
-- It deliberately carries no credential — the projection supplies the
-- organization's own (spec decision 3), so a definition holds no secret
-- by construction rather than by policy.
CREATE TABLE shared_repos (
  id         uuid PRIMARY KEY,
  name       text NOT NULL UNIQUE,
  url        text NOT NULL,
  branch     text NOT NULL,
  subpath    text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

-- shared_repo_id non-null means this git_repos row is a projection of the
-- definition above: its url, branch and subpath are the definition's and
-- read-only inside the organization, while credential_id and the vault
-- identity set remain the organization's own.
--
-- Single-column on purpose, and this is the one place in the schema where
-- that is right: every other foreign key out of an org-scoped table is
-- composite (org_id, id) because both ends are tenant data. shared_repos
-- has no org_id to compose with — it is not tenant data — so composing is
-- not available, and nothing is weakened by its absence because the
-- parent is visible to every organization by design.
--
-- ON DELETE RESTRICT, not CASCADE: deleting a definition out from under a
-- customer's schedules is what spec decision 5 refuses. The service
-- reports that as ErrInUse before the database ever has to.
ALTER TABLE git_repos
  ADD COLUMN shared_repo_id uuid REFERENCES shared_repos (id) ON DELETE RESTRICT;

-- +goose Down
ALTER TABLE git_repos DROP COLUMN shared_repo_id;
DROP TABLE shared_repos;
