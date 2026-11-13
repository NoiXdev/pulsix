-- +goose Up
-- When an agent's current connection began (backlog 3.12c, spec
-- docs/superpowers/specs/2026-11-13-check-correctness-design.md §2). The
-- automatic item expiry counts the agent's heartbeat as a sign of life
-- only once the agent has been connected for checks.MaxInterval plus a
-- 30-minute margin, so an agent back from a long outage does not make a
-- long-interval check's items look gone before that check has run again.
-- Every write that sets last_seen keeps it, or sets it to now() after a
-- gap (agents.sql); a re-enrollment resets it to NULL with last_seen.
--
-- NULL means unknown, and an unknown start never counts, so the column
-- needs no backfill: every existing row reads as unknown until its agent's
-- first heartbeat after the upgrade sets it. Nullable and without a
-- default, the ADD COLUMN touches only the catalog.
ALTER TABLE host_agents ADD COLUMN connected_since timestamptz;

-- +goose Down
-- Development databases only (docs/upgrades.md: migrations only go up).
ALTER TABLE host_agents DROP COLUMN connected_since;
