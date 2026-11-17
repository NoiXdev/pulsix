-- +goose Up
-- The hosts list counts each host's live runs (backlog 3.97): the jobs still
-- queued, claimed, running or cancelling, joined to job_hosts. Live jobs are a
-- handful next to the finished history, so the count starts from them through
-- this partial index and never walks a host's whole run history.
-- Additive: an index only, nothing to migrate or to skew between versions.
CREATE INDEX jobs_live_idx ON jobs (org_id) WHERE status IN ('queued', 'claimed', 'running', 'cancelling');

-- +goose Down
DROP INDEX jobs_live_idx;
