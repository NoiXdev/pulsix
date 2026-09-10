-- name: CreateSession :exec
INSERT INTO sessions (id, user_id, secret_hash, mfa_complete, expires_at) VALUES ($1, $2, $3, $4, $5);

-- name: GetSession :one
SELECT s.*, u.instance_admin FROM sessions s JOIN users u ON u.id = s.user_id
WHERE s.id = $1 AND s.revoked_at IS NULL AND s.expires_at > now();

-- name: MarkSessionMFAComplete :exec
UPDATE sessions SET mfa_complete = true WHERE id = $1;

-- name: RevokeSession :exec
UPDATE sessions SET revoked_at = now() WHERE id = $1;
