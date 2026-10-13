-- +goose Up
-- v0.4c (spec docs/superpowers/specs/2026-10-13-v0.4c-notification-
-- schedules-design.md): a route gains a delivery window — when it may
-- deliver — and an on-call rotation that chooses its channel by the
-- clock.
--
-- The rotation stores a cycle, not shifts. period_days plus anchor
-- answers "who is on call" for any instant by arithmetic, so the roster
-- never runs out and needs no generator to refill it (decision 5). The
-- anchor IS the first handover, date and time together: a separate
-- handover-time column would be one fact stored twice, and the first
-- disagreement between them would have no right answer.
CREATE TABLE oncall_rotations (
  id          uuid PRIMARY KEY,
  org_id      uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  name        text NOT NULL,
  period_days integer NOT NULL CHECK (period_days > 0),
  anchor      timestamptz NOT NULL,
  timezone    text NOT NULL,
  revision    bigint NOT NULL DEFAULT 1,
  created_at  timestamptz NOT NULL,
  updated_at  timestamptz NOT NULL,
  UNIQUE (org_id, name),
  UNIQUE (org_id, id)
);

-- position orders the cycle. UNIQUE (rotation_id, position) is what makes
-- "the channel at index i" a single row rather than a guess.
CREATE TABLE oncall_rotation_channels (
  org_id      uuid NOT NULL,
  rotation_id uuid NOT NULL,
  position    integer NOT NULL CHECK (position >= 0),
  channel_id  uuid NOT NULL,
  PRIMARY KEY (rotation_id, position),
  FOREIGN KEY (org_id, rotation_id) REFERENCES oncall_rotations (org_id, id) ON DELETE CASCADE,
  FOREIGN KEY (org_id, channel_id) REFERENCES notification_channels (org_id, id) ON DELETE CASCADE
);

-- A stand-in: for this span, this channel instead of whatever the cycle
-- says. starts_at < ends_at is enforced here rather than left to the
-- application, because a zero-length span reads in the GUI as a working
-- stand-in while matching nothing at all.
CREATE TABLE oncall_exceptions (
  id          uuid PRIMARY KEY,
  org_id      uuid NOT NULL,
  rotation_id uuid NOT NULL,
  starts_at   timestamptz NOT NULL,
  ends_at     timestamptz NOT NULL,
  channel_id  uuid NOT NULL,
  note        text NOT NULL DEFAULT '',
  created_at  timestamptz NOT NULL,
  FOREIGN KEY (org_id, rotation_id) REFERENCES oncall_rotations (org_id, id) ON DELETE CASCADE,
  FOREIGN KEY (org_id, channel_id) REFERENCES notification_channels (org_id, id) ON DELETE CASCADE,
  CONSTRAINT oncall_exceptions_span CHECK (starts_at < ends_at)
);

CREATE INDEX oncall_exceptions_rotation_idx ON oncall_exceptions (org_id, rotation_id, starts_at);

-- The route's delivery window, in maintenance_windows' own shape so that
-- maintenance.ActiveAt applies with no translation layer (decision 2).
-- The three columns are one fact: a cron without a duration is not a
-- window, it is an instant.
--
-- ON DELETE SET NULL (rotation_id) names the column explicitly. A plain
-- ON DELETE SET NULL on a composite foreign key nulls EVERY referencing
-- column, org_id included — org_id on alert_routes is NOT NULL, so a
-- bare SET NULL made deleting a rotation fail outright rather than
-- clear the route's channel source as the design promises. The
-- column-list form (Postgres 15+) is what limits the clear to
-- rotation_id alone.
ALTER TABLE alert_routes
  ADD COLUMN window_cron             text,
  ADD COLUMN window_duration_seconds integer CHECK (window_duration_seconds > 0),
  ADD COLUMN window_timezone         text,
  ADD COLUMN rotation_id             uuid,
  ADD CONSTRAINT alert_routes_window_complete CHECK (
    (window_cron IS NULL AND window_duration_seconds IS NULL AND window_timezone IS NULL) OR
    (window_cron IS NOT NULL AND window_duration_seconds IS NOT NULL AND window_timezone IS NOT NULL)
  ),
  ADD CONSTRAINT alert_routes_rotation_fkey
    FOREIGN KEY (org_id, rotation_id) REFERENCES oncall_rotations (org_id, id) ON DELETE SET NULL (rotation_id);

