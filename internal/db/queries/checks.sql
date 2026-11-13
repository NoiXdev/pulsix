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

-- name: UpdateCheckCurrent :execrows
-- The existing-tuple half of ingest (backlog 7.57): replaces an existing
-- row wholesale, exactly like UpsertCheckCurrent's conflict arm, and
-- reports 0 rows for a tuple the host has never reported. Only a tuple
-- that is new to the host counts against the per-host tuple cap, so ingest
-- tries this first and reaches for the cap and UpsertCheckCurrent only on
-- 0. The primary key's leading host_id makes it one index probe.
UPDATE check_current SET
  value = sqlc.arg(value), label = sqlc.arg(label), at = sqlc.arg(at), error = sqlc.arg(error)
WHERE org_id = sqlc.arg(org_id) AND host_id = sqlc.arg(host_id)
  AND check_key = sqlc.arg(check_key) AND item = sqlc.arg(item) AND metric = sqlc.arg(metric);

-- name: CountCheckCurrentForHost :one
-- How many (check_key, item, metric) tuples one host holds, for the
-- per-host tuple cap (backlog 7.57). Served by the primary key's leading
-- host_id; ingest asks at most once per host per batch, and only when the
-- batch carries a tuple the host has never reported.
SELECT count(*) FROM check_current WHERE org_id = sqlc.arg(org_id) AND host_id = sqlc.arg(host_id);

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
  -- internal/checks.MaxAbsValue (backlog 3.11c): ingest refuses a value
  -- beyond it, but one stored before may be NaN, ±Infinity or 1e200, and
  -- avg() overflows on 1e200 next to any other sample, failing the route.
  -- NaN sorts above every number in PostgreSQL, so BETWEEN leaves it out.
  AND value BETWEEN -1e100 AND 1e100
GROUP BY bucket
ORDER BY bucket;

-- name: LockHostEvalCursor :execrows
-- Forgetting an item (backlog 3.12b) starts here: it takes the row lock
-- on the host's evaluation cursor that the rule evaluator holds for the
-- whole of one host's evaluation (rules.sql's LockOneEvalCursor) and that
-- ingest takes last, in MarkHostDirty. Unlike the evaluator it WAITS
-- rather than skipping: a forget must not be abandoned because the host
-- is being evaluated right now, and the evaluator itself skips a locked
-- cursor, so it never waits on a forget in turn. Holding it for the rest
-- of the transaction is what keeps an evaluation from writing the item's
-- states or alerts in between the forget's deletes, and from closing or
-- reopening the same alerts while the forget closes them. 0 rows means
-- the host has no cursor, which only a host that never stored a reading
-- has — there is then no evaluation to serialise with.
SELECT host_id FROM check_eval_cursor
WHERE org_id = sqlc.arg(org_id) AND host_id = sqlc.arg(host_id)
FOR UPDATE;

-- name: MarkHostDirtyNow :exec
-- The forget's own evaluation mark: the item's states are gone, so the
-- host's roll-up (check_host_states) is stale until the next evaluation.
-- clock_timestamp() rather than MarkHostDirty's now(): it runs after
-- LockHostEvalCursor may have waited for an evaluation, whose
-- evaluated_at is its own transaction's start — possibly later than this
-- transaction's now(), which would leave the host looking evaluated.
UPDATE check_eval_cursor SET dirty_at = clock_timestamp()
WHERE org_id = sqlc.arg(org_id) AND host_id = sqlc.arg(host_id);

-- name: ItemNewest :one
-- The expiry's re-check inside the forget transaction, after the cursor
-- lock: how many current rows the item has, its newest reading, the newest
-- reading of its check key on the host (any item), the host's newest
-- reading (any check), and whether the host has an agent and when that
-- agent was last seen. checks.itemExpired applies ListExpiredItems' rule
-- to them. 'epoch' stands in for "no reading" and "never seen" so the
-- generated scan never meets a NULL; item_rows and has_agent say whether
-- they are real.
SELECT
  count(*) AS item_rows,
  COALESCE(max(c.at), 'epoch')::timestamptz AS item_newest,
  (SELECT COALESCE(max(k.at), 'epoch') FROM check_current k
   WHERE k.org_id = sqlc.arg(org_id) AND k.host_id = sqlc.arg(host_id)
     AND k.check_key = sqlc.arg(check_key))::timestamptz AS key_newest,
  (SELECT COALESCE(max(h.at), 'epoch') FROM check_current h
   WHERE h.org_id = sqlc.arg(org_id) AND h.host_id = sqlc.arg(host_id))::timestamptz AS host_newest,
  EXISTS (SELECT 1 FROM host_agents a
          WHERE a.org_id = sqlc.arg(org_id) AND a.host_id = sqlc.arg(host_id)) AS has_agent,
  (SELECT COALESCE(max(a.last_seen), 'epoch') FROM host_agents a
   WHERE a.org_id = sqlc.arg(org_id) AND a.host_id = sqlc.arg(host_id))::timestamptz AS agent_seen
