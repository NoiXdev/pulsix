-- name: InsertAuditEvent :exec
INSERT INTO audit_events (id, org_id, actor_id, actor_kind, action, object_type, object_id, summary, meta, at, actor_token_id)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11);

-- name: ListAuditEventsForOrg :many
-- Every optional filter (actor_id, object_type, since, and the cursor's own
-- (at, id) keyset pair) is expressed as "param IS NULL OR <condition>", so
-- one query serves every combination the activity feed route accepts
-- without sqlc generating a family of near-duplicate statements. The cursor
-- condition mirrors ORDER BY at DESC, id DESC exactly: row-constructor
-- comparison "(at, id) < (cursor_at, cursor_id)" is true precisely for rows
-- that sort strictly after the cursor row in that order, which is what
-- keeps pagination stable even when several events share the same at
-- (store_sql.go's cursor always carries id as the tiebreaker).
-- The token's name comes from a left join (spec 2026-11-16 assistant
-- access, "Audit"): empty for sessions, system events and older rows. A
-- personal token (backlog 3.100) is named from its own table.
SELECT e.id, e.org_id, e.actor_id, e.actor_kind, e.action, e.object_type, e.object_id, e.summary, e.meta, e.at,
       e.actor_token_id, COALESCE(t.name, pt.name, '')::text AS token_name
FROM audit_events e
LEFT JOIN api_tokens t ON t.id = e.actor_token_id
LEFT JOIN personal_tokens pt ON pt.id = e.actor_token_id
WHERE e.org_id = sqlc.arg(org_id)
  AND (sqlc.narg(actor_id)::uuid IS NULL OR e.actor_id = sqlc.narg(actor_id)::uuid)
  AND (sqlc.narg(object_type)::text IS NULL OR e.object_type = sqlc.narg(object_type)::text)
  AND (sqlc.narg(since)::timestamptz IS NULL OR e.at >= sqlc.narg(since)::timestamptz)
  AND (
    sqlc.narg(cursor_at)::timestamptz IS NULL
    OR (e.at, e.id) < (sqlc.narg(cursor_at)::timestamptz, sqlc.narg(cursor_id)::uuid)
  )
ORDER BY e.at DESC, e.id DESC
LIMIT sqlc.arg(row_limit);

-- name: ListAuditEventsForActor :many
-- The cross-org counterpart of ListAuditEventsForOrg, for GET /me/activity:
-- filtered by actor_id instead of org_id, with the same optional
-- object_type / since / cursor conditions.
-- The token's name comes from a left join (spec 2026-11-16 assistant
-- access, "Audit"): empty for sessions, system events and older rows. A
-- personal token (backlog 3.100) is named from its own table.
SELECT e.id, e.org_id, e.actor_id, e.actor_kind, e.action, e.object_type, e.object_id, e.summary, e.meta, e.at,
       e.actor_token_id, COALESCE(t.name, pt.name, '')::text AS token_name
FROM audit_events e
LEFT JOIN api_tokens t ON t.id = e.actor_token_id
LEFT JOIN personal_tokens pt ON pt.id = e.actor_token_id
WHERE e.actor_id = sqlc.arg(actor_id)
  AND (sqlc.narg(object_type)::text IS NULL OR e.object_type = sqlc.narg(object_type)::text)
  AND (sqlc.narg(since)::timestamptz IS NULL OR e.at >= sqlc.narg(since)::timestamptz)
  AND (
    sqlc.narg(cursor_at)::timestamptz IS NULL
    OR (e.at, e.id) < (sqlc.narg(cursor_at)::timestamptz, sqlc.narg(cursor_id)::uuid)
  )
ORDER BY e.at DESC, e.id DESC
LIMIT sqlc.arg(row_limit);

-- name: ListAuditEventsForOrgPage :many
-- Offset paging (backlog 3.97) in the default order, newest first: the order
-- audit_events_org_at serves. ListAuditEventsForOrg's filters without the
-- cursor. The token's name is not joined here: the store resolves it for the
-- page's rows with ListAuditTokenNames, once for every page query.
SELECT * FROM audit_events e
WHERE e.org_id = sqlc.arg(org_id)
  AND (sqlc.narg(actor_id)::uuid IS NULL OR e.actor_id = sqlc.narg(actor_id)::uuid)
  AND (sqlc.narg(object_type)::text IS NULL OR e.object_type = sqlc.narg(object_type)::text)
  AND (sqlc.narg(since)::timestamptz IS NULL OR e.at >= sqlc.narg(since)::timestamptz)
ORDER BY e.at DESC, e.id DESC
OFFSET sqlc.arg(row_offset) LIMIT sqlc.arg(row_limit);

-- name: ListAuditEventsForOrgPageAsc :many
-- ListAuditEventsForOrgPage, oldest first; the id runs the same way.
SELECT * FROM audit_events e
WHERE e.org_id = sqlc.arg(org_id)
  AND (sqlc.narg(actor_id)::uuid IS NULL OR e.actor_id = sqlc.narg(actor_id)::uuid)
  AND (sqlc.narg(object_type)::text IS NULL OR e.object_type = sqlc.narg(object_type)::text)
  AND (sqlc.narg(since)::timestamptz IS NULL OR e.at >= sqlc.narg(since)::timestamptz)
