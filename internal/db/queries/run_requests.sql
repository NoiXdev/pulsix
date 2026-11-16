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
-- Only a pending row moves; zero rows means it was already decided. An
-- approval records the fingerprint the person approved and takes the
-- request's own id as the run group, before anything is enqueued, so a
-- claim of one of its jobs always finds it (migration 00103).
UPDATE run_requests SET status = sqlc.arg(status), decided_by = sqlc.arg(decided_by), decided_at = sqlc.arg(decided_at),
  approved_fingerprint = sqlc.narg(approved_fingerprint),
  run_group_id = CASE WHEN sqlc.arg(status)::text = 'approved' THEN id ELSE run_group_id END
WHERE org_id = sqlc.arg(org_id) AND id = sqlc.arg(id) AND status = 'pending';

-- name: RevertRunRequest :exec
-- A failed enqueue puts the request back to pending. run_group_id stays:
-- jobs the enqueue did create keep pointing at this request, and its
-- claim guard refuses them while the request is not approved.
UPDATE run_requests SET status = 'pending', decided_by = NULL, decided_at = NULL, approved_fingerprint = NULL
WHERE org_id = $1 AND id = $2;

-- name: GetRunRequestByRunGroup :one
SELECT * FROM run_requests WHERE org_id = $1 AND run_group_id = $2;
