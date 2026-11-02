-- name: CreateJob :one
INSERT INTO jobs (id, org_id, zone_id, kind, template_id, host_ids, status, run_group_id, trigger, actor_id, payload,
                  depends_on, rotation_credential_id, trigger_ref, hosts_excluded)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
RETURNING *;

-- name: ReserveRunGroup :one
-- The idempotency claim (migration 00067): the first caller for (org, key)
-- inserts and gets its own candidate group id back; every later caller for
-- the same key hits the conflict and gets the FIRST caller's group id back
-- instead — the DO UPDATE is a no-op (it sets the column to its own current
-- value) chosen only so ON CONFLICT still RETURNINGs a row, since DO NOTHING
-- would return none. (xmax = 0) is the standard way to tell the two apart in
-- one round trip: a freshly inserted row's xmax is still unset, an updated
-- row's is not, regardless of whether the update actually changed anything.
INSERT INTO job_idempotency_keys (org_id, idempotency_key, run_group_id)
VALUES (@org_id, @idempotency_key, @run_group_id)
ON CONFLICT (org_id, idempotency_key)
DO UPDATE SET run_group_id = job_idempotency_keys.run_group_id
RETURNING run_group_id, (xmax = 0) AS reserved;

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
  AND (sqlc.narg(trigger)::text IS NULL OR trigger = sqlc.narg(trigger)::text)
  AND (sqlc.narg(trigger_ref)::uuid IS NULL OR trigger_ref = sqlc.narg(trigger_ref)::uuid)
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
SELECT seq, at, host, task, state, payload, attempt
FROM job_events
WHERE org_id = $1 AND job_id = $2 AND seq > $3
ORDER BY seq;

-- name: LockActiveWorker :execrows
-- Backlog 7.36: binds a worker claim to the worker's current state. Run in
-- the claim transaction before ClaimJob, it takes a share lock on the row
-- of an active worker (0 rows: revoked or gone, claim nothing). A
-- revocation's UPDATE in flight holds that row, so the claim waits for it
-- and then sees it; a claim that locked first makes the revocation wait,
-- and the requeue after it then finds the job it claimed.
SELECT 1 FROM workers WHERE id = sqlc.arg(id) AND status = 'active' FOR SHARE;

-- name: WorkerIsActive :one
-- The same question without a lock, asked once more after the payload was
-- built and before it is handed out (backlog 7.36).
SELECT EXISTS (SELECT 1 FROM workers WHERE id = sqlc.arg(id) AND status = 'active');

-- name: ClaimJob :one
-- THE org-unscoped claim (plan Global Constraints): runs on the raw pool,
-- outside any pulsix.org_id scope, keyed by the worker's zones. FOR UPDATE
-- SKIP LOCKED makes concurrent claimers pick distinct rows without
-- blocking each other; the partial jobs_claim_idx serves the inner scan.
-- The brief's claimed state is collapsed into running here, with
-- claimed_at and started_at set together: nothing between the two states
-- ever observes a job, and the schema's CHECK keeps both spellings legal.
-- worker_id records the claiming worker (NULL only for the manager's own
-- in-process claims): the terminal result is accepted from that worker
-- alone (SQLQueue.FinishFor). org_id is the claiming worker's own
-- organization (from its registry row, never from the request): zone ids
-- are visible through each organization's API, so the zone filter alone
-- must never be what keeps another organization's jobs out of reach.
-- cancel_stops_play is the claiming worker's own answer (migration 00072),
-- NULL for an in-process claim.
UPDATE jobs SET status = 'running', worker_id = sqlc.narg(worker_id)::uuid, claimed_at = now(), started_at = now(),
    attempts = attempts + 1, cancel_stops_play = sqlc.narg(cancel_stops_play)::boolean
