-- name: CreateRepo :one
INSERT INTO git_repos (id, org_id, name, url, branch, subpath, credential_id, vault_credential_id)
VALUES (@id, @org_id, @name, @url, @branch, @subpath, @credential_id, @vault_credential_id)
RETURNING *;

-- name: GetRepo :one
-- Scoped by org_id in the WHERE clause, not only relying on RLS (see
-- GetZone's comment in hosts.sql): a repository belonging to a different
-- organization must be invisible, not merely forbidden.
SELECT * FROM git_repos WHERE org_id = $1 AND id = $2;

-- name: ListRepos :many
SELECT * FROM git_repos WHERE org_id = $1 ORDER BY name;

-- name: UpdateRepo :one
-- Replaces every editable field, name included; the unique (org_id, name)
-- constraint is the name-clash guard the store maps to ErrNameTaken. The
-- git server key pin travels with the row (the store passes the current
-- values on; only the service decides when it changes — a URL change drops
-- the pin, the pin's own server is gone).
UPDATE git_repos
SET name = @name, url = @url, branch = @branch, subpath = @subpath,
    credential_id = @credential_id, vault_credential_id = @vault_credential_id,
    ssh_host_key = @ssh_host_key, ssh_host_public_key = @ssh_host_public_key,
    updated_at = now()
WHERE org_id = @org_id AND id = @id
RETURNING *;

-- name: UnpinRepoHostKey :execrows
-- Clears the git server key pin (the operator's re-TOFU escape hatch, the
-- repos mirror of ClearBastionKeyPin): the next successful sync pins
-- whatever the server then presents. Zero rows affected means the
-- repository does not exist in the organization (ErrNotFound).
UPDATE git_repos SET ssh_host_key = NULL, ssh_host_public_key = NULL, updated_at = now()
WHERE org_id = $1 AND id = $2;

-- name: DeleteRepo :execrows
-- No cascade on purpose: a playbook still referencing the repository turns
-- this into a foreign-key violation the store maps to ErrInUse.
DELETE FROM git_repos WHERE org_id = $1 AND id = $2;
