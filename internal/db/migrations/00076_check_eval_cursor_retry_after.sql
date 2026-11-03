-- +goose Up
-- Backlog 7.58. ClaimDueEvalCursors takes every dirty-or-due host ordered
-- by next_due_at, and a host whose evaluation fails every time (a window
-- statement that hits the per-host statement timeout on every try) is
-- dirty for ever: nothing it does ever advances evaluated_at past
-- dirty_at. Pushing its next_due_at back does not move it out of the way —
-- a healthy dirty host's next_due_at is its staleness deadline, minutes
-- ahead, so the failing host still sorts in front of it — and a tenant
-- with a handful of such hosts spends every batch's deadline before any
-- other organization's hosts are reached.
--
-- retry_after holds a host whose own evaluation failed out of the claim
-- for a while, so the hosts behind it get their turn; the claim excludes
-- it whatever its dirty_at and next_due_at say. It is deliberately
-- separate from next_due_at, which is the evaluation's own conclusion
-- about when the host must be looked at again, and from dirty_at, which
-- ingest owns. notification_digests.retry_after (00069) is the same
-- answer to the same starvation on the digest ticker.
--
-- NULL — every row written before this migration, and every row whose
-- last evaluation committed — means "nothing is holding this back".
ALTER TABLE check_eval_cursor ADD COLUMN retry_after timestamptz;

-- +goose Down
ALTER TABLE check_eval_cursor DROP COLUMN retry_after;
