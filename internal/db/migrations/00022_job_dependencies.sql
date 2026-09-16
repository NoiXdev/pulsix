-- +goose Up
-- Job ordering and rotation ownership (final review I1).
--
-- depends_on names a job that must have SUCCEEDED before this one can be
-- claimed. A rotation's per-host verification test depends on that host's
-- rotate_key run — with several workers, a created_at offset alone never
-- ordered them. When the dependency ends in any other terminal status, the
-- queue cancels the dependent (and its own dependents) in the same
-- transaction, so nothing waits forever.
--
-- rotation_credential_id marks every job of a credential's rotation group
-- (rotations, verifications, revocations); a new rotation of the same
-- credential is refused while any of them is not terminal. It is not a
-- foreign key: a rotation's history outlives a deleted credential.
ALTER TABLE jobs ADD COLUMN depends_on uuid;
ALTER TABLE jobs ADD CONSTRAINT jobs_depends_on_fkey FOREIGN KEY (org_id, depends_on) REFERENCES jobs (org_id, id);
ALTER TABLE jobs ADD COLUMN rotation_credential_id uuid;
CREATE INDEX jobs_depends_on_idx ON jobs (depends_on) WHERE depends_on IS NOT NULL;
CREATE INDEX jobs_active_rotation_idx ON jobs (org_id, rotation_credential_id)
  WHERE rotation_credential_id IS NOT NULL AND status NOT IN ('succeeded', 'failed', 'cancelled', 'error');

-- +goose Down
DROP INDEX jobs_active_rotation_idx;
DROP INDEX jobs_depends_on_idx;
ALTER TABLE jobs DROP COLUMN rotation_credential_id;
ALTER TABLE jobs DROP CONSTRAINT jobs_depends_on_fkey;
ALTER TABLE jobs DROP COLUMN depends_on;
