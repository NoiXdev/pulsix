-- +goose Up
-- A re-opened alert continues its stale episode (backlog 4.39). When a
-- tuple's alert closed `stale` (its check disabled, its host un-monitored,
-- a selector edited) and the same subject opens again within one hour,
-- the new episode takes over the previous one's acknowledgement and snooze
-- (internal/alerts' adoption has the rules). continues_alert_id records
-- which episode it continued; NULL for every other alert, every row that
-- exists today included, so the column needs no backfill and the ADD
-- touches only the catalog.
--
-- The reference is composite, (org_id, continues_alert_id) to
-- alerts (org_id, id) (the UNIQUE 00056 added), like every tenant
-- reference since 00014: the database itself refuses a continuation
-- across organizations. ON DELETE SET NULL names only continues_alert_id
-- (the PostgreSQL 15+ column list, as 00065 does for worker_id): nulling
-- org_id would break its NOT NULL. Alerts are deleted with their host or
-- organization, which takes both episodes in one statement, but the
-- referential trigger still looks up every deleted alert's continuations;
-- alerts_continues_idx makes that lookup an index probe instead of a scan
-- of the whole alerts table, which has no retention, per deleted alert.
--
-- alerts_host_closed_idx serves the evaluator's new per-host read, the
-- latest closed episode of each subject closed in the last hour
-- (ListRecentlyClosedAlertsForHost): without it every host evaluation
-- would read its organization's whole alert history, which has no
-- retention. Both indexes are built with a plain CREATE INDEX. The
-- ALTER TABLE takes an ACCESS EXCLUSIVE lock on alerts that the migration
-- holds to its end (both index builds and the foreign key's validation
-- scan), so reads of alerts wait too, as in 00056 (docs/upgrades.md); the
-- continuation index is empty at the upgrade.
ALTER TABLE alerts ADD COLUMN continues_alert_id uuid;
ALTER TABLE alerts ADD CONSTRAINT alerts_continues_alert_fkey
  FOREIGN KEY (org_id, continues_alert_id) REFERENCES alerts (org_id, id) ON DELETE SET NULL (continues_alert_id);
CREATE INDEX alerts_continues_idx ON alerts (org_id, continues_alert_id) WHERE continues_alert_id IS NOT NULL;
CREATE INDEX alerts_host_closed_idx ON alerts (host_id, closed_at) WHERE closed_at IS NOT NULL;

-- +goose Down
-- Development databases only (docs/upgrades.md: migrations only go up).
DROP INDEX alerts_host_closed_idx;
DROP INDEX alerts_continues_idx;
ALTER TABLE alerts DROP COLUMN continues_alert_id;
