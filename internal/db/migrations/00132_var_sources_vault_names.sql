-- +goose Up
-- The names of the repository's vault-encrypted variables (backlog 2.134):
-- what the last read found encrypted with Ansible Vault, as the worker
-- reports it (varsource.Vault: {names: [{name, file}], encrypted_files,
-- unread, cut}), never a value. A run request's extra var named like one
-- is refused (runrequests.checkSecretShadows). NULL means the names are
-- unknown: a record from before this column, or a read by a worker that
-- does not report them; never "none". Nullable and without a default, the
-- ADD COLUMN touches only the catalog.
ALTER TABLE repo_var_sources ADD COLUMN vault_names jsonb;

-- +goose Down
-- Development databases only (docs/upgrades.md: migrations only go up).
ALTER TABLE repo_var_sources DROP COLUMN vault_names;
