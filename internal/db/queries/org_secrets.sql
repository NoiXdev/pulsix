-- name: UpsertOrgSecret :one
-- Create or replace a secret whole: a replace swaps the sealed fields and
-- the field names and moves updated_at, created_at stays the first write's.
-- It returns the row without its sealed fields, and whether it inserted one
-- (xmax is 0 on a freshly inserted row and the updater's id on a replaced
-- one), so the audit trail can tell a create from a replace.
INSERT INTO org_secrets (org_id, name, fields_sealed, field_names, created_at, updated_at)
VALUES (@org_id, @name, @fields_sealed, @field_names::text[], @at, @at)
ON CONFLICT (org_id, name) DO UPDATE
SET fields_sealed = EXCLUDED.fields_sealed, field_names = EXCLUDED.field_names, updated_at = EXCLUDED.updated_at
RETURNING name, field_names, created_at, updated_at, (xmax = 0) AS inserted;

-- name: ListOrgSecrets :many
-- The organization's secrets without their sealed fields: the list never
-- needs a value, so it never reads one.
SELECT name, field_names, created_at, updated_at FROM org_secrets
WHERE org_id = @org_id ORDER BY name;

-- name: OrgSecretExists :one
SELECT EXISTS (SELECT 1 FROM org_secrets WHERE org_id = @org_id AND name = @name);

-- name: GetOrgSecretSealed :one
SELECT fields_sealed FROM org_secrets WHERE org_id = @org_id AND name = @name;

-- name: DeleteOrgSecret :execrows
DELETE FROM org_secrets WHERE org_id = @org_id AND name = @name;

-- name: InsertOrgSecret :one
-- Create a secret only when the organization has none of that name (PUT
-- with If-None-Match: *): no row comes back when it exists.
INSERT INTO org_secrets (org_id, name, fields_sealed, field_names, created_at, updated_at)
VALUES (@org_id, @name, @fields_sealed, @field_names::text[], @at, @at)
ON CONFLICT (org_id, name) DO NOTHING
RETURNING name, field_names, created_at, updated_at;
