-- name: CreateCredential :one
INSERT INTO credentials (id, org_id, name, kind, username, public_key, fingerprint, key_type, secret, expires_at, created_by)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
RETURNING *;

-- name: GetCredential :one
-- Metadata only (backlog 2.17): every column except the sealed secret. The
-- only place that column belongs on the wire is GetCredentialSecret's much
-- narrower select, for the worker's claim path.
SELECT id, org_id, name, kind, username, public_key, fingerprint, key_type, expires_at, created_by, created_at, updated_at
FROM credentials WHERE org_id = $1 AND id = $2;

-- name: GetCredentialSecret :one
-- The ONE read that returns the sealed secret bytes, for the job-claim path
-- only (backlog 2.17): it selects the kind (to know which Secret field is
-- set) and the ciphertext, and deliberately nothing else — no name, no
-- metadata — so a regression can never widen it back into a metadata read
-- that happens to carry the secret. Callers open the ciphertext with the
-- credential AAD; see credentials.Service.OpenSecret.
SELECT kind, secret FROM credentials WHERE org_id = $1 AND id = $2;

-- name: ListCredentials :many
-- Metadata only (backlog 2.17): see GetCredential's comment above — the
-- same narrowing applies here, and matters more, since this is the query
-- that runs once per credentials-page load rather than once per detail
-- view.
SELECT id, org_id, name, kind, username, public_key, fingerprint, key_type, expires_at, created_by, created_at, updated_at
FROM credentials WHERE org_id = $1 ORDER BY name;

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
-- How many jump hosts, hosts and repositories reference each credential,
-- for the "used by" column. Inventory groups never reference credentials
-- directly.
--
-- Repositories were missing here while git_repos.credential_id already
-- carried a foreign key, so deleting a credential a repository still used
-- failed with the database's own constraint error instead of the service's
-- ErrInUse, and the column said "unused" right up to the attempt. The
-- vault column added the second such reference and made the gap worth
-- closing: both are counted, because for this column what matters is
-- whether anything at all still points at the credential.
SELECT c.id,
  (SELECT count(*) FROM bastions b WHERE b.org_id = c.org_id AND b.credential_id = c.id)::int AS bastions,
  (SELECT count(*) FROM host_ssh h WHERE h.org_id = c.org_id AND h.credential_id = c.id)::int AS hosts,
  (SELECT count(*) FROM git_repos r WHERE r.org_id = c.org_id
     AND (r.credential_id = c.id OR r.vault_credential_id = c.id))::int AS repos
FROM credentials c
WHERE c.org_id = $1;

-- name: ListCredentialHosts :many
-- The managed hosts referencing one credential (the rotation enqueue's
-- target list, backlog 2.7): every host whose own SSH settings log in with
-- the credential, with the zone the rotate_key job claims in. Unmanaged
-- hosts are excluded — a job can only run against a managed host, and an
-- operator who unmanaged a host excluded it from provisioning; the usage
-- counter above still counts it, so the "used by" column stays honest.
SELECT h.id, h.zone_id
FROM host_ssh s
JOIN hosts h ON h.org_id = s.org_id AND h.id = s.host_id
WHERE s.org_id = $1 AND s.credential_id = $2 AND h.managed;

-- name: ListCredentialBastions :many
-- The jump hosts referencing one credential, in name order — the other
-- half of the rotation target list.
SELECT * FROM bastions WHERE org_id = $1 AND credential_id = $2 ORDER BY name;
