-- The assistant access switch (spec 2026-11-16 assistant access), on the
-- org_assistant_settings table. Org-scoped like every store query.

-- name: GetOrgAssistantEnabled :one
-- The stored switch; the caller turns pgx.ErrNoRows (no row) into off.
SELECT enabled FROM org_assistant_settings WHERE org_id = $1;

-- name: GetOrgAssistantStartsFollowUps :one
-- Whether an approved run request starts the template's follow-ups; the caller
-- turns pgx.ErrNoRows (no row) into off.
SELECT approved_runs_start_follow_ups FROM org_assistant_settings WHERE org_id = $1;

-- name: EnsureOrgAssistantSettings :exec
-- Creates the organization's row with the defaults (both off, what an absent
-- row reads as) when it has none, so the update has a row to lock.
INSERT INTO org_assistant_settings (org_id) VALUES ($1)
ON CONFLICT (org_id) DO NOTHING;

-- name: LockOrgAssistantSettings :one
-- The stored settings, locked until the update's transaction ends.
SELECT enabled, approved_runs_start_follow_ups FROM org_assistant_settings WHERE org_id = $1 FOR UPDATE;

-- name: UpdateOrgAssistantSettings :exec
UPDATE org_assistant_settings
SET enabled = $2, approved_runs_start_follow_ups = $3, updated_at = now()
WHERE org_id = $1;
