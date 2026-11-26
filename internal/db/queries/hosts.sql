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
-- kind (backlog 3.57) is optional: NULL creates a server, the column's own
-- default, so a caller that predates host kinds needs no change.
INSERT INTO hosts (id, org_id, zone_id, name, address, managed, monitored, facts, kind)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, COALESCE(sqlc.narg(kind)::text, 'server'))
RETURNING *;

-- name: GetHost :one
-- Same org_id-in-WHERE reasoning as GetZone above: a host belonging to a
-- different organization must be invisible (no row, not merely forbidden),
-- which this query guarantees on its own, independent of RLS.
SELECT * FROM hosts WHERE id = $1 AND org_id = $2;

-- name: GetHostForUpdate :one
-- GetHost with the row locked until the transaction ends. FOR NO KEY
-- UPDATE: it does not conflict with the FOR KEY SHARE a job insert takes on
-- its target hosts, so the schedule ticker's enqueue never waits on it.
SELECT * FROM hosts WHERE id = $1 AND org_id = $2 FOR NO KEY UPDATE;

-- name: GetHostRoute :one
-- How runs reach a host, as a person reads it: its SSH user and port, the
-- credential's and the jump host's names. No row when the host has no SSH
-- settings stored (the defaults apply).
SELECT hs.username, hs.port, hs.bastion_mode, COALESCE(c.name, '')::text AS credential_name, COALESCE(b.name, '')::text AS bastion_name
FROM host_ssh hs
LEFT JOIN credentials c ON c.org_id = hs.org_id AND c.id = hs.credential_id
LEFT JOIN bastions b ON b.org_id = hs.org_id AND b.id = hs.bastion_id
WHERE hs.org_id = $1 AND hs.host_id = $2;

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
-- kind (backlog 3.57) is optional: NULL keeps the stored kind, so a
-- client that does not send one cannot turn a client back into a server.
UPDATE hosts
SET zone_id = $3, name = $4, address = $5, managed = $6, monitored = $7,
    kind = COALESCE(sqlc.narg(kind)::text, kind),
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
-- It bumps revision like UpdateHost does, without checking it: the edit
-- dialog sends managed/monitored from its pre-fill, so an edit opened
-- before a capability change (agent enrolment or revocation, the row-menu
-- and sidebar switches) must answer 412 instead of silently undoing it.
UPDATE hosts
SET managed = COALESCE(sqlc.narg(managed)::boolean, managed),
    monitored = COALESCE(sqlc.narg(monitored)::boolean, monitored),
    revision = revision + 1, updated_at = now()
WHERE id = $1 AND org_id = $2
RETURNING *;

-- name: BumpHostRevision :one
-- Backlog 7.79: a write to what hangs off a host (its manual tags, its SSH
-- settings) moves the host's revision like UpdateHost does, so a host edit
-- opened before it answers 412. The caller's revision is optional: NULL
-- bumps unconditionally (API-token clients never sent one), a value bumps
-- only while it is still current. No row back means an unknown host or a
-- stale revision; the store reads the host to tell the two apart.
UPDATE hosts
SET revision = revision + 1, updated_at = now()
WHERE id = @id AND org_id = @org_id
  AND (sqlc.narg(if_revision)::bigint IS NULL OR revision = sqlc.narg(if_revision)::bigint)
RETURNING revision;

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
WITH live AS MATERIALIZED (
  -- Driven from the live side (jobs_live_idx): the few runs still in flight,
  -- never each host's whole history.
  SELECT jh.host_id, count(*) AS n
  FROM jobs j JOIN job_hosts jh ON jh.org_id = j.org_id AND jh.job_id = j.id
  WHERE j.org_id = sqlc.arg(org_id)
    AND j.status IN ('queued', 'claimed', 'running', 'cancelling')
  GROUP BY jh.host_id
)
SELECT h.* FROM hosts h
WHERE h.org_id = sqlc.arg(org_id)
  AND (sqlc.narg(ids)::uuid[] IS NULL OR h.id = ANY(sqlc.narg(ids)::uuid[]))
  AND (sqlc.narg(zone_id)::uuid IS NULL OR h.zone_id = sqlc.narg(zone_id)::uuid)
  AND (sqlc.narg(managed)::boolean IS NULL OR h.managed = sqlc.narg(managed)::boolean)
  AND (sqlc.narg(monitored)::boolean IS NULL OR h.monitored = sqlc.narg(monitored)::boolean)
  AND (
    sqlc.narg(query)::text IS NULL
    OR h.name ILIKE '%' || sqlc.narg(query)::text || '%'
    OR h.address ILIKE '%' || sqlc.narg(query)::text || '%'
  )
  AND (
    sqlc.narg(cursor_name)::text IS NULL
    OR (h.name, h.id) > (sqlc.narg(cursor_name)::text, sqlc.narg(cursor_id)::uuid)
  )
  AND (sqlc.narg(active_runs)::boolean IS NULL OR sqlc.narg(active_runs)::boolean = (h.id IN (SELECT host_id FROM live)))
