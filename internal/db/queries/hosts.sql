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

-- name: ListHostZones :many
-- The zone of each named host, in no particular order: the revocation
-- phase (backlog 2.46) resolves each snapshot target's claim zone. A host
-- deleted mid-rotation is simply absent from the result — its
-- authorized_keys left with it, so nothing remains to revoke.
SELECT id, zone_id FROM hosts
WHERE org_id = $1 AND id = ANY(sqlc.arg(host_ids)::uuid[]);

-- name: UpdateHost :one
-- Sets every mutable column except facts and last_seen_at, which this task
-- gives no route to change — an omitted column here is never silently
-- clobbered, since the caller (hosts.Service) always reads the current row
-- first and writes back the fields it did not intend to change.
-- Backlog 1.31: writes only when the caller's revision is still current,
-- the same optimistic-concurrency shape UpdateInventoryGroup uses. No row
-- back means either a stale revision or an unknown host; the store tells
-- the two apart so the API can answer 412 rather than 404 for the first.
UPDATE hosts
SET zone_id = $3, name = $4, address = $5, managed = $6, monitored = $7,
    revision = revision + 1, updated_at = now()
WHERE id = $1 AND org_id = $2 AND revision = @revision
RETURNING *;

-- name: PatchHostCapabilities :one
-- Expresses a partial capabilities update as one atomic statement — the
-- same nullable-parameter ("param IS NULL OR ...", here COALESCE) idiom
-- ListHosts' own filters already use below — instead of the service
-- reading the row and writing every mutable column back. A read-modify-
-- write is a lost-update race: two concurrent partial updates touching
-- different flags (an Ansible run turning on managed while monitoring
-- turns on monitored) can each read the pre-update row and each write back
-- a version that clobbers the other's change (Task 13 fix round 1, finding
-- 2). A NULL parameter here leaves that column exactly as it was.
UPDATE hosts
SET managed = COALESCE(sqlc.narg(managed)::boolean, managed),
    monitored = COALESCE(sqlc.narg(monitored)::boolean, monitored),
    updated_at = now()
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

-- name: ListAllHosts :many
-- Every host of the organization, for the inventory renderer. Unpaginated on
-- purpose: an inventory is rendered whole (spec §13 sizes an installation at
-- 10 000 hosts).
SELECT * FROM hosts WHERE org_id = $1 ORDER BY name;

-- name: ListHostsByIDs :many
-- The named hosts of one organization, ordered by name. For a caller that
-- already knows exactly which ids it needs — the check list's host_name
-- labels — where the work must be proportional to the page, not to the
-- fleet: ListAllHosts above is right for the inventory renderer, which
-- genuinely wants every host, but reading 10 000 rows to label one
-- host-bound check would be worse than the per-id round trips it replaced.
SELECT * FROM hosts WHERE org_id = $1 AND id = ANY(sqlc.arg(ids)::uuid[]) ORDER BY name;

-- name: OrgsWithoutDefaultZone :many
-- Used only by the startup reconciliation pass (internal/app.Build, via
-- hosts.Service.ReconcileDefaultZones): every organization that has no
-- zone named sqlc.arg(default_zone_slug) (hosts.DefaultZoneSlug, "default")
-- — the state a post-commit tenancy.OrgCreatedHook failure can leave an
-- organization in (see that type's own doc comment), which a caller
-- retrying org creation cannot repair because the slug is already taken.
-- organizations carries no row-level security of its own (see Task 4's
-- migration), so this plain scan sees every organization regardless of
-- pulsix.org_id.
SELECT o.id FROM organizations o
WHERE NOT EXISTS (
  SELECT 1 FROM zones z WHERE z.org_id = o.id AND z.slug = sqlc.arg(default_zone_slug)::text
);

-- name: GetZonesBySlug :many
-- Zone lookup without an org scope — the `worker add` CLI path, which runs
-- before any request context exists and must resolve the slug across every
-- organization (slugs are unique per org, not instance-wide). Returns all
-- matches ordered by organization slug so a caller can either take the
-- single row or ask the operator to disambiguate.
SELECT z.id, z.org_id, o.slug AS org_slug
FROM zones z
JOIN organizations o ON o.id = z.org_id
WHERE z.slug = $1
ORDER BY o.slug;
