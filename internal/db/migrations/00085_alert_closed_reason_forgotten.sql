-- +goose Up
-- Forgetting an item that is gone (backlog 3.12b) closes its open alerts
-- with a reason of their own: 'resolved' would claim the problem was
-- fixed and 'stale' is the sweep's "data stopped", while the item was
-- deliberately forgotten. This widens 00049's two-value CHECK on
-- alerts.closed_reason by 'forgotten' and changes nothing else.
--
-- Postgres has no "widen a CHECK", so it is a DROP and ADD of the
-- constraint under its own name — the shape 00056 used for
-- alert_events.kind. It is additive in effect: every existing row holds
-- 'resolved', 'stale' or NULL, all of which the new list permits, so the
-- ADD validates without repairing anything. The validation scans alerts
-- (it has no retention policy and only grows) under the ALTER's lock;
-- that scan is a sequential read, not an index build, and is short next
-- to 00056's.
ALTER TABLE alerts DROP CONSTRAINT alerts_closed_reason_check;
ALTER TABLE alerts ADD CONSTRAINT alerts_closed_reason_check
  CHECK (closed_reason IN ('resolved', 'stale', 'forgotten'));

-- +goose Down
-- Development databases only (docs/upgrades.md: migrations only go up).
-- A forgotten alert has no older word that fits, so it reads as stale.
UPDATE alerts SET closed_reason = 'stale' WHERE closed_reason = 'forgotten';
ALTER TABLE alerts DROP CONSTRAINT alerts_closed_reason_check;
ALTER TABLE alerts ADD CONSTRAINT alerts_closed_reason_check
  CHECK (closed_reason IN ('resolved', 'stale'));