-- The fourth selection table: severities that cross a closed window
-- (decision 3). Deliberately a separate table rather than a flag on
-- alert_route_severities, because it is a different question — "does the
-- route fire on this" versus "does it fire even now".
CREATE TABLE alert_route_breakthrough_severities (
  org_id   uuid NOT NULL,
  route_id uuid NOT NULL,
  severity text NOT NULL CHECK (severity IN ('warn', 'crit', 'unknown')),
  PRIMARY KEY (route_id, severity),
  FOREIGN KEY (org_id, route_id) REFERENCES alert_routes (org_id, id) ON DELETE CASCADE
);

-- The fifth ledger state. Holding for a delivery window is structurally
-- what 'suppressed' already does — hold, re-check, send when the alert is
-- still open — but it is a different WHY, and v0.4b's decision 8 made
-- "why did nothing arrive" a queryable fact rather than an absence.
ALTER TABLE alert_notify_state
  DROP CONSTRAINT alert_notify_state_state_check;
ALTER TABLE alert_notify_state
  ADD CONSTRAINT alert_notify_state_state_check
  CHECK (state IN ('sent', 'suppressed', 'unrouted', 'expired', 'deferred'));

-- The catch-up pass now reads two states, so the partial index must cover
-- both or it stops being used.
DROP INDEX alert_notify_state_pending_idx;
CREATE INDEX alert_notify_state_pending_idx
  ON alert_notify_state (org_id) WHERE state IN ('suppressed', 'deferred');

ALTER TABLE oncall_rotations ENABLE ROW LEVEL SECURITY;
ALTER TABLE oncall_rotations FORCE ROW LEVEL SECURITY;
ALTER TABLE oncall_rotation_channels ENABLE ROW LEVEL SECURITY;
ALTER TABLE oncall_rotation_channels FORCE ROW LEVEL SECURITY;
ALTER TABLE oncall_exceptions ENABLE ROW LEVEL SECURITY;
ALTER TABLE oncall_exceptions FORCE ROW LEVEL SECURITY;
ALTER TABLE alert_route_breakthrough_severities ENABLE ROW LEVEL SECURITY;
ALTER TABLE alert_route_breakthrough_severities FORCE ROW LEVEL SECURITY;

-- +goose StatementBegin
DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['oncall_rotations', 'oncall_rotation_channels',
                           'oncall_exceptions', 'alert_route_breakthrough_severities']
  LOOP
    EXECUTE format('CREATE POLICY org_isolation ON %I USING (org_id = NULLIF(current_setting(''pulsix.org_id'', true), '''')::uuid)', t);
    EXECUTE format('CREATE POLICY org_isolation_bypass ON %I USING (current_setting(''pulsix.org_id'', true) IS NULL OR current_setting(''pulsix.org_id'', true) = '''')', t);
  END LOOP;
END $$;
-- +goose StatementEnd

-- +goose Down
DROP INDEX alert_notify_state_pending_idx;
CREATE INDEX alert_notify_state_pending_idx
  ON alert_notify_state (org_id) WHERE state = 'suppressed';
ALTER TABLE alert_notify_state DROP CONSTRAINT alert_notify_state_state_check;
ALTER TABLE alert_notify_state
  ADD CONSTRAINT alert_notify_state_state_check
  CHECK (state IN ('sent', 'suppressed', 'unrouted', 'expired'));
DROP TABLE alert_route_breakthrough_severities;
ALTER TABLE alert_routes
  DROP CONSTRAINT alert_routes_rotation_fkey,
  DROP CONSTRAINT alert_routes_window_complete,
  DROP COLUMN rotation_id,
  DROP COLUMN window_timezone,
  DROP COLUMN window_duration_seconds,
  DROP COLUMN window_cron;
DROP TABLE oncall_exceptions;
DROP TABLE oncall_rotation_channels;
DROP TABLE oncall_rotations;
