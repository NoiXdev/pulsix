-- Host-key pins and connection-test history (spec §5, backlog 2.7c).
-- All queries are org-scoped by (org_id, host_id) exactly like hosts.sql's
-- own queries; the org-unscoped job-queue paths (claim payload pins,
-- recordTest) run them through explicit org id values on the raw pool.

-- name: UpsertHostKeyPin :execrows
-- Creates the host_ssh row with its defaults when the host carries no SSH
-- settings of its own (username '', port 22, bastion_mode 'inherit') and
-- stores the pin; an existing row keeps every other column untouched.
INSERT INTO host_ssh (host_id, org_id, ssh_host_key)
VALUES ($2, $1, $3)
ON CONFLICT (host_id) DO UPDATE
SET ssh_host_key = EXCLUDED.ssh_host_key, updated_at = now();

-- name: ClearHostKeyPin :execrows
UPDATE host_ssh SET ssh_host_key = NULL, updated_at = now()
WHERE org_id = $1 AND host_id = $2;

-- name: ListPinnedHostKeys :many
-- The pinned fingerprint of each named host that carries one (empty when
-- none does): the claim path turns the ids into the job's host_keys map.
SELECT host_id, ssh_host_key FROM host_ssh
WHERE org_id = $1 AND host_id = ANY(sqlc.arg(host_ids)::uuid[]) AND ssh_host_key IS NOT NULL;

-- name: ListHostKeyChanged :many
-- The named hosts whose LATEST connection test reports a changed host key
-- (spec §5 block-on-mismatch). "Latest" is the newest row per host by at;
-- the pin comparison is defense in depth for a pin that was written after
-- the latest test without a fresh test observing it.
SELECT DISTINCT t.host_id
FROM host_connection_tests t
LEFT JOIN host_ssh s ON s.org_id = t.org_id AND s.host_id = t.host_id
WHERE t.org_id = $1
  AND t.host_id = ANY(sqlc.arg(host_ids)::uuid[])
  AND t.at = (
    SELECT max(latest.at) FROM host_connection_tests latest
    WHERE latest.org_id = t.org_id AND latest.host_id = t.host_id
  )
  AND (
    t.host_key_state = 'changed'
    OR (s.ssh_host_key IS NOT NULL AND s.ssh_host_key <> '' AND t.host_key <> '' AND s.ssh_host_key <> t.host_key)
  );

-- name: CreateHostConnectionTest :execrows
INSERT INTO host_connection_tests (id, org_id, host_id, at, ok, host_key, host_key_state, checks)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8);

-- name: ListOrgIDs :many
-- Every organization id, the background passes' iteration root (the daily
-- re-test). organizations carries no row-level security of its own, so a
-- plain scan sees them all; there is no org scope to honor here.
SELECT id FROM organizations ORDER BY id;

-- name: GetLatestHostConnectionTest :one
-- The newest test row of one host; ErrNotFound (pgx.ErrNoRows) when the
-- host has never been tested.
SELECT * FROM host_connection_tests
WHERE org_id = $1 AND host_id = $2
ORDER BY at DESC
LIMIT 1;

-- name: ListHostsDueForTest :many
-- The org's managed hosts with no connection test newer than sqlc.arg(since)
-- and no connection_test job still queued, claimed or running — the daily
-- re-test selector. The jobs check keeps a second pass from double-queueing
-- a host whose first job has not finished yet (the "no duplicate queueing"
-- invariant).
SELECT h.* FROM hosts h
WHERE h.org_id = $1 AND h.managed
  AND NOT EXISTS (
    SELECT 1 FROM host_connection_tests t
    WHERE t.org_id = h.org_id AND t.host_id = h.id AND t.at > sqlc.arg(since)::timestamptz
  )
  AND NOT EXISTS (
    SELECT 1 FROM jobs j
    WHERE j.org_id = h.org_id AND j.kind = 'connection_test'
      AND j.status IN ('queued', 'claimed', 'running', 'cancelling')
      AND h.id = ANY(j.host_ids)
  )
ORDER BY h.name;
