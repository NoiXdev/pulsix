-- +goose Up
-- A fifth credential kind, and the repository field that names one.
--
-- Ansible Vault was the one secret Pulsix could hold nowhere. The worker
-- generates its own ansible.cfg for every run and points ANSIBLE_CONFIG
-- at it, so a vault_password_file committed to a repository's own config
-- is ignored; nothing ever set ANSIBLE_VAULT_PASSWORD_FILE; and the
-- runner never passed --vault-password-file. Meanwhile
-- internal/ansiblevars already lets a variable's VALUE carry Ansible's
-- own {"__ansible_vault": "..."} marker through — so an encrypted blob
-- reached Ansible and Ansible could not decrypt it. All three doors
-- were shut and one window was open.
--
-- The kind belongs in credentials rather than in a store of its own:
-- sealing, rotation, the expiry warning, the usage count and the whole
-- GUI already exist there, and 00013's own comment ("Channel secrets
-- arrive with alerting") shows the list was always meant to grow.
ALTER TABLE credentials DROP CONSTRAINT credentials_kind_check;
ALTER TABLE credentials
  ADD CONSTRAINT credentials_kind_check
  CHECK (kind IN ('ssh_key', 'ssh_password', 'git_key', 'git_token', 'vault_password'));

-- The vault password hangs off the REPOSITORY, not off the job template
-- that Semaphore would use. The encrypted files live in the repository,
-- so that is where the password belongs: ten templates over one
-- repository would otherwise need the same assignment ten times, and the
-- eleventh would be forgotten. The cost, recorded here because it is the
-- known limit: one repository, one vault password. Ansible supports
-- several vault ids for several environments; expressing that would make
-- this a list rather than a column.
--
-- The foreign key is composite and deliberately carries no ON DELETE, in
-- step with credential_id beside it: a credential a repository still
-- names cannot be deleted out from under it.
ALTER TABLE git_repos ADD COLUMN vault_credential_id uuid;
ALTER TABLE git_repos
  ADD CONSTRAINT git_repos_vault_credential_fkey
  FOREIGN KEY (org_id, vault_credential_id) REFERENCES credentials (org_id, id);

-- +goose Down
ALTER TABLE git_repos DROP CONSTRAINT git_repos_vault_credential_fkey;
ALTER TABLE git_repos DROP COLUMN vault_credential_id;
ALTER TABLE credentials DROP CONSTRAINT credentials_kind_check;
ALTER TABLE credentials
  ADD CONSTRAINT credentials_kind_check
  CHECK (kind IN ('ssh_key', 'ssh_password', 'git_key', 'git_token'));
