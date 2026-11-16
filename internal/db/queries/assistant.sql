-- The assistant access switch (spec 2026-11-16 assistant access), on the
-- org_assistant_settings table. Org-scoped like every store query.

-- name: GetOrgAssistantEnabled :one
-- The stored switch; the caller turns pgx.ErrNoRows (no row) into off.
SELECT enabled FROM org_assistant_settings WHERE org_id = $1;

-- name: UpsertOrgAssistantEnabled :exec
INSERT INTO org_assistant_settings (org_id, enabled)
VALUES ($1, $2)
ON CONFLICT (org_id) DO UPDATE SET enabled = EXCLUDED.enabled, updated_at = now();

-- name: GetOrgAssistantStartsFollowUps :one
-- Whether an approved run request starts the template's follow-ups; the caller
-- turns pgx.ErrNoRows (no row) into off.
SELECT approved_runs_start_follow_ups FROM org_assistant_settings WHERE org_id = $1;

-- name: UpsertOrgAssistantStartsFollowUps :exec
INSERT INTO org_assistant_settings (org_id, approved_runs_start_follow_ups)
VALUES ($1, $2)
ON CONFLICT (org_id) DO UPDATE SET approved_runs_start_follow_ups = EXCLUDED.approved_runs_start_follow_ups, updated_at = now();
