-- +goose Up
-- Run requests that sync first (spec 2026-11-25 assistant API gaps, A,
-- "sync_first"): a request made with sync_first starts a sync of its
-- repository and waits for it in waiting_for_sync. When that sync succeeds
-- the request becomes pending and its expiry starts; when the sync fails,
-- is cancelled or does not finish in time, the request becomes failed with
-- the reason in sync_error.
--
-- The status CHECK only widens: every existing row passes it. expires_at is
-- NULL only while a request waits (its expiry has not started), which the
-- second CHECK holds; every existing row has one.
ALTER TABLE run_requests DROP CONSTRAINT run_requests_status_check;
ALTER TABLE run_requests ADD CONSTRAINT run_requests_status_check
  CHECK (status IN ('pending', 'approved', 'rejected', 'waiting_for_sync', 'failed'));
ALTER TABLE run_requests ALTER COLUMN expires_at DROP NOT NULL;
ALTER TABLE run_requests ADD CONSTRAINT run_requests_expires_at_check
  CHECK (expires_at IS NOT NULL OR status = 'waiting_for_sync');

-- sync_job_id is the sync_repo job a request waits on (no foreign key, as
-- job_id: the history outlives a purged job); a waiting request always has
-- one. expires_in_minutes is the expiry the request asked for, which a
-- waiting request starts once it becomes pending (NULL on rows from before
-- this column: the default hour). sync_error is why a failed request's sync
-- did not let it become pending.
ALTER TABLE run_requests ADD COLUMN sync_job_id uuid;
ALTER TABLE run_requests ADD COLUMN expires_in_minutes integer;
ALTER TABLE run_requests ADD COLUMN sync_error text;
ALTER TABLE run_requests ADD CONSTRAINT run_requests_sync_job_check
  CHECK (status <> 'waiting_for_sync' OR sync_job_id IS NOT NULL);

-- The waiting requests, which the sync's finish and the manager's sweep
-- look up (by their sync job, or all of them).
CREATE INDEX run_requests_waiting_idx ON run_requests (sync_job_id) WHERE status = 'waiting_for_sync';

-- +goose Down
-- Development databases only (docs/upgrades.md: migrations only go up).
DROP INDEX run_requests_waiting_idx;
ALTER TABLE run_requests DROP CONSTRAINT run_requests_sync_job_check;
ALTER TABLE run_requests DROP COLUMN sync_error;
ALTER TABLE run_requests DROP COLUMN expires_in_minutes;
ALTER TABLE run_requests DROP COLUMN sync_job_id;
DELETE FROM run_requests WHERE status IN ('waiting_for_sync', 'failed');
ALTER TABLE run_requests DROP CONSTRAINT run_requests_expires_at_check;
ALTER TABLE run_requests ALTER COLUMN expires_at SET NOT NULL;
ALTER TABLE run_requests DROP CONSTRAINT run_requests_status_check;
ALTER TABLE run_requests ADD CONSTRAINT run_requests_status_check
  CHECK (status IN ('pending', 'approved', 'rejected'));
