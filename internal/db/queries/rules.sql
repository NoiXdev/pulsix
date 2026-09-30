-- name: CreateRule :exec
-- created_at/updated_at are supplied by the caller (internal/rules.Service's
-- injected clock), not DEFAULT now(): Store.Create returns only an error,
-- never a round-tripped row (internal/rules.Store's own doc comment), so
-- the timestamps this statement writes are the only ones the service ever
-- sees again.
INSERT INTO check_rules (
  id, org_id, name, enabled, origin, scope, group_id, host_id, item,
  check_key, metric, severity, mode, ast, clear_ast, source, for_duration,
  created_at, updated_at
) VALUES (
  sqlc.arg(id), sqlc.arg(org_id), sqlc.arg(name), sqlc.arg(enabled), sqlc.arg(origin),
  sqlc.arg(scope), sqlc.arg(group_id), sqlc.arg(host_id), sqlc.arg(item),
  sqlc.arg(check_key), sqlc.arg(metric), sqlc.arg(severity), sqlc.arg(mode),
  sqlc.arg(ast)::jsonb, sqlc.arg(clear_ast)::jsonb, sqlc.arg(source), sqlc.arg(for_duration),
  sqlc.arg(created_at), sqlc.arg(updated_at)
);

-- name: GetRule :one
SELECT * FROM check_rules WHERE org_id = sqlc.arg(org_id) AND id = sqlc.arg(id);

-- name: UpdateRule :execrows
-- Full replace of every mutable column; origin, created_at are deliberately
-- absent from the SET list — origin never changes after creation and
-- created_at is not this statement's to touch (the service carries both
-- through from the row it fetched before calling this). updated_at is the
-- caller's clock value, the same reasoning as CreateRule's own comment.
UPDATE check_rules
SET name = sqlc.arg(name), enabled = sqlc.arg(enabled), scope = sqlc.arg(scope),
    group_id = sqlc.arg(group_id), host_id = sqlc.arg(host_id), item = sqlc.arg(item),
    check_key = sqlc.arg(check_key), metric = sqlc.arg(metric), severity = sqlc.arg(severity),
    mode = sqlc.arg(mode), ast = sqlc.arg(ast)::jsonb, clear_ast = sqlc.arg(clear_ast)::jsonb,
    source = sqlc.arg(source), for_duration = sqlc.arg(for_duration), updated_at = sqlc.arg(updated_at)
WHERE org_id = sqlc.arg(org_id) AND id = sqlc.arg(id);

-- name: DeleteRule :execrows
DELETE FROM check_rules WHERE org_id = sqlc.arg(org_id) AND id = sqlc.arg(id);

-- name: ListRules :many
-- The optional-parameter idiom hosts.sql's ListHosts and audit.sql already
-- use ("param IS NULL OR <condition>"): one statement serves an unfiltered
-- list and every combination of Filter's three fields, rather than the
-- service assembling SQL text per call.
SELECT * FROM check_rules
WHERE org_id = sqlc.arg(org_id)
  AND (sqlc.narg(scope)::text IS NULL OR scope = sqlc.narg(scope)::text)
  AND (sqlc.narg(check_key)::text IS NULL OR check_key = sqlc.narg(check_key)::text)
  AND (sqlc.narg(host_id)::uuid IS NULL OR host_id = sqlc.narg(host_id)::uuid)
ORDER BY created_at, id;

-- name: ListCheckStates :many
-- Task 7 (Store.StatesForHost): every tuple's current state for one host,
-- read straight off check_states' own primary key prefix (host_id) with
-- org_id re-asserted in the WHERE clause the same way every other store in
-- this codebase double-checks org scope alongside row-level security (see
-- e.g. GetRule above). Ordered by (check_key, item, metric) so two calls
-- against the same, unchanged data always agree on order.
SELECT * FROM check_states
WHERE org_id = sqlc.arg(org_id) AND host_id = sqlc.arg(host_id)
ORDER BY check_key, item, metric;

-- name: CountStateChangesSince :many
-- Task 7 (Store.ChangeCounts): the flapping count, one row per tuple that
-- changed at least once since `since`. This is check_state_changes_lookup_
-- idx's one named query (00046's own comment): org_id and host_id pin the
-- index's leading columns, `at >= since` is the range condition the index's
-- trailing `at DESC` column serves, and the GROUP BY reduces to exactly
-- the map ChangeCounts hands back — a tuple absent from the result changed
-- zero times, not an error.
SELECT check_key, item, metric, count(*)::bigint AS change_count
FROM check_state_changes
WHERE org_id = sqlc.arg(org_id) AND host_id = sqlc.arg(host_id) AND at >= sqlc.arg(since)
GROUP BY check_key, item, metric;
