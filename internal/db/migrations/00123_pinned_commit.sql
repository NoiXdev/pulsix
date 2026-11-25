-- +goose Up
-- The commit an approved run runs (spec 2026-11-25 assistant API gaps, A,
-- decision D3). An approval stores the commit its picture showed, the
-- commit of the repository's last successful sync, on the request and on
-- every job it enqueues; the claim compares the two, and the worker checks
-- that commit out instead of the branch tip. NULL is a run without a pin:
-- every row from before these columns, a run of an editor playbook, and
-- every run a person, a schedule or a trigger started.
ALTER TABLE run_requests ADD COLUMN pinned_commit text;
ALTER TABLE jobs ADD COLUMN pinned_commit text;

-- What the sync behind repo_var_sources.commit_sha fetched: the digest of
-- the repository's URL, branch and subpath at the sync's claim
-- (jobs.SyncedRef), so a pin can tell that the repository changed after
-- the sync. NULL for a record of a sync from before this column, which
-- therefore cannot be pinned until the repository is synced again.
ALTER TABLE repo_var_sources ADD COLUMN synced_ref text;

-- +goose Down
-- Development databases only (docs/upgrades.md: migrations only go up).
ALTER TABLE repo_var_sources DROP COLUMN synced_ref;
ALTER TABLE jobs DROP COLUMN pinned_commit;
ALTER TABLE run_requests DROP COLUMN pinned_commit;
