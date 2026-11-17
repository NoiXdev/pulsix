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

-- name: PersonalTokensAllowed :one
-- The organization's switch; no row means allowed.
SELECT COALESCE((SELECT personal_tokens_allowed FROM org_token_settings WHERE org_id = $1), true)::boolean AS allowed;

-- name: SetPersonalTokensAllowed :exec
INSERT INTO org_token_settings (org_id, personal_tokens_allowed, updated_at) VALUES ($1, $2, now())
ON CONFLICT (org_id) DO UPDATE SET personal_tokens_allowed = EXCLUDED.personal_tokens_allowed, updated_at = now();

-- The owner's management (backlog 3.100, Task 17). These read across every
-- organization a user's tokens are granted in, so they run at instance
-- level (no organization bound), where the bypass policy applies.

-- name: LockPersonalTokenOwner :exec
-- Holds the owner row until the transaction ends, so two creates of one
-- user serialize here and cannot both pass the limit. The count is a
-- statement of its own, run after the lock is held: under read committed a
-- statement sees the rows committed when it began, so a count in the
-- locking statement would miss a token committed while it waited. NO KEY
-- UPDATE does not block rows that merely reference the user (sessions,
-- tokens).
SELECT u.id FROM users u WHERE u.id = $1 FOR NO KEY UPDATE;

-- name: CountPersonalTokens :one
-- The user's tokens that are not revoked, expired ones included.
SELECT count(*)::int AS n FROM personal_tokens WHERE user_id = $1 AND revoked_at IS NULL;

-- name: GetPersonalToken :one
-- One of the user's tokens unless revoked; held until the transaction
-- ends, so concurrent edits of one token apply one after the other.
SELECT id, user_id, name, kind, areas, expires_at, last_used_at, created_at
FROM personal_tokens WHERE id = $1 AND user_id = $2 AND revoked_at IS NULL
FOR NO KEY UPDATE;

-- name: ListPersonalTokens :many
SELECT id, user_id, name, kind, areas, expires_at, last_used_at, created_at
FROM personal_tokens WHERE user_id = $1 AND revoked_at IS NULL
ORDER BY created_at, id;

-- name: ListPersonalTokenGrantsForUser :many
SELECT g.token_id, g.org_id, o.slug, o.name, g.role, (g.blocked_at IS NOT NULL)::boolean AS blocked,
       COALESCE(s.personal_tokens_allowed, true)::boolean AS allowed, g.last_used_at
FROM personal_token_grants g
JOIN organizations o ON o.id = g.org_id
LEFT JOIN org_token_settings s ON s.org_id = g.org_id
WHERE g.user_id = $1
ORDER BY o.slug;

-- name: ListPersonalTokenGrantsForToken :many
SELECT g.token_id, g.org_id, o.slug, o.name, g.role, (g.blocked_at IS NOT NULL)::boolean AS blocked,
       COALESCE(s.personal_tokens_allowed, true)::boolean AS allowed, g.last_used_at
FROM personal_token_grants g
JOIN organizations o ON o.id = g.org_id
LEFT JOIN org_token_settings s ON s.org_id = g.org_id
WHERE g.token_id = $1
ORDER BY o.slug;

-- name: UpdatePersonalToken :one
UPDATE personal_tokens SET name = $3, areas = sqlc.narg(areas)::text[], expires_at = $4
WHERE id = $1 AND user_id = $2 AND revoked_at IS NULL
RETURNING id, user_id, name, kind, areas, expires_at, last_used_at, created_at;

-- name: SetPersonalTokenGrantRole :execrows
-- The owner changes a cap; a blocked grant is never the owner's to change.
UPDATE personal_token_grants SET role = $3 WHERE token_id = $1 AND org_id = $2 AND blocked_at IS NULL;

-- name: DeletePersonalTokenGrant :execrows
-- The owner removes a grant; a blocked grant stays.
DELETE FROM personal_token_grants WHERE token_id = $1 AND org_id = $2 AND blocked_at IS NULL;

