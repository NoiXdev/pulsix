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

-- name: LockWorkerByName :one
-- Certificate adoption after a restore (backlog 7.47): the row stays
-- locked until the adopted certificate is inserted, so a revocation or a
-- renewal cannot slip in between the checks and the insert.
SELECT * FROM workers WHERE org_id = sqlc.arg(org_id) AND name = sqlc.arg(name) FOR UPDATE;

-- name: NewestWorkerCertificateStart :one
-- The latest not_before among a worker's certificates, revoked ones
-- included (not_before is NOT NULL, so every certificate counts).
SELECT not_before FROM worker_certificates
WHERE org_id = sqlc.arg(org_id) AND worker_id = sqlc.arg(worker_id)
ORDER BY not_before DESC
LIMIT 1;

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

-- name: MarkWorkerEnrollmentUsed :execrows
-- The single-use guard: unused and unexpired by the database's own clock,
-- not the caller's. 0 affected rows means the enrollment was already used,
-- has expired or does not exist for this org; the caller treats that as a
-- refusal and rolls the whole redeem transaction back.
UPDATE worker_enrollments SET used_at = now(), worker_id = sqlc.narg(worker_id)
WHERE org_id = sqlc.arg(org_id) AND id = sqlc.arg(id) AND used_at IS NULL AND expires_at > now();

-- name: GetWorker :one
SELECT * FROM workers WHERE org_id = sqlc.arg(org_id) AND id = sqlc.arg(id);

-- name: RevokeWorker :one
-- Idempotent: a second revoke keeps the first timestamp.
UPDATE workers SET status = 'revoked', revoked_at = COALESCE(revoked_at, now())
WHERE org_id = sqlc.arg(org_id) AND id = sqlc.arg(id)
RETURNING *;

-- name: RevokeWorkerCertificates :exec
UPDATE worker_certificates SET revoked_at = now()
WHERE org_id = sqlc.arg(org_id) AND worker_id = sqlc.arg(worker_id) AND revoked_at IS NULL;

-- name: ListWorkerCertExpiries :many
-- The latest expiry of each worker's unrevoked certificates (Workers page).
SELECT worker_id, max(not_after)::timestamptz AS expires_at
FROM worker_certificates
WHERE org_id = sqlc.arg(org_id) AND revoked_at IS NULL
GROUP BY worker_id;

-- name: ListWorkerCurrentJobs :many
-- The job each worker is running right now (Workers page).
SELECT DISTINCT ON (worker_id) worker_id, id
FROM jobs
WHERE org_id = sqlc.arg(org_id) AND worker_id IS NOT NULL AND status IN ('claimed', 'running', 'cancelling')
ORDER BY worker_id, claimed_at DESC;
