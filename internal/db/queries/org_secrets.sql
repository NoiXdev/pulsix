-- name: UpsertOrgSecret :exec
-- Create or replace a secret whole: a replace swaps the sealed fields and
-- the field names and moves updated_at, created_at stays the first write's.
INSERT INTO org_secrets (org_id, name, fields_sealed, field_names, created_at, updated_at)
VALUES (@org_id, @name, @fields_sealed, @field_names::text[], @at, @at)
ON CONFLICT (org_id, name) DO UPDATE
SET fields_sealed = EXCLUDED.fields_sealed, field_names = EXCLUDED.field_names, updated_at = EXCLUDED.updated_at;

-- name: ListOrgSecrets :many
-- The organization's secrets without their sealed fields: the list never
-- needs a value, so it never reads one.
SELECT name, field_names, created_at, updated_at FROM org_secrets
WHERE org_id = @org_id ORDER BY name;

-- name: GetOrgSecretSealed :one
SELECT fields_sealed FROM org_secrets WHERE org_id = @org_id AND name = @name;

-- name: DeleteOrgSecret :execrows
DELETE FROM org_secrets WHERE org_id = @org_id AND name = @name;
