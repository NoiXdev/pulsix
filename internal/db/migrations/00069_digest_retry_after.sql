-- +goose Up
-- Backlog 2.82's starvation half. LockDueDigests orders by next_run_at
-- and takes the oldest digestBatch rows. A digest whose build keeps
-- failing never advances, so it keeps the oldest next_run_at and is
-- re-selected on every tick -- and with digestBatch such organizations,
-- no healthy one ever gets a slot. The jam only clears when the stuck
-- rows pass maxDigestLate, by which point the digests queued behind
-- them are themselves too late to send and are skipped for that week,
-- silently.
--
-- retry_after holds a failing row out of the scan for a while, so the
-- rows behind it get their turn. It is deliberately separate from
-- next_run_at, which means "the occurrence this digest covers": moving
-- that to delay a retry would change which week the message is about.
--
-- NULL -- every row written before this migration, and every row whose
-- last attempt succeeded -- means "nothing is holding this back".
ALTER TABLE notification_digests ADD COLUMN retry_after timestamptz;

-- +goose Down
ALTER TABLE notification_digests DROP COLUMN retry_after;
