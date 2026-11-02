-- +goose Up
-- Backlog 7.2. backup_runs is the history of backup attempts: when one
-- started and ended, scheduled or manual, who asked for it, and how it went.
-- It is history, not the catalogue: the folder or bucket decides which
-- backups exist (internal/backup.Service.List merges the two).
--
-- Instance-level like instance_settings (00009): no org_id, no row-level
-- security. pulsix_app gets its privileges from the default privileges of
-- 00002; the explicit grant below only states them.
--
-- A scheduled attempt claims its daily slot: the partial unique index on
-- slot is what lets several managers agree on who runs it (an INSERT ... ON
-- CONFLICT DO NOTHING that inserted nothing lost the slot). A manual attempt
-- has no slot.
--
-- "A backup is running" is not decided here: a running backup holds a
-- session advisory lock (internal/backup.AdvisoryKeyBackup) for its whole
-- duration, and a row still running while nobody holds the lock is stale
-- (a crashed manager, or a restored snapshot) and gets marked abandoned.
CREATE TABLE backup_runs (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  kind        text NOT NULL CHECK (kind IN ('scheduled', 'manual')),
  slot        timestamptz,
  started_at  timestamptz NOT NULL,
  finished_at timestamptz,
  state       text NOT NULL CHECK (state IN ('running', 'succeeded', 'failed', 'abandoned')),
  object_name text,
  size_bytes  bigint,
  error       text,
  actor_id    uuid REFERENCES users (id) ON DELETE SET NULL,
  CONSTRAINT backup_runs_slot_scheduled CHECK (slot IS NULL OR kind = 'scheduled'),
  CONSTRAINT backup_runs_finished_when_done CHECK ((state = 'running') = (finished_at IS NULL))
);

CREATE UNIQUE INDEX backup_runs_scheduled_slot ON backup_runs (slot) WHERE kind = 'scheduled';
CREATE INDEX backup_runs_started_at ON backup_runs (started_at DESC);

GRANT SELECT, INSERT, UPDATE, DELETE ON backup_runs TO pulsix_app;

-- +goose Down
DROP TABLE backup_runs;
