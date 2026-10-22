-- +goose Up
-- RunRequest.IdempotencyKey (backlog 2.60): EnqueueRunWith's enqueue and the
-- schedule ticker's write-back are not the same transaction — the ticker
-- commits the enqueue, then writes schedule_runs and next_run_at back under
-- its own savepoint (internal/schedules/due_sql.go's processRow). A failure
-- between those two steps (a write error, a panic, a crashed manager) left
-- the schedule's occurrence due again, and the next tick decided it again:
-- decide re-enqueues, since from the ticker's own tables nothing ever
-- happened. One row per (org, key) gives that second attempt something to
-- recognise: the schedule ticker's key names the schedule and the due
-- instant it fires for, so a retried decision of the same occurrence finds
-- its key already claimed and replays the first attempt's run group instead
-- of creating a second one. Only the schedule ticker sets a key today; every
-- other trigger (manual, webhook, retry) leaves it empty and is unaffected.
CREATE TABLE job_idempotency_keys (
  org_id          uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  idempotency_key text NOT NULL,
  run_group_id    uuid NOT NULL,
  created_at      timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (org_id, idempotency_key)
);

ALTER TABLE job_idempotency_keys ENABLE ROW LEVEL SECURITY;
ALTER TABLE job_idempotency_keys FORCE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON job_idempotency_keys
  USING (org_id = NULLIF(current_setting('pulsix.org_id', true), '')::uuid);
CREATE POLICY org_isolation_bypass ON job_idempotency_keys
  USING (current_setting('pulsix.org_id', true) IS NULL OR current_setting('pulsix.org_id', true) = '');

-- +goose Down
DROP TABLE job_idempotency_keys;