FROM check_current c
WHERE c.org_id = sqlc.arg(org_id) AND c.host_id = sqlc.arg(host_id)
  AND c.check_key = sqlc.arg(check_key) AND c.item = sqlc.arg(item);

-- name: DeleteItemCurrent :execrows
-- Every current row of one item, all of its metrics. SKIP LOCKED is what
-- keeps the forget free of deadlocks with ingest: an ingest transaction
-- updates its check_current rows first and takes the cursor lock last
-- (MarkHostDirty), while the forget already holds the cursor. A row an
-- in-flight ingest holds is therefore one whose reading will commit after
-- the forget — it is left to land after it and re-create the item, which
-- is what a reading arriving during the forget may do. Waiting for it
-- instead would wait on a transaction that waits on this one.
DELETE FROM check_current c
USING (
  SELECT l.host_id, l.check_key, l.item, l.metric FROM check_current l
  WHERE l.org_id = sqlc.arg(org_id) AND l.host_id = sqlc.arg(host_id)
    AND l.check_key = sqlc.arg(check_key) AND l.item = sqlc.arg(item)
  FOR UPDATE SKIP LOCKED
) locked
WHERE c.host_id = locked.host_id AND c.check_key = locked.check_key
  AND c.item = locked.item AND c.metric = locked.metric;

-- name: DeleteItemStates :exec
-- One item's rule states, every metric — only ever written by the
-- evaluator under the cursor lock the forget holds.
DELETE FROM check_states
WHERE org_id = sqlc.arg(org_id) AND host_id = sqlc.arg(host_id)
  AND check_key = sqlc.arg(check_key) AND item = sqlc.arg(item);

-- name: DeleteItemStateChanges :exec
-- One item's state-change history (the flapping count reads it), so a
-- reappearance starts fresh. check_state_changes_lookup_idx serves it.
DELETE FROM check_state_changes
WHERE org_id = sqlc.arg(org_id) AND host_id = sqlc.arg(host_id)
  AND check_key = sqlc.arg(check_key) AND item = sqlc.arg(item);

-- name: DeleteItemCheckItem :exec
-- The item's discovery row (check_items, migration 00048), so a forgotten
-- item leaves the rule-item suggestions and decision previews too, and a
-- reappearance is discovered again with a new first_seen. check_items owns
-- no row for the whole-host item "" (00048's CHECK), so for "" this
-- deletes nothing. The table belongs to internal/items; a forget is the
-- one writer outside it, because the delete must share the forget's
-- transaction.
DELETE FROM check_items
WHERE org_id = sqlc.arg(org_id) AND host_id = sqlc.arg(host_id)
  AND check_key = sqlc.arg(check_key) AND item = sqlc.arg(item);

-- name: ListExpiredItems :many
-- The automatic expiry's selection (backlog 3.12b), across every
-- organization — it runs unscoped on the bare pool, like the alert sweep.
-- An item is due when its newest reading (max at over its metrics, so one
-- fresh metric keeps the whole item) is older than cutoff (now - period)
-- AND its host kept reporting after T = that newest + the period, which is
-- the host's last sign of life after T:
--   (a) its check key on the host has a reading after T (a sibling item,
--       say another mount, still reports), or
--   (b) the host has an agent last seen after T (the check itself was
--       removed, the agent is alive), or
--   (c) the host has no agent and any reading of it is after T.
-- The manager's own probes write under the host too, so for a host with
-- an agent only (a) and (b) count: an offline agent's items keep their
-- values however long the server's checks of the host keep running. A
-- host that stopped reporting altogether has no sign of life after T, so
-- it keeps every value. GREATEST ignores the NULL of an agent never seen.
-- Every comparison is strict. Oldest first, so a backlog drains from the
-- longest-gone end. One pass over check_current, aggregated by item, then
-- by check key and by host; host_agents joins on its primary key.
-- checks.itemExpired is the same rule in Go, over ItemNewest, for the
-- re-check inside each item's own forget transaction.
WITH items AS (
  SELECT org_id, host_id, check_key, item, max(at) AS newest
  FROM check_current
  GROUP BY org_id, host_id, check_key, item
), key_newest AS (
  SELECT host_id, check_key, max(newest) AS newest FROM items GROUP BY host_id, check_key
), host_newest AS (
  SELECT host_id, max(newest) AS newest FROM key_newest GROUP BY host_id
)
SELECT i.org_id, i.host_id, i.check_key, i.item
FROM items i
JOIN key_newest k ON k.host_id = i.host_id AND k.check_key = i.check_key
JOIN host_newest h ON h.host_id = i.host_id
LEFT JOIN host_agents a ON a.host_id = i.host_id
WHERE i.newest < sqlc.arg(cutoff)::timestamptz
  AND GREATEST(k.newest, CASE WHEN a.host_id IS NULL THEN h.newest ELSE a.last_seen END)
      > i.newest + make_interval(secs => sqlc.arg(period_seconds)::double precision)
ORDER BY i.newest, i.org_id, i.host_id, i.check_key, i.item
LIMIT sqlc.arg(lim);
