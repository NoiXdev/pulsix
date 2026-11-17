-- +goose Up
-- The hosts list counts each host's live runs (backlog 3.97): job_hosts joined
-- to the jobs that are still queued, claimed, running or cancelling. Live jobs
-- are a handful next to the finished history, so a partial index on them keeps
-- that join proportional to the live runs, not to every run ever made.
-- Additive: an index only, nothing to migrate or to skew between versions.
CREATE INDEX jobs_live_idx ON jobs (org_id) WHERE status IN ('queued', 'claimed', 'running', 'cancelling');

-- +goose Down
DROP INDEX jobs_live_idx;
