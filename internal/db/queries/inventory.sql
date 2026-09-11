-- name: CreateInventoryGroup :one
INSERT INTO inventory_groups (id, org_id, parent_id, name, description, schema, vars, bastion_id)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: GetInventoryGroup :one
SELECT * FROM inventory_groups WHERE org_id = $1 AND id = $2;

-- name: ListInventoryGroups :many
SELECT * FROM inventory_groups WHERE org_id = $1 ORDER BY name;

-- name: UpdateInventoryGroup :one
-- Writes only when the caller's revision is still current; no row back means
-- a stale revision or an unknown group (the store tells the two apart).
UPDATE inventory_groups
SET parent_id = @parent_id, name = @name, description = @description, schema = @schema,
    vars = @vars, bastion_id = @bastion_id, revision = revision + 1, updated_at = now()
WHERE org_id = @org_id AND id = @id AND revision = @revision
RETURNING *;

-- name: DeleteInventoryGroup :execrows
DELETE FROM inventory_groups WHERE org_id = $1 AND id = $2;

-- name: CountInventoryMembers :many
SELECT group_id, count(*)::int AS members
FROM inventory_memberships WHERE org_id = $1 GROUP BY group_id;

-- name: UpsertInventoryMembership :one
INSERT INTO inventory_memberships (group_id, host_id, org_id, vars)
VALUES ($1, $2, $3, $4)
ON CONFLICT (group_id, host_id) DO UPDATE
SET vars = EXCLUDED.vars, revision = inventory_memberships.revision + 1, updated_at = now()
RETURNING *;

-- name: UpdateInventoryMembership :one
UPDATE inventory_memberships
SET vars = @vars, revision = revision + 1, updated_at = now()
WHERE org_id = @org_id AND group_id = @group_id AND host_id = @host_id AND revision = @revision
RETURNING *;

-- name: GetInventoryMembership :one
SELECT * FROM inventory_memberships WHERE org_id = $1 AND group_id = $2 AND host_id = $3;

-- name: DeleteInventoryMembership :execrows
DELETE FROM inventory_memberships WHERE org_id = $1 AND group_id = $2 AND host_id = $3;

-- name: ListInventoryMemberships :many
SELECT * FROM inventory_memberships WHERE org_id = $1 ORDER BY group_id, host_id;

-- name: ListGroupMembers :many
SELECT m.*, h.name AS host_name
FROM inventory_memberships m
JOIN hosts h ON h.org_id = m.org_id AND h.id = m.host_id
WHERE m.org_id = $1 AND m.group_id = $2
ORDER BY h.name;

-- name: ListHostMemberships :many
SELECT m.*, g.name AS group_name
FROM inventory_memberships m
JOIN inventory_groups g ON g.org_id = m.org_id AND g.id = m.group_id
WHERE m.org_id = $1 AND m.host_id = $2
ORDER BY g.name;
