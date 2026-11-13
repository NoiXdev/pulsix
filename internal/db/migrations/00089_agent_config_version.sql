-- +goose Up
-- The configuration check per agent (backlog 3.13c, spec
-- docs/superpowers/specs/2026-11-13-check-correctness-design.md §4). Every
-- heartbeat carries the configuration version the agent runs, and the
-- manager compares it with the one it resolves for the host
-- (internal/agentapi processHeartbeat); these columns keep the latest
-- comparison so the GUI can tell whether the agent runs the current
-- configuration: config_version is what the agent reported (the empty
-- string for an agent that has applied none yet), config_expected what
-- the manager resolved, config_checked_at when.
--
-- The three are written together (RecordAgentConfigCheck, agents.sql), only
-- by a comparison that actually ran, and a re-enrollment resets them to
-- NULL with last_seen. NULL means not compared yet, so the columns need no
-- backfill: every agent's next comparison after the upgrade fills them.
-- Nullable and without a default, the ADD COLUMNs touch only the catalog.
ALTER TABLE host_agents
  ADD COLUMN config_version text,
  ADD COLUMN config_expected text,
  ADD COLUMN config_checked_at timestamptz;

-- +goose Down
-- Development databases only (docs/upgrades.md: migrations only go up).
ALTER TABLE host_agents
  DROP COLUMN config_checked_at,
  DROP COLUMN config_expected,
  DROP COLUMN config_version;
