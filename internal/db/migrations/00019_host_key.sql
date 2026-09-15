-- +goose Up
-- Host-key pinning and connection-test history (spec §5, backlog 2.7c).
--
-- host_ssh gains the pinned OpenSSH fingerprint of the host's public key:
-- "SHA256:<base64 raw std encoding of the SHA-256 digest of the key blob>",
-- exactly what `ssh-keygen -lf` and ssh.FingerprintSHA256 print. NULL means
-- unpinned — trust-on-first-use: the first connection test observes the key
-- and the operator pins what was seen; every later test compares against the
-- pin and fails closed on a mismatch. The LIKE guard keeps a non-null value
-- in the fingerprint shape without trying to validate the digest itself.
ALTER TABLE host_ssh ADD COLUMN ssh_host_key text CHECK (ssh_host_key IS NULL OR ssh_host_key LIKE 'SHA256:%');

-- One row per executed connection test, appended by the job queue when a
-- connection_test job finishes. host_key is the fingerprint the probe
-- observed (empty when the handshake never got that far) and
-- host_key_state is the TOFU verdict of that observation against the pin:
-- unpinned (no pin at test time, or nothing seen), pinned (matches the
-- pin), changed (differs from the pin — the fail-closed case that blocks
-- run jobs for the host until an operator re-pins).
--
-- The composite foreign key pins the test to a host of its own
-- organization (hosts' UNIQUE (org_id, id) from migration 00014) and
-- cascades on host delete: test history is not kept for a removed host.
CREATE TABLE host_connection_tests (
  id             uuid PRIMARY KEY,
  org_id         uuid NOT NULL,
  host_id        uuid NOT NULL,
  at             timestamptz NOT NULL DEFAULT now(),
  ok             boolean NOT NULL,
  host_key       text NOT NULL DEFAULT '',
  host_key_state text NOT NULL CHECK (host_key_state IN ('unpinned', 'pinned', 'changed')),
  checks         jsonb NOT NULL DEFAULT '[]',
  UNIQUE (org_id, id),
  FOREIGN KEY (org_id, host_id) REFERENCES hosts (org_id, id) ON DELETE CASCADE
);
CREATE INDEX host_connection_tests_org_host_at_idx ON host_connection_tests (org_id, host_id, at DESC);

-- Row-level security in the standard pair form: org_isolation admits only
-- the bound organization, org_isolation_bypass applies when no organization
-- is bound (instance-level statements). The org-unscoped job-queue finish
-- path (recordTest) reads and writes this table on the raw pool with
-- explicit org_id values; every API read runs through hosts.Service inside
-- the request's org scope.
ALTER TABLE host_connection_tests ENABLE ROW LEVEL SECURITY;
ALTER TABLE host_connection_tests FORCE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON host_connection_tests
  USING (org_id = NULLIF(current_setting('pulsix.org_id', true), '')::uuid);
CREATE POLICY org_isolation_bypass ON host_connection_tests
  USING (current_setting('pulsix.org_id', true) IS NULL OR current_setting('pulsix.org_id', true) = '');

-- +goose Down
DROP TABLE host_connection_tests;
ALTER TABLE host_ssh DROP COLUMN ssh_host_key;
