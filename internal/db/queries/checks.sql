-- name: CreateMonitoringGroup :one
INSERT INTO monitoring_groups (id, org_id, name, selector, priority)
VALUES (sqlc.arg(id), sqlc.arg(org_id), sqlc.arg(name), sqlc.arg(selector), sqlc.arg(priority))
RETURNING *;

-- name: ListMonitoringGroups :many
SELECT * FROM monitoring_groups WHERE org_id = sqlc.arg(org_id) ORDER BY name;

-- name: UpdateMonitoringGroup :one
-- Full replace of every mutable column; org_id in the WHERE clause is what
-- actually makes "the group must belong to the org" enforceable from a
-- plain application connection (see hosts.sql's GetZone/GetHost for the
-- same reasoning about RLS not being the only guard).
UPDATE monitoring_groups
SET name = sqlc.arg(name), selector = sqlc.arg(selector), priority = sqlc.arg(priority), updated_at = now()
WHERE org_id = sqlc.arg(org_id) AND id = sqlc.arg(id)
RETURNING *;

-- name: DeleteMonitoringGroup :execrows
DELETE FROM monitoring_groups WHERE org_id = sqlc.arg(org_id) AND id = sqlc.arg(id);
