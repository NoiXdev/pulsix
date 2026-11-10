-- name: CreatePlaybook :one
-- The source/repo_id/path/inline pairing is guarded twice: once here by the
-- service (which sets exactly the fields its source needs) and once by the
-- playbooks table's CHECK constraints — a mismatched insert must fail at
-- the database, not silently store a hybrid row. var_source is an editor
-- playbook's parse result (backlog 7.75), NULL for a repo playbook.
INSERT INTO playbooks (id, org_id, repo_id, path, inline, source, var_source)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: GetPlaybook :one
-- Scoped by org_id in the WHERE clause, not only relying on RLS (see
-- GetRepo's comment in repos.sql): a playbook belonging to a different
-- organization must be invisible, not merely forbidden.
SELECT * FROM playbooks WHERE org_id = $1 AND id = $2;

-- name: ListPlaybooks :many
-- Every column but var_source (backlog 7.114): an editor playbook's parse
-- result is up to 1 MiB and a list never shows it.
SELECT id, org_id, repo_id, path, inline, source, created_at, updated_at
FROM playbooks WHERE org_id = $1 ORDER BY created_at, id;

-- name: UpdatePlaybookInline :one
-- An editor playbook's inline text is its only editable field; its parse
-- result (var_source) is replaced with it. The source guard in the WHERE
-- clause refuses a repo playbook even if a caller skipped the service's
-- own check.
UPDATE playbooks SET inline = @inline, var_source = @var_source, updated_at = now()
WHERE org_id = @org_id AND id = @id AND source = 'editor'
RETURNING *;

-- name: FindRepoPlaybook :one
-- The playbook registered for a repository path. Nothing keeps a path
-- unique (no index exists and none can be added additively over rows that
-- may already repeat one), so the oldest row is the one reused.
SELECT * FROM playbooks
WHERE org_id = @org_id AND repo_id = @repo_id AND path = @path
ORDER BY created_at, id
LIMIT 1;

-- name: ListRepoPlaybookPaths :many
-- The playbooks registered for a repository, oldest first, without their
-- contents: the discovered-playbook list marks the paths they name.
SELECT id, path FROM playbooks
WHERE org_id = @org_id AND repo_id = @repo_id
ORDER BY created_at, id;

-- name: DeletePlaybook :execrows
-- A job template still referencing the playbook is a foreign-key violation
-- the store maps to ErrInUse — no cascade, on purpose.
DELETE FROM playbooks WHERE org_id = $1 AND id = $2;
