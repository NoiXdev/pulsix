-- +goose Up
-- Org-level monitoring settings (backlog 3.57). One row per organization,
-- an absent row meaning every setting at its default. Today that is only
-- client_absent_days: after how many days without contact a client host's
-- agent raises the "not seen since …" warning (0 = never, default 14,
-- at most 365). Backlog 3.15 adds its retention setting to this table.
--
-- The default lives in three places that must agree: the column default
-- here, the COALESCE of GetOrgClientAbsentDays
-- (internal/db/queries/org_monitoring_settings.sql) and
-- monitorsettings.DefaultClientAbsentDays; a test pins them together.
--
-- The row-level security pair is the standard form, copied from
-- org_job_settings (00033): org_isolation admits only the bound
-- organization (every API statement runs inside the request's org scope),
-- org_isolation_bypass applies when no organization is bound (the
-- evaluation ticker reads across organizations).
CREATE TABLE org_monitoring_settings (
  org_id             uuid PRIMARY KEY REFERENCES organizations(id) ON DELETE CASCADE,
  client_absent_days integer NOT NULL DEFAULT 14
                     CHECK (client_absent_days BETWEEN 0 AND 365),
  updated_at         timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE org_monitoring_settings ENABLE ROW LEVEL SECURITY;
ALTER TABLE org_monitoring_settings FORCE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON org_monitoring_settings
  USING (org_id = NULLIF(current_setting('pulsix.org_id', true), '')::uuid);
CREATE POLICY org_isolation_bypass ON org_monitoring_settings
  USING (current_setting('pulsix.org_id', true) IS NULL OR current_setting('pulsix.org_id', true) = '');

-- +goose Down
-- Development databases only (docs/upgrades.md: migrations only go up).
DROP TABLE org_monitoring_settings;
