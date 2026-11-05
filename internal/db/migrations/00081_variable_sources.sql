-- +goose Up
-- The central variable editor (backlog 7.75). A repo sync reads every
-- playbook's declared variables (internal/varsource) and the manager keeps
-- what it read: repo_var_sources is one row per repository (the commit the
-- read saw, when, and the notes about the repository itself),
-- repo_playbook_sources one row per discovered playbook with the parse
-- result. worker_reads is false when the last sync came from a worker that
-- reads no sources (one minor older, docs/upgrades.md): the previous read
-- is kept, only flagged. left_out names the playbooks a cap left unread;
-- they keep their previous rows.
CREATE TABLE repo_var_sources (
  repo_id      uuid PRIMARY KEY,
  org_id       uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  commit_sha   text NOT NULL DEFAULT '',
  synced_at    timestamptz NOT NULL,
  worker_reads boolean NOT NULL,
  incomplete   boolean NOT NULL DEFAULT false,
  left_out     jsonb NOT NULL DEFAULT '[]',
  notes        jsonb NOT NULL DEFAULT '[]',
  FOREIGN KEY (org_id, repo_id) REFERENCES git_repos (org_id, id) ON DELETE CASCADE
);

ALTER TABLE repo_var_sources ENABLE ROW LEVEL SECURITY;
ALTER TABLE repo_var_sources FORCE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON repo_var_sources
  USING (org_id = NULLIF(current_setting('pulsix.org_id', true), '')::uuid);
CREATE POLICY org_isolation_bypass ON repo_var_sources
  USING (current_setting('pulsix.org_id', true) IS NULL OR current_setting('pulsix.org_id', true) = '');

CREATE TABLE repo_playbook_sources (
  org_id  uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  repo_id uuid NOT NULL,
  path    text NOT NULL,
  result  jsonb NOT NULL,
  PRIMARY KEY (repo_id, path),
  FOREIGN KEY (org_id, repo_id) REFERENCES git_repos (org_id, id) ON DELETE CASCADE
);

ALTER TABLE repo_playbook_sources ENABLE ROW LEVEL SECURITY;
ALTER TABLE repo_playbook_sources FORCE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON repo_playbook_sources
  USING (org_id = NULLIF(current_setting('pulsix.org_id', true), '')::uuid);
CREATE POLICY org_isolation_bypass ON repo_playbook_sources
  USING (current_setting('pulsix.org_id', true) IS NULL OR current_setting('pulsix.org_id', true) = '');

-- An inline playbook's own parse result (NULL until it is saved again).
ALTER TABLE playbooks ADD COLUMN var_source jsonb;

-- A template's variables: revision drives If-Match on the variables page,
-- vars_mode how sources reach the template, vars_source_rev the commit (or
-- inline revision) last applied and vars_applied_at when.
ALTER TABLE job_templates
  ADD COLUMN revision        bigint NOT NULL DEFAULT 1,
  ADD COLUMN vars_mode       text NOT NULL DEFAULT 'off' CHECK (vars_mode IN ('off', 'manual', 'automatic')),
  ADD COLUMN vars_source_rev text NOT NULL DEFAULT '',
  ADD COLUMN vars_applied_at timestamptz;

-- +goose Down
ALTER TABLE job_templates
  DROP COLUMN vars_applied_at,
  DROP COLUMN vars_source_rev,
  DROP COLUMN vars_mode,
  DROP COLUMN revision;
ALTER TABLE playbooks DROP COLUMN var_source;
DROP TABLE repo_playbook_sources;
DROP TABLE repo_var_sources;
