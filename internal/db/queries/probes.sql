-- Probe vantage points (backlog 3.13a, migration 00098).

-- name: ListProbeAssignmentsForOrg :many
-- Every stored assignment of one organization, for the diff of
-- ReplaceOrgAssignments.
SELECT * FROM probe_assignments WHERE org_id = sqlc.arg(org_id);

-- name: ListProbeAssignmentsForAgent :many
-- One agent's assignments, by the primary key's leading agent_host_id.
SELECT * FROM probe_assignments
WHERE org_id = sqlc.arg(org_id) AND agent_host_id = sqlc.arg(agent_host_id)
ORDER BY target_host_id, check_key;

-- name: ListProbeSlotAgents :many
-- The agents currently assigned to one (target host, check key) slot.
SELECT agent_host_id FROM probe_assignments
WHERE org_id = sqlc.arg(org_id) AND target_host_id = sqlc.arg(target_host_id) AND check_key = sqlc.arg(check_key)
ORDER BY agent_host_id;

-- name: InsertProbeAssignment :exec
INSERT INTO probe_assignments (org_id, agent_host_id, target_host_id, check_key, plugin, params, interval_seconds, address)
VALUES (
  sqlc.arg(org_id), sqlc.arg(agent_host_id), sqlc.arg(target_host_id), sqlc.arg(check_key), sqlc.arg(plugin),
  sqlc.arg(params)::jsonb, sqlc.arg(interval_seconds), sqlc.arg(address)
);

-- name: UpdateProbeAssignment :exec
-- A changed assignment keeps its row; assigned_at records the change.
UPDATE probe_assignments SET
  plugin = sqlc.arg(plugin), params = sqlc.arg(params)::jsonb,
  interval_seconds = sqlc.arg(interval_seconds), address = sqlc.arg(address), assigned_at = now()
WHERE org_id = sqlc.arg(org_id) AND agent_host_id = sqlc.arg(agent_host_id)
  AND target_host_id = sqlc.arg(target_host_id) AND check_key = sqlc.arg(check_key);

-- name: DeleteProbeAssignment :exec
DELETE FROM probe_assignments
WHERE org_id = sqlc.arg(org_id) AND agent_host_id = sqlc.arg(agent_host_id)
  AND target_host_id = sqlc.arg(target_host_id) AND check_key = sqlc.arg(check_key);

-- name: InsertProbeSample :exec
INSERT INTO probe_samples (at, org_id, host_id, check_key, vantage, item, metric, value, label)
VALUES (
  sqlc.arg(at), sqlc.arg(org_id), sqlc.arg(host_id), sqlc.arg(check_key), sqlc.arg(vantage), sqlc.arg(item),
  sqlc.arg(metric), sqlc.arg(value), sqlc.arg(label)
);

-- name: UpsertProbeCurrent :exec
-- Replaces the row wholesale, like UpsertCheckCurrent.
INSERT INTO probe_current (org_id, host_id, check_key, vantage, item, metric, value, label, at, error)
VALUES (
  sqlc.arg(org_id), sqlc.arg(host_id), sqlc.arg(check_key), sqlc.arg(vantage), sqlc.arg(item), sqlc.arg(metric),
  sqlc.arg(value), sqlc.arg(label), sqlc.arg(at), sqlc.arg(error)
)
ON CONFLICT (host_id, check_key, vantage, item, metric) DO UPDATE SET
  value = EXCLUDED.value, label = EXCLUDED.label, at = EXCLUDED.at, error = EXCLUDED.error;

-- name: ListProbeCurrent :many
-- Every per-vantage current row of one check on one host.
SELECT * FROM probe_current
WHERE org_id = sqlc.arg(org_id) AND host_id = sqlc.arg(host_id) AND check_key = sqlc.arg(check_key)
ORDER BY vantage, item, metric;

-- name: DeleteProbeCurrentExcept :exec
-- The vantages of one slot not in keep lose their current rows; samples
-- stay until retention.
DELETE FROM probe_current
WHERE org_id = sqlc.arg(org_id) AND host_id = sqlc.arg(host_id) AND check_key = sqlc.arg(check_key)
  AND NOT (vantage = ANY(sqlc.arg(keep)::text[]));

-- name: DeleteItemProbeCurrent :exec
-- Every vantage's current rows of one item: the item forget's share
-- (backlog 3.117). Samples stay until retention.
DELETE FROM probe_current
WHERE org_id = sqlc.arg(org_id) AND host_id = sqlc.arg(host_id) AND check_key = sqlc.arg(check_key)
  AND item = sqlc.arg(item);

-- name: DeleteMetricProbeCurrent :exec
-- Every vantage's current rows of one metric: the metric forget's share
-- (backlog 3.117).
DELETE FROM probe_current
WHERE org_id = sqlc.arg(org_id) AND host_id = sqlc.arg(host_id) AND check_key = sqlc.arg(check_key)
  AND item = sqlc.arg(item) AND metric = sqlc.arg(metric);

-- name: ProbeSeriesForHost :many
-- One vantage's samples of one metric, downsampled like SeriesForHost
-- (checks.sql): the same bucketing, NULL filter and value bounds, so a
-- vantage's graph reads exactly like the check's own.
SELECT time_bucket(make_interval(secs => sqlc.arg(bucket_seconds)::double precision), at)::timestamptz AS bucket,
       avg(value)::double precision AS value
FROM probe_samples
WHERE org_id = sqlc.arg(org_id) AND host_id = sqlc.arg(host_id) AND check_key = sqlc.arg(check_key)
  AND vantage = sqlc.arg(vantage) AND item = sqlc.arg(item) AND metric = sqlc.arg(metric)
  AND at >= sqlc.arg(from_at) AND at < sqlc.arg(to_at)
  AND value IS NOT NULL
  AND value BETWEEN -1e100 AND 1e100
GROUP BY bucket
ORDER BY bucket;
