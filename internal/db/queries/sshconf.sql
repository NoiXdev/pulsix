-- name: CreateBastion :one
INSERT INTO bastions (id, org_id, name, address, port, username, credential_id, via_bastion_id)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: GetBastion :one
SELECT * FROM bastions WHERE org_id = $1 AND id = $2;

-- name: ListBastions :many
SELECT * FROM bastions WHERE org_id = $1 ORDER BY name;

-- name: UpdateBastion :one
UPDATE bastions
SET name = $3, address = $4, port = $5, username = $6, credential_id = $7, via_bastion_id = $8, updated_at = now()
WHERE org_id = $1 AND id = $2
RETURNING *;

-- name: DeleteBastion :execrows
DELETE FROM bastions WHERE org_id = $1 AND id = $2;

-- name: SetZoneBastion :execrows
UPDATE zones SET bastion_id = $3 WHERE org_id = $1 AND id = $2;

-- name: ListZoneBastions :many
SELECT id, bastion_id FROM zones WHERE org_id = $1 AND bastion_id IS NOT NULL;

-- name: GetHostSSH :one
SELECT * FROM host_ssh WHERE org_id = $1 AND host_id = $2;

-- name: UpsertHostSSH :one
INSERT INTO host_ssh (host_id, org_id, username, port, credential_id, bastion_mode, bastion_id)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (host_id) DO UPDATE
SET username = EXCLUDED.username, port = EXCLUDED.port, credential_id = EXCLUDED.credential_id,
    bastion_mode = EXCLUDED.bastion_mode, bastion_id = EXCLUDED.bastion_id, updated_at = now()
RETURNING *;

-- name: ListHostSSH :many
SELECT * FROM host_ssh WHERE org_id = $1;
