-- name: GetOrganizationByID :one
SELECT * FROM organizations WHERE id = $1;

-- name: UpdateOrganization :one
UPDATE organizations SET name = $2 WHERE id = $1 RETURNING *;

-- name: ListMembers :many
SELECT u.id AS user_id, u.email, u.display_name, m.role, u.disabled_at, m.created_at
FROM org_memberships m JOIN users u ON u.id = m.user_id
WHERE m.org_id = $1 ORDER BY m.created_at;

-- name: UpdateMembershipRole :exec
UPDATE org_memberships SET role = $3 WHERE org_id = $1 AND user_id = $2;

-- name: DeleteMembership :execrows
DELETE FROM org_memberships WHERE org_id = $1 AND user_id = $2;

-- name: CountOwners :one
SELECT count(*) FROM org_memberships WHERE org_id = $1 AND role = 'owner';

-- name: InsertInvite :exec
INSERT INTO invites (id, org_id, email, role, token_hash, invited_by, expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7);

-- name: TakeInvite :one
-- Atomically finds an invite that is unaccepted and unexpired as of now and
-- marks it accepted in the same statement, so two concurrent requests
-- racing on the same token can never both succeed: the UPDATE's WHERE
-- clause re-checks accepted_at IS NULL, and Postgres serializes concurrent
-- updates to the same row, so only the first commits and the second finds
-- no matching row (mapped to ErrNotFound by the caller) rather than both
-- reading "unaccepted" before either writes.
UPDATE invites SET accepted_at = sqlc.arg(now)::timestamptz
WHERE token_hash = $1 AND accepted_at IS NULL AND expires_at > sqlc.arg(now)::timestamptz
RETURNING *;

-- name: SetUserPasswordAndName :exec
UPDATE users SET password_hash = $2, display_name = $3 WHERE id = $1;

-- name: DisableUser :exec
UPDATE users SET disabled_at = $2 WHERE id = $1;

-- name: RevokeSessionsForUser :exec
UPDATE sessions SET revoked_at = now() WHERE user_id = $1 AND revoked_at IS NULL;

-- name: RevokeTokensForUser :exec
UPDATE api_tokens SET revoked_at = now() WHERE user_id = $1 AND revoked_at IS NULL;
