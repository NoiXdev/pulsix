-- +goose Up
-- A dry run's non-secret options (backlog 3.126): {"tags": [...], "diff":
-- bool, "draft": bool}, set at enqueue next to check_mode so the run view
-- can show them without opening the sealed payload. Never the draft's text
-- or its hash. NULL for every other job, and for every existing one.
ALTER TABLE jobs ADD COLUMN dry_run jsonb;

-- +goose Down
ALTER TABLE jobs DROP COLUMN dry_run;
