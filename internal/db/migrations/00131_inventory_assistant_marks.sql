-- +goose Up
-- Inventory values an assistant writes reach a run as plain text (spec
-- 2026-11-25 assistant API gaps, decision D6, backlog 2.136). Per
-- top-level key, which values of a group (assistant_keys), which schema
-- defaults of a group (assistant_default_keys) and which values of a
-- membership (assistant_keys) an assistant wrote: the renderer emits them
-- in the inert form Ansible never templates, looks up or decrypts, until a
-- person saves the object, which clears its marks. Additive, with a
-- constant default: a catalog change, every existing value stays a
-- person's.
ALTER TABLE inventory_groups ADD COLUMN assistant_keys text[] NOT NULL DEFAULT '{}';
ALTER TABLE inventory_groups ADD COLUMN assistant_default_keys text[] NOT NULL DEFAULT '{}';
ALTER TABLE inventory_memberships ADD COLUMN assistant_keys text[] NOT NULL DEFAULT '{}';

-- +goose Down
-- Development databases only (docs/upgrades.md: migrations only go up).
ALTER TABLE inventory_memberships DROP COLUMN assistant_keys;
ALTER TABLE inventory_groups DROP COLUMN assistant_default_keys;
ALTER TABLE inventory_groups DROP COLUMN assistant_keys;
