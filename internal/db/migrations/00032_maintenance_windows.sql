-- +goose Up
-- Maintenance windows (spec 2026-09-18 §6). The target is a host, an
-- inventory group or the whole organization; host_id and group_id carry
-- the composite foreign keys a polymorphic target_id could not, and
-- deleting the target deletes its windows. A window is one-off
-- (starts_at/ends_at) or recurring (cron + duration_seconds + timezone,
-- evaluated like schedules). v0.4 adds monitoring-group targets and alert
-- silencing, v0.5 public announcements.
CREATE TABLE maintenance_windows (
  id               uuid PRIMARY KEY,
  org_id           uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  name             text NOT NULL,
  target_kind      text NOT NULL CHECK (target_kind IN ('host', 'inventory_group', 'org')),
  host_id          uuid,
  group_id         uuid,
  starts_at        timestamptz,
  ends_at          timestamptz,
  cron             text,
  duration_seconds integer,
  timezone         text,
  note             text NOT NULL DEFAULT '',
  created_by       uuid,
  created_at       timestamptz NOT NULL DEFAULT now(),
  updated_at       timestamptz NOT NULL DEFAULT now(),
  revision         bigint NOT NULL DEFAULT 1,
  UNIQUE (org_id, name),
  UNIQUE (org_id, id),
  CHECK ((target_kind = 'host') = (host_id IS NOT NULL)),
  CHECK ((target_kind = 'inventory_group') = (group_id IS NOT NULL)),
  CHECK ((starts_at IS NULL) = (ends_at IS NULL)),
  CHECK ((cron IS NULL) = (starts_at IS NOT NULL)),
  CHECK ((cron IS NULL) = (duration_seconds IS NULL) AND (cron IS NULL) = (timezone IS NULL)),
  CHECK (ends_at IS NULL OR ends_at > starts_at),
  CHECK (duration_seconds IS NULL OR duration_seconds BETWEEN 60 AND 2678400),
  FOREIGN KEY (org_id, host_id) REFERENCES hosts (org_id, id) ON DELETE CASCADE,
  FOREIGN KEY (org_id, group_id) REFERENCES inventory_groups (org_id, id) ON DELETE CASCADE
);
CREATE INDEX maintenance_windows_org_idx ON maintenance_windows (org_id);

ALTER TABLE maintenance_windows ENABLE ROW LEVEL SECURITY;
ALTER TABLE maintenance_windows FORCE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON maintenance_windows
  USING (org_id = NULLIF(current_setting('pulsix.org_id', true), '')::uuid);
CREATE POLICY org_isolation_bypass ON maintenance_windows
  USING (current_setting('pulsix.org_id', true) IS NULL OR current_setting('pulsix.org_id', true) = '');

-- The maintenance guard's outcome (spec §6). Dropping and re-adding the
-- named CHECK only relaxes it; no row changes.
ALTER TABLE schedule_runs DROP CONSTRAINT schedule_runs_outcome_check;
ALTER TABLE schedule_runs ADD CONSTRAINT schedule_runs_outcome_check
  CHECK (outcome IN ('triggered', 'skipped_overlap', 'skipped_maintenance', 'missed', 'failed'));

-- +goose Down
-- NOT VALID: rows recorded as skipped_maintenance stay as they are; the
-- narrower CHECK applies to new rows only, so the rollback never fails and
-- deletes nothing.
ALTER TABLE schedule_runs DROP CONSTRAINT schedule_runs_outcome_check;
ALTER TABLE schedule_runs ADD CONSTRAINT schedule_runs_outcome_check
  CHECK (outcome IN ('triggered', 'skipped_overlap', 'missed', 'failed')) NOT VALID;
DROP TABLE maintenance_windows;
