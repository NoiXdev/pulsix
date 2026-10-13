-- Task 1, spec docs/superpowers/specs/2026-10-13-v0.4c-notification-
-- schedules-design.md, "Data model": the statements a later task's
-- internal/oncall.SQLStore issues for rotation CRUD, the ordered channel
-- cycle, and the exception list. Modelled on
-- internal/db/queries/alert_routes.sql's own shape and comment style
-- throughout.

-- name: CreateRotation :one
-- revision is left off the column list, the same reason CreateAlertRoute's
-- own comment gives for alert_routes: the migration's DEFAULT 1 is what a
-- brand new rotation starts at, and a caller-supplied value could only ever
-- disagree with that by mistake.
INSERT INTO oncall_rotations (id, org_id, name, period_days, anchor, timezone, created_at, updated_at)
VALUES (@id, @org_id, @name, @period_days, @anchor, @timezone, @created_at, @updated_at)
RETURNING *;

-- name: GetRotation :one
SELECT * FROM oncall_rotations WHERE org_id = @org_id AND id = @id;

-- name: ListRotations :many
SELECT * FROM oncall_rotations WHERE org_id = @org_id ORDER BY name;

-- name: UpdateRotation :execrows
-- :execrows, not :one with RETURNING, UpdateAlertRoute's own choice for the
-- same reason: zero rows affected is the one signal the caller needs (a
-- stale revision per backlog 1.31's own If-Match, or an id that no longer
-- exists), and the Store turns that into ErrConflict vs ErrNotFound by
-- re-reading the row, not this statement's job to distinguish.
UPDATE oncall_rotations SET
  name = @name, period_days = @period_days, anchor = @anchor, timezone = @timezone,
  revision = revision + 1, updated_at = @updated_at
WHERE org_id = @org_id AND id = @id AND revision = @revision;

-- name: DeleteRotation :execrows
-- oncall_rotation_channels and oncall_exceptions cascade off oncall_rotations'
-- own id (this migration's composite FKs); any alert_routes.rotation_id
-- pointing here is cleared by alert_routes_rotation_fkey's own
-- ON DELETE SET NULL (rotation_id) rather than taking the route with it
-- (migration 00054's own comment on that constraint). So this is the only
-- statement a rotation deletion ever needs.
DELETE FROM oncall_rotations WHERE org_id = @org_id AND id = @id;

-- name: ReplaceRotationChannels :exec
-- ReplaceRouteChannels' own shape (alert_routes.sql), for the ordered
-- channel cycle: the whole list in one round trip, DELETE as a CTE ahead of
-- the INSERT so nothing else can observe the cycle half-replaced between
-- them. WITH ORDINALITY is what turns the caller's array order into the
-- position column — the array's own index IS the cycle order, so there is
-- nothing else position could mean. Ordinality starts at 1; the schema's
-- own position starts at 0 (CHECK (position >= 0), the same base ChannelAt's
-- arithmetic wants), hence "ord - 1". An empty array is a legal no-op that
-- clears the cycle to nothing, the same reading ReplaceRouteChannels' own
-- comment gives an empty channel selection.
WITH deleted AS (
  DELETE FROM oncall_rotation_channels WHERE org_id = @org_id AND rotation_id = @rotation_id
)
INSERT INTO oncall_rotation_channels (org_id, rotation_id, position, channel_id)
SELECT @org_id, @rotation_id, ord - 1, channel_id
FROM unnest(@channel_ids::uuid[]) WITH ORDINALITY AS t(channel_id, ord);

-- name: ListRotationChannels :many
SELECT position, channel_id FROM oncall_rotation_channels
WHERE org_id = @org_id AND rotation_id = @rotation_id
ORDER BY position;

-- name: CreateException :one
INSERT INTO oncall_exceptions (id, org_id, rotation_id, starts_at, ends_at, channel_id, note, created_at)
VALUES (@id, @org_id, @rotation_id, @starts_at, @ends_at, @channel_id, @note, @created_at)
RETURNING *;

-- name: ListExceptions :many
SELECT * FROM oncall_exceptions
WHERE org_id = @org_id AND rotation_id = @rotation_id
ORDER BY starts_at;

-- name: DeleteException :execrows
DELETE FROM oncall_exceptions WHERE org_id = @org_id AND id = @id;
