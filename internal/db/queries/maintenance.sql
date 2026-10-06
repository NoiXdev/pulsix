-- name: CreateMaintenanceWindow :one
INSERT INTO maintenance_windows (id, org_id, name, target_kind, host_id, group_id, monitoring_group_id, starts_at,
  ends_at, cron, duration_seconds, timezone, note, reason, created_by)
VALUES (@id, @org_id, @name, @target_kind, sqlc.narg(host_id), sqlc.narg(group_id), sqlc.narg(monitoring_group_id),
  sqlc.narg(starts_at), sqlc.narg(ends_at), sqlc.narg(cron), sqlc.narg(duration_seconds), sqlc.narg(timezone), @note,
  @reason, sqlc.narg(created_by))
RETURNING id;

-- name: GetMaintenanceWindow :one
SELECT w.*, COALESCE(h.name, g.name, m.name, '')::text AS target_name
FROM maintenance_windows AS w
LEFT JOIN hosts AS h ON h.org_id = w.org_id AND h.id = w.host_id
LEFT JOIN inventory_groups AS g ON g.org_id = w.org_id AND g.id = w.group_id
LEFT JOIN monitoring_groups AS m ON m.org_id = w.org_id AND m.id = w.monitoring_group_id
WHERE w.org_id = @org_id AND w.id = @id;

-- name: ListMaintenanceWindows :many
SELECT w.*, COALESCE(h.name, g.name, m.name, '')::text AS target_name
FROM maintenance_windows AS w
LEFT JOIN hosts AS h ON h.org_id = w.org_id AND h.id = w.host_id
LEFT JOIN inventory_groups AS g ON g.org_id = w.org_id AND g.id = w.group_id
LEFT JOIN monitoring_groups AS m ON m.org_id = w.org_id AND m.id = w.monitoring_group_id
WHERE w.org_id = @org_id
ORDER BY w.name;

-- name: UpdateMaintenanceWindow :execrows
UPDATE maintenance_windows SET name = @name, target_kind = @target_kind, host_id = sqlc.narg(host_id),
  group_id = sqlc.narg(group_id), monitoring_group_id = sqlc.narg(monitoring_group_id),
  starts_at = sqlc.narg(starts_at), ends_at = sqlc.narg(ends_at), cron = sqlc.narg(cron),
  duration_seconds = sqlc.narg(duration_seconds), timezone = sqlc.narg(timezone), note = @note, reason = @reason,
  revision = revision + 1, updated_at = now()
WHERE org_id = @org_id AND id = @id AND revision = @revision;

-- name: DeleteMaintenanceWindow :execrows
DELETE FROM maintenance_windows WHERE org_id = @org_id AND id = @id;
