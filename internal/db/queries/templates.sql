-- name: InsertTemplate :one
-- The referenced playbook and credential are validated by the service
-- first; the composite foreign keys are the second guard — a reference
-- vanishing between check and insert surfaces as a foreign-key violation
-- the store maps to ErrNotFound.
INSERT INTO job_templates (id, org_id, name, playbook_id, scope, "limit", extra_vars, extra_vars_schema, credential_id)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING *;

-- name: GetTemplate :one
-- Scoped by org_id in the WHERE clause, not only relying on RLS (see
-- GetZone's comment in hosts.sql): a template belonging to a different
-- organization must be invisible, not merely forbidden.
SELECT * FROM job_templates WHERE org_id = $1 AND id = $2;

-- name: ListTemplates :many
SELECT * FROM job_templates WHERE org_id = $1 ORDER BY name;

-- name: UpdateTemplate :one
-- Full replacement of every editable field (PUT semantics); updated_at is
-- set here, the way every other store's UPDATE does.
UPDATE job_templates SET
  name = $3, playbook_id = $4, scope = $5, "limit" = $6,
  extra_vars = $7, extra_vars_schema = $8, credential_id = $9, updated_at = now()
WHERE org_id = $1 AND id = $2
RETURNING *;

-- name: DeleteTemplate :execrows
-- Jobs reference templates by template_id with no foreign key on purpose:
-- deleting a template that ran before is allowed, and a finished run keeps
-- its dangling reference (the run view tolerates it).
DELETE FROM job_templates WHERE org_id = $1 AND id = $2;