WHERE id = (
  SELECT j.id FROM jobs AS j
  WHERE j.status = 'queued' AND j.org_id = sqlc.arg(org_id)::uuid AND j.zone_id = ANY(sqlc.arg(zone_ids)::uuid[])
    -- A dependent job waits until its dependency SUCCEEDED (any other end
    -- cancels it in the dependency's terminal transition).
    AND (j.depends_on IS NULL OR EXISTS (
      SELECT 1 FROM jobs AS dep WHERE dep.id = j.depends_on AND dep.status = 'succeeded'
    ))
  ORDER BY j.created_at
  FOR UPDATE SKIP LOCKED
  LIMIT 1
)
RETURNING *;

-- name: RecordVaultIdentities :exec
-- The claim-time record backlog 2.91 exists for: called from the same
-- transaction ClaimJob's UPDATE runs in (internal/jobs/queue_sql.go's
-- claim), so a job this transaction commits as 'running' can never be
-- read back without its resolved vault identity set already committed
-- alongside it. identities is always a JSON array, even an empty one — see
-- migration 00064's own comment for why NULL (never called) and '[]'
-- (called, nothing to decrypt) must stay distinguishable. Never called
-- again after this: a run's own history does not change after the run
-- that made it, and nothing else about this column is ever written.
UPDATE jobs SET resolved_vault_identities = sqlc.arg(identities)::jsonb WHERE id = sqlc.arg(id);

-- name: RequeueStaleJobs :many
-- Reaper pass 1 (org-unscoped, raw pool): claims whose worker went away
-- go back to queued for the next claim round — but only while the job has
-- attempts left (attempts counts claims; see ListExhaustedStaleJobs for the
-- rest). The live ids — the jobs whose bound stream still shows signs of
-- life — are excluded; an empty array excludes nothing (id = ANY('{}') is
-- false for every row).
UPDATE jobs SET status = 'queued', worker_id = NULL, claimed_at = NULL, started_at = NULL, cancel_stops_play = NULL
WHERE status IN ('claimed', 'running') AND claimed_at < @stale_before::timestamptz
  AND NOT (id = ANY(@live::uuid[]))
  AND attempts < @max_attempts::integer
RETURNING id;

-- name: ListExhaustedStaleJobs :many
-- Reaper pass 1b (org-unscoped, raw pool): stale claims that used up their
-- attempts. The reaper fails each through the queue's terminal transition
-- instead of requeuing it again.
SELECT id, attempts FROM jobs
WHERE status IN ('claimed', 'running') AND claimed_at < @stale_before::timestamptz
  AND NOT (id = ANY(@live::uuid[]))
  AND attempts >= @max_attempts::integer;

-- name: FailExhaustedStaleJob :execrows
-- The exhaustion transition (backlog 2.53): the reaper lists its exhausted
-- candidates first and fails each through the queue's terminal transition
-- afterwards. The staleness and attempts checks ride INSIDE this UPDATE —
-- the same cutoff the reaper's scan used — so a job that got a fresh claim
-- (claimed_at refreshed, attempts incremented) or otherwise moved on
-- between the scan and this write is skipped instead of failed. The row
-- count is the caller's "was it still the stale exhausted job" signal.
UPDATE jobs SET status = @failed_status::text,
    stats = COALESCE(@stats::jsonb, stats), error = @err_text, finished_at = @finished_at
WHERE id = @id
  AND status = ANY(@from_statuses::text[])
  AND (sqlc.narg(worker_id)::uuid IS NULL OR worker_id = sqlc.narg(worker_id)::uuid)
  AND claimed_at < @stale_before::timestamptz
  AND status IN ('claimed', 'running')
  AND attempts >= @max_attempts::integer;

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

-- name: PinRepoHostKey :execrows
-- The TOFU completion of the sync sink (backlog 2.41): the first successful
-- sync's observed git-server key pins the repository. The WHERE clause is
-- what makes it pure TOFU — a row that already carries a pin is left alone
-- (the worker's strict check refuses a changed key before any data is
-- transferred, so a sync that succeeded against a changed key can never
-- happen; the sink never needs to overwrite). Zero rows affected means the
-- repository was unpinned meanwhile or is gone — recorded by the caller's
-- other statements.
UPDATE git_repos SET ssh_host_key = @host_key, ssh_host_public_key = @public_key, updated_at = now()
WHERE org_id = @org_id AND id = @id AND ssh_host_key IS NULL;

-- name: CancelQueuedDependents :many
-- A job that ended without succeeding can never let its dependents run:
-- they (and their own dependents, transitively) are cancelled in the same
-- transaction, with the reason and their sealed payloads wiped. Runs inside
-- the queue's org-bound terminal transaction.
WITH RECURSIVE chain AS (
  SELECT d.id FROM jobs AS d WHERE d.depends_on = sqlc.arg(job_id)::uuid AND d.status = 'queued'
  UNION
  SELECT d.id FROM jobs AS d JOIN chain ON d.depends_on = chain.id WHERE d.status = 'queued'
)
UPDATE jobs SET status = 'cancelled', finished_at = now(), payload = NULL,
    error = sqlc.arg(reason)::text
