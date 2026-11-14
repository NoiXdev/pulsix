-- +goose Up
-- Local checks (backlog 3.5a): every heartbeat of an agent that reads the
-- checks section of its local configuration file (agent.yaml) reports the
-- checks defined there that it runs — key, plugin, params and interval,
-- as a JSON array of {key, plugin, params, interval_seconds} with params
-- an object — and the problems that kept other entries from running. The
-- manager validates both again and keeps the latest report per agent, next
-- to the secrets report (00086), for the host page and the rules.
--
-- Both columns are NULL for "unknown": an agent older than local checks
-- sends no secrets state, and a registration that has not reported yet
-- has none. The heartbeat writes them together with the secrets columns,
-- so either all four are NULL or none is; zero local checks is '[]' and
-- zero problems '{}', not NULL. Nullable and without a default, the ADD
-- COLUMNs touch only the catalog — existing rows read as unknown.
ALTER TABLE host_agents ADD COLUMN local_checks jsonb;
ALTER TABLE host_agents ADD COLUMN local_check_problems text[];

-- +goose Down
-- Development databases only (docs/upgrades.md: migrations only go up).
ALTER TABLE host_agents DROP COLUMN local_check_problems;
ALTER TABLE host_agents DROP COLUMN local_checks;
