-- +goose Up
-- Revocation targets snapshot (backlog 2.46): one row per rotation group,
-- written by EnqueueRotation in the SAME transaction as the group's jobs.
-- targets is {"host_ids": [...], "bastion_ids": [...]} — the referencing
-- hosts and jump hosts as they stood at enqueue time. The rotation's
-- revoke_key phase reads its targets from this row instead of the live
-- reference lists: a target that stopped referencing the credential
-- mid-rotation is still revoked, because the OLD key is still in its
-- authorized_keys and must be removed (removing a stale entry for a key
-- that no longer authenticates anything is harmless; leaving it is not).
-- The snapshot carries ids only — no keys, no secrets.
--
-- The composite foreign key mirrors credentials' UNIQUE (org_id, id); the
-- ON DELETE CASCADE is deliberate: a deleted credential's snapshot is
-- meaningless (the finalize path skips a deleted credential already), and
-- a hard reference would make every rotated credential undeletable — the
-- same posture that keeps jobs.rotation_credential_id free of a foreign
-- key so rotation history outlives a deleted credential.
CREATE TABLE rotation_groups (
  id            uuid PRIMARY KEY,
  org_id        uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  credential_id uuid NOT NULL,
  targets       jsonb NOT NULL,
  created_at    timestamptz NOT NULL DEFAULT now(),
  UNIQUE (org_id, id),
  FOREIGN KEY (org_id, credential_id) REFERENCES credentials (org_id, id) ON DELETE CASCADE
);

-- The standard row-level security pair: org_isolation admits only the
-- bound organization (every API statement runs inside the request's org
-- scope, the enqueue and finish transactions alike), org_isolation_bypass
-- applies when no organization is bound.
ALTER TABLE rotation_groups ENABLE ROW LEVEL SECURITY;
ALTER TABLE rotation_groups FORCE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON rotation_groups
  USING (org_id = NULLIF(current_setting('pulsix.org_id', true), '')::uuid);
CREATE POLICY org_isolation_bypass ON rotation_groups
  USING (current_setting('pulsix.org_id', true) IS NULL OR current_setting('pulsix.org_id', true) = '');

-- +goose Down
DROP TABLE rotation_groups;
