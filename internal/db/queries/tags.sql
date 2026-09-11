-- name: ListHostTags :many
-- Every tag on the host, both sources, ordered by key then source so
-- "manual" and "fact" rows for the same key sit next to each other in the
-- result — internal/hosts.SQLStore.ListHostTags does no further sorting of
-- its own.
SELECT key, value, source FROM host_tags
WHERE org_id = $1 AND host_id = $2
ORDER BY key, source;

-- name: DeleteManualHostTags :exec
-- The first half of ReplaceManualTags' delete-then-insert (see
-- internal/hosts.SQLStore.ReplaceManualTags): every "manual" row for this
-- host is removed, and only "manual" — a "fact" row for the same key is
-- never touched by this statement, which is what "fact tags are left
-- untouched" actually means at the storage layer.
DELETE FROM host_tags WHERE org_id = $1 AND host_id = $2 AND source = 'manual';

-- name: InsertHostTag :exec
-- The second half of ReplaceManualTags' delete-then-insert, and also how a
-- later task will record a "fact" tag (source = 'fact' instead of
-- 'manual') — this query itself is source-agnostic, taking source as a
-- caller-supplied argument rather than hardcoding one.
INSERT INTO host_tags (host_id, org_id, key, value, source)
VALUES ($1, $2, $3, $4, $5);

-- name: ListTagKeyValueCounts :many
-- Every distinct (key, value) pair recorded anywhere in the organization,
-- with how many hosts carry it — the data GET /orgs/{org}/tags groups into
-- KeyValues for the tag-selector editor's autocomplete. count is
-- COUNT(DISTINCT host_id), not COUNT(*), so a host that happens to carry
-- the same key=value as both a manual and a fact tag is not counted twice.
SELECT key, value, COUNT(DISTINCT host_id)::bigint AS count
FROM host_tags
WHERE org_id = $1
GROUP BY key, value
ORDER BY key, value;
