-- +goose Up
-- When a tuple last went unmeasured after a judgement (backlog 4.42): its
-- check disabled or taken off the host, the host no longer monitored, a
-- selector edited so the host drops out, or a metric no longer asked for.
-- The evaluation leaves such a tuple's check_states row standing (the
-- alert sink relies on it) and stamps this column on the first evaluation
-- of the gap, with that evaluation's own moment. While it is later than
-- evaluated_at the row is no previous state: the tuple starts fresh when
-- it is measured again — a new since, no carried candidate, no held
-- severity — and the flapping count leaves out the transitions from
-- before the gap for as long as they are inside its window.
--
-- NULL means the row never went unmeasured since this column exists —
-- every row right after the upgrade — so the column needs no backfill.
-- Nullable and without a default, the ADD COLUMN touches only the
-- catalog.
ALTER TABLE check_states ADD COLUMN unmeasured_at timestamptz;

-- +goose Down
-- Development databases only (docs/upgrades.md: migrations only go up).
ALTER TABLE check_states DROP COLUMN unmeasured_at;
