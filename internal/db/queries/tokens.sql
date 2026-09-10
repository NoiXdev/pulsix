-- name: InsertAPIToken :exec
INSERT INTO api_tokens (id, user_id, org_id, name, role, secret_hash, expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7);

-- name: GetAPITokenByHash :one
SELECT t.*, u.instance_admin FROM api_tokens t JOIN users u ON u.id = t.user_id
WHERE t.secret_hash = $1 AND t.revoked_at IS NULL AND (t.expires_at IS NULL OR t.expires_at > now());

-- name: ListAPITokensForOrg :many
SELECT * FROM api_tokens WHERE org_id = $1 AND revoked_at IS NULL ORDER BY created_at;

-- name: RevokeAPIToken :execrows
UPDATE api_tokens SET revoked_at = now() WHERE id = $1 AND org_id = $2 AND revoked_at IS NULL;

-- name: TouchAPIToken :exec
UPDATE api_tokens SET last_used_at = $2 WHERE id = $1;
