-- +goose Up
-- What an approval binds of the follow-up runs it starts (backlog 2.143,
-- the owner's decision of 2026-11-27): one entry per follow-up of the
-- approved selection, keyed by its chain path and template, holding the
-- fingerprint of its hosts and vars as the approval saw them and the hosts
-- they were taken from (its parent's). The claim of a follow-up's job
-- compares the live state with it, as the claim of the run itself compares
-- the run picture. NULL is no binding: a request not approved, of another
-- kind, or approved before this column, whose follow-ups run unchecked as
-- before. Nullable without a default, so the ADD COLUMN touches only the
-- catalog.
ALTER TABLE run_requests ADD COLUMN approved_follow_ups jsonb;

-- The claim walks from a follow-up's run group up its chain to the run
-- request that started it: run_follow_ups_child_idx finds each step.

-- +goose Down
-- Development databases only (docs/upgrades.md: migrations only go up).
ALTER TABLE run_requests DROP COLUMN approved_follow_ups;
