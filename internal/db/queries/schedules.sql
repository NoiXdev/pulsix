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
SELECT DISTINCT ON (schedule_id) * FROM schedule_runs WHERE org_id = @org_id
ORDER BY schedule_id, decided_at DESC, id DESC;
