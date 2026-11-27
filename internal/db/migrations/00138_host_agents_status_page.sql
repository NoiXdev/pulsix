-- +goose Up
-- Whether the agent's local status page is on, and where (backlog 3.132),
-- as the agent reports it in every heartbeat: off, loopback, network, or
-- failed (its agent.yaml asks for it but the settings are refused or the
-- page could not start). Only the host's own agent.yaml turns the page on;
-- its address, port and token are never sent.
--
-- NULL means the agent never reported it — every row right after the
-- upgrade, and an agent older than the page for ever — and the agent card
-- then shows nothing. A re-enrollment resets it with the other reports
-- (UpsertHostAgent). Nullable and without a default, the ADD COLUMN
-- touches only the catalog, and the CHECK passes every existing row.
ALTER TABLE host_agents ADD COLUMN status_page text
  CHECK (status_page IN ('off', 'loopback', 'network', 'failed'));

-- +goose Down
-- Development databases only (docs/upgrades.md: migrations only go up).
ALTER TABLE host_agents DROP COLUMN status_page;
