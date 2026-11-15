-- +goose Up
-- Probe vantage points (backlog 3.13a, spec docs/superpowers/specs/
-- 2026-11-16-probe-vantages-design.md): a synthetic check can be probed
-- from the manager and from agents of monitoring groups. Three tables and
-- one column hold the storage; the grants need no statement of their own
-- (00002's default privileges give pulsix_app the table rights).
--
-- host_agents.probes_supported is what the agent reports in its heartbeat:
-- an agent that does not say so is never given an assignment.
--
-- probe_assignments is the runner's computed answer to "which agent probes
-- which target's check": one row per (agent host, target host, check key).
-- params is the check's params minus the vantage params, canonical JSON;
-- the runner replaces each organization's set as a diff in one transaction.
-- Both hosts reference hosts with the composite (org_id, id) key (00014),
-- so an assignment never crosses organizations, and a deleted host takes
-- its rows with it.
--
-- probe_current and probe_samples are check_current and check_samples
-- (00044) with one more key column, `vantage`: the text `manager` or the
-- canonical lower-case UUID of the agent's host. Read 00044's comments for
-- the reasoning that carries over (keys, not definition ids; item is ''
-- for the whole host; an error row carries neither value nor label and has
-- no sample). The chunk caveat of the row-level security on a hypertable
-- (00044, internal/db's TestHypertableChunksDoNotInheritRowLevelSecurity)
-- applies to probe_samples unchanged.
ALTER TABLE host_agents ADD COLUMN probes_supported boolean NOT NULL DEFAULT false;

CREATE TABLE probe_assignments (
  org_id           uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  agent_host_id    uuid NOT NULL,
  target_host_id   uuid NOT NULL,
  check_key        text NOT NULL,
  plugin           text NOT NULL,
  params           jsonb NOT NULL,
  interval_seconds integer NOT NULL CHECK (interval_seconds > 0),
  address          text NOT NULL,
  assigned_at      timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (agent_host_id, target_host_id, check_key),
  FOREIGN KEY (org_id, agent_host_id) REFERENCES hosts (org_id, id) ON DELETE CASCADE,
  FOREIGN KEY (org_id, target_host_id) REFERENCES hosts (org_id, id) ON DELETE CASCADE
);
CREATE INDEX probe_assignments_slot_idx ON probe_assignments (target_host_id, check_key);
CREATE INDEX probe_assignments_org_idx ON probe_assignments (org_id);

CREATE TABLE probe_current (
  org_id    uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  host_id   uuid NOT NULL,
  check_key text NOT NULL,
  vantage   text NOT NULL CHECK (vantage = 'manager' OR vantage ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'),
  item      text NOT NULL DEFAULT '',
  metric    text NOT NULL,
  value     double precision,
  label     text,
  at        timestamptz NOT NULL,
  error     text,
  PRIMARY KEY (host_id, check_key, vantage, item, metric),
  CHECK (
    (error IS NOT NULL AND value IS NULL AND label IS NULL) OR
    (error IS NULL AND (value IS NOT NULL) <> (label IS NOT NULL))
  ),
  FOREIGN KEY (org_id, host_id) REFERENCES hosts (org_id, id) ON DELETE CASCADE
);
CREATE INDEX probe_current_org_idx ON probe_current (org_id);

CREATE TABLE probe_samples (
  at        timestamptz NOT NULL,
  org_id    uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  host_id   uuid NOT NULL,
  check_key text NOT NULL,
  vantage   text NOT NULL CHECK (vantage = 'manager' OR vantage ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'),
  item      text NOT NULL DEFAULT '',
  metric    text NOT NULL,
  value     double precision,
  label     text,
  CHECK ((value IS NOT NULL) <> (label IS NOT NULL)),
  FOREIGN KEY (org_id, host_id) REFERENCES hosts (org_id, id) ON DELETE CASCADE
);
SELECT create_hypertable('probe_samples', 'at', chunk_time_interval => INTERVAL '1 day');
SELECT add_retention_policy('probe_samples', INTERVAL '90 days');
CREATE INDEX probe_samples_lookup_idx ON probe_samples (org_id, host_id, check_key, vantage, item, metric, at DESC);

ALTER TABLE probe_assignments ENABLE ROW LEVEL SECURITY;
ALTER TABLE probe_assignments FORCE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON probe_assignments
  USING (org_id = NULLIF(current_setting('pulsix.org_id', true), '')::uuid);
CREATE POLICY org_isolation_bypass ON probe_assignments
  USING (current_setting('pulsix.org_id', true) IS NULL OR current_setting('pulsix.org_id', true) = '');

ALTER TABLE probe_current ENABLE ROW LEVEL SECURITY;
ALTER TABLE probe_current FORCE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON probe_current
  USING (org_id = NULLIF(current_setting('pulsix.org_id', true), '')::uuid);
CREATE POLICY org_isolation_bypass ON probe_current
  USING (current_setting('pulsix.org_id', true) IS NULL OR current_setting('pulsix.org_id', true) = '');

ALTER TABLE probe_samples ENABLE ROW LEVEL SECURITY;
ALTER TABLE probe_samples FORCE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON probe_samples
  USING (org_id = NULLIF(current_setting('pulsix.org_id', true), '')::uuid);
CREATE POLICY org_isolation_bypass ON probe_samples
  USING (current_setting('pulsix.org_id', true) IS NULL OR current_setting('pulsix.org_id', true) = '');

-- +goose Down
-- Development databases only (docs/upgrades.md: migrations only go up).
DROP TABLE probe_samples;
DROP TABLE probe_current;
DROP TABLE probe_assignments;
ALTER TABLE host_agents DROP COLUMN probes_supported;
