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
-- access, "Audit"): empty for sessions, system events and older rows.
SELECT e.id, e.org_id, e.actor_id, e.actor_kind, e.action, e.object_type, e.object_id, e.summary, e.meta, e.at,
       e.actor_token_id, COALESCE(t.name, '')::text AS token_name
FROM audit_events e LEFT JOIN api_tokens t ON t.id = e.actor_token_id
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
-- access, "Audit"): empty for sessions, system events and older rows.
SELECT e.id, e.org_id, e.actor_id, e.actor_kind, e.action, e.object_type, e.object_id, e.summary, e.meta, e.at,
       e.actor_token_id, COALESCE(t.name, '')::text AS token_name
FROM audit_events e LEFT JOIN api_tokens t ON t.id = e.actor_token_id
WHERE e.actor_id = sqlc.arg(actor_id)
  AND (sqlc.narg(object_type)::text IS NULL OR e.object_type = sqlc.narg(object_type)::text)
  AND (sqlc.narg(since)::timestamptz IS NULL OR e.at >= sqlc.narg(since)::timestamptz)
  AND (
    sqlc.narg(cursor_at)::timestamptz IS NULL
    OR (e.at, e.id) < (sqlc.narg(cursor_at)::timestamptz, sqlc.narg(cursor_id)::uuid)
  )
ORDER BY e.at DESC, e.id DESC
LIMIT sqlc.arg(row_limit);
