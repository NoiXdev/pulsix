-- +goose Up
-- Agent secrets (backlog 3.5): every heartbeat of an agent that reads the
-- local configuration file (agent.yaml) reports the names of the secrets
-- it loaded and the file's state ("absent", "ok" or the error that made it
-- drop every secret). The manager keeps the latest report per agent, next
-- to last_seen, for the host page. Never a secret's value.
--
-- Both columns are NULL for "unknown": an agent older than the file sends
-- no state, and a registration that has not reported yet has none. The
-- heartbeat writes the two together, so either both are NULL or neither
-- is; zero names is '{}', not NULL. Nullable and without a default, the
-- ADD COLUMNs touch only the catalog — existing rows read as unknown.
ALTER TABLE host_agents ADD COLUMN secret_names text[];
ALTER TABLE host_agents ADD COLUMN secrets_state text;

-- +goose Down
-- Development databases only (docs/upgrades.md: migrations only go up).
ALTER TABLE host_agents DROP COLUMN secrets_state;
ALTER TABLE host_agents DROP COLUMN secret_names;