ORDER BY e.at ASC, e.id ASC
OFFSET sqlc.arg(row_offset) LIMIT sqlc.arg(row_limit);

-- name: CountAuditEventsForOrg :one
-- ListAuditEventsForOrgPage's filters, counted: the total of a paged answer.
SELECT count(*) FROM audit_events e
WHERE e.org_id = sqlc.arg(org_id)
  AND (sqlc.narg(actor_id)::uuid IS NULL OR e.actor_id = sqlc.narg(actor_id)::uuid)
  AND (sqlc.narg(object_type)::text IS NULL OR e.object_type = sqlc.narg(object_type)::text)
  AND (sqlc.narg(since)::timestamptz IS NULL OR e.at >= sqlc.narg(since)::timestamptz);

-- name: ListAuditEventsForActorPage :many
-- The cross-org counterpart of ListAuditEventsForOrgPage, for GET
-- /me/activity: the same default order and filters, by actor instead of org.
SELECT * FROM audit_events e
WHERE e.actor_id = sqlc.arg(actor_id)
  AND (sqlc.narg(object_type)::text IS NULL OR e.object_type = sqlc.narg(object_type)::text)
  AND (sqlc.narg(since)::timestamptz IS NULL OR e.at >= sqlc.narg(since)::timestamptz)
ORDER BY e.at DESC, e.id DESC
OFFSET sqlc.arg(row_offset) LIMIT sqlc.arg(row_limit);

-- name: ListAuditEventsForActorPageAsc :many
-- ListAuditEventsForActorPage, oldest first; the id runs the same way.
SELECT * FROM audit_events e
WHERE e.actor_id = sqlc.arg(actor_id)
  AND (sqlc.narg(object_type)::text IS NULL OR e.object_type = sqlc.narg(object_type)::text)
  AND (sqlc.narg(since)::timestamptz IS NULL OR e.at >= sqlc.narg(since)::timestamptz)
ORDER BY e.at ASC, e.id ASC
OFFSET sqlc.arg(row_offset) LIMIT sqlc.arg(row_limit);

-- name: CountAuditEventsForActor :one
-- ListAuditEventsForActorPage's filters, counted: the total of a paged answer.
SELECT count(*) FROM audit_events e
WHERE e.actor_id = sqlc.arg(actor_id)
  AND (sqlc.narg(object_type)::text IS NULL OR e.object_type = sqlc.narg(object_type)::text)
  AND (sqlc.narg(since)::timestamptz IS NULL OR e.at >= sqlc.narg(since)::timestamptz);

-- name: ListAuditTokenNames :many
-- The names of the tokens a page of events was made with: the one place the
-- activity pages resolve a token's name (the cursor feeds keep their own
-- left joins), so a change of where tokens live touches this query only.
-- Organization tokens are confined by row-level security to the bound
-- organization; personal tokens (backlog 3.100) belong to a user and are
-- named from their own table.
SELECT id, name FROM api_tokens WHERE id = ANY(sqlc.arg(ids)::uuid[])
UNION ALL
SELECT id, name FROM personal_tokens WHERE id = ANY(sqlc.arg(ids)::uuid[]);

-- name: LastAuditActor :one
-- Who stopped the object (the person who paused a schedule or disabled a
-- trigger before an assistant's change, spec 2026-11-25 C2): the newest
-- event of stop_actions on it, or of all_actions on the whole kind (pause
-- all), after the newest of start_actions on it (a resume or an enable).
-- The token it was made with, by name, and whether that is an assistant
-- token (the line then names the assistant, not the token's owner).
-- Only events since the object was created count (since), which also
-- bounds the walk through the organization's history.
WITH last_start AS (
  SELECT max(at) AS at FROM audit_events
  WHERE org_id = sqlc.arg(org_id)::uuid AND object_type = sqlc.arg(object_type) AND object_id = sqlc.arg(object_id)
    AND action = ANY(sqlc.arg(start_actions)::text[]) AND at >= sqlc.arg(since)::timestamptz
)
SELECT e.actor_id, e.actor_token_id, e.at,
  COALESCE(t.name, p.name, '')::text AS token_name,
  COALESCE(t.kind = 'assistant', p.kind = 'assistant', false)::boolean AS assistant
FROM audit_events e
LEFT JOIN api_tokens t ON t.id = e.actor_token_id
LEFT JOIN personal_tokens p ON p.id = e.actor_token_id
WHERE e.org_id = sqlc.arg(org_id)::uuid AND e.object_type = sqlc.arg(object_type)
  AND ((e.object_id = sqlc.arg(object_id) AND e.action = ANY(sqlc.arg(stop_actions)::text[]))
    OR e.action = ANY(sqlc.arg(all_actions)::text[]))
  AND e.at >= sqlc.arg(since)::timestamptz
  AND e.at > COALESCE((SELECT at FROM last_start), '-infinity'::timestamptz)
ORDER BY e.at DESC, e.id DESC LIMIT 1;
