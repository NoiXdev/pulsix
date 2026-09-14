-- name: CreateCredential :one
INSERT INTO credentials (id, org_id, name, kind, username, public_key, fingerprint, key_type, secret, expires_at, created_by)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
RETURNING *;

-- name: GetCredential :one
SELECT * FROM credentials WHERE org_id = $1 AND id = $2;

-- name: GetCredentialSecret :one
-- The ONE read that returns the sealed secret bytes, for the job-claim path
-- only (backlog 2.17): it selects the kind (to know which Secret field is
-- set) and the ciphertext, and deliberately nothing else — no name, no
-- metadata — so a regression can never widen it back into a metadata read
-- that happens to carry the secret. Callers open the ciphertext with the
-- credential AAD; see credentials.Service.OpenSecret.
SELECT kind, secret FROM credentials WHERE org_id = $1 AND id = $2;

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

-- name: ListCredentialUsage :many
-- How many jump hosts and hosts reference each credential, for the "used by"
-- column. Inventory groups never reference credentials directly.
SELECT c.id,
  (SELECT count(*) FROM bastions b WHERE b.org_id = c.org_id AND b.credential_id = c.id)::int AS bastions,
  (SELECT count(*) FROM host_ssh h WHERE h.org_id = c.org_id AND h.credential_id = c.id)::int AS hosts
FROM credentials c
WHERE c.org_id = $1;
