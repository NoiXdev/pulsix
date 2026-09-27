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

-- name: CreateCheckDefinition :one
INSERT INTO check_definitions (
  id, org_id, key, plugin, params, interval_seconds, enabled, origin, binding_kind, group_id, host_id
) VALUES (
  sqlc.arg(id), sqlc.arg(org_id), sqlc.arg(key), sqlc.arg(plugin), sqlc.arg(params)::jsonb,
  sqlc.arg(interval_seconds), sqlc.arg(enabled), sqlc.arg(origin), sqlc.arg(binding_kind),
  sqlc.arg(group_id), sqlc.arg(host_id)
)
RETURNING *;

-- name: GetCheckDefinition :one
SELECT * FROM check_definitions WHERE org_id = sqlc.arg(org_id) AND id = sqlc.arg(id);

-- name: ListCheckDefinitions :many
SELECT * FROM check_definitions WHERE org_id = sqlc.arg(org_id) ORDER BY key;

-- name: UpdateCheckDefinition :one
-- Full replace of every mutable column, exactly like UpdateMonitoringGroup;
-- origin is deliberately absent from the SET list — it never changes after
-- creation, so the service carries it through unchanged rather than this
-- query being able to touch it.
UPDATE check_definitions
SET key = sqlc.arg(key), plugin = sqlc.arg(plugin), params = sqlc.arg(params)::jsonb,
    interval_seconds = sqlc.arg(interval_seconds), enabled = sqlc.arg(enabled),
    binding_kind = sqlc.arg(binding_kind), group_id = sqlc.arg(group_id), host_id = sqlc.arg(host_id),
    updated_at = now()
WHERE org_id = sqlc.arg(org_id) AND id = sqlc.arg(id)
RETURNING *;

-- name: DeleteCheckDefinition :execrows
DELETE FROM check_definitions WHERE org_id = sqlc.arg(org_id) AND id = sqlc.arg(id);
