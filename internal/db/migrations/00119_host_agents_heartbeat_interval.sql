-- +goose Up
-- The agent's own heartbeat interval in seconds, as it reports it at the
-- start of every stream (backlog 3.58). The agents list's online window is
-- three of these intervals and never less than 90 s (agents.OnlineWindow);
-- every reader of the window computes it per row from this column: the
-- Agents page's online verdict, online_since in the touch and heartbeat
-- writes (00092), the probe candidates and the rule evaluation's presence
-- read. The manager bounds what it stores to the agent's own range (5 s to
-- 10 min).
--
-- NULL means the agent never reported it — every row right after the
-- upgrade, and an agent older than the field for ever — which is the 90 s
-- window every agent had before, so the column needs no backfill.
-- Nullable and without a default, the ADD COLUMN touches only the
-- catalog.
ALTER TABLE host_agents ADD COLUMN heartbeat_interval_s integer;

-- +goose Down
-- Development databases only (docs/upgrades.md: migrations only go up).
ALTER TABLE host_agents DROP COLUMN heartbeat_interval_s;
