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
-- One host's agent for its detail card, with the same certificate window
-- ListHostAgents reports.
SELECT sqlc.embed(h), c.not_after AS cert_not_after, ho.name AS host_name
FROM host_agents h
JOIN hosts ho ON ho.org_id = h.org_id AND ho.id = h.host_id
LEFT JOIN agent_certificates c ON c.serial = h.cert_serial
WHERE h.org_id = sqlc.arg(org_id) AND h.host_id = sqlc.arg(host_id);

-- name: ListHostAgents :many
-- The Agents page's rows: the agent plus the validity window of the
-- certificate it currently authenticates with (the join is by the active
-- serial, so a revoked predecessor's window never shows).
SELECT sqlc.embed(h), c.not_after AS cert_not_after, ho.name AS host_name
FROM host_agents h
JOIN hosts ho ON ho.org_id = h.org_id AND ho.id = h.host_id
LEFT JOIN agent_certificates c ON c.serial = h.cert_serial
WHERE h.org_id = sqlc.arg(org_id)
ORDER BY h.agent_name;

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
-- The validity window comes from the issued leaf; it is display data (the
-- agent renews from its own certificate, the manager never re-reads this).
INSERT INTO agent_certificates (serial, org_id, host_id, not_before, not_after)
VALUES (sqlc.arg(serial), sqlc.arg(org_id), sqlc.arg(host_id), sqlc.arg(not_before), sqlc.arg(not_after));

-- name: LockLiveAgentCertificate :execrows
-- A renewal's proof, inside its own transaction, that the serial it renews
-- from is still the host's live certificate (backlog 7.18). FOR UPDATE
-- orders it against a concurrent revoke: whichever commits first, the other
-- sees it, so a revoked agent cannot renew itself back into the registry.
SELECT 1 FROM agent_certificates
WHERE serial = sqlc.arg(serial) AND org_id = sqlc.arg(org_id) AND host_id = sqlc.arg(host_id) AND revoked_at IS NULL
FOR UPDATE;

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

-- name: LockHostAgentsByName :many
-- Certificate adoption after a restore (backlog 7.47): an agent
-- certificate names only its organization and agent name, and agent names
-- are not unique, so every matching row is locked and the caller refuses
-- unless exactly one matched. The lock holds until the adopted certificate
-- is registered, so a revocation or a renewal cannot slip in between the
-- checks and the swap.
SELECT * FROM host_agents
WHERE org_id = sqlc.arg(org_id) AND agent_name = sqlc.arg(agent_name)
FOR UPDATE;

-- name: GetAgentCertificate :one
-- One certificate row by serial, inside the caller's org scope.
SELECT * FROM agent_certificates WHERE serial = sqlc.arg(serial);

-- name: NewestAgentCertificateStart :one
-- The latest start among a host's certificates, revoked ones included:
-- not_before, or created_at for a row written before migration 00039 that
-- its backfill could not reach.
SELECT COALESCE(not_before, created_at)::timestamptz AS started
FROM agent_certificates
WHERE org_id = sqlc.arg(org_id) AND host_id = sqlc.arg(host_id)
ORDER BY started DESC
LIMIT 1;

-- name: UpsertHostFacts :execrows
-- The facts push's merge (RecordFacts, Task 8): the jsonb || concatenation
-- is an object merge where the RIGHT side wins for duplicate keys — every
-- reported key REPLACES its previous value, every key the agent did not
-- report survives untouched. That asymmetry is the semantics on purpose:
-- the agent sends the complete set each push, so an unreported key is a
-- stale fact from an older agent version, and keeping it (instead of
-- deleting it) is the simplest honest behavior — the operator still sees
-- what the host used to report. Stale keys are kept only while the
-- merged object stays at or under max_keys (agents.MaxStoredFacts, backlog
-- 7.56): a push whose merge would exceed it REPLACES the stored set with
-- the reported one, so keys can never accumulate without bound across
-- pushes. A set already above the cap (stored before it existed) is
-- replaced by the next push the same way. 0 affected rows is the caller's
-- ErrNotFound (unknown host, or one belonging to another organization).
UPDATE hosts SET
  facts = CASE
    WHEN (SELECT count(*) FROM jsonb_object_keys(COALESCE(facts, '{}'::jsonb) || sqlc.arg(facts)::jsonb)) <= sqlc.arg(max_keys)::integer
      THEN COALESCE(facts, '{}'::jsonb) || sqlc.arg(facts)::jsonb
    ELSE sqlc.arg(facts)::jsonb
  END,
  updated_at = now()
WHERE id = sqlc.arg(host_id) AND org_id = sqlc.arg(org_id);

-- name: UpsertFactTag :exec
-- One automatic fact tag (source = 'fact'): the guarded upsert — a manual
-- row for the same key ALWAYS wins, so the SELECT produces no row while
-- the operator's tag exists (nothing inserted, nothing updated), a manual
-- row that was removed re-admits the fact on the next push, and an
-- existing fact row is updated in place. The guard and the conflict
-- target are both (host_id, key): source is part of the primary key, so a
-- manual and a fact row for the same key coexist by design (00011) and
-- every reader merges them with manual winning.
INSERT INTO host_tags (host_id, org_id, key, value, source)
SELECT sqlc.arg(host_id), sqlc.arg(org_id), sqlc.arg(key), sqlc.arg(value), 'fact'
WHERE NOT EXISTS (
  SELECT 1 FROM host_tags
  WHERE host_id = sqlc.arg(host_id) AND key = sqlc.arg(key) AND source = 'manual'
)
ON CONFLICT (host_id, key, source) DO UPDATE SET value = EXCLUDED.value;
