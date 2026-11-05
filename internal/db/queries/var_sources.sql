-- The variable sources a repo sync read (backlog 7.75, migration 00081).
-- Written on the job finish path (repos.Service.RecordVarSources, inside
-- the finish transaction), read by the variables page and the apply.
--
-- Both writes of repo_var_sources select the repository instead of naming
-- it: a repository deleted while its sync ran inserts nothing, where a
-- plain INSERT would fail its foreign key and abort the whole finish
-- transaction.

-- name: UpsertRepoVarSources :execrows
-- A worker's read: the record is replaced, worker_reads set. Zero rows:
-- the repository is gone. The row lock it takes also orders two finishes
-- of the same repository, so their playbook replaces do not interleave.
INSERT INTO repo_var_sources (repo_id, org_id, commit_sha, synced_at, worker_reads, incomplete, left_out, notes)
SELECT r.id, r.org_id, @commit_sha::text, @synced_at::timestamptz, true, @incomplete::boolean, @left_out::jsonb, @notes::jsonb
FROM git_repos r
WHERE r.org_id = @org_id AND r.id = @repo_id
ON CONFLICT (repo_id) DO UPDATE SET
  commit_sha = EXCLUDED.commit_sha, synced_at = EXCLUDED.synced_at, worker_reads = true,
  incomplete = EXCLUDED.incomplete, left_out = EXCLUDED.left_out, notes = EXCLUDED.notes;

-- name: MarkRepoVarSourcesUnread :exec
-- A sync by a worker that reads no sources: an existing record is kept
-- and only flagged; without one an empty, flagged record is created.
INSERT INTO repo_var_sources (repo_id, org_id, commit_sha, synced_at, worker_reads)
SELECT r.id, r.org_id, '', @synced_at::timestamptz, false
FROM git_repos r
WHERE r.org_id = @org_id AND r.id = @repo_id
ON CONFLICT (repo_id) DO UPDATE SET worker_reads = false;

-- name: DeleteRepoPlaybookSources :exec
DELETE FROM repo_playbook_sources WHERE org_id = @org_id AND repo_id = @repo_id;

-- name: InsertRepoPlaybookSource :exec
INSERT INTO repo_playbook_sources (org_id, repo_id, path, result) VALUES (@org_id, @repo_id, @path, @result);

-- name: GetRepoVarSources :one
SELECT commit_sha, synced_at, worker_reads, incomplete, left_out, notes
FROM repo_var_sources
WHERE org_id = @org_id AND repo_id = @repo_id;

-- name: ListRepoPlaybookSources :many
-- Byte order, the order Go sorts paths in.
SELECT path, result FROM repo_playbook_sources
WHERE org_id = @org_id AND repo_id = @repo_id
ORDER BY path COLLATE "C";

-- name: GetRepoPlaybookSource :one
SELECT path, result FROM repo_playbook_sources
WHERE org_id = @org_id AND repo_id = @repo_id AND path = @path;
