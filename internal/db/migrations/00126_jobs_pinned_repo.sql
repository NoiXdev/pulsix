-- +goose Up
-- The repository a pinned job's commit belongs to (final review of spec
-- 2026-11-25 A, M4): a retry runs its original job's commit again, which
-- only exists in that repository. A retry request whose template has since
-- moved to another repository or to an editor playbook is then blocked in
-- its picture and refused at approval, instead of failing on the worker
-- ("commit … is no longer in the repository"). Set with pinned_commit at
-- the enqueue; NULL for every unpinned job and for jobs pinned before this
-- column. No foreign key, like job_id elsewhere: the history outlives a
-- deleted repository. Nullable and without a default, the ADD COLUMN
-- touches only the catalog.
ALTER TABLE jobs ADD COLUMN pinned_repo_id uuid;

-- +goose Down
-- Development databases only (docs/upgrades.md: migrations only go up).
ALTER TABLE jobs DROP COLUMN pinned_repo_id;
