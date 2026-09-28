-- name: CreateMonitoringGroup :one
INSERT INTO monitoring_groups (id, org_id, name, selector, priority)
VALUES (sqlc.arg(id), sqlc.arg(org_id), sqlc.arg(name), sqlc.arg(selector), sqlc.arg(priority))
RETURNING *;

-- name: ListMonitoringGroups :many
SELECT * FROM monitoring_groups WHERE org_id = sqlc.arg(org_id) ORDER BY name;

-- name: UpdateMonitoringGroup :one
-- Full replace of every mutable column; org_id in the WHERE clause is what
-- actually makes "the group must belong to the org" enforceable from a
-- plain application connection (see hosts.sql's GetZone/GetHost for the
-- same reasoning about RLS not being the only guard).
UPDATE monitoring_groups
SET name = sqlc.arg(name), selector = sqlc.arg(selector), priority = sqlc.arg(priority), updated_at = now()
WHERE org_id = sqlc.arg(org_id) AND id = sqlc.arg(id)
RETURNING *;

-- name: DeleteMonitoringGroup :execrows
DELETE FROM monitoring_groups WHERE org_id = sqlc.arg(org_id) AND id = sqlc.arg(id);

-- name: CreateCheckDefinition :one
INSERT INTO check_definitions (
  id, org_id, key, plugin, params, interval_seconds, enabled, origin, binding_kind, group_id, host_id
) VALUES (
  sqlc.arg(id), sqlc.arg(org_id), sqlc.arg(key), sqlc.arg(plugin), sqlc.arg(params)::jsonb,
  sqlc.arg(interval_seconds), sqlc.arg(enabled), sqlc.arg(origin), sqlc.arg(binding_kind),
  sqlc.arg(group_id), sqlc.arg(host_id)
)
RETURNING *;

-- name: GetCheckDefinition :one
SELECT * FROM check_definitions WHERE org_id = sqlc.arg(org_id) AND id = sqlc.arg(id);

-- name: ListCheckDefinitions :many
SELECT * FROM check_definitions WHERE org_id = sqlc.arg(org_id) ORDER BY key;

-- name: UpdateCheckDefinition :one
-- Full replace of every mutable column, exactly like UpdateMonitoringGroup;
-- origin is deliberately absent from the SET list — it never changes after
-- creation, so the service carries it through unchanged rather than this
-- query being able to touch it.
UPDATE check_definitions
SET key = sqlc.arg(key), plugin = sqlc.arg(plugin), params = sqlc.arg(params)::jsonb,
    interval_seconds = sqlc.arg(interval_seconds), enabled = sqlc.arg(enabled),
    binding_kind = sqlc.arg(binding_kind), group_id = sqlc.arg(group_id), host_id = sqlc.arg(host_id),
    updated_at = now()
WHERE org_id = sqlc.arg(org_id) AND id = sqlc.arg(id)
RETURNING *;

-- name: DeleteCheckDefinition :execrows
DELETE FROM check_definitions WHERE org_id = sqlc.arg(org_id) AND id = sqlc.arg(id);

-- name: HostsExisting :many
-- Which of ids actually belong to org_id, right now — the "unknown host"
-- half of RecordObservations' rejection rule. Mirrors hosts.sql's
-- ListHostZones (same ANY(...::uuid[]) shape): a host absent from the
-- result is either deleted or never existed in this organization, and
-- either way its observations are rejected, not inserted.
SELECT id FROM hosts WHERE org_id = sqlc.arg(org_id) AND id = ANY(sqlc.arg(host_ids)::uuid[]);

-- name: InsertCheckSample :exec
-- One row of history. Never consults check_definitions — see
-- check_samples' own migration comment (00044) for why an unknown
-- check_key is still written.
INSERT INTO check_samples (at, org_id, host_id, check_key, item, metric, value, label)
VALUES (
  sqlc.arg(at), sqlc.arg(org_id), sqlc.arg(host_id), sqlc.arg(check_key), sqlc.arg(item), sqlc.arg(metric),
  sqlc.arg(value), sqlc.arg(label)
);

-- name: UpsertCheckCurrent :exec
-- Replaces the (host_id, check_key, item, metric) row wholesale: value,
-- label, at and error together, never a partial update — an error
-- observation must clear a previous value/label (and vice versa), which a
-- SET list that only touched some columns could leave stale.
INSERT INTO check_current (org_id, host_id, check_key, item, metric, value, label, at, error)
VALUES (
  sqlc.arg(org_id), sqlc.arg(host_id), sqlc.arg(check_key), sqlc.arg(item), sqlc.arg(metric),
  sqlc.arg(value), sqlc.arg(label), sqlc.arg(at), sqlc.arg(error)
)
ON CONFLICT (host_id, check_key, item, metric) DO UPDATE SET
  value = EXCLUDED.value, label = EXCLUDED.label, at = EXCLUDED.at, error = EXCLUDED.error;

-- name: ListCheckCurrent :many
-- Every current row for one host, disappeared items included (they are
-- never deleted — check_current's own doc comment) — ordering is only for
-- deterministic output, the caller decides what "stale" means.
SELECT * FROM check_current WHERE org_id = sqlc.arg(org_id) AND host_id = sqlc.arg(host_id)
ORDER BY check_key, item, metric;

-- name: SeriesForHost :many
-- One metric's samples for one host, downsampled server-side: bucket_seconds
-- is the caller's own (to-from)/buckets, turned into an interval through
-- make_interval rather than string-formatted into the query text, which
-- keeps this a plain parameterized statement.
--
-- `value IS NOT NULL` is load-bearing, not tidiness. avg() does skip NULL
-- rows, but a BUCKET that contains nothing but label rows still forms, and
-- avg() over it is NULL, which the generated scan refuses ("cannot scan
-- NULL into *float64") — a 500 on a shipped route, reachable from two
-- ordinary ingests on the seeded service check (a state reading, then a
-- collection failure; filed as backlog 3.29 and closed here, pinned by
-- internal/api's TestHostMetricsAfterACollectionErrorIsAnEmptySeries).
-- Filtering the rows out instead means such a bucket never forms at all,
-- so the answer is an honest empty series. The caller refuses a metric it can SEE is discrete
-- before reaching here; this is what covers the case it cannot see,
-- where the only current row is an error carrying neither value nor label.
SELECT time_bucket(make_interval(secs => sqlc.arg(bucket_seconds)::double precision), at)::timestamptz AS bucket,
       avg(value)::double precision AS value
FROM check_samples
WHERE org_id = sqlc.arg(org_id) AND host_id = sqlc.arg(host_id) AND check_key = sqlc.arg(check_key)
  AND item = sqlc.arg(item) AND metric = sqlc.arg(metric)
  AND at >= sqlc.arg(from_at) AND at < sqlc.arg(to_at)
  AND value IS NOT NULL
GROUP BY bucket
ORDER BY bucket;