ORDER BY h.name ASC, h.id ASC
LIMIT sqlc.arg(row_limit);

-- name: ListAllHosts :many
-- Every host of the organization, for the inventory renderer. Unpaginated on
-- purpose: an inventory is rendered whole (spec §13 sizes an installation at
-- 10 000 hosts).
SELECT * FROM hosts WHERE org_id = $1 ORDER BY name;

-- name: ListScopeHosts :many
-- Every host of the organization with only what a scope selects on (id,
-- name, zone, managed): what an inventory change reaches, read inside the
-- change's transaction without the facts.
SELECT id, name, zone_id, managed FROM hosts WHERE org_id = $1 ORDER BY name;

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

-- name: ListHostsPage :many
-- Offset paging (backlog 3.97) in the default order, name then id: the one
-- order the (org_id, name) unique index serves, so it stays an index scan.
-- The filters are ListHosts' own, plus active_runs: true keeps hosts with a
-- live run, false those without, NULL both.
WITH live AS MATERIALIZED (
  -- Driven from the live side (jobs_live_idx): the few runs still in flight,
  -- never each host's whole history.
  SELECT jh.host_id, count(*) AS n
  FROM jobs j JOIN job_hosts jh ON jh.org_id = j.org_id AND jh.job_id = j.id
  WHERE j.org_id = sqlc.arg(org_id)
    AND j.status IN ('queued', 'claimed', 'running', 'cancelling')
  GROUP BY jh.host_id
)
SELECT h.* FROM hosts h
WHERE h.org_id = sqlc.arg(org_id)
  AND (sqlc.narg(ids)::uuid[] IS NULL OR h.id = ANY(sqlc.narg(ids)::uuid[]))
  AND (sqlc.narg(zone_id)::uuid IS NULL OR h.zone_id = sqlc.narg(zone_id)::uuid)
  AND (sqlc.narg(managed)::boolean IS NULL OR h.managed = sqlc.narg(managed)::boolean)
  AND (sqlc.narg(monitored)::boolean IS NULL OR h.monitored = sqlc.narg(monitored)::boolean)
  AND (
    sqlc.narg(query)::text IS NULL
    OR h.name ILIKE '%' || sqlc.narg(query)::text || '%'
    OR h.address ILIKE '%' || sqlc.narg(query)::text || '%'
  )
  AND (sqlc.narg(active_runs)::boolean IS NULL OR sqlc.narg(active_runs)::boolean = (h.id IN (SELECT host_id FROM live)))
ORDER BY h.name ASC, h.id ASC
OFFSET sqlc.arg(row_offset) LIMIT sqlc.arg(row_limit);

-- name: ListHostsPageSorted :many
-- Every other key and direction (backlog 3.97). The sort key is whitelisted
-- by the caller; each CASE is NULL on every row unless its key is chosen, so
-- the others drop out. last_seen puts never-seen hosts last in both
-- directions (NULLS LAST), active_runs reads the live-run counts of the CTE, and a tie falls back to name then id so pages are disjoint.
WITH live AS MATERIALIZED (
  -- Driven from the live side (jobs_live_idx): the few runs still in flight,
  -- never each host's whole history.
  SELECT jh.host_id, count(*) AS n
  FROM jobs j JOIN job_hosts jh ON jh.org_id = j.org_id AND jh.job_id = j.id
  WHERE j.org_id = sqlc.arg(org_id)
    AND j.status IN ('queued', 'claimed', 'running', 'cancelling')
  GROUP BY jh.host_id
)
SELECT h.* FROM hosts h
JOIN zones z ON z.id = h.zone_id AND z.org_id = h.org_id
LEFT JOIN live ON live.host_id = h.id
WHERE h.org_id = sqlc.arg(org_id)
  AND (sqlc.narg(ids)::uuid[] IS NULL OR h.id = ANY(sqlc.narg(ids)::uuid[]))
  AND (sqlc.narg(zone_id)::uuid IS NULL OR h.zone_id = sqlc.narg(zone_id)::uuid)
  AND (sqlc.narg(managed)::boolean IS NULL OR h.managed = sqlc.narg(managed)::boolean)
  AND (sqlc.narg(monitored)::boolean IS NULL OR h.monitored = sqlc.narg(monitored)::boolean)
  AND (
    sqlc.narg(query)::text IS NULL
    OR h.name ILIKE '%' || sqlc.narg(query)::text || '%'
    OR h.address ILIKE '%' || sqlc.narg(query)::text || '%'
  )
  AND (sqlc.narg(active_runs)::boolean IS NULL OR sqlc.narg(active_runs)::boolean = (COALESCE(live.n, 0) > 0))
