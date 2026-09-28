-- +goose Up
-- Sample storage (v0.3b-1, spec docs/superpowers/specs/2026-09-27-v0.3b-
-- checks-design.md, "Data model" and "Sample delivery"): the time-series
-- half of the check pipeline. check_definitions (00043) says what is
-- measured; these two tables hold what was actually observed.
--
-- check_samples is a TimescaleDB hypertable on `at`, one-day chunks —
-- create_hypertable's own defaults create the time-column index, the same
-- way job_events (00018) is set up. Samples are keyed by `check_key` (text),
-- never by check_definitions.id: a definition can be edited or deleted
-- without orphaning or rewriting history, and the key is what an operator
-- recognises on a graph (internal/checks.Observation's own doc comment
-- explains why RecordObservations therefore never consults
-- check_definitions at all). `item` is `''` for whole-host metrics — never
-- NULL, so equality comparisons in WHERE clauses stay simple.
--
-- Exactly one of value/label is set per sample: a numeric metric (cpu,
-- memory, disk usage) carries value; a discrete one (a service's
-- active/failed state) carries label. Mapping the discrete case onto value
-- would be an invention the rule engine (v0.3b-2) would have to undo.
--
-- The composite foreign key (org_id, host_id) REFERENCES hosts (org_id, id)
-- relies on hosts' own UNIQUE (org_id, id) from migration 00014, the same
-- guard check_definitions' host binding (00043) already uses — a sample can
-- never reference a host belonging to another organization. It also gives
-- internal/checks.SQLStore.RecordObservations a real constraint to check a
-- batch's host ids against (a host that was deleted between the check and
-- the insert would still fail this FK; the day-to-day "unknown host"
-- rejection queries the hosts table directly, ahead of the insert, so one
-- bad host in a batch is counted and skipped rather than failing the
-- transaction for every other observation in it).
--
-- Retention: a 90-day, instance-wide retention policy, set here rather than
-- left for an operator to configure — per-organization retention and
-- continuous aggregates are both explicitly deferred (see the design doc's
-- own "Retention" section).
CREATE TABLE check_samples (
  at        timestamptz NOT NULL,
  org_id    uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  host_id   uuid NOT NULL,
  check_key text NOT NULL,
  item      text NOT NULL DEFAULT '',
  metric    text NOT NULL,
  value     double precision,
  label     text,
  CHECK ((value IS NOT NULL) <> (label IS NOT NULL)),
  FOREIGN KEY (org_id, host_id) REFERENCES hosts (org_id, id) ON DELETE CASCADE
);
SELECT create_hypertable('check_samples', 'at', chunk_time_interval => INTERVAL '1 day');
SELECT add_retention_policy('check_samples', INTERVAL '90 days');
CREATE INDEX check_samples_lookup_idx ON check_samples (org_id, host_id, check_key, item, metric, at DESC);

-- check_current is the latest known value of one (host, check_key, item,
-- metric) tuple, upserted on every ingest — the host page's monitoring
-- section reads this instead of scanning the hypertable. The primary key
-- is exactly (host_id, check_key, item, metric), per the brief: no org_id
-- in it (host_id already determines the organization through the FK
-- below), and no `at`, since there is only ever one current row per tuple.
--
-- Nothing ever deletes a check_current row: when a mount is unmounted or a
-- service uninstalled, the agent simply stops sending that (check_key,
-- item, metric) — there is no "disappearance" event to react to. The row
-- is left exactly as it was last reported, and its own `at` going stale is
-- what tells the host page (a later task, three-interval threshold) to
-- render it as stale rather than current — internal/checks.Current's own
-- doc comment states this so it is not "fixed" into a delete later.
--
-- The CHECK is the one the brief calls out as easy to get wrong: a sample
-- row (error IS NULL) has exactly one of value/label; an error row (error
-- IS NOT NULL, collection failed, "Sample delivery": "a collection error is
-- not a sample") has neither.
CREATE TABLE check_current (
  org_id    uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  host_id   uuid NOT NULL,
  check_key text NOT NULL,
  item      text NOT NULL DEFAULT '',
  metric    text NOT NULL,
  value     double precision,
  label     text,
  at        timestamptz NOT NULL,
  error     text,
  PRIMARY KEY (host_id, check_key, item, metric),
  CHECK (
    (error IS NOT NULL AND value IS NULL AND label IS NULL) OR
    (error IS NULL AND (value IS NOT NULL) <> (label IS NOT NULL))
  ),
  FOREIGN KEY (org_id, host_id) REFERENCES hosts (org_id, id) ON DELETE CASCADE
);
CREATE INDEX check_current_org_idx ON check_current (org_id);

-- The standard row-level security pair (see e.g. migration 00037) on both
-- tables. job_events (00018) is the one deliberate exception in this
-- schema, for a reason (an org-unscoped worker append path) that does not
-- apply to the check pipeline's own ingest, which always knows its
-- organization.
--
-- Be precise about what the pair on check_samples buys, because a
-- hypertable is not an ordinary table here. The policy protects queries
-- that go THROUGH check_samples. It does not reach the chunks: those are
-- separate relations TimescaleDB creates on demand in
-- _timescaledb_internal, with relrowsecurity = false and this table's ACL
-- copied onto them, and PostgreSQL applies a parent's policy only to a
-- query on the parent. A session scoped to one organization that names a
-- chunk directly can therefore read, update and delete every other
-- organization's rows in it.
--
-- No mechanism closes that, and three were probed against
-- timescale/timescaledb:2.30.0-pg18 before this note was written: the
-- schema-level revoke that would hide chunks stops pulsix_app running any
-- statement at all, a revoke on the chunk tables lasts until the next
-- chunk inherits the ACL again, and per-chunk row-level security works
-- perfectly but nothing applies it to a chunk that does not exist yet
-- (TimescaleDB creates chunks without firing a ddl_command_end event
-- trigger). internal/db's TestHypertableChunksDoNotInheritRowLevelSecurity
-- records all of it and fails if the database's behaviour ever changes.
--
-- What bounds the exposure is already true of every table in this schema:
-- the org_isolation_bypass policy means any session that can execute
-- arbitrary SQL as pulsix_app reads every organization by clearing
-- pulsix.org_id, through the parent, without going near a chunk. These
-- policies guard against a missing scope in the application's own
-- statements, not against arbitrary SQL — and no application code names
-- a chunk.
ALTER TABLE check_samples ENABLE ROW LEVEL SECURITY;
ALTER TABLE check_samples FORCE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON check_samples
  USING (org_id = NULLIF(current_setting('pulsix.org_id', true), '')::uuid);
CREATE POLICY org_isolation_bypass ON check_samples
  USING (current_setting('pulsix.org_id', true) IS NULL OR current_setting('pulsix.org_id', true) = '');

ALTER TABLE check_current ENABLE ROW LEVEL SECURITY;
ALTER TABLE check_current FORCE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON check_current
  USING (org_id = NULLIF(current_setting('pulsix.org_id', true), '')::uuid);
CREATE POLICY org_isolation_bypass ON check_current
  USING (current_setting('pulsix.org_id', true) IS NULL OR current_setting('pulsix.org_id', true) = '');

-- +goose Down
DROP TABLE check_current;
DROP TABLE check_samples;
