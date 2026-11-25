-- +goose Up
-- The enrollment tokens an enrolling run hands its hosts (spec 2026-11-25
-- assistant API gaps, B). The claim of a job with a confirmed enroll list
-- (migration 00127) mints one host-bound single-use token per listed host
-- without an agent; the job links them, so a re-claim, a requeue and every
-- terminal path revoke (delete) the ones still unused, and the run view
-- counts the used ones. NULL for every token a person created on a host's
-- Agent card and for every existing row; a deleted job leaves its used
-- tokens' history unlinked. The column is added without a default, a
-- catalog change; the foreign key validates an all-NULL column.
ALTER TABLE agent_enrollments ADD COLUMN job_id uuid;
ALTER TABLE agent_enrollments ADD CONSTRAINT agent_enrollments_job_fk
  FOREIGN KEY (org_id, job_id) REFERENCES jobs (org_id, id) ON DELETE SET NULL (job_id);
CREATE INDEX agent_enrollments_job_idx ON agent_enrollments (org_id, job_id) WHERE job_id IS NOT NULL;

-- How many tokens the job's claims minted in all (a re-claim mints afresh
-- and adds to it). The tokens themselves are only stored as their hash,
-- and an unused one is deleted when it is revoked, so the count is kept on
-- the job. A constant default: a catalog change.
ALTER TABLE jobs ADD COLUMN enroll_tokens_minted integer NOT NULL DEFAULT 0;

-- +goose Down
-- Development databases only (docs/upgrades.md: migrations only go up).
ALTER TABLE jobs DROP COLUMN enroll_tokens_minted;
DROP INDEX agent_enrollments_job_idx;
ALTER TABLE agent_enrollments DROP CONSTRAINT agent_enrollments_job_fk;
ALTER TABLE agent_enrollments DROP COLUMN job_id;
