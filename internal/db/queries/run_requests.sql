-- Run requests (spec 2026-11-16 assistant access). Org-scoped like every
-- store query; each statement also names org_id.

-- name: InsertRunRequest :exec
-- A request is pending (expires_at set) or, made with sync_first, waiting
-- for its sync (expires_at NULL, sync_job_id set; migration 00124).
INSERT INTO run_requests (id, org_id, kind, template_id, template_revision, host_id, job_id, reason,
  requested_by_user, requested_by_token, status, expires_at, created_at, overrides, sync_job_id, expires_in_minutes)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16);

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
-- or, when token is given, of that one token (the per-token cap), which
-- also counts the token's requests waiting for their sync (migration
-- 00124): they become pending without asking again.
SELECT count(*)::int AS n FROM run_requests
WHERE org_id = sqlc.arg(org_id)
  AND ((status = 'pending' AND expires_at > sqlc.arg(now)::timestamptz)
    OR (sqlc.narg(token)::uuid IS NOT NULL AND status = 'waiting_for_sync'))
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
-- Only a pending row moves, and a rejection also moves one waiting for its
-- sync (whose expiry, never started, becomes the decision's time);
-- zero rows means it was already decided. An approval records the
-- fingerprint the person approved and takes the request's own id as the
-- run group, before anything is enqueued, so a claim of one of its jobs
-- always finds it (migration 00103), and the commit the run is pinned to
-- (migration 00123; NULL for none).
UPDATE run_requests SET status = sqlc.arg(status), decided_by = sqlc.arg(decided_by), decided_at = sqlc.arg(decided_at),
  approved_fingerprint = sqlc.narg(approved_fingerprint), pinned_commit = sqlc.narg(pinned_commit),
  run_group_id = CASE WHEN sqlc.arg(status)::text = 'approved' THEN id ELSE run_group_id END,
  expires_at = COALESCE(expires_at, sqlc.arg(decided_at))
WHERE org_id = sqlc.arg(org_id)::uuid AND id = sqlc.arg(id)
  AND (status = 'pending' OR (status = 'waiting_for_sync' AND sqlc.arg(status)::text = 'rejected'));

-- name: RevertRunRequest :exec
-- A failed enqueue puts the request back to pending. run_group_id stays:
-- jobs the enqueue did create keep pointing at this request, and its
-- claim guard refuses them while the request is not approved.
UPDATE run_requests SET status = 'pending', decided_by = NULL, decided_at = NULL, approved_fingerprint = NULL, pinned_commit = NULL
WHERE org_id = $1 AND id = $2;

-- name: GetRunRequestByRunGroup :one
SELECT * FROM run_requests WHERE org_id = $1 AND run_group_id = $2;

-- name: ListWaitingRunRequests :many
-- The requests waiting for their sync (migration 00124) with the sync
-- job's status and error, both NULL when the job is gone: those of one
-- sync job, or, without one, every one the caller sees (the manager's
-- sweep runs without an organization scope and sees all of them), the
-- oldest first.
SELECT r.org_id, r.id, r.created_at, r.expires_in_minutes, r.sync_job_id,
  j.status AS sync_status, j.error AS sync_error
FROM run_requests r
LEFT JOIN jobs j ON j.org_id = r.org_id AND j.id = r.sync_job_id
WHERE r.status = 'waiting_for_sync'
  AND (sqlc.narg(sync_job_id)::uuid IS NULL OR r.sync_job_id = sqlc.narg(sync_job_id)::uuid)
ORDER BY r.created_at, r.id
LIMIT 1000;

-- name: EndRunRequestWait :execrows
-- A waiting request becomes pending (its expiry starts) or failed (the
-- expiry is the failure's time, sync_error the reason); zero rows means it
-- no longer waits.
UPDATE run_requests SET status = sqlc.arg(status), expires_at = sqlc.arg(expires_at), sync_error = sqlc.narg(sync_error)
WHERE org_id = sqlc.arg(org_id) AND id = sqlc.arg(id) AND status = 'waiting_for_sync';
