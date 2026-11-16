-- name: PlanTemplateFollowUps :execrows
-- Snapshots a template's configured follow-ups as pending rows of a run
-- group at launch. A second plan for the same group (a retried launch) is a
-- no-op per template.
INSERT INTO run_follow_ups (id, org_id, parent_group_id, template_id, condition, position,
                            root_trigger, root_ref, depth, path, configured_by)
SELECT gen_random_uuid(), f.org_id, @parent_group_id, f.follow_up_template_id, f.condition, f.position,
       @root_trigger, sqlc.narg(root_ref), @depth, @path::uuid[], sqlc.narg(configured_by)
FROM job_template_follow_ups f
WHERE f.org_id = @org_id AND f.template_id = @template_id
ON CONFLICT (parent_group_id, template_id) DO NOTHING;

-- name: InsertRunFollowUp :execrows
-- One follow-up of a run's own override list. Zero rows affected means the
-- group already has a row for that template.
INSERT INTO run_follow_ups (id, org_id, parent_group_id, template_id, condition, position,
                            root_trigger, root_ref, depth, path, configured_by)
VALUES (@id, @org_id, @parent_group_id, @template_id, @condition, @position,
        @root_trigger, sqlc.narg(root_ref), @depth, @path::uuid[], sqlc.narg(configured_by))
ON CONFLICT (parent_group_id, template_id) DO NOTHING;

-- name: CopyRunFollowUps :execrows
-- A rerun or retry inherits the source group's follow-up configuration as
-- fresh pending rows, whatever became of the originals.
INSERT INTO run_follow_ups (id, org_id, parent_group_id, template_id, condition, position,
                            root_trigger, root_ref, depth, path, configured_by)
SELECT gen_random_uuid(), r.org_id, @to_group, r.template_id, r.condition, r.position,
       r.root_trigger, r.root_ref, r.depth, r.path, sqlc.narg(configured_by)
FROM run_follow_ups r
WHERE r.org_id = @org_id AND r.parent_group_id = @from_group
ON CONFLICT (parent_group_id, template_id) DO NOTHING;

-- name: ListRunFollowUps :many
-- A group's follow-ups with the template's current name (empty once the
-- template is deleted) and the first job of the child run (the nil uuid when
-- no child run started).
SELECT r.*,
       COALESCE(t.name, '')::text AS template_name,
       COALESCE(fj.id, '00000000-0000-0000-0000-000000000000'::uuid)::uuid AS child_first_job_id
FROM run_follow_ups r
LEFT JOIN job_templates t ON t.org_id = r.org_id AND t.id = r.template_id
LEFT JOIN LATERAL (
  SELECT j.id FROM jobs j
  WHERE j.org_id = r.org_id AND j.run_group_id = r.child_group_id AND j.kind = 'run'
  ORDER BY j.created_at, j.id LIMIT 1) fj ON true
WHERE r.org_id = @org_id AND r.parent_group_id = @parent_group_id
ORDER BY r.position, r.created_at, r.id;

-- name: GetFollowUpOfChainedRun :one
-- The follow-up a chained run group was started for, by the table's unique
-- key: the run's trigger_ref is the parent group, its template the
-- follow-up's. Not by child_group_id: the starter writes that in its batch
-- transaction, which commits only after the whole batch, so a quick child
-- run (or one replayed after a lost write-back) can finish before the row
-- names it. Same shape as ListRunFollowUps.
SELECT r.*,
       COALESCE(t.name, '')::text AS template_name,
       COALESCE(fj.id, '00000000-0000-0000-0000-000000000000'::uuid)::uuid AS child_first_job_id
FROM run_follow_ups r
LEFT JOIN job_templates t ON t.org_id = r.org_id AND t.id = r.template_id
LEFT JOIN LATERAL (
  SELECT j.id FROM jobs j
  WHERE j.org_id = r.org_id AND j.run_group_id = r.child_group_id AND j.kind = 'run'
  ORDER BY j.created_at, j.id LIMIT 1) fj ON true
WHERE r.org_id = @org_id AND r.parent_group_id = @parent_group_id AND r.template_id = @template_id;

-- name: ResolvePendingFollowUps :execrows
-- The parent group has finished: every pending follow-up becomes due (and is
-- handed the hosts it plays on) or is skipped with the reason. next_attempt_at
-- starts at now for a due row, so the starter's index finds it.
UPDATE run_follow_ups
SET state = @state::text,
    reason = @reason,
    host_ids = @host_ids::uuid[],
    decided_at = @now::timestamptz,
    next_attempt_at = CASE WHEN @state::text = 'due' THEN @now::timestamptz END
WHERE org_id = @org_id AND parent_group_id = @parent_group_id AND state = 'pending';

