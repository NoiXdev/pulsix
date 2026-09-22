-- +goose Up
-- Org-level job settings (backlog 2.44). One row per organization, absent
-- rows meaning every setting at its default — today that is only
-- jobs_require_pinned_hosts (off: the spec's trust-on-first-use-for-one-
-- job stays the default). Job-related org preferences land here; a future
-- general org-settings table can adopt the rows later.
--
-- The row-level security pair is the standard form: org_isolation admits
-- only the bound organization (every API statement runs inside the
-- request's org scope), org_isolation_bypass applies when no organization
-- is bound. The jobs service reads the setting inside enqueue transactions
-- whose request context carries the scope.
CREATE TABLE org_job_settings (
  org_id                    uuid PRIMARY KEY REFERENCES organizations(id) ON DELETE CASCADE,
  jobs_require_pinned_hosts boolean NOT NULL DEFAULT false,
  updated_at                timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE org_job_settings ENABLE ROW LEVEL SECURITY;
ALTER TABLE org_job_settings FORCE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON org_job_settings
  USING (org_id = NULLIF(current_setting('pulsix.org_id', true), '')::uuid);
CREATE POLICY org_isolation_bypass ON org_job_settings
  USING (current_setting('pulsix.org_id', true) IS NULL OR current_setting('pulsix.org_id', true) = '');

-- +goose Down
DROP TABLE org_job_settings;