ORDER BY
  (CASE WHEN sqlc.arg(sort_key)::text = 'name' AND NOT sqlc.arg(sort_desc)::boolean THEN h.name END) ASC,
  (CASE WHEN sqlc.arg(sort_key)::text = 'name' AND sqlc.arg(sort_desc)::boolean THEN h.name END) DESC,
  (CASE WHEN sqlc.arg(sort_key)::text = 'zone' AND NOT sqlc.arg(sort_desc)::boolean THEN z.name END) ASC,
  (CASE WHEN sqlc.arg(sort_key)::text = 'zone' AND sqlc.arg(sort_desc)::boolean THEN z.name END) DESC,
  (CASE WHEN sqlc.arg(sort_key)::text = 'managed' AND NOT sqlc.arg(sort_desc)::boolean THEN h.managed::int END) ASC,
  (CASE WHEN sqlc.arg(sort_key)::text = 'managed' AND sqlc.arg(sort_desc)::boolean THEN h.managed::int END) DESC,
  (CASE WHEN sqlc.arg(sort_key)::text = 'monitored' AND NOT sqlc.arg(sort_desc)::boolean THEN h.monitored::int END) ASC,
  (CASE WHEN sqlc.arg(sort_key)::text = 'monitored' AND sqlc.arg(sort_desc)::boolean THEN h.monitored::int END) DESC,
  (CASE WHEN sqlc.arg(sort_key)::text = 'last_seen' AND NOT sqlc.arg(sort_desc)::boolean THEN h.last_seen_at END) ASC NULLS LAST,
  (CASE WHEN sqlc.arg(sort_key)::text = 'last_seen' AND sqlc.arg(sort_desc)::boolean THEN h.last_seen_at END) DESC NULLS LAST,
  (CASE WHEN sqlc.arg(sort_key)::text = 'active_runs' AND NOT sqlc.arg(sort_desc)::boolean THEN COALESCE(live.n, 0) END) ASC,
  (CASE WHEN sqlc.arg(sort_key)::text = 'active_runs' AND sqlc.arg(sort_desc)::boolean THEN COALESCE(live.n, 0) END) DESC,
  h.name ASC, h.id ASC
OFFSET sqlc.arg(row_offset) LIMIT sqlc.arg(row_limit);

-- name: CountHosts :one
-- ListHostsPage's filters, counted: the total of a paged answer.
WITH live AS MATERIALIZED (
  -- Driven from the live side (jobs_live_idx): the few runs still in flight,
  -- never each host's whole history.
  SELECT jh.host_id, count(*) AS n
  FROM jobs j JOIN job_hosts jh ON jh.org_id = j.org_id AND jh.job_id = j.id
  WHERE j.org_id = sqlc.arg(org_id)
    AND j.status IN ('queued', 'claimed', 'running', 'cancelling')
  GROUP BY jh.host_id
)
SELECT count(*) FROM hosts h
WHERE h.org_id = sqlc.arg(org_id)
  AND (sqlc.narg(ids)::uuid[] IS NULL OR h.id = ANY(sqlc.narg(ids)::uuid[]))
  AND (sqlc.narg(zone_id)::uuid IS NULL OR h.zone_id = sqlc.narg(zone_id)::uuid)
  AND (sqlc.narg(managed)::boolean IS NULL OR h.managed = sqlc.narg(managed)::boolean)
  AND (sqlc.narg(monitored)::boolean IS NULL OR h.monitored = sqlc.narg(monitored)::boolean)
  AND (
    sqlc.narg(query)::text IS NULL
    OR h.name ILIKE '%' || sqlc.narg(query)::text || '%'
    OR h.address ILIKE '%' || sqlc.narg(query)::text || '%'
  )
  AND (sqlc.narg(active_runs)::boolean IS NULL OR sqlc.narg(active_runs)::boolean = (h.id IN (SELECT host_id FROM live)));

-- name: ActiveRunsForHosts :many
-- Live runs (queued, claimed, running, cancelling) per named host; a host
-- with none is absent. Served by jobs_live_idx.
WITH live AS MATERIALIZED (
  -- Driven from the live side (jobs_live_idx): the few runs still in flight,
  -- never each host's whole history.
  SELECT jh.host_id, count(*) AS n
  FROM jobs j JOIN job_hosts jh ON jh.org_id = j.org_id AND jh.job_id = j.id
  WHERE j.org_id = sqlc.arg(org_id)
    AND j.status IN ('queued', 'claimed', 'running', 'cancelling')
  GROUP BY jh.host_id
)
SELECT host_id, n::int AS runs FROM live WHERE host_id = ANY(sqlc.arg(ids)::uuid[]);
