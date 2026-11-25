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
-- ON DELETE SET NULL: alerts are deleted with their host (the composite
-- FK, 00049), which takes both episodes in one statement, so the action
-- never fires today; it only keeps a future deletion of a single episode
-- from being refused by the one continuing it.
--
-- alerts_host_closed_idx serves the evaluator's new per-host read, the
-- latest closed episode of each subject closed in the last hour
-- (ListRecentlyClosedAlertsForHost): without it every host evaluation
-- would read its organization's whole alert history, which has no
-- retention. Built with a plain CREATE INDEX, which blocks writes to
-- alerts while it builds (docs/upgrades.md).
ALTER TABLE alerts ADD COLUMN continues_alert_id uuid REFERENCES alerts(id) ON DELETE SET NULL;
CREATE INDEX alerts_host_closed_idx ON alerts (host_id, closed_at) WHERE closed_at IS NOT NULL;

-- +goose Down
-- Development databases only (docs/upgrades.md: migrations only go up).
DROP INDEX alerts_host_closed_idx;
ALTER TABLE alerts DROP COLUMN continues_alert_id;
