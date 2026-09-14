-- name: CreateRepo :one
INSERT INTO git_repos (id, org_id, name, url, branch, subpath, credential_id)
VALUES ($1, $2, $3, $4, $5, $6, $7)
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
-- constraint is the name-clash guard the store maps to ErrNameTaken.
UPDATE git_repos
SET name = @name, url = @url, branch = @branch, subpath = @subpath,
    credential_id = @credential_id, updated_at = now()
WHERE org_id = @org_id AND id = @id
RETURNING *;

-- name: DeleteRepo :execrows
-- No cascade on purpose: a playbook still referencing the repository turns
-- this into a foreign-key violation the store maps to ErrInUse.
DELETE FROM git_repos WHERE org_id = $1 AND id = $2;
