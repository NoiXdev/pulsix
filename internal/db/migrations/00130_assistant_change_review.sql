-- +goose Up
-- Pausing what runs unattended after an assistant's change (spec
-- 2026-11-25 assistant API gaps, C2, decision D5). When a change made with
-- an assistant token alters what a job template or a repository runs, the
-- schedules of the affected templates are paused and the triggers that run
-- them (or sync that repository) are disabled, each with the reason
-- 'assistant_change', and a person reviews each one through a request of
-- kind schedule_review or trigger_review (migration 00129 admits both).
-- The reason says why the item stopped, so its page can point at the
-- review; resuming or enabling it clears the reason in the same statement,
-- which the CHECKs hold every writer to. Nullable columns without a
-- default: catalog changes; the CHECKs pass every existing row (all NULL).
ALTER TABLE schedules ADD COLUMN paused_reason text;
ALTER TABLE schedules ADD CONSTRAINT schedules_paused_reason_check
  CHECK (paused_reason IS NULL OR (paused_reason = 'assistant_change' AND paused));

ALTER TABLE job_triggers ADD COLUMN disabled_reason text;
ALTER TABLE job_triggers ADD CONSTRAINT job_triggers_disabled_reason_check
  CHECK (disabled_reason IS NULL OR (disabled_reason = 'assistant_change' AND NOT enabled));

-- The open review of a paused item: one per schedule or trigger, found by
-- its target (the reviewer refreshes it when another change hits the item,
-- a person's resume closes it).
CREATE INDEX run_requests_review_target_idx ON run_requests (org_id, target_id)
  WHERE kind IN ('schedule_review', 'trigger_review');

-- +goose Down
-- Development databases only (docs/upgrades.md: migrations only go up).
DROP INDEX run_requests_review_target_idx;
ALTER TABLE job_triggers DROP CONSTRAINT job_triggers_disabled_reason_check;
ALTER TABLE job_triggers DROP COLUMN disabled_reason;
ALTER TABLE schedules DROP CONSTRAINT schedules_paused_reason_check;
ALTER TABLE schedules DROP COLUMN paused_reason;
