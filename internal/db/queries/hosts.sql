-- name: CreateZone :one
INSERT INTO zones (id, org_id, slug, name) VALUES ($1, $2, $3, $4) RETURNING *;

-- name: GetZone :one
-- Scoped by org_id in the WHERE clause, not only relying on RLS: this is
-- what actually makes "the zone must belong to the org" enforceable from a
-- plain application connection (testdb.Open, and every non-WithOrg caller
-- in this codebase, never sets pulsix.org_id, so org_isolation_bypass is
-- the policy that applies and every row is visible to RLS alone).
SELECT * FROM zones WHERE id = $1 AND org_id = $2;

-- name: ListZonesForOrg :many
SELECT * FROM zones WHERE org_id = $1 ORDER BY slug;

-- name: CreateHost :one
INSERT INTO hosts (id, org_id, zone_id, name, address, managed, monitored, facts)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
RETURNING *;

-- name: GetHost :one
-- Same org_id-in-WHERE reasoning as GetZone above: a host belonging to a
-- different organization must be invisible (no row, not merely forbidden),
-- which this query guarantees on its own, independent of RLS.
SELECT * FROM hosts WHERE id = $1 AND org_id = $2;

-- name: UpdateHost :one
-- Sets every mutable column except facts and last_seen_at, which this task
-- gives no route to change — an omitted column here is never silently
-- clobbered, since the caller (hosts.Service) always reads the current row
-- first and writes back the fields it did not intend to change.
UPDATE hosts
SET zone_id = $3, name = $4, address = $5, managed = $6, monitored = $7, updated_at = now()
WHERE id = $1 AND org_id = $2
RETURNING *;

-- name: DeleteHost :execrows
DELETE FROM hosts WHERE id = $1 AND org_id = $2;

-- name: ListHosts :many
-- Every optional filter (zone_id, managed, monitored, a free-text query
-- against name/address, and the cursor's own (name, id) keyset pair) is
-- expressed as "param IS NULL OR <condition>", the same one-query-serves-
-- every-combination shape ListAuditEventsForOrg uses (audit.sql). Ordering
-- is ascending on (name, id) — alphabetical by host name, id as the
-- tiebreaker for two hosts that could never actually share a name (name is
-- unique per org), kept anyway for the same defensive reason the audit
-- feed's (at, id) tiebreaker exists. The cursor condition
-- "(name, id) > (cursor_name, cursor_id)" is the ascending-order mirror of
-- ListAuditEventsForOrg's "(at, id) < (cursor_at, cursor_id)": it resumes
-- strictly after the last row of the previous page.
SELECT * FROM hosts
WHERE org_id = sqlc.arg(org_id)
  AND (sqlc.narg(zone_id)::uuid IS NULL OR zone_id = sqlc.narg(zone_id)::uuid)
  AND (sqlc.narg(managed)::boolean IS NULL OR managed = sqlc.narg(managed)::boolean)
  AND (sqlc.narg(monitored)::boolean IS NULL OR monitored = sqlc.narg(monitored)::boolean)
  AND (
    sqlc.narg(query)::text IS NULL
    OR name ILIKE '%' || sqlc.narg(query)::text || '%'
    OR address ILIKE '%' || sqlc.narg(query)::text || '%'
  )
  AND (
    sqlc.narg(cursor_name)::text IS NULL
    OR (name, id) > (sqlc.narg(cursor_name)::text, sqlc.narg(cursor_id)::uuid)
  )
ORDER BY name ASC, id ASC
LIMIT sqlc.arg(row_limit);