-- name: RevokePersonalToken :execrows
UPDATE personal_tokens SET revoked_at = now() WHERE id = $1 AND user_id = $2 AND revoked_at IS NULL;

-- name: CountLivePersonalTokenGrants :one
-- A token's unblocked grants, and their organization when there is one.
SELECT count(*)::int AS n, COALESCE((array_agg(org_id))[1], '00000000-0000-0000-0000-000000000000')::uuid AS org_id
FROM personal_token_grants WHERE token_id = $1 AND blocked_at IS NULL;

-- An organization's view (spec rule 6). These run bound to the organization;
-- row-level security admits only its grants, and the statements name the
-- organization as well.

-- name: ListPersonalTokenGrantsForOrg :many
-- Grants of tokens that are not revoked, and every blocked grant: a block
-- keeps its owner out of the organization even after they revoked the
-- token (the block is per person and organization), so the admins must
-- still see it to lift it.
SELECT g.token_id, t.name, t.kind, g.user_id, u.display_name, u.email, g.role,
       (g.blocked_at IS NOT NULL)::boolean AS blocked, (t.revoked_at IS NOT NULL)::boolean AS token_revoked,
       g.last_used_at, t.created_at, t.expires_at
FROM personal_token_grants g
JOIN personal_tokens t ON t.id = g.token_id
JOIN users u ON u.id = g.user_id
WHERE g.org_id = $1 AND (t.revoked_at IS NULL OR g.blocked_at IS NOT NULL)
ORDER BY t.created_at, t.id;

-- name: GetPersonalTokenGrantForOrg :one
SELECT g.token_id, t.name, t.kind, g.user_id, u.display_name, u.email, g.role,
       (g.blocked_at IS NOT NULL)::boolean AS blocked, (t.revoked_at IS NOT NULL)::boolean AS token_revoked,
       g.last_used_at, t.created_at, t.expires_at
FROM personal_token_grants g
JOIN personal_tokens t ON t.id = g.token_id
JOIN users u ON u.id = g.user_id
WHERE g.org_id = $1 AND g.token_id = $2 AND (t.revoked_at IS NULL OR g.blocked_at IS NOT NULL);

-- name: BlockPersonalTokenGrantEntry :one
-- An organization admin's revocation of one grant of a token that is not
-- revoked, in one statement: two admins racing get one row and one none,
-- and the row names the cap that was blocked.
UPDATE personal_token_grants g SET blocked_at = now(), blocked_by = sqlc.narg(blocked_by)::uuid
FROM personal_tokens t, users u
WHERE g.org_id = sqlc.arg(org_id)::uuid AND g.token_id = sqlc.arg(token_id)::uuid AND g.blocked_at IS NULL
  AND t.id = g.token_id AND t.revoked_at IS NULL AND u.id = g.user_id
RETURNING g.token_id, t.name, t.kind, g.user_id, u.display_name, u.email, g.role,
       (g.blocked_at IS NOT NULL)::boolean AS blocked, (t.revoked_at IS NOT NULL)::boolean AS token_revoked,
       g.last_used_at, t.created_at, t.expires_at;

-- name: PersonalTokenOrgBlocked :one
-- Whether an admin blocked any grant of the user in the organization, of
-- any of their tokens, revoked ones included: the block is per person and
-- organization, so no token of theirs gets a grant there until it is lifted.
SELECT EXISTS (
  SELECT 1 FROM personal_token_grants
  WHERE user_id = $1 AND org_id = $2 AND blocked_at IS NOT NULL
)::boolean AS blocked;

-- name: UnblockPersonalTokenOwner :execrows
-- Lifting the block deletes every blocked grant of the user in the
-- organization: the owner may add the organization again.
DELETE FROM personal_token_grants WHERE org_id = $1 AND user_id = $2 AND blocked_at IS NOT NULL;
