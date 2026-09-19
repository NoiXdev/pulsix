-- +goose Up
-- Task 5 fix round 1: a run group must be reported at most once (spec
-- 2026-09-18 §7 "at most one message per run and channel"). CancelJob
-- commits a queued job's queued -> cancelled transition as a standalone
-- statement, outside any group lock, so a concurrent finish elsewhere can
-- see the group complete and report it before the cancel's own (locked)
-- completeness check runs — the lock around the check alone cannot close
-- that window. The flag lives on the group's own first job (the run
-- view's own anchor row, already selected as RunGroupResult.FirstJobID),
-- so no new table or RLS policy is needed: a conditional UPDATE keyed by
-- that job's id is the atomic "did I win the report" gate — Postgres
-- serializes two concurrent UPDATEs of the same row regardless of which
-- transaction or advisory lock got there first, so at most one caller
-- ever sees a row affected.
ALTER TABLE jobs ADD COLUMN run_group_reported boolean NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE jobs DROP COLUMN run_group_reported;
