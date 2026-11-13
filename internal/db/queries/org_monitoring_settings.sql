-- name: GetOrgMonitoringSettings :one
-- Org-level monitoring settings (backlog 3.57, table
-- org_monitoring_settings from migration 00091): the organization's stored
-- row, read inside the request's organization scope; the caller turns
-- pgx.ErrNoRows (no row yet) into the defaults.
SELECT client_absent_days FROM org_monitoring_settings WHERE org_id = $1;

-- name: UpsertOrgMonitoringSettings :exec
-- Replaces the stored settings; the row is created when the organization
-- has none. The table's CHECK refuses a value outside 0..365.
INSERT INTO org_monitoring_settings (org_id, client_absent_days)
VALUES ($1, $2)
ON CONFLICT (org_id) DO UPDATE
SET client_absent_days = EXCLUDED.client_absent_days, updated_at = now();

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
