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
-- The status filter is the effective status (backlog 2.120), applied before
-- the cap so decided rows never hide a pending one: "expired" is a pending
-- row at or past expires_at on the caller's clock (now), "pending" one
-- before it. A NULL status lists every row.
SELECT * FROM run_requests
WHERE org_id = sqlc.arg(org_id)
  AND (sqlc.narg(status)::text IS NULL OR sqlc.narg(status)::text = CASE
    WHEN status = 'pending' AND expires_at <= sqlc.arg(now)::timestamptz THEN 'expired' ELSE status END)
ORDER BY created_at DESC, id DESC LIMIT 500;

-- name: CountPendingRunRequests :one
-- The pending, unexpired requests of the organization (the approval badge)
-- or, when token is given, of that one token (the per-token cap).
SELECT count(*)::int AS n FROM run_requests
WHERE org_id = sqlc.arg(org_id) AND status = 'pending' AND expires_at > sqlc.arg(now)::timestamptz
  AND (sqlc.narg(token)::uuid IS NULL OR requested_by_token = sqlc.narg(token)::uuid);

-- name: ListRunRequestTokens :many
-- The tokens that made requests, by id (backlog 2.120): the name and whether
-- the token can still ask here. requested_by_token has no foreign key, so a
-- deleted token is simply missing. An organization token is confined to
-- the organization (row-level security and org_id); a personal token
-- (backlog 3.100) reads as revoked here when it is revoked, its grant for
-- this organization is gone or blocked (its owner's block included), or the
-- organization no longer allows personal tokens. A deactivated owner stops
-- both kinds. These are the sign-in rules of GetAPITokenByHash (tokens.sql),
-- GetPersonalTokenByHash, GetPersonalTokenGrant and PersonalTokensAllowed
-- (personal_tokens.sql): change them together. Columns are named, never
-- t.*: secret_hash is not read.
SELECT t.id, t.name, (t.revoked_at IS NOT NULL OR u.disabled_at IS NOT NULL)::boolean AS revoked, t.expires_at
FROM api_tokens t JOIN users u ON u.id = t.user_id
WHERE t.org_id = sqlc.arg(org_id)::uuid AND t.id = ANY(sqlc.arg(ids)::uuid[])
UNION ALL
SELECT t.id, t.name, (
  t.revoked_at IS NOT NULL OR u.disabled_at IS NOT NULL OR g.token_id IS NULL OR g.blocked_at IS NOT NULL
  OR EXISTS (SELECT 1 FROM personal_token_grants b
             WHERE b.org_id = sqlc.arg(org_id)::uuid AND b.user_id = t.user_id AND b.blocked_at IS NOT NULL)
  OR NOT COALESCE((SELECT s.personal_tokens_allowed FROM org_token_settings s WHERE s.org_id = sqlc.arg(org_id)::uuid), true)
)::boolean AS revoked, t.expires_at
FROM personal_tokens t JOIN users u ON u.id = t.user_id
LEFT JOIN personal_token_grants g ON g.token_id = t.id AND g.org_id = sqlc.arg(org_id)::uuid
WHERE t.id = ANY(sqlc.arg(ids)::uuid[]);

-- name: DecideRunRequest :execrows
-- Only a pending row moves; zero rows means it was already decided. An
-- approval records the fingerprint the person approved and takes the
-- request's own id as the run group, before anything is enqueued, so a
-- claim of one of its jobs always finds it (migration 00103).
UPDATE run_requests SET status = sqlc.arg(status), decided_by = sqlc.arg(decided_by), decided_at = sqlc.arg(decided_at),
  approved_fingerprint = sqlc.narg(approved_fingerprint),
  run_group_id = CASE WHEN sqlc.arg(status)::text = 'approved' THEN id ELSE run_group_id END
WHERE org_id = sqlc.arg(org_id)::uuid AND id = sqlc.arg(id) AND status = 'pending';

-- name: RevertRunRequest :exec
-- A failed enqueue puts the request back to pending. run_group_id stays:
-- jobs the enqueue did create keep pointing at this request, and its
-- claim guard refuses them while the request is not approved.
UPDATE run_requests SET status = 'pending', decided_by = NULL, decided_at = NULL, approved_fingerprint = NULL
WHERE org_id = $1 AND id = $2;

-- name: GetRunRequestByRunGroup :one
SELECT * FROM run_requests WHERE org_id = $1 AND run_group_id = $2;