-- name: PlayedHosts :many
-- The hosts a follow-up plays on: per run job of the group, the hosts its
-- recap names as ok, changed or skipped that the job targeted and that did
-- not also fail or become unreachable. A limit or the playbook's hosts:
-- narrows what was played without changing jobs.host_ids, so the recap, not
-- host_ids, is the truth. The guards mirror runGroupQuery
-- (internal/jobs/run_group.go): jsonb_typeof screens a JSON null, and
-- pg_input_is_valid screens a bare host name before the uuid cast.
WITH g AS (
  SELECT host_ids,
    CASE WHEN jsonb_typeof(stats->'ok_hosts') = 'array' THEN stats->'ok_hosts' ELSE '[]'::jsonb END
    || CASE WHEN jsonb_typeof(stats->'changed_hosts') = 'array' THEN stats->'changed_hosts' ELSE '[]'::jsonb END
    || CASE WHEN jsonb_typeof(stats->'skipped_hosts') = 'array' THEN stats->'skipped_hosts' ELSE '[]'::jsonb END AS played,
    CASE WHEN jsonb_typeof(stats->'failed_hosts') = 'array' THEN stats->'failed_hosts' ELSE '[]'::jsonb END
    || CASE WHEN jsonb_typeof(stats->'unreachable_hosts') = 'array' THEN stats->'unreachable_hosts' ELSE '[]'::jsonb END AS bad
  FROM jobs
  WHERE org_id = @org_id AND run_group_id = @run_group_id AND kind = 'run'
)
SELECT DISTINCT h.host_id::uuid AS host_id
FROM g, jsonb_array_elements_text(g.played) AS h(host_id)
WHERE pg_input_is_valid(h.host_id, 'uuid')
  AND h.host_id::uuid = ANY(g.host_ids)
  AND NOT EXISTS (
    SELECT 1 FROM jsonb_array_elements_text(g.bad) AS b(host_id)
    WHERE pg_input_is_valid(b.host_id, 'uuid') AND b.host_id::uuid = h.host_id::uuid)
ORDER BY 1;

-- name: LockDueFollowUps :many
-- The starter's scan: runs in a raw-pool transaction without pulsix.org_id
-- (bypass policy), across every organization; SKIP LOCKED lets several
-- managers tick without a leader. FOR NO KEY UPDATE, like LockDueSchedules:
-- the starter never changes a key column.
SELECT * FROM run_follow_ups
WHERE state = 'due' AND next_attempt_at <= @now::timestamptz
ORDER BY next_attempt_at, id
LIMIT @lim
FOR NO KEY UPDATE SKIP LOCKED;

-- name: TryLockFollowUpTemplate :one
-- The starter's per-template start lock, taken in its batch transaction and
-- held until the batch commits, after the enqueue it guards has committed.
-- A second manager starting the same template in the meantime does not get
-- it, and once it does, the active-run check sees the first one's run. The
-- try variant never waits: two batches locking two templates in opposite
-- orders cannot deadlock.
SELECT pg_try_advisory_xact_lock(hashtext(@lock_key::text))::bool AS locked;

-- name: ListStalePendingFollowUps :many
-- The sweep's scan (raw pool, bypass policy): pending rows older than the
-- cutoff whose parent group has no unfinished run job any more, i.e. the
-- group's finish report was lost or the launch created no job at all.
SELECT * FROM run_follow_ups r
WHERE r.state = 'pending' AND r.created_at < @before::timestamptz
  AND NOT EXISTS (
    SELECT 1 FROM jobs j
    WHERE j.org_id = r.org_id AND j.run_group_id = r.parent_group_id AND j.kind = 'run'
      AND j.status NOT IN ('succeeded', 'failed', 'cancelled', 'error'))
ORDER BY r.created_at, r.id
LIMIT @lim;

-- name: RunGroupOutcome :one
-- The verdict of a finished run group, for the sweep: the job count, how many
-- failed or errored and how many were cancelled.
SELECT count(*) AS jobs,
       count(*) FILTER (WHERE status IN ('failed', 'error')) AS failed_or_error,
       count(*) FILTER (WHERE status = 'cancelled') AS cancelled
FROM jobs
WHERE org_id = @org_id AND run_group_id = @run_group_id AND kind = 'run';

-- name: MarkFollowUpStarted :execrows
UPDATE run_follow_ups
SET state = 'started', child_group_id = @child_group_id, reason = @reason, started_at = @now::timestamptz
WHERE org_id = @org_id AND id = @id AND state = 'due';

-- name: MarkFollowUpSkipped :execrows
UPDATE run_follow_ups
SET state = 'skipped', reason = @reason
WHERE org_id = @org_id AND id = @id AND state = 'due';

-- name: MarkFollowUpWaiting :execrows
-- The follow-up's template is busy: the row stays due and is retried later,
-- without counting an attempt.
UPDATE run_follow_ups
SET reason = @reason, next_attempt_at = @next_attempt_at::timestamptz
WHERE org_id = @org_id AND id = @id AND state = 'due';

-- name: BumpFollowUpAttempt :one
-- A start attempt failed: count it, keep the error as the reason and retry
-- later. Returns the attempts so far so the caller can give up.
UPDATE run_follow_ups
SET attempts = attempts + 1, reason = @error, next_attempt_at = @next_attempt_at::timestamptz
WHERE org_id = @org_id AND id = @id AND state = 'due'
RETURNING attempts;

-- name: DismissPendingFollowUps :execrows
UPDATE run_follow_ups
SET state = 'dismissed', reason = @reason, decided_at = @now::timestamptz
WHERE org_id = @org_id AND parent_group_id = @parent_group_id AND state = 'pending';

-- name: HasPendingFollowUps :one
-- The sink's cheap gate: most finished groups have no follow-ups at all.
SELECT EXISTS (
  SELECT 1 FROM run_follow_ups
  WHERE org_id = @org_id AND parent_group_id = @parent_group_id AND state = 'pending');
