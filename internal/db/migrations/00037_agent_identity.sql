-- +goose Up
-- Agent identity (v0.3a, spec §8): the host_agents / agent_certificates
-- mirror of the worker identity tables (00027). One agent per host, one
-- active certificate serial per agent row; re-enrollment replaces the row
-- and revokes the previous serial (service-side, single transaction — no
-- trigger). agent_certificates is the serial registry the gRPC interceptor
-- resolves: serial is its primary key and globally unique, which is what
-- lets GetBySerial run without an organization filter.
--
-- hosts.monitored is NOT flipped here: it is a capability flag with its own
-- semantics (hosts.Service.SetCapabilities, hosts' partial-update race
-- discipline), not a hidden side effect of an identity table. The flip to
-- monitored happens service-side on enrollment — internal/agents documents
-- it at Registry.Register, and Task 7's wiring performs it.

CREATE TABLE host_agents (
  host_id     uuid PRIMARY KEY,
  org_id      uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  agent_name  text NOT NULL,
  cert_serial text NOT NULL UNIQUE,
  last_seen   timestamptz,
  version     text NOT NULL DEFAULT '',
  created_at  timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (org_id, host_id) REFERENCES hosts (org_id, id) ON DELETE CASCADE
);

CREATE TABLE agent_certificates (
  serial     text PRIMARY KEY,
  org_id     uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  host_id    uuid NOT NULL,
  revoked_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (org_id, host_id) REFERENCES hosts (org_id, id) ON DELETE CASCADE
);
CREATE INDEX agent_certificates_host_idx ON agent_certificates (org_id, host_id);

-- The standard row-level security pair: org_isolation admits only the
-- bound organization (every API statement runs inside the request's org
-- scope), org_isolation_bypass applies when no organization is bound —
-- the gRPC interceptors' serial lookups run unbounded and rely on the
-- serial's uniqueness, never on the bypass to see a sibling org's row.
ALTER TABLE host_agents ENABLE ROW LEVEL SECURITY;
ALTER TABLE host_agents FORCE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON host_agents
  USING (org_id = NULLIF(current_setting('pulsix.org_id', true), '')::uuid);
CREATE POLICY org_isolation_bypass ON host_agents
  USING (current_setting('pulsix.org_id', true) IS NULL OR current_setting('pulsix.org_id', true) = '');

ALTER TABLE agent_certificates ENABLE ROW LEVEL SECURITY;
ALTER TABLE agent_certificates FORCE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON agent_certificates
  USING (org_id = NULLIF(current_setting('pulsix.org_id', true), '')::uuid);
CREATE POLICY org_isolation_bypass ON agent_certificates
  USING (current_setting('pulsix.org_id', true) IS NULL OR current_setting('pulsix.org_id', true) = '');

-- +goose Down
DROP TABLE agent_certificates;
DROP TABLE host_agents;
