-- name: LockRepoForVarSources :one
-- The variable sources a repo sync read (backlog 7.75, migration 00081)
-- are written on the job finish path, inside the finish transaction
-- (repos.Service.RecordVarSources). It locks the repository row first and
-- only then reads the previous record it merges into: the lock holds until
-- the finish commits, so two overlapping finishes of one repository take
-- turns instead of both merging into the same previous rows. NO KEY UPDATE
-- leaves foreign key checks against the row (which take KEY SHARE)
-- unblocked. No row: the repository is gone.
SELECT id FROM git_repos WHERE org_id = @org_id AND id = @repo_id FOR NO KEY UPDATE;

-- name: UpsertRepoVarSources :execrows
-- A worker's read: the record is replaced, worker_reads set. vault_names
-- is NULL when the read does not name the vault-encrypted variables (an
-- older worker's, migration 00132). Like
-- MarkRepoVarSourcesUnread it selects the repository instead of naming it:
-- a repository deleted while its sync ran inserts nothing (zero rows),
-- where a plain INSERT would fail its foreign key and abort the whole
-- finish transaction.
INSERT INTO repo_var_sources (repo_id, org_id, commit_sha, synced_at, worker_reads, incomplete, left_out, notes, synced_ref, sync_claimed_at, vault_names)
SELECT r.id, r.org_id, @commit_sha::text, @synced_at::timestamptz, true, @incomplete::boolean, @left_out::jsonb, @notes::jsonb,
       sqlc.narg(synced_ref)::text, sqlc.narg(sync_claimed_at)::timestamptz, sqlc.narg(vault_names)::jsonb
FROM git_repos r
WHERE r.org_id = @org_id AND r.id = @repo_id
ON CONFLICT (repo_id) DO UPDATE SET
  commit_sha = EXCLUDED.commit_sha, synced_at = EXCLUDED.synced_at, worker_reads = true,
  incomplete = EXCLUDED.incomplete, left_out = EXCLUDED.left_out, notes = EXCLUDED.notes,
  synced_ref = EXCLUDED.synced_ref, sync_claimed_at = EXCLUDED.sync_claimed_at, vault_names = EXCLUDED.vault_names;

-- name: MarkRepoVarSourcesUnread :exec
-- A sync by a worker that reads no sources: an existing record is kept
-- and only flagged; without one an empty, flagged record is created. A
-- repository gone meanwhile inserts nothing (see UpsertRepoVarSources).
INSERT INTO repo_var_sources (repo_id, org_id, commit_sha, synced_at, worker_reads)
SELECT r.id, r.org_id, '', @synced_at::timestamptz, false
FROM git_repos r
WHERE r.org_id = @org_id AND r.id = @repo_id
ON CONFLICT (repo_id) DO UPDATE SET worker_reads = false;

-- name: DeleteRepoPlaybookSources :exec
DELETE FROM repo_playbook_sources WHERE org_id = @org_id AND repo_id = @repo_id;

-- name: InsertRepoPlaybookSource :exec
-- content and content_left_out: the playbook's text as the sync sent it, or
-- why it sent none (backlog 3.127); both NULL when it sent neither.
INSERT INTO repo_playbook_sources (org_id, repo_id, path, result, content, content_left_out)
VALUES (@org_id, @repo_id, @path, @result, sqlc.narg(content)::text, sqlc.narg(content_left_out)::text);

-- name: GetRepoVarSources :one
SELECT commit_sha, synced_at, worker_reads, incomplete, left_out, notes, synced_ref, sync_claimed_at, vault_names
FROM repo_var_sources
WHERE org_id = @org_id AND repo_id = @repo_id;

-- name: ListRepoPlaybookSources :many
-- Byte order, the order Go sorts paths in.
SELECT path, result FROM repo_playbook_sources
WHERE org_id = @org_id AND repo_id = @repo_id
ORDER BY path COLLATE "C";

-- name: GetPlaybookVarSource :one
-- The repository's record and the row of one playbook path in one
-- statement (backlog 7.87), so the two come from the same snapshot: no
-- record answers no row, a record without a row for the path answers its
-- columns with a null path and result.
SELECT r.commit_sha, r.synced_at, r.worker_reads, r.incomplete, r.left_out, r.notes, r.synced_ref, r.vault_names,
       p.path, p.result
FROM repo_var_sources r
LEFT JOIN repo_playbook_sources p ON p.org_id = r.org_id AND p.repo_id = r.repo_id AND p.path = sqlc.arg(path)
WHERE r.org_id = sqlc.arg(org_id) AND r.repo_id = sqlc.arg(repo_id);

-- name: ListRepoPlaybookTexts :many
-- Every playbook row's text, for a sync that keeps rows of an earlier one.
SELECT path, content, content_left_out FROM repo_playbook_sources
WHERE org_id = @org_id AND repo_id = @repo_id;

-- name: GetRepoPlaybookText :one
-- One playbook path's text with the record it belongs to, in one statement
-- (one snapshot). No record answers no row; a record without a row for the
-- path (the last sync did not report it) answers its commit and time with
-- a null text and reason. kept: the record's left_out names the path, so
-- its row is from an earlier read than the record's commit.
SELECT r.commit_sha, r.synced_at, (r.left_out @> jsonb_build_array(sqlc.arg(path)::text))::boolean AS kept,
       p.content, p.content_left_out
FROM repo_var_sources r
LEFT JOIN repo_playbook_sources p ON p.org_id = r.org_id AND p.repo_id = r.repo_id AND p.path = sqlc.arg(path)
WHERE r.org_id = sqlc.arg(org_id) AND r.repo_id = sqlc.arg(repo_id);
