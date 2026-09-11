-- name: CreateCredential :one
INSERT INTO credentials (id, org_id, name, kind, username, public_key, fingerprint, key_type, secret, expires_at, created_by)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
RETURNING *;

-- name: GetCredential :one
SELECT * FROM credentials WHERE org_id = $1 AND id = $2;

-- name: ListCredentials :many
SELECT * FROM credentials WHERE org_id = $1 ORDER BY name;

-- name: UpdateCredential :one
-- A NULL secret keeps the stored one: renaming a credential or changing its
-- expiry must not require re-entering the secret.
UPDATE credentials
SET name = @name, username = @username, expires_at = @expires_at,
    public_key = @public_key, fingerprint = @fingerprint, key_type = @key_type,
    secret = COALESCE(sqlc.narg('secret')::bytea, secret), updated_at = now()
WHERE org_id = @org_id AND id = @id
RETURNING *;

-- name: DeleteCredential :execrows
DELETE FROM credentials WHERE org_id = $1 AND id = $2;
