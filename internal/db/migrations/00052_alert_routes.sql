-- +goose Up
-- v0.4b (spec docs/superpowers/specs/2026-10-12-v0.4b-alert-routes-
-- design.md, "Data model"): routes say which alert reaches which
-- notification channel. Routes ADD UP rather than override — backlog
-- 4.1b's own ruling — so there is no precedence column here and no
-- tie-break: every matching route contributes its channels, and the
-- union is taken in Go.
--
-- The scope target is two typed columns rather than one polymorphic
-- scope_id, because the spec promises that a route dies with the group
-- or host it points at and one column cannot carry a foreign key to two
-- tables. Both targets already offer UNIQUE (org_id, id) — monitoring
-- groups since 00042, hosts since 00014 — so the cascade is real rather
-- than something the application has to remember. The CHECK is what
-- keeps the pair honest: exactly one target for a scoped route, none at
-- all for an organization-wide one. The Go side still sees a single
-- optional ScopeID; the store maps it to the right column.
CREATE TABLE alert_routes (
  id             uuid PRIMARY KEY,
  org_id         uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  name           text NOT NULL,
  enabled        boolean NOT NULL DEFAULT true,
  scope          text NOT NULL CHECK (scope IN ('org', 'monitoring_group', 'host')),
  scope_group_id uuid,
  scope_host_id  uuid,
  revision       bigint NOT NULL DEFAULT 1,
  created_at     timestamptz NOT NULL,
  updated_at     timestamptz NOT NULL,
  UNIQUE (org_id, name),
  UNIQUE (org_id, id),
  FOREIGN KEY (org_id, scope_group_id) REFERENCES monitoring_groups (org_id, id) ON DELETE CASCADE,
  FOREIGN KEY (org_id, scope_host_id) REFERENCES hosts (org_id, id) ON DELETE CASCADE,
  CONSTRAINT alert_routes_scope_target CHECK (
    (scope = 'org'              AND scope_group_id IS NULL     AND scope_host_id IS NULL) OR
    (scope = 'monitoring_group' AND scope_group_id IS NOT NULL AND scope_host_id IS NULL) OR
    (scope = 'host'             AND scope_group_id IS NULL     AND scope_host_id IS NOT NULL)
  )
);

-- The three selection tables. One row per chosen value, rather than an
-- array column, so that a foreign key can hold for the channel and so
-- that "which routes send to this channel" is an index lookup.
CREATE TABLE alert_route_severities (
  org_id   uuid NOT NULL,
  route_id uuid NOT NULL,
  severity text NOT NULL CHECK (severity IN ('warn', 'crit', 'unknown')),
  PRIMARY KEY (route_id, severity),
  FOREIGN KEY (org_id, route_id) REFERENCES alert_routes (org_id, id) ON DELETE CASCADE
);

CREATE TABLE alert_route_events (
  org_id   uuid NOT NULL,
  route_id uuid NOT NULL,
  kind     text NOT NULL CHECK (kind IN ('opened', 'severity_raised', 'acknowledged',
                                         'ack_voided', 'snoozed', 'unsnoozed', 'closed')),
  PRIMARY KEY (route_id, kind),
  FOREIGN KEY (org_id, route_id) REFERENCES alert_routes (org_id, id) ON DELETE CASCADE
);

-- The composite foreign key is what keeps a route from pointing at
-- another organization's channel: notification_channels already carries
-- UNIQUE (org_id, id) for exactly this.
CREATE TABLE alert_route_channels (
  org_id     uuid NOT NULL,
  route_id   uuid NOT NULL,
  channel_id uuid NOT NULL,
  PRIMARY KEY (route_id, channel_id),
  FOREIGN KEY (org_id, route_id) REFERENCES alert_routes (org_id, id) ON DELETE CASCADE,
  FOREIGN KEY (org_id, channel_id) REFERENCES notification_channels (org_id, id) ON DELETE CASCADE
);

CREATE INDEX alert_route_channels_channel_idx ON alert_route_channels (org_id, channel_id);