WHERE id IN (SELECT id FROM chain)
RETURNING id;

-- name: CountActiveRotationJobs :one
-- The rotation exclusivity check: jobs of the credential's rotation groups
-- that are not terminal yet.
SELECT count(*) FROM jobs
WHERE org_id = $1 AND rotation_credential_id = $2
  AND status NOT IN ('succeeded', 'failed', 'cancelled', 'error');

-- name: CreateRotationGroupTargets :exec
-- The revocation targets snapshot (backlog 2.46), written inside the
-- rotation group's own insert transaction: the referencing host and jump
-- host ids as they stood at enqueue time, keyed by the group id.
INSERT INTO rotation_groups (id, org_id, credential_id, targets)
VALUES ($1, $2, $3, $4);

-- name: GetRotationGroupTargets :one
-- The snapshot the revoke_key phase reads its targets from — NOT the live
-- reference lists: a target that stopped referencing the credential
-- mid-rotation is still revoked (backlog 2.46).
SELECT targets FROM rotation_groups WHERE org_id = $1 AND id = $2;

-- name: HasActiveTemplateJob :one
SELECT EXISTS (
  SELECT 1 FROM jobs
  WHERE org_id = $1 AND template_id = $2
    AND status NOT IN ('succeeded', 'failed', 'cancelled', 'error')
);

-- name: HasActiveRepoSync :one
-- sync_repo payloads are plain JSON ({"repo_id": ...}), never sealed.
SELECT EXISTS (
  SELECT 1 FROM jobs
  WHERE org_id = sqlc.arg(org_id) AND kind = 'sync_repo'
    AND payload->>'repo_id' = sqlc.arg(repo_id)::text
    AND status NOT IN ('succeeded', 'failed', 'cancelled', 'error')
);

-- name: RequeueWorkerJobs :many
-- A revoked worker's claimed and running jobs go back to the queue while
-- attempts remain (the claim already counted the attempt); the reason
-- stays in error until the next finish overwrites it.
UPDATE jobs SET status = 'queued', worker_id = NULL, claimed_at = NULL, started_at = NULL, cancel_stops_play = NULL,
    error = sqlc.arg(reason)::text
WHERE org_id = sqlc.arg(org_id) AND worker_id = sqlc.arg(worker_id)
  AND status IN ('claimed', 'running') AND attempts < sqlc.arg(max_attempts)::integer
RETURNING id;

-- name: ListWorkerJobsToEnd :many
-- The rest of a revoked worker's jobs: exhausted claims end as error,
-- cancelling ones as cancelled.
SELECT id, status FROM jobs
WHERE org_id = sqlc.arg(org_id) AND worker_id = sqlc.arg(worker_id)
  AND (status = 'cancelling' OR (status IN ('claimed', 'running') AND attempts >= sqlc.arg(max_attempts)::integer));

-- name: ListUnreportedFinishedRunGroups :many
-- Backlog 2.87: run groups that are complete but whose run.finished was
-- never delivered. The live paths report a group from the transaction
-- that completes it, and the cancel path from a SEPARATE transaction
-- opened after CancelJob already committed — so a manager that dies in
-- between leaves a finished group with no report and nothing to retry
-- it.
--
-- run_group_reported is only ever set on the group's first job, so
-- "nobody in the group is marked" is the whole test. Raw pool, bypass
-- policy, every organization — the reaper that runs it has no org of
-- its own.
--
-- The window keeps the sweep from resurrecting ancient groups after a
-- long outage: a notification about a run from last month is noise, not
-- news. finished_at can be NULL on a job cancelled before it ever
-- started, so the age is taken from created_at where it is.
SELECT org_id, run_group_id
FROM jobs
WHERE kind = 'run'
GROUP BY org_id, run_group_id
HAVING bool_and(status IN ('succeeded', 'failed', 'cancelled', 'error'))
   AND NOT bool_or(run_group_reported)
   AND max(COALESCE(finished_at, created_at)) >= @since::timestamptz
ORDER BY max(COALESCE(finished_at, created_at))
LIMIT @lim;
