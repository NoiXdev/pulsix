-- +goose Up
-- v0.2e (backlog 2.89, spec docs/superpowers/specs/2026-10-19-v0.2e-multiple-
-- vault-passwords-design.md): a repository can decrypt exactly one vault
-- today, because git_repos.vault_credential_id (00055) is a single nullable
-- column. Ansible has supported several vault passwords per play since 2.4
-- via --vault-id label@file / vault_identity_list, and real playbook
-- repositories use that: production and staging variables are deliberately
-- encrypted with different passwords. These two link tables replace the
-- single column with a labelled set, one per repository and one per job
-- template (spec decision 1 — a template may add passwords a shared
-- repository's other templates must not have).
--
-- The label lives on the LINK ROW, not on the credential: ansible-vault
-- encrypt --vault-id prod@... stamps the label into the encrypted file's own
-- header, so it is a property of the encrypted content, which can call the
-- same credential "prod" in one repository and "production" in another
-- (spec's "The label is not decoration"). PRIMARY KEY (parent, label) is
-- what makes "one password per label" a schema fact rather than a service
-- rule the way credentials_kind_check already is one row up — a credential
-- may appear under two different labels, but a label may not repeat.
CREATE TABLE git_repo_vault_credentials (
  org_id        uuid NOT NULL,
  repo_id       uuid NOT NULL,
  credential_id uuid NOT NULL,
  label         text NOT NULL,
  PRIMARY KEY (repo_id, label),
  -- Deliberate asymmetry (spec "Note the deliberate asymmetry"): the parent
  -- cascades because deleting a repository should take its links with it,
  -- but the credential carries no ON DELETE, exactly like
  -- git_repos.vault_credential_id (00055) beside it — a credential a
  -- repository still names cannot be deleted out from under it, which
  -- credentials' own store already maps to ErrInUse.
  FOREIGN KEY (org_id, repo_id)       REFERENCES git_repos (org_id, id)   ON DELETE CASCADE,
  FOREIGN KEY (org_id, credential_id) REFERENCES credentials (org_id, id)
);

CREATE TABLE job_template_vault_credentials (
  org_id        uuid NOT NULL,
  template_id   uuid NOT NULL,
  credential_id uuid NOT NULL,
  label         text NOT NULL,
  PRIMARY KEY (template_id, label),
  FOREIGN KEY (org_id, template_id)   REFERENCES job_templates (org_id, id) ON DELETE CASCADE,
  FOREIGN KEY (org_id, credential_id) REFERENCES credentials (org_id, id)
);

-- Backfill: every existing git_repos.vault_credential_id becomes exactly one
-- link row labelled 'default' — Ansible's own default vault id, not a made-
-- up descriptive name, so a file that was encrypted without an explicit
-- --vault-id (the only way anyone could use this feature before today)
-- keeps matching under the label Ansible already tries first, and behaviour
-- is unchanged for every installation that has one. job_templates never had
-- an equivalent column (00055 added the vault password only to git_repos),
-- so there is nothing to backfill there.
--
-- The column itself is left behind on purpose — migrations here are
-- additive only, and nothing reads git_repos.vault_credential_id after this
-- point. Dropping it is a separate backlog row, filed once a release has
-- shipped with nothing reading it.
INSERT INTO git_repo_vault_credentials (org_id, repo_id, credential_id, label)
SELECT org_id, id, vault_credential_id, 'default'
FROM git_repos
WHERE vault_credential_id IS NOT NULL;

-- +goose StatementBegin
DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['git_repo_vault_credentials', 'job_template_vault_credentials'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY org_isolation ON %I USING (org_id = NULLIF(current_setting(''pulsix.org_id'', true), '''')::uuid)', t);
    EXECUTE format('CREATE POLICY org_isolation_bypass ON %I USING (current_setting(''pulsix.org_id'', true) IS NULL OR current_setting(''pulsix.org_id'', true) = '''')', t);
  END LOOP;
END $$;
-- +goose StatementEnd

-- +goose Down
DROP TABLE job_template_vault_credentials;
DROP TABLE git_repo_vault_credentials;
