-- +goose Up
-- Triggered runs (spec 2026-09-18 §2 "Actor and permissions"): jobs keep
-- their existing trigger column as the trigger kind — now constrained to
-- the four kinds — and gain the id of the schedule or webhook trigger that
-- started them. No foreign key: schedules and triggers are deleted freely
-- and runs outlive them, like template_id.
ALTER TABLE jobs ADD COLUMN trigger_ref uuid;
ALTER TABLE jobs ADD CONSTRAINT jobs_trigger_check
  CHECK (trigger IN ('manual', 'schedule', 'webhook', 'system'));

-- The overlap guard and the webhook on_active check ask "is a run of this
-- template still active?" on every due schedule and delivery.
CREATE INDEX jobs_active_template_idx ON jobs (org_id, template_id)
  WHERE template_id IS NOT NULL AND status NOT IN ('succeeded', 'failed', 'cancelled', 'error');

-- +goose Down
DROP INDEX jobs_active_template_idx;
ALTER TABLE jobs DROP CONSTRAINT jobs_trigger_check;
ALTER TABLE jobs DROP COLUMN trigger_ref;
