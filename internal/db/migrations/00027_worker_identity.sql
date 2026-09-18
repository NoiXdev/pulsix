-- +goose Up
-- Worker identity (spec 2026-09-18 §4): token enrollment, per-certificate
-- revocation and renewal. A worker may hold several certificates (the
-- renewed one and the old one until it expires); the gRPC interceptor
-- resolves the presented serial here on every RPC. workers.cert_serial
-- stays (NOT NULL, UNIQUE) as the first certificate's serial but is no
-- longer read.
ALTER TABLE workers ADD COLUMN status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'revoked'));
ALTER TABLE workers ADD COLUMN revoked_at timestamptz;

CREATE TABLE worker_certificates (
  serial     text PRIMARY KEY,
  org_id     uuid NOT NULL,
  worker_id  uuid NOT NULL,
  not_before timestamptz NOT NULL,
  not_after  timestamptz NOT NULL,
  revoked_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (org_id, worker_id) REFERENCES workers (org_id, id) ON DELETE CASCADE
);
CREATE INDEX worker_certificates_worker_idx ON worker_certificates (org_id, worker_id);

ALTER TABLE worker_certificates ENABLE ROW LEVEL SECURITY;
ALTER TABLE worker_certificates FORCE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON worker_certificates
  USING (org_id = NULLIF(current_setting('pulsix.org_id', true), '')::uuid);
CREATE POLICY org_isolation_bypass ON worker_certificates
  USING (current_setting('pulsix.org_id', true) IS NULL OR current_setting('pulsix.org_id', true) = '');

-- Every existing worker keeps authenticating: its serial moves into the
-- certificate table. The validity window is an approximation (the 825-day
-- leaves of v0.2b), used for display only — the worker renews from its
-- real certificate.
INSERT INTO worker_certificates (serial, org_id, worker_id, not_before, not_after)
SELECT cert_serial, org_id, id, created_at, created_at + interval '825 days' FROM workers;

-- One-time enrollment tokens: only the SHA-256 of the secret is kept.
CREATE TABLE worker_enrollments (
  id          uuid PRIMARY KEY,
  org_id      uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  zone_id     uuid NOT NULL,
  name        text NOT NULL,
  secret_hash bytea NOT NULL,
  expires_at  timestamptz NOT NULL,
  used_at     timestamptz,
  worker_id   uuid,
  created_by  uuid,
  created_at  timestamptz NOT NULL DEFAULT now(),
  UNIQUE (org_id, id),
  FOREIGN KEY (org_id, zone_id) REFERENCES zones (org_id, id) ON DELETE CASCADE
);
CREATE INDEX worker_enrollments_pending_idx ON worker_enrollments (org_id, expires_at) WHERE used_at IS NULL;

ALTER TABLE worker_enrollments ENABLE ROW LEVEL SECURITY;
ALTER TABLE worker_enrollments FORCE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON worker_enrollments
  USING (org_id = NULLIF(current_setting('pulsix.org_id', true), '')::uuid);
CREATE POLICY org_isolation_bypass ON worker_enrollments
  USING (current_setting('pulsix.org_id', true) IS NULL OR current_setting('pulsix.org_id', true) = '');

-- +goose Down
DROP TABLE worker_enrollments;
DROP TABLE worker_certificates;
ALTER TABLE workers DROP COLUMN revoked_at;
ALTER TABLE workers DROP COLUMN status;
