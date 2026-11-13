-- +goose Up
-- Retention per organization (backlog 3.15, spec
-- docs/superpowers/specs/2026-11-13-retention-per-org-design.md, "Per
-- organization"): how many days the organization keeps its measurements
-- and state history (check_samples, check_state_changes). NULL means no
-- choice: the instance maximum (PULSIX_SAMPLE_RETENTION, 7..365, default
-- 90) applies. The effective retention is the smaller of the choice and
-- that maximum (monitorsettings.EffectiveRetentionDays); the hourly
-- retention purge (monitorsettings.Purger) deletes an organization's older
-- rows when it is below the maximum, and TimescaleDB's own retention
-- policy keeps enforcing the maximum for everyone.
--
-- Nullable and without a default, the ADD COLUMN touches only the catalog,
-- and every existing row reads as "no choice": an upgrade changes nothing.
ALTER TABLE org_monitoring_settings
  ADD COLUMN retention_days integer CHECK (retention_days IN (30, 90, 365));

-- +goose Down
-- Development databases only (docs/upgrades.md: migrations only go up).
ALTER TABLE org_monitoring_settings DROP COLUMN retention_days;
