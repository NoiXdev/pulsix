-- name: GetOrgMonitoringSettings :one
-- Org-level monitoring settings (backlog 3.57, table
-- org_monitoring_settings from migration 00091): the organization's stored
-- row, read inside the request's organization scope; the caller turns
-- pgx.ErrNoRows (no row yet) into the defaults. retention_days (backlog
-- 3.15, 00093) is NULL when the organization made no retention choice.
SELECT client_absent_days, retention_days FROM org_monitoring_settings WHERE org_id = $1;

-- name: UpsertOrgMonitoringSettings :one
-- Writes the settings; the row is created when the organization has none.
-- client_absent_days is always replaced. retention_days is replaced only
-- when set_retention is true (NULL then clears the choice back to the
-- instance maximum); otherwise the stored choice is kept, and a new row
-- starts without one. One statement, so a concurrent write of the other
-- setting is never lost to a read-modify-write. The table's CHECKs refuse
-- client_absent_days outside 0..365 and retention_days outside
-- (30, 90, 365). Returns the row as stored.
INSERT INTO org_monitoring_settings (org_id, client_absent_days, retention_days)
VALUES (
  sqlc.arg(org_id), sqlc.arg(client_absent_days),
  CASE WHEN sqlc.arg(set_retention)::boolean THEN sqlc.narg(retention_days)::integer END
)
ON CONFLICT (org_id) DO UPDATE
SET client_absent_days = EXCLUDED.client_absent_days,
    retention_days = CASE WHEN sqlc.arg(set_retention)::boolean
                          THEN EXCLUDED.retention_days
                          ELSE org_monitoring_settings.retention_days END,
    updated_at = now()
RETURNING client_absent_days, retention_days;

-- name: GetOrgClientAbsentDays :one
-- The evaluator's read (backlog 3.57): the organization's client absent
-- threshold in days with the default applied, so a missing row reads as 14
-- (0 = the warning is off). Always exactly one row. The 14 must equal the
-- column default (00091) and monitorsettings.DefaultClientAbsentDays; a
-- test pins the three together.
SELECT COALESCE(
  (SELECT s.client_absent_days FROM org_monitoring_settings s WHERE s.org_id = sqlc.arg(org_id)),
  14
)::integer AS client_absent_days;
