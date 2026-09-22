-- Org-level job settings (backlog 2.44), on the org_job_settings table
-- migration 00033 creates. Org-scoped like every other store query: the
-- statements run inside the request's organization scope, whose
-- org_isolation policy admits only that organization's row.

-- name: GetOrgJobSettings :one
-- The organization's stored job settings; the caller turns pgx.ErrNoRows
-- (no row yet) into the defaults.
SELECT jobs_require_pinned_hosts FROM org_job_settings WHERE org_id = $1;

-- name: UpsertOrgJobSettings :execrows
-- Replaces the stored settings; the row is created with its defaults when
-- the organization has none.
INSERT INTO org_job_settings (org_id, jobs_require_pinned_hosts)
VALUES ($1, $2)
ON CONFLICT (org_id) DO UPDATE
SET jobs_require_pinned_hosts = EXCLUDED.jobs_require_pinned_hosts, updated_at = now();
