-- +goose Up
-- The runs list filters by schedule or webhook trigger (spec 2026-09-18
-- §8), and the calendar and the digest read a schedule's run groups.
CREATE INDEX jobs_trigger_ref_idx ON jobs (org_id, trigger_ref, created_at DESC) WHERE trigger_ref IS NOT NULL;
CREATE INDEX jobs_run_group_idx ON jobs (org_id, run_group_id);

-- +goose Down
DROP INDEX jobs_run_group_idx;
DROP INDEX jobs_trigger_ref_idx;
