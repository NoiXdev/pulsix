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

-- name: DeleteRotationChannels :exec
-- The first half of replacing the ordered channel cycle; InsertRotationChannels
-- is the second. ReplaceRouteChannels' own shape (alert_routes.sql) combines
-- both halves into one WITH statement — a DELETE CTE the INSERT does not
-- itself read from, ahead of the INSERT. That shape is unsafe here and was
-- the original design: oncall_rotation_channels' primary key is
-- (rotation_id, position), and a cycle's new list almost always reuses
-- position 0, the same key the old list's position 0 row already holds, so
-- the replacement overlaps the very key it means to change. Postgres
-- documents that sub-statements inside one WITH run against the same
-- snapshot with their relative order unspecified
-- (https://www.postgresql.org/docs/current/queries-with.html#QUERIES-WITH-MODIFYING)
-- — not merely a visibility caveat, since TestStoreUpdateReplacesChannelsAndRotationFields
-- hit exactly this as a reproducible "duplicate key value violates unique
-- constraint oncall_rotation_channels_pkey" on every update that kept any
-- channel at the same position, which for a rotation already holding
-- channels is every update. Two statements sent in sequence on the same
-- transaction (the store calls this, then InsertRotationChannels, inside
-- its own Begin/Commit) is what removes the ambiguity: the DELETE commits
-- its index change before the INSERT ever runs, the same ordering the
-- CTE's comment intended but could not actually guarantee.
DELETE FROM oncall_rotation_channels WHERE org_id = @org_id AND rotation_id = @rotation_id;

-- name: InsertRotationChannels :exec
-- WITH ORDINALITY is what turns the caller's array order into the position
-- column — the array's own index IS the cycle order, so there is nothing
-- else position could mean. Ordinality starts at 1; the schema's own
-- position starts at 0 (CHECK (position >= 0), the same base ChannelAt's
-- arithmetic wants), hence "ord - 1". An empty array is a legal no-op that
-- adds nothing, leaving the cycle cleared by the DELETE above — the same
-- reading ReplaceRouteChannels' own comment gives an empty channel
-- selection.
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
