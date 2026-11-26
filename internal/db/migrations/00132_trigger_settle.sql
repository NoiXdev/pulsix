-- +goose Up
-- A webhook trigger can wait for a quiet moment (backlog 2.137): with a
-- settle delay, deliveries are collected into one pending run that starts
-- settle_seconds after the last of them, at the latest max_wait_seconds
-- after the first (NULL: ten times the settle delay), and only once no run
-- the trigger started is still active. 0 keeps today's behaviour, a run
-- per delivery. Constant defaults and a nullable column touch only the
-- catalog; every existing trigger passes both checks.
ALTER TABLE job_triggers ADD COLUMN settle_seconds integer NOT NULL DEFAULT 0;
ALTER TABLE job_triggers ADD COLUMN max_wait_seconds integer;
ALTER TABLE job_triggers ADD CONSTRAINT job_triggers_settle_check
  CHECK (settle_seconds = 0 OR settle_seconds BETWEEN 5 AND 3600);
ALTER TABLE job_triggers ADD CONSTRAINT job_triggers_max_wait_check
  CHECK (max_wait_seconds IS NULL OR (settle_seconds > 0 AND max_wait_seconds BETWEEN settle_seconds AND 86400));

-- The collected run of a settling trigger: at most one per trigger. id
-- names the window: its deliveries point at it (trigger_deliveries.
-- pending_id) and the run it starts is enqueued under an idempotency key
-- made from it. due_at is when it starts (settle after the last delivery,
-- never past deadline_at); a row past due waits while a run the trigger
-- started is active. overrides are the last delivery's mapped values,
-- untrusted, sealed with AAD trigger-pending:<org>:<trigger> (NULL: none).
-- The manager's ticker claims due rows FOR UPDATE SKIP LOCKED and deletes
-- each one it starts or drops.
CREATE TABLE trigger_pending_runs (
  trigger_id       uuid PRIMARY KEY,
  org_id           uuid NOT NULL,
  id               uuid NOT NULL UNIQUE,
  first_at         timestamptz NOT NULL,
  last_at          timestamptz NOT NULL,
  due_at           timestamptz NOT NULL,
  deadline_at      timestamptz NOT NULL,
  deliveries       integer NOT NULL CHECK (deliveries > 0),
  last_delivery_id uuid NOT NULL,
  overrides        bytea,
  FOREIGN KEY (org_id, trigger_id) REFERENCES job_triggers (org_id, id) ON DELETE CASCADE
);
CREATE INDEX trigger_pending_runs_due_idx ON trigger_pending_runs (due_at);

ALTER TABLE trigger_pending_runs ENABLE ROW LEVEL SECURITY;
ALTER TABLE trigger_pending_runs FORCE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON trigger_pending_runs
  USING (org_id = NULLIF(current_setting('pulsix.org_id', true), '')::uuid);
CREATE POLICY org_isolation_bypass ON trigger_pending_runs
  USING (current_setting('pulsix.org_id', true) IS NULL OR current_setting('pulsix.org_id', true) = '');

-- A delivery collected into a pending run: outcome 'collected', pending_id
-- the window, job_group_id the run once it started. No foreign key: the
-- pending row goes when its run starts, the log stays. No index either:
-- the window's deliveries are found within the trigger's own (at most
-- 500) rows. The outcome CHECK only widens: every existing row passes it.
ALTER TABLE trigger_deliveries ADD COLUMN pending_id uuid;
ALTER TABLE trigger_deliveries DROP CONSTRAINT trigger_deliveries_outcome_check;
ALTER TABLE trigger_deliveries ADD CONSTRAINT trigger_deliveries_outcome_check CHECK (outcome IN (
  'triggered', 'skipped_active', 'rejected_ip', 'rejected_auth',
  'rate_limited', 'invalid_payload', 'disabled', 'failed', 'collected'));

-- +goose Down
-- Development databases only (docs/upgrades.md: migrations only go up).
DELETE FROM trigger_deliveries WHERE outcome = 'collected';
ALTER TABLE trigger_deliveries DROP CONSTRAINT trigger_deliveries_outcome_check;
ALTER TABLE trigger_deliveries ADD CONSTRAINT trigger_deliveries_outcome_check CHECK (outcome IN (
  'triggered', 'skipped_active', 'rejected_ip', 'rejected_auth',
  'rate_limited', 'invalid_payload', 'disabled', 'failed'));
ALTER TABLE trigger_deliveries DROP COLUMN pending_id;
DROP TABLE trigger_pending_runs;
ALTER TABLE job_triggers DROP CONSTRAINT job_triggers_max_wait_check;
ALTER TABLE job_triggers DROP CONSTRAINT job_triggers_settle_check;
ALTER TABLE job_triggers DROP COLUMN max_wait_seconds;
ALTER TABLE job_triggers DROP COLUMN settle_seconds;
