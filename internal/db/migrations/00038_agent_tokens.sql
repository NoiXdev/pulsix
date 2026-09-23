-- +goose Up
-- Agent enrollment tokens, the 00027 shape plus the agent-specific token
-- options (v0.3a): a token may be pre-bound to an existing host (the agent
-- then takes that host's identity) or unbound (the first agent to redeem
-- it creates the host, named by the enrollment's name_hint or the token
-- presenter), and it may be multi-use (max_uses/uses) — Task 7's staging
-- tokens enroll a fleet at once. The secret is stored only as SHA-256
-- (`secret_hash`); the token is printed once at creation.
--
-- agent_enrollments (org_id, id) mirrors worker_enrollments' UNIQUE pair
-- exactly — every org-owned child table targets its parent's (org_id, id)
-- constraints, so the composite foreign keys below are enforceable.

CREATE TABLE agent_enrollments (
  id          uuid PRIMARY KEY,
  org_id      uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  zone_id     uuid NOT NULL,
  host_id     uuid,
  name_hint   text,
  secret_hash bytea NOT NULL,
  max_uses    int NOT NULL DEFAULT 1 CHECK (max_uses BETWEEN 1 AND 100),
  uses        int NOT NULL DEFAULT 0,
  expires_at  timestamptz NOT NULL,
  used_at     timestamptz,
  created_by  uuid,
  created_at  timestamptz NOT NULL DEFAULT now(),
  UNIQUE (org_id, id),
  FOREIGN KEY (org_id, zone_id) REFERENCES zones (org_id, id) ON DELETE CASCADE,
  FOREIGN KEY (org_id, host_id) REFERENCES hosts (org_id, id) ON DELETE CASCADE
);
CREATE INDEX agent_enrollments_pending_idx ON agent_enrollments (org_id, expires_at) WHERE used_at IS NULL;

ALTER TABLE agent_enrollments ENABLE ROW LEVEL SECURITY;
ALTER TABLE agent_enrollments FORCE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON agent_enrollments
  USING (org_id = NULLIF(current_setting('pulsix.org_id', true), '')::uuid);
CREATE POLICY org_isolation_bypass ON agent_enrollments
  USING (current_setting('pulsix.org_id', true) IS NULL OR current_setting('pulsix.org_id', true) = '');

-- +goose Down
DROP TABLE agent_enrollments;
