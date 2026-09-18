-- +goose Up
-- Schedules for job templates (spec 2026-09-18 §2). Several schedules per
-- template; deleting a template (API only for now) deletes its schedules.
-- simple keeps the form input so it round-trips; cron always holds the
-- expression the ticker evaluates (simple mode compiles to it on save).
-- limit / extra_vars are per-run overrides; secret values inside
-- extra_vars are sealed with AAD schedule:<org>:<id>. next_run_at is NULL
-- while paused. revision drives If-Match; the ticker never bumps it.
CREATE TABLE schedules (
  id           uuid PRIMARY KEY,
  org_id       uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  template_id  uuid NOT NULL,
  name         text NOT NULL,
  mode         text NOT NULL CHECK (mode IN ('cron', 'simple')),
  cron         text NOT NULL,
  simple       jsonb,
  timezone     text NOT NULL,
  guards       jsonb NOT NULL,
  "limit"      text,
  extra_vars   jsonb,
  paused       boolean NOT NULL DEFAULT false,
  next_run_at  timestamptz,
  last_run_at  timestamptz,
  created_by   uuid,
  created_at   timestamptz NOT NULL DEFAULT now(),
  updated_at   timestamptz NOT NULL DEFAULT now(),
  revision     bigint NOT NULL DEFAULT 1,
  UNIQUE (org_id, name),
  UNIQUE (org_id, id),
  CHECK ((mode = 'simple') = (simple IS NOT NULL)),
  FOREIGN KEY (org_id, template_id) REFERENCES job_templates (org_id, id) ON DELETE CASCADE
);
-- The ticker's due scan (bypass scope, every org) and the per-template list.
CREATE INDEX schedules_due_idx ON schedules (next_run_at) WHERE NOT paused;
CREATE INDEX schedules_template_idx ON schedules (org_id, template_id);

ALTER TABLE schedules ENABLE ROW LEVEL SECURITY;
ALTER TABLE schedules FORCE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON schedules
  USING (org_id = NULLIF(current_setting('pulsix.org_id', true), '')::uuid);
CREATE POLICY org_isolation_bypass ON schedules
  USING (current_setting('pulsix.org_id', true) IS NULL OR current_setting('pulsix.org_id', true) = '');

-- One row per decision of the ticker (or "run now"). The newest 500 per
-- schedule are kept (the ticker trims). The outcome CHECK is named so
-- v0.2d can widen it for skipped_maintenance.
CREATE TABLE schedule_runs (
  id           uuid PRIMARY KEY,
  org_id       uuid NOT NULL,
  schedule_id  uuid NOT NULL,
  due_at       timestamptz NOT NULL,
  decided_at   timestamptz NOT NULL,
  outcome      text NOT NULL CONSTRAINT schedule_runs_outcome_check
                 CHECK (outcome IN ('triggered', 'skipped_overlap', 'missed', 'failed')),
  reason       text NOT NULL DEFAULT '',
  job_group_id uuid,
  UNIQUE (org_id, id),
  FOREIGN KEY (org_id, schedule_id) REFERENCES schedules (org_id, id) ON DELETE CASCADE
);
CREATE INDEX schedule_runs_schedule_idx ON schedule_runs (org_id, schedule_id, decided_at DESC);

ALTER TABLE schedule_runs ENABLE ROW LEVEL SECURITY;
ALTER TABLE schedule_runs FORCE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON schedule_runs
  USING (org_id = NULLIF(current_setting('pulsix.org_id', true), '')::uuid);
CREATE POLICY org_isolation_bypass ON schedule_runs
  USING (current_setting('pulsix.org_id', true) IS NULL OR current_setting('pulsix.org_id', true) = '');

-- +goose Down
DROP TABLE schedule_runs;
DROP TABLE schedules;
