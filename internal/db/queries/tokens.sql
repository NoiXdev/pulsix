-- name: InsertAPIToken :exec
INSERT INTO api_tokens (id, user_id, org_id, name, role, secret_hash, expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7);

-- name: GetAPITokenByHash :one
-- Columns are named explicitly (not t.*) so secret_hash is never scanned
-- into Go here: the generated row struct would otherwise carry a
-- json:"secret_hash" field one careless marshal away from a leak. now is
-- the caller-supplied clock (see auth.Tokens' injected clock), not the
-- database's own now(), so expiry is evaluated against the same time the
-- rest of Resolve reasons about.
-- u.disabled_at IS NULL is the same deactivation backstop GetSession
-- carries in auth.sql (Finding 2, Task 10 fix round 1): a bearer token
-- minted by a since-disabled user must stop resolving here too, not just
-- get revoked at deactivation time.
SELECT t.id, t.user_id, t.org_id, t.name, t.role, t.expires_at, t.last_used_at, t.revoked_at, t.created_at, u.instance_admin
FROM api_tokens t JOIN users u ON u.id = t.user_id
WHERE t.secret_hash = $1 AND t.revoked_at IS NULL AND (t.expires_at IS NULL OR t.expires_at > sqlc.arg(now)::timestamptz) AND u.disabled_at IS NULL;

-- name: ListAPITokensForOrg :many
-- Columns are named explicitly for the same reason as GetAPITokenByHash
-- above: secret_hash must never be scanned into a Go struct here.
SELECT id, user_id, org_id, name, role, expires_at, last_used_at, revoked_at, created_at
FROM api_tokens WHERE org_id = $1 AND revoked_at IS NULL ORDER BY created_at;

-- name: RevokeAPIToken :execrows
UPDATE api_tokens SET revoked_at = now() WHERE id = $1 AND org_id = $2 AND revoked_at IS NULL;

-- name: TouchAPIToken :exec
-- Backlog 1.22: resolves touch last_used_at on every bearer-authenticated
-- request, so the write is throttled to at most once per minute per token
-- right here in the statement rather than with in-process state: the
-- predicate reads the database's own now(), so it holds identically across
-- managers and needs no synchronization. last_used_at IS NULL admits the
-- token's very first touch; afterwards a row only matches once its
-- last_used_at has aged past the window, so repeated resolves inside the
-- minute affect zero rows. A zero-row outcome is success (the touch was
-- throttled), not an error — see SQLTokenStore.TouchLastUsed.
UPDATE api_tokens SET last_used_at = $2
WHERE id = $1 AND (last_used_at IS NULL OR last_used_at < now() - interval '1 minute');

-- name: APITokenUsable :one
-- Whether the token id is still usable as of now — GetAPITokenByHash's own
-- filter, by id. A long-running request (the job log stream) re-checks the
-- token it was authorised with this way, so a revocation, an expiry or the
-- owner's deactivation ends it (backlog 7.22).
SELECT EXISTS (
  SELECT 1 FROM api_tokens t JOIN users u ON u.id = t.user_id
  WHERE t.id = sqlc.arg(id)::uuid AND t.revoked_at IS NULL
    AND (t.expires_at IS NULL OR t.expires_at > sqlc.arg(now)::timestamptz) AND u.disabled_at IS NULL
);
