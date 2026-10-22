-- name: CreateSchedule :one
INSERT INTO schedules (id, org_id, template_id, name, mode, cron, simple, timezone, guards, "limit", extra_vars, paused, next_run_at, created_by)
VALUES (@id, @org_id, @template_id, @name, @mode, @cron, @simple, @timezone, @guards, sqlc.narg(limit_override), @extra_vars, false, sqlc.narg(next_run_at), sqlc.narg(created_by))
RETURNING *;

-- name: GetSchedule :one
SELECT * FROM schedules WHERE org_id = @org_id AND id = @id;

-- name: ListSchedules :many
SELECT * FROM schedules
WHERE org_id = @org_id AND (sqlc.narg(template_id)::uuid IS NULL OR template_id = sqlc.narg(template_id)::uuid)
ORDER BY name;

-- name: UpdateSchedule :one
-- No row back means a stale revision or an unknown schedule (the store
-- tells the two apart with GetSchedule).
UPDATE schedules SET template_id = @template_id, name = @name, mode = @mode, cron = @cron, simple = @simple,
  timezone = @timezone, guards = @guards, "limit" = sqlc.narg(limit_override), extra_vars = @extra_vars,
  next_run_at = sqlc.narg(next_run_at), revision = revision + 1, updated_at = now()
WHERE org_id = @org_id AND id = @id AND revision = @revision
RETURNING *;

-- name: SetSchedulePaused :one
UPDATE schedules SET paused = @paused, next_run_at = sqlc.narg(next_run_at), revision = revision + 1, updated_at = now()
WHERE org_id = @org_id AND id = @id
RETURNING *;

-- name: PauseAllSchedules :execrows
UPDATE schedules SET paused = true, next_run_at = NULL, revision = revision + 1, updated_at = now()
WHERE org_id = @org_id AND NOT paused;

-- name: DeleteSchedule :execrows
DELETE FROM schedules WHERE org_id = @org_id AND id = @id;

-- name: InsertScheduleRun :exec
INSERT INTO schedule_runs (id, org_id, schedule_id, due_at, decided_at, outcome, reason, job_group_id)
VALUES (@id, @org_id, @schedule_id, @due_at, @decided_at, @outcome, @reason, sqlc.narg(job_group_id));

-- name: ListScheduleRuns :many
SELECT * FROM schedule_runs WHERE org_id = @org_id AND schedule_id = @schedule_id
ORDER BY decided_at DESC, id DESC LIMIT @lim;

-- name: LatestScheduleRuns :many
-- One row per schedule of the org (the list's "Last" column): a LATERAL
-- join against schedules, one index descent per schedule on
-- schedule_runs_schedule_idx (org_id, schedule_id, decided_at DESC), costs
-- O(schedules) — the DISTINCT ON this replaced scanned every run the
-- organization had ever recorded to produce the same result (backlog 2.61).
SELECT r.* FROM schedules AS s
CROSS JOIN LATERAL (
  SELECT * FROM schedule_runs
  WHERE org_id = s.org_id AND schedule_id = s.id
  ORDER BY decided_at DESC, id DESC
  LIMIT 1
) AS r
WHERE s.org_id = @org_id;

-- name: LockDueSchedules :many
-- The ticker's due scan: runs in a raw-pool transaction without
-- pulsix.org_id (bypass policy), across every organization; SKIP LOCKED
-- lets several managers tick without a leader (spec §2). FOR NO KEY
-- UPDATE, not FOR UPDATE: the ticker never changes a key column, and NO
-- KEY UPDATE does not conflict with the FOR KEY SHARE a schedule_runs
-- insert's FK check takes (e.g. a concurrent "run now") — only with
-- another UPDATE/DELETE of the schedule row itself. Fix round 1, Important
-- #3 ("pool exhaustion circular wait", the v0.2b pattern).
SELECT * FROM schedules
WHERE NOT paused AND next_run_at IS NOT NULL AND next_run_at <= @now::timestamptz
ORDER BY next_run_at
LIMIT @lim
FOR NO KEY UPDATE SKIP LOCKED;

-- name: AdvanceSchedule :exec
-- Never touches revision or updated_at: an operator's If-Match stays valid.
UPDATE schedules SET last_run_at = @last_run_at, next_run_at = sqlc.narg(next_run_at)
WHERE org_id = @org_id AND id = @id;

-- name: TrimScheduleRuns :exec
DELETE FROM schedule_runs AS r
WHERE r.org_id = @org_id AND r.schedule_id = @schedule_id
  AND r.id NOT IN (
    SELECT k.id FROM schedule_runs AS k
    WHERE k.org_id = @org_id AND k.schedule_id = @schedule_id
    ORDER BY k.decided_at DESC, k.id DESC
    LIMIT @keep
  );

-- name: ListScheduleRunsBetween :many
-- The calendar's past occurrences and the digest's week: every decision
-- whose due time falls in [from, to).
SELECT * FROM schedule_runs
WHERE org_id = @org_id AND due_at >= @from_at::timestamptz AND due_at < @to_at::timestamptz
ORDER BY due_at, id;
