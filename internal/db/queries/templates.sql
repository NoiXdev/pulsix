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
-- set here, the way every other store's UPDATE does. if_revision is the
-- revision the write is based on, NULL to skip the check (an API client
-- without If-Match); no row back means a stale revision or an unknown
-- template (the store tells the two apart). The variables source columns
-- (vars_mode, vars_source_rev, vars_applied_at) are left as they are.
UPDATE job_templates SET
  name = @name, playbook_id = @playbook_id, scope = @scope, "limit" = @lim,
  extra_vars = @extra_vars, extra_vars_schema = @extra_vars_schema, credential_id = @credential_id,
  revision = revision + 1, updated_at = now()
WHERE org_id = @org_id AND id = @id
  AND (sqlc.narg(if_revision)::bigint IS NULL OR revision = sqlc.narg(if_revision))
RETURNING *;

-- name: UpdateTemplateVariables :one
-- A template's variables: the schema, the values and where they were last
-- applied from, under UpdateTemplate's revision guard. Nothing else of the
-- template changes.
UPDATE job_templates SET
  extra_vars = @extra_vars, extra_vars_schema = @extra_vars_schema,
  vars_mode = @vars_mode, vars_source_rev = @vars_source_rev, vars_applied_at = @vars_applied_at,
  revision = revision + 1, updated_at = now()
WHERE org_id = @org_id AND id = @id
  AND (sqlc.narg(if_revision)::bigint IS NULL OR revision = sqlc.narg(if_revision))
RETURNING *;

-- name: UpdateTemplateVarsSource :one
-- A template's fields and variables source columns without its values: a
-- mode change and the automatic apply (backlog 7.75), neither of which
-- may seal, unseal or drop a stored value. Under UpdateTemplate's
-- revision guard.
UPDATE job_templates SET
  extra_vars_schema = @extra_vars_schema,
  vars_mode = @vars_mode, vars_source_rev = @vars_source_rev, vars_applied_at = @vars_applied_at,
  revision = revision + 1, updated_at = now()
WHERE org_id = @org_id AND id = @id
  AND (sqlc.narg(if_revision)::bigint IS NULL OR revision = sqlc.narg(if_revision))
RETURNING *;

-- name: ListAutomaticTemplatesForRepo :many
-- The templates a repository's sync applies its new variable sources to:
-- those in automatic mode whose playbook belongs to the repository.
SELECT * FROM job_templates
WHERE job_templates.org_id = @org_id AND job_templates.vars_mode = 'automatic'
  AND job_templates.playbook_id IN (
    SELECT p.id FROM playbooks p WHERE p.org_id = @org_id AND p.repo_id = @repo_id)
ORDER BY job_templates.name;

-- name: ListAutomaticTemplatesForPlaybook :many
-- The templates an editor playbook's save applies its new variable
-- sources to: those of the playbook in automatic mode.
SELECT * FROM job_templates
WHERE org_id = @org_id AND playbook_id = @playbook_id AND vars_mode = 'automatic'
ORDER BY name;

-- name: DeleteTemplate :execrows
-- Jobs reference templates by template_id with no foreign key on purpose:
-- deleting a template that ran before is allowed, and a finished run keeps
-- its dangling reference (the run view tolerates it).
DELETE FROM job_templates WHERE org_id = $1 AND id = $2;
