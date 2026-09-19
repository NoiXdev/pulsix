-- +goose Up
-- Notification channels, their delivery queue and log, schedule notification
-- rules and the weekly digest (spec 2026-09-18 §5, §7). v0.4 extends the
-- same tables with more channel kinds, routes and escalation.

-- config is the kind's settings; for webhooks the URL, every header value
-- and the signing secret are sealed with AAD channel:<org>:<id>.
CREATE TABLE notification_channels (
  id          uuid PRIMARY KEY,
  org_id      uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  kind        text NOT NULL CHECK (kind IN ('email', 'webhook')),
  name        text NOT NULL,
  config      jsonb NOT NULL,
  enabled     boolean NOT NULL DEFAULT true,
  created_by  uuid,
  created_at  timestamptz NOT NULL DEFAULT now(),
  updated_at  timestamptz NOT NULL DEFAULT now(),
  revision    bigint NOT NULL DEFAULT 1,
  UNIQUE (org_id, name),
  UNIQUE (org_id, id)
);

ALTER TABLE notification_channels ENABLE ROW LEVEL SECURITY;
ALTER TABLE notification_channels FORCE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON notification_channels
  USING (org_id = NULLIF(current_setting('pulsix.org_id', true), '')::uuid);
CREATE POLICY org_isolation_bypass ON notification_channels
  USING (current_setting('pulsix.org_id', true) IS NULL OR current_setting('pulsix.org_id', true) = '');

-- One row per message and channel. (org_id, event_key, channel_id) is the
-- idempotency key: an event is enqueued at most once per channel however
-- often its source retries. payload is the rendered-from event (never log
-- output, variables or secrets). A pending row whose locked_until lies in
-- the future is being sent by a manager; attempts counts claims.
CREATE TABLE notification_deliveries (
  id              uuid PRIMARY KEY,
  org_id          uuid NOT NULL,
  channel_id      uuid NOT NULL,
  event_key       text NOT NULL,
  event_kind      text NOT NULL CONSTRAINT notification_deliveries_event_kind_check
                    CHECK (event_kind IN ('run.finished', 'schedule.skipped', 'schedule.failed', 'digest.weekly', 'test')),
  payload         jsonb NOT NULL,
  status          text NOT NULL CONSTRAINT notification_deliveries_status_check
                    CHECK (status IN ('pending', 'sent', 'failed')),
  attempts        integer NOT NULL DEFAULT 0,
  next_attempt_at timestamptz NOT NULL,
  locked_until    timestamptz,
  last_error      text NOT NULL DEFAULT '',
  created_at      timestamptz NOT NULL,
  sent_at         timestamptz,
  UNIQUE (org_id, event_key, channel_id),
  UNIQUE (org_id, id),
  FOREIGN KEY (org_id, channel_id) REFERENCES notification_channels (org_id, id) ON DELETE CASCADE
);
-- The dispatcher's due scan (bypass scope, every org) and the per-channel log.
CREATE INDEX notification_deliveries_due_idx ON notification_deliveries (next_attempt_at) WHERE status = 'pending';
CREATE INDEX notification_deliveries_channel_idx ON notification_deliveries (org_id, channel_id, created_at DESC);

ALTER TABLE notification_deliveries ENABLE ROW LEVEL SECURITY;
ALTER TABLE notification_deliveries FORCE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON notification_deliveries
  USING (org_id = NULLIF(current_setting('pulsix.org_id', true), '')::uuid);
CREATE POLICY org_isolation_bypass ON notification_deliveries
  USING (current_setting('pulsix.org_id', true) IS NULL OR current_setting('pulsix.org_id', true) = '');

-- A schedule's notify_rules, merged per channel: one row per (schedule,
-- channel) with every event category that channel is told about, so a run
-- yields at most one message per channel by construction.
CREATE TABLE schedule_notifications (
  org_id      uuid NOT NULL,
  schedule_id uuid NOT NULL,
  channel_id  uuid NOT NULL,
  events      text[] NOT NULL CHECK (cardinality(events) > 0 AND events <@ ARRAY['failure', 'change', 'success', 'skipped']),
  PRIMARY KEY (org_id, schedule_id, channel_id),
  FOREIGN KEY (org_id, schedule_id) REFERENCES schedules (org_id, id) ON DELETE CASCADE,
  FOREIGN KEY (org_id, channel_id) REFERENCES notification_channels (org_id, id) ON DELETE CASCADE
);

ALTER TABLE schedule_notifications ENABLE ROW LEVEL SECURITY;
ALTER TABLE schedule_notifications FORCE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON schedule_notifications
  USING (org_id = NULLIF(current_setting('pulsix.org_id', true), '')::uuid);
CREATE POLICY org_isolation_bypass ON schedule_notifications
  USING (current_setting('pulsix.org_id', true) IS NULL OR current_setting('pulsix.org_id', true) = '');

-- The weekly digest, one row per organization. weekday is 0 (Sunday) to 6,
-- at_time HH:MM in timezone. next_run_at is NULL while disabled.
CREATE TABLE notification_digests (
  org_id      uuid PRIMARY KEY REFERENCES organizations(id) ON DELETE CASCADE,
  enabled     boolean NOT NULL,
  weekday     smallint NOT NULL CHECK (weekday BETWEEN 0 AND 6),
  at_time     text NOT NULL,
  timezone    text NOT NULL,
  next_run_at timestamptz,
  last_period text NOT NULL DEFAULT '',
  updated_at  timestamptz NOT NULL DEFAULT now(),
  CHECK (enabled = (next_run_at IS NOT NULL))
);
CREATE INDEX notification_digests_due_idx ON notification_digests (next_run_at) WHERE enabled;

ALTER TABLE notification_digests ENABLE ROW LEVEL SECURITY;
ALTER TABLE notification_digests FORCE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON notification_digests
  USING (org_id = NULLIF(current_setting('pulsix.org_id', true), '')::uuid);
CREATE POLICY org_isolation_bypass ON notification_digests
  USING (current_setting('pulsix.org_id', true) IS NULL OR current_setting('pulsix.org_id', true) = '');

CREATE TABLE notification_digest_channels (
  org_id     uuid NOT NULL REFERENCES notification_digests (org_id) ON DELETE CASCADE,
  channel_id uuid NOT NULL,
  PRIMARY KEY (org_id, channel_id),
  FOREIGN KEY (org_id, channel_id) REFERENCES notification_channels (org_id, id) ON DELETE CASCADE
);

ALTER TABLE notification_digest_channels ENABLE ROW LEVEL SECURITY;
ALTER TABLE notification_digest_channels FORCE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON notification_digest_channels
  USING (org_id = NULLIF(current_setting('pulsix.org_id', true), '')::uuid);
CREATE POLICY org_isolation_bypass ON notification_digest_channels
  USING (current_setting('pulsix.org_id', true) IS NULL OR current_setting('pulsix.org_id', true) = '');

-- +goose Down
DROP TABLE notification_digest_channels;
DROP TABLE notification_digests;
DROP TABLE schedule_notifications;
DROP TABLE notification_deliveries;
DROP TABLE notification_channels;
