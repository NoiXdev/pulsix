-- +goose Up
-- When the sync behind a repository's recorded read was last claimed by a
-- worker (final review of spec 2026-11-25 A, M2 and re-review N2): two
-- syncs of one repository can run at once on two workers and finish in
-- either order, and the read of the one that fetched earlier, finishing
-- last, used to overwrite the newer one's commit. RecordVarSources keeps
-- the record of the sync claimed later (a sync never claimed counts by
-- when it was queued), so a sync the reaper requeued and a worker claimed
-- again after a push wins. NULL is a record from before this column: any
-- sync replaces it. Nullable and without a default, the ADD COLUMN touches
-- only the catalog.
ALTER TABLE repo_var_sources ADD COLUMN sync_claimed_at timestamptz;

-- +goose Down
-- Development databases only (docs/upgrades.md: migrations only go up).
ALTER TABLE repo_var_sources DROP COLUMN sync_claimed_at;
