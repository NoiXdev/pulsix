-- +goose Up
-- A run in Ansible's check mode (a playbook dry run, backlog 3.126): set at
-- enqueue from the sealed run overrides, so lists, filters and the claim's
-- worker gate see it without opening the payload. False for every existing
-- run: none was a dry run.
ALTER TABLE jobs ADD COLUMN check_mode boolean NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE jobs DROP COLUMN check_mode;
