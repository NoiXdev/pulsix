-- Personal tokens (backlog 3.100). Every statement names its columns
-- explicitly, never t.*, so secret_hash is never scanned into a Go struct.

-- name: InsertPersonalToken :exec
INSERT INTO personal_tokens (id, user_id, name, secret_hash, kind, areas, expires_at, created_at)
VALUES ($1, $2, $3, $4, COALESCE(NULLIF(sqlc.arg(kind)::text, ''), 'api'), sqlc.narg(areas)::text[], $5, $6);

-- name: InsertPersonalTokenGrant :exec
INSERT INTO personal_token_grants (token_id, user_id, org_id, role) VALUES ($1, $2, $3, $4);

-- name: GetPersonalTokenByHash :one
-- The bearer lookup: one indexed read on secret_hash, before any
-- organization is known. now is the caller's clock, as in GetAPITokenByHash.
-- A deactivated owner's token stops resolving here too (the same backstop
-- GetAPITokenByHash carries). users.instance_admin is deliberately not read:
-- a personal token never acts as an instance administrator.
SELECT t.id, t.user_id, t.name, t.kind, t.areas, t.expires_at, t.last_used_at, t.created_at
FROM personal_tokens t JOIN users u ON u.id = t.user_id
WHERE t.secret_hash = $1 AND t.revoked_at IS NULL
  AND (t.expires_at IS NULL OR t.expires_at > sqlc.arg(now)::timestamptz) AND u.disabled_at IS NULL;

-- name: PersonalTokenUsable :one
-- GetPersonalTokenByHash's filter, by id: a long-running request (the job
-- log stream) re-checks the token it was authorised with this way.
SELECT EXISTS (
  SELECT 1 FROM personal_tokens t JOIN users u ON u.id = t.user_id
  WHERE t.id = sqlc.arg(id)::uuid AND t.revoked_at IS NULL
    AND (t.expires_at IS NULL OR t.expires_at > sqlc.arg(now)::timestamptz) AND u.disabled_at IS NULL
);

-- name: TouchPersonalToken :exec
-- At most one write per minute per token, decided and stamped by the
-- database's own now() (TouchAPIToken's throttle, without a Go timestamp).
UPDATE personal_tokens SET last_used_at = now()
WHERE id = $1 AND (last_used_at IS NULL OR last_used_at < now() - interval '1 minute');

-- name: GetPersonalTokenGrant :one
-- The grant of token_id for org_id, blocked or not. Under an organization's
-- scope row-level security admits only that organization's grants.
SELECT role, (blocked_at IS NOT NULL)::boolean AS blocked
FROM personal_token_grants WHERE token_id = $1 AND org_id = $2;

-- name: TouchPersonalTokenGrant :exec
-- TouchPersonalToken's throttle for one grant.
UPDATE personal_token_grants SET last_used_at = now()
WHERE token_id = $1 AND org_id = $2 AND (last_used_at IS NULL OR last_used_at < now() - interval '1 minute');

-- name: BlockPersonalTokenGrant :execrows
-- An organization admin's revocation of one grant: it sticks until lifted.
UPDATE personal_token_grants SET blocked_at = now(), blocked_by = sqlc.narg(blocked_by)::uuid
WHERE token_id = $1 AND org_id = $2 AND blocked_at IS NULL;

-- name: PersonalTokensAllowed :one
-- The organization's switch; no row means allowed.
SELECT COALESCE((SELECT personal_tokens_allowed FROM org_token_settings WHERE org_id = $1), true)::boolean AS allowed;

-- name: SetPersonalTokensAllowed :exec
INSERT INTO org_token_settings (org_id, personal_tokens_allowed, updated_at) VALUES ($1, $2, now())
ON CONFLICT (org_id) DO UPDATE SET personal_tokens_allowed = EXCLUDED.personal_tokens_allowed, updated_at = now();