-- The ledger, and the reason there is no cursor. alert_events.id is a
-- gen_random_uuid() and therefore not monotonic, and a watermark over
-- `at` would lose an event whose transaction took its timestamp early
-- and committed late. One row per decided event instead: the forward
-- pass is an anti-join against this table, so ordering and overtaking
-- both stop mattering and the primary key IS the idempotency.
--
-- 'expired' is terminal: an event that was suppressed and whose alert
-- closed before the suppression lapsed can never be caught up, because
-- the catch-up pass requires an open alert. Without it such a row would
-- sit in that pass's working set forever.
CREATE TABLE alert_notify_state (
  org_id         uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  alert_event_id uuid PRIMARY KEY REFERENCES alert_events(id) ON DELETE CASCADE,
  state          text NOT NULL CHECK (state IN ('sent', 'suppressed', 'unrouted', 'expired')),
  decided_at     timestamptz NOT NULL
);

-- The catch-up pass reads only suppressed rows; a partial index keeps it
-- cheap as the ledger grows with history.
CREATE INDEX alert_notify_state_pending_idx
  ON alert_notify_state (org_id) WHERE state = 'suppressed';

-- FORCE alongside ENABLE on every table, not ENABLE alone: the brief's own
-- text omitted FORCE, but every org-owned table in this schema carries
-- both (00049's own pair is the model named above), and
-- TestEveryOrgOwnedTableForcesRowLevelSecurity (rls_catalog_test.go) scans
-- every table with an org_id column and fails any that lacks it — without
-- FORCE, a session connected as the table owner would bypass RLS
-- entirely, which is exactly the gap that test exists to catch.
ALTER TABLE alert_routes ENABLE ROW LEVEL SECURITY;
ALTER TABLE alert_routes FORCE ROW LEVEL SECURITY;
ALTER TABLE alert_route_severities ENABLE ROW LEVEL SECURITY;
ALTER TABLE alert_route_severities FORCE ROW LEVEL SECURITY;
ALTER TABLE alert_route_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE alert_route_events FORCE ROW LEVEL SECURITY;
ALTER TABLE alert_route_channels ENABLE ROW LEVEL SECURITY;
ALTER TABLE alert_route_channels FORCE ROW LEVEL SECURITY;
ALTER TABLE alert_notify_state ENABLE ROW LEVEL SECURITY;
ALTER TABLE alert_notify_state FORCE ROW LEVEL SECURITY;

-- +goose StatementBegin
DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['alert_routes', 'alert_route_severities', 'alert_route_events',
                           'alert_route_channels', 'alert_notify_state']
  LOOP
    EXECUTE format('CREATE POLICY org_isolation ON %I USING (org_id = NULLIF(current_setting(''pulsix.org_id'', true), '''')::uuid)', t);
    EXECUTE format('CREATE POLICY org_isolation_bypass ON %I USING (current_setting(''pulsix.org_id'', true) IS NULL OR current_setting(''pulsix.org_id'', true) = '''')', t);
  END LOOP;
END $$;
-- +goose StatementEnd

-- The delivery table's event_kind enum gains the alert batch. Additive,
-- like every migration here: rows written before this keep meaning what
-- they meant.
ALTER TABLE notification_deliveries
  DROP CONSTRAINT notification_deliveries_event_kind_check;
ALTER TABLE notification_deliveries
  ADD CONSTRAINT notification_deliveries_event_kind_check
  CHECK (event_kind IN ('run.finished', 'schedule.skipped', 'schedule.failed',
                        'digest.weekly', 'test', 'alert.batch'));

-- +goose Down
ALTER TABLE notification_deliveries
  DROP CONSTRAINT notification_deliveries_event_kind_check;
ALTER TABLE notification_deliveries
  ADD CONSTRAINT notification_deliveries_event_kind_check
  CHECK (event_kind IN ('run.finished', 'schedule.skipped', 'schedule.failed',
                        'digest.weekly', 'test'));
DROP TABLE alert_notify_state;
DROP TABLE alert_route_channels;
DROP TABLE alert_route_events;
DROP TABLE alert_route_severities;
DROP TABLE alert_routes;
