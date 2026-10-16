-- +goose Up

-- alerts predates the composite-key convention for its own referents, the
-- same gap 00053's own comment found and closed for alert_events: it
-- carries PRIMARY KEY (id) and no UNIQUE (org_id, id), so there is nothing
-- yet for alert_escalations' composite foreign key below to point at.
-- Adding it is free — (org_id, id) is unique the moment id alone is — and
-- it is additive for the same reason 00053 gave: no existing row can
-- violate a uniqueness that id alone already guaranteed.
--
-- This ADD CONSTRAINT takes an ACCESS EXCLUSIVE lock on alerts for as
-- long as building the backing unique index takes — the same cost 00053
-- paid silently for alert_events. alerts carries no retention policy
-- (00049's own "No retention policy") and only grows, so on a
-- long-running installation this table can already be large when this
-- migration runs: an operator applying it should expect the lock, not be
-- surprised by it.
ALTER TABLE alerts ADD CONSTRAINT alerts_org_id_key UNIQUE (org_id, id);

-- A route's escalation ladder (v0.4d, backlog 4.9). Position 0 is NOT
-- stored: it is the route's own channels or rotation, delivered at once,
-- exactly as before this migration. That is what lets every existing
-- route keep its behaviour without being migrated.
CREATE TABLE alert_route_steps (
  org_id        uuid NOT NULL,
  route_id      uuid NOT NULL,
  position      integer NOT NULL CHECK (position >= 1),
  -- Relative to the previous step's real firing, never absolute from the
  -- alert (spec decision 3) — which is why a chain can never arrive all
  -- at once after a long silence.
  delay_seconds integer NOT NULL CHECK (delay_seconds > 0),
  -- A step names a rotation or a fixed channel set, the same two sources
  -- the route itself offers. NULL here means "see
  -- alert_route_step_channels".
  rotation_id   uuid,
  PRIMARY KEY (route_id, position),
  -- (route_id, position) alone is enough to order and look up a step, so
  -- it stays the primary key. But it carries no org_id, so a child's
  -- foreign key against it alone would never check the child's own
  -- org_id against the step it names — the exact shape 00053 found and
  -- closed for alert_notify_state against alert_events. This triple is
  -- what alert_route_step_channels' own foreign key below references
  -- instead, so a channel row's org_id is checked against its step, not
  -- only against its channel.
  UNIQUE (org_id, route_id, position),
  FOREIGN KEY (org_id, route_id) REFERENCES alert_routes (org_id, id) ON DELETE CASCADE,
  FOREIGN KEY (org_id, rotation_id) REFERENCES oncall_rotations (org_id, id) ON DELETE SET NULL (rotation_id)
);

CREATE TABLE alert_route_step_channels (
  org_id     uuid NOT NULL,
  route_id   uuid NOT NULL,
  position   integer NOT NULL,
  channel_id uuid NOT NULL,
  PRIMARY KEY (route_id, position, channel_id),
  FOREIGN KEY (org_id, route_id, position) REFERENCES alert_route_steps (org_id, route_id, position) ON DELETE CASCADE,
  FOREIGN KEY (org_id, channel_id) REFERENCES notification_channels (org_id, id) ON DELETE CASCADE
);

-- The anti-join ledger. A step is due when it has no row here; writing
-- the row is what makes it fired. Position 0 is the base: the moment the
-- route first really delivered for this alert, observed rather than
-- computed from a cron expression (spec decision 3).
CREATE TABLE alert_escalations (
  org_id   uuid NOT NULL,
  alert_id uuid NOT NULL,
  route_id uuid NOT NULL,
  position integer NOT NULL CHECK (position >= 0),
  fired_at timestamptz NOT NULL,
  PRIMARY KEY (alert_id, route_id, position),
  FOREIGN KEY (org_id, alert_id) REFERENCES alerts (org_id, id) ON DELETE CASCADE,
  FOREIGN KEY (org_id, route_id) REFERENCES alert_routes (org_id, id) ON DELETE CASCADE
);

-- The pass asks "which open alerts have an unfired step", so the index
-- leads with the alert.
CREATE INDEX alert_escalations_alert_idx ON alert_escalations (org_id, alert_id, route_id, position);

-- 'escalated' is the eighth kind; the other seven are 00049's own. This
-- is a DROP and ADD of the constraint rather than an ALTER, because
-- Postgres has no "widen a CHECK" — but it is additive in effect: every
-- existing row satisfies the new set, so nothing is invalidated.
ALTER TABLE alert_events DROP CONSTRAINT alert_events_kind_check;
ALTER TABLE alert_events ADD CONSTRAINT alert_events_kind_check
  CHECK (kind IN ('opened', 'severity_raised', 'acknowledged', 'ack_voided',
                  'snoozed', 'unsnoozed', 'closed', 'escalated'));

-- +goose StatementBegin
DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['alert_route_steps', 'alert_route_step_channels', 'alert_escalations'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY org_isolation ON %I USING (org_id = NULLIF(current_setting(''pulsix.org_id'', true), '''')::uuid)', t);
    EXECUTE format('CREATE POLICY org_isolation_bypass ON %I USING (current_setting(''pulsix.org_id'', true) IS NULL OR current_setting(''pulsix.org_id'', true) = '''')', t);
  END LOOP;
END $$;
-- +goose StatementEnd

-- +goose Down
DROP TABLE alert_escalations;
DROP TABLE alert_route_step_channels;
DROP TABLE alert_route_steps;
ALTER TABLE alert_events DROP CONSTRAINT alert_events_kind_check;
ALTER TABLE alert_events ADD CONSTRAINT alert_events_kind_check
  CHECK (kind IN ('opened', 'severity_raised', 'acknowledged', 'ack_voided',
                  'snoozed', 'unsnoozed', 'closed'));
ALTER TABLE alerts DROP CONSTRAINT alerts_org_id_key;
