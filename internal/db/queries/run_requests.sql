-- Run requests (spec 2026-11-16 assistant access). Org-scoped like every
-- store query; each statement also names org_id.

-- name: InsertRunRequest :exec
INSERT INTO run_requests (id, org_id, kind, template_id, template_revision, host_id, job_id, reason,
  requested_by_user, requested_by_token, status, expires_at, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, 'pending', $11, $12);

-- name: GetRunRequest :one
SELECT * FROM run_requests WHERE org_id = $1 AND id = $2;

-- name: ListRunRequests :many
-- Newest first, at most the 500 most recent: a list view, not an archive.
SELECT * FROM run_requests WHERE org_id = $1 ORDER BY created_at DESC, id DESC LIMIT 500;

-- name: DecideRunRequest :execrows
-- Only a pending row moves; zero rows means it was already decided.
UPDATE run_requests SET status = sqlc.arg(status), decided_by = sqlc.arg(decided_by), decided_at = sqlc.arg(decided_at)
WHERE org_id = sqlc.arg(org_id) AND id = sqlc.arg(id) AND status = 'pending';

-- name: SetRunRequestRunGroup :exec
UPDATE run_requests SET run_group_id = $3 WHERE org_id = $1 AND id = $2;

-- name: RevertRunRequest :exec
UPDATE run_requests SET status = 'pending', decided_by = NULL, decided_at = NULL, run_group_id = NULL
WHERE org_id = $1 AND id = $2;

-- name: GetRunRequestByRunGroup :one
SELECT * FROM run_requests WHERE org_id = $1 AND run_group_id = $2;
