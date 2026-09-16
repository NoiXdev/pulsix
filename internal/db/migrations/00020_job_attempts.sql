-- +goose Up
-- Job attempts (final review I3/I5). attempts counts how often a job was
-- claimed: the claim increments it, and the reaper requeues a stale claim
-- only while attempts is below the configured maximum
-- (PULSIX_JOB_MAX_ATTEMPTS, default 3) — beyond it the job fails with a
-- clear error instead of looping forever (a claim response the worker can
-- never receive, a payload that crashes the worker).
--
-- job_events.attempt stamps each event with the attempt that produced it,
-- so the run view can show a requeued job's attempts in order. Existing
-- rows predate the counter and read as attempt 0.
ALTER TABLE jobs ADD COLUMN attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0);
ALTER TABLE job_events ADD COLUMN attempt integer NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE job_events DROP COLUMN attempt;
ALTER TABLE jobs DROP COLUMN attempts;
