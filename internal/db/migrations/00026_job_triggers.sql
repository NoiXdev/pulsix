-- +goose Up
-- Incoming webhook triggers (spec 2026-09-18 §3). The id (UUIDv7) is the
-- URL identifier; the hook endpoints resolve it without an org scope.
-- Exactly one target: a job template (template_run) or a git repository
-- (repo_sync, the 2.8 push webhook). auth and signature_header derive
-- from preset; secret is sealed with AAD trigger:<org>:<id>.
CREATE TABLE job_triggers (
  id               uuid PRIMARY KEY,
  org_id           uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  kind             text NOT NULL CHECK (kind IN ('template_run', 'repo_sync')),
  template_id      uuid,
  repo_id          uuid,
  name             text NOT NULL,
  enabled          boolean NOT NULL DEFAULT true,
  preset           text NOT NULL CHECK (preset IN ('pulsix', 'github', 'gitea', 'gitlab', 'bearer')),
  auth             text NOT NULL CHECK (auth IN ('hmac', 'token')),
  signature_header text NOT NULL,
  timestamp_header text NOT NULL DEFAULT '',
  secret           bytea NOT NULL,
  allow_cidrs      cidr[] NOT NULL DEFAULT '{}',
  mapping          jsonb NOT NULL DEFAULT '{}',
  on_active        text NOT NULL DEFAULT 'queue' CHECK (on_active IN ('queue', 'skip')),
  rate_limit       jsonb NOT NULL,
  created_by       uuid,
  created_at       timestamptz NOT NULL DEFAULT now(),
  updated_at       timestamptz NOT NULL DEFAULT now(),
  revision         bigint NOT NULL DEFAULT 1,
  UNIQUE (org_id, name),
  UNIQUE (org_id, id),
  CHECK ((kind = 'template_run') = (template_id IS NOT NULL AND repo_id IS NULL)),
  CHECK ((kind = 'repo_sync') = (repo_id IS NOT NULL AND template_id IS NULL)),
  FOREIGN KEY (org_id, template_id) REFERENCES job_templates (org_id, id) ON DELETE CASCADE,
  FOREIGN KEY (org_id, repo_id) REFERENCES git_repos (org_id, id) ON DELETE CASCADE
);
CREATE INDEX job_triggers_template_idx ON job_triggers (org_id, template_id) WHERE template_id IS NOT NULL;
CREATE INDEX job_triggers_repo_idx ON job_triggers (org_id, repo_id) WHERE repo_id IS NOT NULL;

ALTER TABLE job_triggers ENABLE ROW LEVEL SECURITY;
ALTER TABLE job_triggers FORCE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON job_triggers
  USING (org_id = NULLIF(current_setting('pulsix.org_id', true), '')::uuid);
CREATE POLICY org_isolation_bypass ON job_triggers
  USING (current_setting('pulsix.org_id', true) IS NULL OR current_setting('pulsix.org_id', true) = '');

-- One row per delivery; no request body is ever stored. Rejections are
-- throttled to one row per trigger, outcome and minute; the newest 500 per
-- trigger are kept. 'failed' (enqueue refused) extends the spec's list.
CREATE TABLE trigger_deliveries (
  id           uuid PRIMARY KEY,
  org_id       uuid NOT NULL,
  trigger_id   uuid NOT NULL,
  at           timestamptz NOT NULL,
  source_ip    text NOT NULL DEFAULT '',
  outcome      text NOT NULL CONSTRAINT trigger_deliveries_outcome_check CHECK (outcome IN (
                 'triggered', 'skipped_active', 'rejected_ip', 'rejected_auth',
                 'rate_limited', 'invalid_payload', 'disabled', 'failed')),
  reason       text NOT NULL DEFAULT '',
  job_group_id uuid,
  UNIQUE (org_id, id),
  FOREIGN KEY (org_id, trigger_id) REFERENCES job_triggers (org_id, id) ON DELETE CASCADE
);
CREATE INDEX trigger_deliveries_trigger_idx ON trigger_deliveries (org_id, trigger_id, at DESC);

ALTER TABLE trigger_deliveries ENABLE ROW LEVEL SECURITY;
ALTER TABLE trigger_deliveries FORCE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON trigger_deliveries
  USING (org_id = NULLIF(current_setting('pulsix.org_id', true), '')::uuid);
CREATE POLICY org_isolation_bypass ON trigger_deliveries
  USING (current_setting('pulsix.org_id', true) IS NULL OR current_setting('pulsix.org_id', true) = '');

-- The shared token bucket: one conditional UPDATE ... RETURNING takes a
-- token, so several managers share one limit per trigger.
CREATE TABLE trigger_buckets (
  trigger_id uuid PRIMARY KEY,
  org_id     uuid NOT NULL,
  tokens     double precision NOT NULL,
  updated_at timestamptz NOT NULL,
  FOREIGN KEY (org_id, trigger_id) REFERENCES job_triggers (org_id, id) ON DELETE CASCADE
);

ALTER TABLE trigger_buckets ENABLE ROW LEVEL SECURITY;
ALTER TABLE trigger_buckets FORCE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON trigger_buckets
  USING (org_id = NULLIF(current_setting('pulsix.org_id', true), '')::uuid);
CREATE POLICY org_isolation_bypass ON trigger_buckets
  USING (current_setting('pulsix.org_id', true) IS NULL OR current_setting('pulsix.org_id', true) = '');

-- +goose Down
DROP TABLE trigger_buckets;
DROP TABLE trigger_deliveries;
DROP TABLE job_triggers;
