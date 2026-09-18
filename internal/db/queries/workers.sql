-- name: InsertWorker :one
INSERT INTO workers (id, org_id, zone_id, name, cert_serial, version)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: ListWorkers :many
SELECT * FROM workers WHERE org_id = $1 ORDER BY name;

-- name: DeleteWorker :execrows
DELETE FROM workers WHERE org_id = $1 AND id = $2;

-- name: LookupWorkerSerial :one
-- The gRPC authentication path, org-unscoped: a presented serial resolves
-- to its certificate and worker; the caller decides revoked vs usable.
SELECT sqlc.embed(w), c.serial AS cert_serial_presented, c.not_before AS cert_not_before,
       c.not_after AS cert_not_after, c.revoked_at AS cert_revoked_at
FROM worker_certificates c
JOIN workers w ON w.org_id = c.org_id AND w.id = c.worker_id
WHERE c.serial = sqlc.arg(serial);

-- name: TouchWorkerByID :exec
-- Org-unscoped heartbeat by worker id (a renewed certificate has a new serial).
UPDATE workers SET last_seen = now(), version = sqlc.arg(version) WHERE id = sqlc.arg(id);

-- name: InsertWorkerCertificate :exec
INSERT INTO worker_certificates (serial, org_id, worker_id, not_before, not_after)
VALUES (sqlc.arg(serial), sqlc.arg(org_id), sqlc.arg(worker_id), sqlc.arg(not_before), sqlc.arg(not_after));

-- name: GetWorkerByName :one
SELECT * FROM workers WHERE org_id = sqlc.arg(org_id) AND name = sqlc.arg(name);

-- name: CreateWorkerEnrollment :one
INSERT INTO worker_enrollments (id, org_id, zone_id, name, secret_hash, expires_at, created_by)
VALUES (sqlc.arg(id), sqlc.arg(org_id), sqlc.arg(zone_id), sqlc.arg(name), sqlc.arg(secret_hash), sqlc.arg(expires_at), sqlc.narg(created_by))
RETURNING *;

-- name: ListPendingWorkerEnrollments :many
SELECT * FROM worker_enrollments
WHERE org_id = sqlc.arg(org_id) AND used_at IS NULL AND expires_at > sqlc.arg(now)
ORDER BY created_at;

-- name: DeleteWorkerEnrollment :execrows
DELETE FROM worker_enrollments WHERE org_id = sqlc.arg(org_id) AND id = sqlc.arg(id) AND used_at IS NULL;

-- name: GetWorkerEnrollmentOrg :one
-- Enroll's first step, org-unscoped: the id is the token's public half.
SELECT org_id FROM worker_enrollments WHERE id = sqlc.arg(id);

-- name: LockWorkerEnrollment :one
SELECT * FROM worker_enrollments WHERE org_id = sqlc.arg(org_id) AND id = sqlc.arg(id) FOR UPDATE;

-- name: MarkWorkerEnrollmentUsed :exec
UPDATE worker_enrollments SET used_at = now(), worker_id = sqlc.narg(worker_id)
WHERE org_id = sqlc.arg(org_id) AND id = sqlc.arg(id);
