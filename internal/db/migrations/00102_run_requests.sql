-- +goose Up
-- Run requests (spec docs/superpowers/specs/2026-11-16-assistant-access-
-- design.md, "Run requests"): an assistant asks for a run of a job template
-- (kind run) or for a retry of a job's failed hosts (kind retry_failed),
-- and a person approves or rejects it. "Expired" is never stored: a pending
-- row past expires_at reads as expired. The fingerprint an approval checks
-- is always recomputed, so it has no column.
--
-- Deleting the template deletes its requests. job_id and
-- requested_by_token carry no foreign key: the history outlives a purged
-- job or a token row. run_group_id is set on approval and is how a token
-- may cancel the runs its own requests started.
CREATE TABLE run_requests (
  id                 uuid PRIMARY KEY,
  org_id             uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  kind               text NOT NULL CHECK (kind IN ('run', 'retry_failed')),
  template_id        uuid NOT NULL REFERENCES job_templates(id) ON DELETE CASCADE,
  template_revision  bigint NOT NULL,
  host_id            uuid,
  job_id             uuid,
  reason             text NOT NULL,
  requested_by_user  uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  requested_by_token uuid,
  status             text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'approved', 'rejected')),
  decided_by         uuid REFERENCES users(id) ON DELETE SET NULL,
  decided_at         timestamptz,
  run_group_id       uuid,
  expires_at         timestamptz NOT NULL,
  created_at         timestamptz NOT NULL DEFAULT now(),
  CHECK ((kind = 'retry_failed') = (job_id IS NOT NULL))
);
CREATE INDEX run_requests_org_created_idx ON run_requests (org_id, created_at DESC);
CREATE INDEX run_requests_run_group_idx ON run_requests (org_id, run_group_id) WHERE run_group_id IS NOT NULL;

ALTER TABLE run_requests ENABLE ROW LEVEL SECURITY;
ALTER TABLE run_requests FORCE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON run_requests
  USING (org_id = NULLIF(current_setting('pulsix.org_id', true), '')::uuid);
CREATE POLICY org_isolation_bypass ON run_requests
  USING (current_setting('pulsix.org_id', true) IS NULL OR current_setting('pulsix.org_id', true) = '');

-- +goose Down
DROP TABLE run_requests;
