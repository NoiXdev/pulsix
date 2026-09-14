-- name: InsertWorker :one
INSERT INTO workers (id, org_id, zone_id, name, cert_serial, version)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: ListWorkers :many
SELECT * FROM workers WHERE org_id = $1 ORDER BY name;

-- name: GetWorkerBySerial :one
-- Org-unscoped on purpose: the gRPC interceptor has no org context, and the
-- serial is globally unique (the secret the worker authenticates with).
SELECT * FROM workers WHERE cert_serial = $1;

-- name: TouchWorker :execrows
-- Org-unscoped for the same reason as GetWorkerBySerial; updates only
-- last_seen and version, and matches nothing for an unknown serial.
UPDATE workers SET last_seen = now(), version = $2 WHERE cert_serial = $1;

-- name: DeleteWorker :execrows
DELETE FROM workers WHERE org_id = $1 AND id = $2;
