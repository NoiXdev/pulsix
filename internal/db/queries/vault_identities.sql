-- Task 2, spec docs/superpowers/specs/2026-10-19-v0.2e-multiple-vault-
-- passwords-design.md: read and replace a repository's or a job template's
-- labelled vault credential set (migration 00059's two tables). Modelled on
-- internal/db/queries/alert_routes.sql's own replace-a-set shape and
-- comment style throughout.

-- name: ListRepoVaultCredentials :many
-- One repository's labelled vault passwords, in label order — the same
-- order ResolveVaultIdentities (internal/repos/vault.go) itself produces,
-- so a caller reading straight off this query already sees a stable list
-- before it is ever unioned with a template's own set. That parity needs
-- COLLATE "C": Postgres's default collation orders by the database's
-- locale, Go's `<` orders by byte value, and the two disagree on mixed-case
-- labels ("Prod" vs "prod") — without pinning this, the list a client reads
-- here and the list a run resolves could be ordered differently for the
-- same set, even though the generated ansible.cfg (always Go-sorted) is
-- unaffected either way.
SELECT credential_id, label FROM git_repo_vault_credentials
WHERE org_id = @org_id AND repo_id = @repo_id
ORDER BY label COLLATE "C";

-- name: DeleteRepoVaultCredentials :exec
-- The first half of replacing a repository's set; InsertRepoVaultCredentials
-- is the second. Two statements, never one data-modifying WITH — a CTE's
-- statement order is undefined in Postgres
-- (https://www.postgresql.org/docs/current/queries-with.html#QUERIES-WITH-MODIFYING),
-- the exact mistake DeleteRouteSeverities' own comment (alert_routes.sql)
-- describes losing a row to in v0.4b. The caller's own replace call
-- (repos.SQLStore.ReplaceRepoVaultIdentities, store_sql.go) runs both inside
-- one transaction (932c009) precisely so this DELETE cannot commit before
-- that INSERT runs — a failing insert now rolls the delete back with it
-- instead of leaving the set wiped to empty.
DELETE FROM git_repo_vault_credentials WHERE org_id = @org_id AND repo_id = @repo_id;

-- name: InsertRepoVaultCredentials :exec
-- InsertCheckItems' own two-unnests-in-lockstep shape (items.sql): the
-- caller builds credential_ids and labels from the same slice in one pass,
-- so the two arrays are always the same length and the lockstep never pads
-- a shorter one with NULLs. Two empty arrays are a legal no-op — "replace
-- with zero vault passwords" is exactly what ReplaceRepoVaultIdentities
-- means when the slice it is given is empty.
--
-- No ON CONFLICT: PRIMARY KEY (repo_id, label) is what makes "one password
-- per label" a schema fact (the spec's own words) rather than a service
-- rule. A label repeated within one call — which the caller should never
-- produce, since it builds this from a single org's already-labelled set —
-- is therefore a genuine bug, and a unique violation surfacing it loudly is
-- the correct outcome, not something to absorb with DO NOTHING the way
-- InsertRouteSeverities absorbs a value that is allowed to repeat with no
-- consequence.
INSERT INTO git_repo_vault_credentials (org_id, repo_id, credential_id, label)
SELECT @org_id, @repo_id, unnest(@credential_ids::uuid[]), unnest(@labels::text[]);

-- name: ListTemplateVaultCredentials :many
-- ListRepoVaultCredentials' own shape, own COLLATE "C" and own reason, for
-- a job template's own set (migration 00059's second table).
SELECT credential_id, label FROM job_template_vault_credentials
WHERE org_id = @org_id AND template_id = @template_id
ORDER BY label COLLATE "C";

-- name: DeleteTemplateVaultCredentials :exec
-- DeleteRepoVaultCredentials' own shape and own reason, for the template's
-- set.
DELETE FROM job_template_vault_credentials WHERE org_id = @org_id AND template_id = @template_id;

-- name: InsertTemplateVaultCredentials :exec
-- InsertRepoVaultCredentials' own shape and own reason, for the template's
-- set.
INSERT INTO job_template_vault_credentials (org_id, template_id, credential_id, label)
SELECT @org_id, @template_id, unnest(@credential_ids::uuid[]), unnest(@labels::text[]);
