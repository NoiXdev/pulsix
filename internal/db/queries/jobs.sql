-- name: CreateJob :one
INSERT INTO jobs (id, org_id, zone_id, kind, template_id, host_ids, status, run_group_id, trigger, actor_id, payload)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
RETURNING *;

-- name: GetJob :one
-- Scoped by org_id in the WHERE clause, not only relying on RLS (see
-- GetZone's comment in hosts.sql): a job belonging to a different
-- organization must be invisible, not merely forbidden.
--
-- The three display joins resolve what the run view shows from ids the
-- job row itself only carries: the actor's display name, the worker's
-- name (both NULL when the id is), and the template's current name —
-- which a deleted template (dangling template_id) leaves NULL; the API
-- renders that as "(deleted)". All joins are LEFT: the job row must come
-- back complete no matter what any of the three ids point at.
SELECT j.*, u.display_name AS actor_name, w.name AS worker_name,
       jt.name AS template_name
FROM jobs AS j
LEFT JOIN users AS u ON u.id = j.actor_id
LEFT JOIN workers AS w ON w.id = j.worker_id
LEFT JOIN job_templates AS jt ON jt.id = j.template_id
WHERE j.org_id = $1 AND j.id = $2;

-- name: ListJobs :many
-- Every optional filter (zone, status, kind, template, run group, and the
-- cursor's own (created_at, id) keyset pair) is expressed as "param IS
-- NULL OR <condition>", the same one-query-serves-every-combination shape
-- ListHosts uses. Ordering is descending on (created_at, id) — newest runs
-- first, id as the tiebreaker — so the cursor condition resumes strictly
-- before the last row of the previous page.
SELECT * FROM jobs
WHERE org_id = sqlc.arg(org_id)
  AND (sqlc.narg(zone_id)::uuid IS NULL OR zone_id = sqlc.narg(zone_id)::uuid)
  AND (sqlc.narg(status)::text IS NULL OR status = sqlc.narg(status)::text)
  AND (sqlc.narg(kind)::text IS NULL OR kind = sqlc.narg(kind)::text)
  AND (sqlc.narg(template_id)::uuid IS NULL OR template_id = sqlc.narg(template_id)::uuid)
  AND (sqlc.narg(run_group_id)::uuid IS NULL OR run_group_id = sqlc.narg(run_group_id)::uuid)
  AND (
    sqlc.narg(cursor_at)::timestamptz IS NULL
    OR (created_at, id) < (sqlc.narg(cursor_at)::timestamptz, sqlc.narg(cursor_id)::uuid)
  )
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(row_limit);

-- name: CancelJob :one
-- One atomic statement for both cancel shapes: a queued job has never been
-- shipped to a worker, so it goes straight to 'cancelled' (terminal), while
-- a claimed/running job enters 'cancelling' and waits for the worker's
-- confirmation through ReportResult. The WHERE clause rejects jobs that are
-- already leaving or past the queue (execrows would report 0 rows then, but
-- :one reports "no rows" as pgx.ErrNoRows, which the store maps).
UPDATE jobs
SET status = CASE WHEN status = 'queued' THEN 'cancelled' ELSE 'cancelling' END,
    finished_at = CASE WHEN status = 'queued' THEN now() END
WHERE org_id = $1 AND id = $2 AND status IN ('queued', 'claimed', 'running')
RETURNING status;

-- name: ListJobEvents :many
-- The run view's event log. job_events has no row-level security (see
-- 00018_jobs.sql), so the org filter here — after Service.Events has
-- resolved the job and its organization — is what keeps one tenant from
-- reading another's events.
SELECT seq, at, host, task, state, payload
FROM job_events
WHERE org_id = $1 AND job_id = $2 AND seq > $3
ORDER BY seq;

-- name: ClaimJob :one
-- THE org-unscoped claim (plan Global Constraints): runs on the raw pool,
-- outside any pulsix.org_id scope, keyed by the worker's zones. FOR UPDATE
-- SKIP LOCKED makes concurrent claimers pick distinct rows without
-- blocking each other; the partial jobs_claim_idx serves the inner scan.
-- The brief's claimed state is collapsed into running here, with
-- claimed_at and started_at set together: nothing between the two states
-- ever observes a job, and the schema's CHECK keeps both spellings legal.
-- worker_id stays NULL: the Claimer port carries no worker identity (see
-- SQLQueue's doc comment).
UPDATE jobs SET status = 'running', worker_id = NULL, claimed_at = now(), started_at = now()
WHERE id = (
  SELECT id FROM jobs
  WHERE status = 'queued' AND zone_id = ANY($1::uuid[])
  ORDER BY created_at
  FOR UPDATE SKIP LOCKED
  LIMIT 1
)
RETURNING *;

-- name: RequeueStaleJobs :many
-- Reaper pass 1 (org-unscoped, raw pool): claims whose worker went away
-- go back to queued for the next claim round. The live ids — the jobs
-- whose bound stream still shows signs of life — are excluded; an empty
-- array excludes nothing (id = ANY('{}') is false for every row).
UPDATE jobs SET status = 'queued', worker_id = NULL, claimed_at = NULL, started_at = NULL
WHERE status IN ('claimed', 'running') AND claimed_at < @stale_before::timestamptz
  AND NOT (id = ANY(@live::uuid[]))
RETURNING id;

-- name: ListCancellingJobs :many
-- Reaper pass 2 candidates (org-unscoped, raw pool): every job waiting for
-- its worker to confirm a cancel. The deadline check happens in Go, where
-- the cancel-request timestamps live (see cancelRegistry).
SELECT id, claimed_at FROM jobs WHERE status = 'cancelling';

-- name: UpdateRepoSyncState :execrows
-- The terminal result of a sync_repo job, recorded on the repository row by
-- the queue's finish path (SQLQueue.Finish → jobs.Store.RecordSyncResult):
-- last_sync carries when the job ended, last_sync_error its failure reason
-- (a NULL clears the previous failure on success). The WHERE clause is
-- scoped by org_id and its update count is the caller's ErrNotFound signal
-- — a repository deleted while its sync ran records nothing.
UPDATE git_repos SET last_sync = @last_sync, last_sync_error = @last_sync_error
WHERE org_id = @org_id AND id = @id;
