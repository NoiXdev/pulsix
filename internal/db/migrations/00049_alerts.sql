-- +goose Up
-- Alerts (v0.4a, spec docs/superpowers/specs/2026-10-06-v0.4a-alerts-
-- design.md, "Data model"): the durable episode the rule engine (v0.3b-2)
-- never recorded. check_states (00046) only ever holds the current state
-- of a tuple; the moment it flips back to ok, the fact that it was ever
-- crit is gone except for the 90-day check_state_changes log. These two
-- tables are that record: one row per episode ("this host's disk was
-- critical from 14:03 to 14:51, Anna acknowledged it at 14:12"), not one
-- row per tick, and they carry no retention policy — an alert must outlive
-- the 90 days its underlying state-change rows get, because a year-old
-- alert standing after its timeline has rolled off is an accepted
-- consequence (the design's own "No retention policy"), not a bug.
--
-- The centre of this table is not a column but an index. Two concurrent
-- evaluation managers could otherwise both decide "no open alert exists
-- for this subject" and both insert one, and without a third party
-- enforcing it, "one episode, one alert" would be a convention the
-- evaluator has to honour rather than a fact the database guarantees.
-- alerts_one_open_per_subject below is a *partial* unique index — unique
-- only WHERE closed_at IS NULL — precisely so that history may hold any
-- number of closed alerts for the same subject while at most one of them
-- is ever open at a time; a plain (non-partial) unique constraint on the
-- same columns would make reopening a resolved subject impossible.
--
-- scope/host_id/check_key/item/metric are the subject, in the same two
-- shapes check_rules' own scope distinguishes (00045): 'tuple' is a single
-- (check_key, item, metric) reading, 'host' is the roll-up alert the host
-- itself carries when its worst tuple state is 'unknown' (see the design's
-- "Host alert" step). The two CHECKs tie scope to the three text columns
-- exactly the way check_item_decisions' own scope/host_id CHECK (00048)
-- ties a decision's scope to its host_id: a 'host' row carries none of
-- them, a 'tuple' row carries at least check_key and metric (item may
-- still be '' — not every check has an item, e.g. cpu).
--
-- opened_at is the state's own `since` (check_states.since), not the
-- tick's `now`: the episode began when the state began, not when the
-- ticker happened to come past and notice it — the same "stamp the
-- event's own time, not the observer's" distinction check_state_changes
-- draws for `at`.
--
-- severity/peak_severity both carry the enum check_states.state does
-- (minus 'ok', which an open alert can never be at): severity is the
-- alert's *current* reading, peak_severity only ever rises, recording the
-- worst the episode ever reached even after the tuple has since improved
-- to something less severe but not yet ok.
--
-- closed_at/closed_reason are tied together by CHECK, exactly like
-- check_states' own candidate_state/candidate_since pair (00046): a closed
-- alert always has both, an open one has neither. 'resolved' is the tuple
-- going back to ok; 'stale' is the sweep (a later task) closing an alert
-- whose tuple has stopped reporting altogether.
--
-- acknowledged_at/acknowledged_by/acknowledged_severity are likewise tied
-- by CHECK: an unacknowledged alert has none of the three.
-- acknowledged_severity is the severity *at the moment of acknowledgement*
-- — the design's decision 2, "an acknowledgement can be silently voided by
-- a rising severity": a later task compares the alert's current severity
-- against this column to decide whether the ack still holds, which is
-- exactly why it has to be stored rather than inferred from severity at
-- read time. acknowledged_by is ON DELETE SET NULL: deleting the user who
-- acknowledged an incident must not delete the record that it was
-- acknowledged, mirroring the same choice this migration makes below for
-- alert_events.actor_id, and the same reasoning check_states' own rule_id
-- (00046) already documents for "the fact survives the actor".
--
-- snoozed_until/snooze_until_change and was_flapping are plain flags a
-- later task's suppression logic and flap detection write; nothing here
-- constrains them beyond their types, because nothing downstream depends
-- on a database-level invariant between them.
--
-- The composite FK (org_id, host_id) REFERENCES hosts (org_id, id) is the
-- same "can only reference this organization's own host" guard every
-- other composite reference in this schema carries (check_states, 00046,
-- is the most recent example) — ON DELETE CASCADE matches check_states'
-- own choice: a deleted host takes its alerts with it, open or closed,
-- because an alert naming a host that no longer exists is a row no query
-- can ever usefully reach again.
CREATE TABLE alerts (
  id                    uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id                uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  host_id               uuid NOT NULL,
  scope                 text NOT NULL CHECK (scope IN ('tuple', 'host')),
  check_key             text NOT NULL DEFAULT '',
  item                  text NOT NULL DEFAULT '',
  metric                text NOT NULL DEFAULT '',
  opened_at             timestamptz NOT NULL,
  severity              text NOT NULL CHECK (severity IN ('warn', 'crit', 'unknown')),
  peak_severity         text NOT NULL CHECK (peak_severity IN ('warn', 'crit', 'unknown')),
  closed_at             timestamptz,
  closed_reason         text CHECK (closed_reason IN ('resolved', 'stale')),
  acknowledged_at       timestamptz,
  acknowledged_by       uuid REFERENCES users(id) ON DELETE SET NULL,
  acknowledged_severity text CHECK (acknowledged_severity IN ('warn', 'crit', 'unknown')),
  ack_note              text NOT NULL DEFAULT '',
  snoozed_until         timestamptz,
  snooze_until_change   boolean NOT NULL DEFAULT false,
  was_flapping          boolean NOT NULL DEFAULT false,
  CHECK ((closed_at IS NULL) = (closed_reason IS NULL)),
  CHECK ((acknowledged_at IS NULL) = (acknowledged_severity IS NULL)),
  CHECK (scope <> 'host' OR (check_key = '' AND item = '' AND metric = '')),
  CHECK (scope <> 'tuple' OR (check_key <> '' AND metric <> '')),
  FOREIGN KEY (org_id, host_id) REFERENCES hosts (org_id, id) ON DELETE CASCADE
);

CREATE UNIQUE INDEX alerts_one_open_per_subject
  ON alerts (host_id, scope, check_key, item, metric) WHERE closed_at IS NULL;
-- The open-alert feed (a fleet-wide "what's open right now" listing) reads
-- only open alerts, newest first, per organization; alerts_org_opened_idx
-- below serves the same shape for the full history view, which has to
-- include closed alerts too and cannot use a partial index.
CREATE INDEX alerts_open_idx ON alerts (org_id, opened_at DESC) WHERE closed_at IS NULL;
CREATE INDEX alerts_org_opened_idx ON alerts (org_id, opened_at DESC);

-- alert_events is the append-only history behind the current-state columns
-- above: every column on alerts answers "what is true right now", while
-- this table answers "what happened, in order" — the design's own answer
-- to decision 2, because an acknowledgement silently voided by a rising
-- severity would otherwise leave no trace that anyone ever held it, which
-- is exactly the question an operator asks after an incident.
-- v0.4b's escalation steps hang off this same table without a new one.
--
-- org_id here is deliberately **not** left for a join through alert_id:
-- row-level security evaluates one table's own row, it cannot reach
-- through a foreign key, so a policy that joined back to alerts to find
-- the organization would be both circular (RLS filtering the join's own
-- target) and slow (a join on every row this table will ever hold, for
-- every read). Carrying org_id here directly is the same policy shape
-- every other table in this schema uses, no special case.
--
-- actor_id is ON DELETE SET NULL, not CASCADE, for the same reason
-- alerts.acknowledged_by above is: deleting the user who performed an
-- action must not delete the record that the action happened.
CREATE TABLE alert_events (
  id       uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  org_id   uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  alert_id uuid NOT NULL REFERENCES alerts(id) ON DELETE CASCADE,
  at       timestamptz NOT NULL,
  kind     text NOT NULL CHECK (kind IN ('opened', 'severity_raised', 'acknowledged',
                                         'ack_voided', 'snoozed', 'unsnoozed', 'closed')),
  actor_id uuid REFERENCES users(id) ON DELETE SET NULL,
  payload  jsonb NOT NULL DEFAULT '{}'::jsonb
);
CREATE INDEX alert_events_alert_idx ON alert_events (alert_id, at);

-- The standard row-level security pair (see e.g. migration 00037) on both
-- tables — every org-owned table in this schema carries it.
ALTER TABLE alerts ENABLE ROW LEVEL SECURITY;
ALTER TABLE alerts FORCE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON alerts
  USING (org_id = NULLIF(current_setting('pulsix.org_id', true), '')::uuid);
CREATE POLICY org_isolation_bypass ON alerts
  USING (current_setting('pulsix.org_id', true) IS NULL OR current_setting('pulsix.org_id', true) = '');

ALTER TABLE alert_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE alert_events FORCE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON alert_events
  USING (org_id = NULLIF(current_setting('pulsix.org_id', true), '')::uuid);
CREATE POLICY org_isolation_bypass ON alert_events
  USING (current_setting('pulsix.org_id', true) IS NULL OR current_setting('pulsix.org_id', true) = '');

-- +goose Down
DROP TABLE alert_events;
DROP TABLE alerts;
