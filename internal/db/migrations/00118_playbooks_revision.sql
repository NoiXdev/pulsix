-- +goose Up
-- The editor's save conflict guard (backlog 3.127): revision drives If-Match
-- on a playbook's text save; every saved text moves it by one.
ALTER TABLE playbooks ADD COLUMN revision bigint NOT NULL DEFAULT 1;

-- +goose Down
ALTER TABLE playbooks DROP COLUMN revision;
