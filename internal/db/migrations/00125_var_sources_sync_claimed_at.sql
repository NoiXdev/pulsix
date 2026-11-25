-- +goose Up
-- When the sync behind a repository's recorded read was queued (final
-- review of spec 2026-11-25 A, M2): two syncs of one repository can run at
-- once on two workers and finish in either order, and the read of the one
-- queued earlier, finishing last, used to overwrite the newer one's commit.
-- RecordVarSources now keeps the record of the sync queued later. NULL is a
-- record from before this column (or a sync whose queue time was not
-- passed): any sync replaces it. Nullable and without a default, the ADD
-- COLUMN touches only the catalog.
ALTER TABLE repo_var_sources ADD COLUMN sync_queued_at timestamptz;

-- +goose Down
-- Development databases only (docs/upgrades.md: migrations only go up).
ALTER TABLE repo_var_sources DROP COLUMN sync_queued_at;
