-- +goose Up
-- v0.4a decision 3 (spec docs/superpowers/specs/2026-10-06-v0.4a-alerts-
-- design.md, "Decisions" #3 and "Data model"): silencing is not a second
-- subsystem, it is the face of a maintenance window — a one-off window with
-- internal visibility and reason = 'silence'. That reuses targets, overlap
-- handling, RLS, API and GUI instead of standing up a parallel table.
-- Additive, with a DEFAULT, so every window written before this migration
-- keeps meaning what it meant: Postgres backfills the default into
-- existing rows when a NOT NULL column is added this way, no UPDATE
-- needed.
ALTER TABLE maintenance_windows
  ADD COLUMN reason text NOT NULL DEFAULT 'maintenance'
    CHECK (reason IN ('maintenance', 'silence')),
  ADD COLUMN monitoring_group_id uuid;
ALTER TABLE maintenance_windows
  ADD CONSTRAINT maintenance_windows_monitoring_group_fk
  FOREIGN KEY (org_id, monitoring_group_id) REFERENCES monitoring_groups (org_id, id) ON DELETE CASCADE;

-- The second addition decision 3 and the design's "Data model" call for:
-- the target kind monitoring_group. target_kind's own CHECK predates
-- monitoring groups (migration 00032) and must widen to admit the new
-- value, or every window built against it would fail this constraint
-- despite passing Go validation. Dropping and re-adding the unnamed CHECK
-- (named maintenance_windows_target_kind_check by Postgres's own
-- convention) only relaxes it, the same move migration 00032 already made
-- for schedule_runs.outcome: no existing row's target_kind changes, so no
-- row can violate the new, wider list.
ALTER TABLE maintenance_windows
  DROP CONSTRAINT maintenance_windows_target_kind_check,
  ADD CONSTRAINT maintenance_windows_target_kind_check
    CHECK (target_kind IN ('host', 'inventory_group', 'monitoring_group', 'org'));

-- +goose Down
ALTER TABLE maintenance_windows
  DROP CONSTRAINT maintenance_windows_target_kind_check,
  ADD CONSTRAINT maintenance_windows_target_kind_check
    CHECK (target_kind IN ('host', 'inventory_group', 'org'));
ALTER TABLE maintenance_windows
  DROP CONSTRAINT maintenance_windows_monitoring_group_fk,
  DROP COLUMN monitoring_group_id, DROP COLUMN reason;
