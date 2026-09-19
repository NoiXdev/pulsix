-- name: CreateNotificationChannel :one
INSERT INTO notification_channels (id, org_id, kind, name, config, enabled, created_by)
VALUES (@id, @org_id, @kind, @name, @config, @enabled, sqlc.narg(created_by))
RETURNING *;

-- name: GetNotificationChannel :one
SELECT * FROM notification_channels WHERE org_id = @org_id AND id = @id;

-- name: ListNotificationChannels :many
SELECT * FROM notification_channels WHERE org_id = @org_id ORDER BY name;

-- name: UpdateNotificationChannel :one
-- No row back means a stale revision or an unknown channel.
UPDATE notification_channels SET name = @name, config = @config, enabled = @enabled,
  revision = revision + 1, updated_at = now()
WHERE org_id = @org_id AND id = @id AND revision = @revision
RETURNING *;

-- name: DeleteNotificationChannel :execrows
DELETE FROM notification_channels WHERE org_id = @org_id AND id = @id;
