-- name: InsertAuditEvent :exec
INSERT INTO audit_events (id, org_id, actor_id, actor_kind, action, object_type, object_id, summary, meta, at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10);

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
SELECT id, org_id, actor_id, actor_kind, action, object_type, object_id, summary, meta, at
FROM audit_events
WHERE org_id = sqlc.arg(org_id)
  AND (sqlc.narg(actor_id)::uuid IS NULL OR actor_id = sqlc.narg(actor_id)::uuid)
  AND (sqlc.narg(object_type)::text IS NULL OR object_type = sqlc.narg(object_type)::text)
  AND (sqlc.narg(since)::timestamptz IS NULL OR at >= sqlc.narg(since)::timestamptz)
  AND (
    sqlc.narg(cursor_at)::timestamptz IS NULL
    OR (at, id) < (sqlc.narg(cursor_at)::timestamptz, sqlc.narg(cursor_id)::uuid)
  )
ORDER BY at DESC, id DESC
LIMIT sqlc.arg(row_limit);

-- name: ListAuditEventsForActor :many
-- The cross-org counterpart of ListAuditEventsForOrg, for GET /me/activity:
-- filtered by actor_id instead of org_id, with the same optional
-- object_type / since / cursor conditions.
SELECT id, org_id, actor_id, actor_kind, action, object_type, object_id, summary, meta, at
FROM audit_events
WHERE actor_id = sqlc.arg(actor_id)
  AND (sqlc.narg(object_type)::text IS NULL OR object_type = sqlc.narg(object_type)::text)
  AND (sqlc.narg(since)::timestamptz IS NULL OR at >= sqlc.narg(since)::timestamptz)
  AND (
    sqlc.narg(cursor_at)::timestamptz IS NULL
    OR (at, id) < (sqlc.narg(cursor_at)::timestamptz, sqlc.narg(cursor_id)::uuid)
  )
ORDER BY at DESC, id DESC
LIMIT sqlc.arg(row_limit);
