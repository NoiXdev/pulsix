-- name: CreateAgentEnrollment :one
INSERT INTO agent_enrollments (id, org_id, zone_id, host_id, name_hint, secret_hash, max_uses, expires_at, created_by)
VALUES (sqlc.arg(id), sqlc.arg(org_id), sqlc.arg(zone_id), sqlc.narg(host_id), sqlc.narg(name_hint), sqlc.arg(secret_hash), sqlc.arg(max_uses), sqlc.arg(expires_at), sqlc.narg(created_by))
RETURNING *;

-- name: ListPendingAgentEnrollments :many
SELECT * FROM agent_enrollments
WHERE org_id = sqlc.arg(org_id) AND used_at IS NULL AND expires_at > sqlc.arg(now)
ORDER BY created_at;

-- name: DeleteAgentEnrollment :execrows
DELETE FROM agent_enrollments WHERE org_id = sqlc.arg(org_id) AND id = sqlc.arg(id) AND used_at IS NULL;

-- name: GetAgentEnrollmentOrg :one
-- Claim's first step, org-unscoped: the id is the token's public half.
SELECT org_id FROM agent_enrollments WHERE id = sqlc.arg(id);

-- name: LockAgentEnrollment :one
SELECT * FROM agent_enrollments WHERE org_id = sqlc.arg(org_id) AND id = sqlc.arg(id) FOR UPDATE;

-- name: ConsumeAgentEnrollmentUse :execrows
-- The use-count guard (the agent counterpart of MarkWorkerEnrollmentUsed,
-- generalized to multi-use): 1 affected row means this claim consumed a
-- use; 0 means the enrollment is exhausted, expired (by the database's own
-- clock), withdrawn or unknown. For a multi-use token uses increments and
-- the row stays claimable until max_uses is reached; the single-use token
-- is just max_uses = 1, where that same statement flips used_at. One
-- conditional UPDATE inside the claim transaction — never a read-checked
-- write — is what keeps two concurrent claims of the last use from both
-- succeeding: the second claim's UPDATE sees the first's committed use and
-- matches 0 rows.
UPDATE agent_enrollments SET
  uses = uses + 1,
  used_at = CASE WHEN uses + 1 >= max_uses THEN now() ELSE used_at END
WHERE org_id = sqlc.arg(org_id) AND id = sqlc.arg(id)
  AND used_at IS NULL
  AND uses < max_uses
  AND expires_at > now();

-- name: GetHostAgent :one
SELECT * FROM host_agents WHERE org_id = sqlc.arg(org_id) AND host_id = sqlc.arg(host_id);

-- name: ListHostAgents :many
SELECT * FROM host_agents WHERE org_id = sqlc.arg(org_id) ORDER BY agent_name;

-- name: UpsertHostAgent :one
-- The active agent of a host: re-enrollment replaces the row in place
-- (ON CONFLICT on the host primary key), so exactly one agent row exists
-- per host at any time. agent_certificates carries the previous serials —
-- the service marks the old one revoked in the same transaction.
INSERT INTO host_agents (host_id, org_id, agent_name, cert_serial, version)
VALUES (sqlc.arg(host_id), sqlc.arg(org_id), sqlc.arg(agent_name), sqlc.arg(cert_serial), sqlc.arg(version))
ON CONFLICT (host_id) DO UPDATE
SET agent_name = EXCLUDED.agent_name, cert_serial = EXCLUDED.cert_serial,
    version = EXCLUDED.version, last_seen = NULL, created_at = now()
RETURNING *;

-- name: TouchAgentBySerial :exec
-- Org-unscoped heartbeat by serial: the gRPC stream has no org context
-- (the serial is the secret and is globally unique).
UPDATE host_agents SET last_seen = now(), version = sqlc.arg(version)
WHERE cert_serial = sqlc.arg(serial);

-- name: DeleteHostAgent :execrows
DELETE FROM host_agents WHERE org_id = sqlc.arg(org_id) AND host_id = sqlc.arg(host_id);

-- name: InsertAgentCertificate :exec
INSERT INTO agent_certificates (serial, org_id, host_id) VALUES (sqlc.arg(serial), sqlc.arg(org_id), sqlc.arg(host_id));

-- name: RevokeAgentCertificates :exec
-- Idempotent: a second revoke keeps the first timestamp.
UPDATE agent_certificates SET revoked_at = now()
WHERE org_id = sqlc.arg(org_id) AND host_id = sqlc.arg(host_id) AND revoked_at IS NULL;

-- name: LookupAgentSerial :one
-- The gRPC authentication path, org-unscoped: a presented serial resolves
-- to its certificate and host; the caller decides revoked vs usable.
SELECT sqlc.embed(h), c.serial AS cert_serial_presented, c.revoked_at AS cert_revoked_at
FROM agent_certificates c
JOIN host_agents h ON h.org_id = c.org_id AND h.host_id = c.host_id
WHERE c.serial = sqlc.arg(serial);
