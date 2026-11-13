-- +goose Up
-- When the agent's current online period began (backlog 3.57, spec
-- docs/superpowers/specs/2026-11-13-client-hosts-design.md, "Evaluation").
-- An online client host's staleness is measured from no earlier than this,
-- so a 1-hour check last read before a notebook was shut down for the
-- night is not unknown the moment its agent is back. Every write that sets
-- last_seen keeps it while the previous last_seen is within the agents
-- list's online window (agents.StaleAfter), and sets it to now() after a
-- longer gap, when last_seen is NULL or when it is unknown (agents.sql).
-- A registration (renewal or re-enrollment) resets it to NULL, so the
-- first touch under the new serial starts a new period. connected_since
-- (00088) is unrelated: it spans gaps of up to 24 hours and serves the
-- item expiry only.
--
-- NULL means unknown, and an unknown start anchors nothing (the reading
-- alone decides, as before), so the column needs no backfill: every
-- existing row reads as unknown until its agent's first touch after the
-- upgrade sets it. Nullable and without a default, the ADD COLUMN touches
-- only the catalog.
ALTER TABLE host_agents ADD COLUMN online_since timestamptz;

-- +goose Down
-- Development databases only (docs/upgrades.md: migrations only go up).
ALTER TABLE host_agents DROP COLUMN online_since;
