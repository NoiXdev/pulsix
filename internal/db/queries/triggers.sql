-- name: CreateTrigger :one
INSERT INTO job_triggers (id, org_id, kind, template_id, repo_id, name, enabled, preset, auth, signature_header,
                          timestamp_header, secret, allow_cidrs, mapping, on_active, rate_limit, created_by)
VALUES (sqlc.arg(id), sqlc.arg(org_id), sqlc.arg(kind), sqlc.narg(template_id), sqlc.narg(repo_id), sqlc.arg(name),
        sqlc.arg(enabled), sqlc.arg(preset), sqlc.arg(auth), sqlc.arg(signature_header), sqlc.arg(timestamp_header),
        sqlc.arg(secret), sqlc.arg(allow_cidrs), sqlc.arg(mapping), sqlc.arg(on_active), sqlc.arg(rate_limit), sqlc.narg(created_by))
RETURNING *;

-- name: GetTrigger :one
SELECT * FROM job_triggers WHERE org_id = sqlc.arg(org_id) AND id = sqlc.arg(id);

-- name: ResolveTrigger :one
-- The hook endpoints' lookup: no org in the path, no org scope bound (the
-- bypass policy applies); the unguessable id is the only key.
SELECT * FROM job_triggers WHERE id = sqlc.arg(id);

-- name: ListTriggers :many
SELECT * FROM job_triggers
WHERE org_id = sqlc.arg(org_id)
  AND (sqlc.narg(template_id)::uuid IS NULL OR template_id = sqlc.narg(template_id)::uuid)
  AND (sqlc.narg(repo_id)::uuid IS NULL OR repo_id = sqlc.narg(repo_id)::uuid)
ORDER BY name;

-- name: UpdateTrigger :one
UPDATE job_triggers SET name = sqlc.arg(name), enabled = sqlc.arg(enabled), preset = sqlc.arg(preset), auth = sqlc.arg(auth),
  signature_header = sqlc.arg(signature_header), timestamp_header = sqlc.arg(timestamp_header),
  allow_cidrs = sqlc.arg(allow_cidrs), mapping = sqlc.arg(mapping), on_active = sqlc.arg(on_active),
  rate_limit = sqlc.arg(rate_limit), revision = revision + 1, updated_at = now()
WHERE org_id = sqlc.arg(org_id) AND id = sqlc.arg(id) AND revision = sqlc.arg(revision)
RETURNING *;

-- name: SetTriggerSecret :one
UPDATE job_triggers SET secret = sqlc.arg(secret), revision = revision + 1, updated_at = now()
WHERE org_id = sqlc.arg(org_id) AND id = sqlc.arg(id)
RETURNING *;

-- name: DeleteTrigger :execrows
DELETE FROM job_triggers WHERE org_id = sqlc.arg(org_id) AND id = sqlc.arg(id);

-- name: FillTriggerBucket :exec
INSERT INTO trigger_buckets (trigger_id, org_id, tokens, updated_at)
VALUES (sqlc.arg(trigger_id), sqlc.arg(org_id), sqlc.arg(tokens), sqlc.arg(at))
ON CONFLICT (trigger_id) DO UPDATE SET tokens = EXCLUDED.tokens, updated_at = EXCLUDED.updated_at;

-- name: TakeTriggerToken :one
-- Refill by elapsed time (capped at capacity), then take one token — only
-- when at least one is there. No row back means rate limited.
--
-- updated_at only ever moves FORWARD (backlog 2.62). It used to take
-- whichever manager's wall clock made the take, so a manager running
-- behind its peers wrote a timestamp in the past and the next take, from
-- a correct clock, measured elapsed time from there and refilled tokens
-- for seconds that never passed — repeat it and the limit stops limiting.
-- GREATEST pins the bucket to the furthest-ahead clock that has touched
-- it, which errs toward refilling too SLOWLY: the safe direction for a
-- rate limit, and self-correcting as real time catches up. (The database
-- clock would remove manager clocks entirely, as it does for the
-- notification queue in backlog 2.80, but the elapsed-time arithmetic
-- here is what the tests drive by passing a later `now`.)
UPDATE trigger_buckets
SET tokens = LEAST(sqlc.arg(capacity)::float8,
      tokens + GREATEST(EXTRACT(EPOCH FROM (sqlc.arg(now)::timestamptz - updated_at))::float8, 0) * sqlc.arg(refill)::float8) - 1,
    updated_at = GREATEST(updated_at, sqlc.arg(now)::timestamptz)
WHERE org_id = sqlc.arg(org_id) AND trigger_id = sqlc.arg(trigger_id)
  AND LEAST(sqlc.arg(capacity)::float8,
      tokens + GREATEST(EXTRACT(EPOCH FROM (sqlc.arg(now)::timestamptz - updated_at))::float8, 0) * sqlc.arg(refill)::float8) >= 1
RETURNING tokens;

-- name: GetTriggerBucket :one
SELECT tokens, updated_at FROM trigger_buckets WHERE org_id = sqlc.arg(org_id) AND trigger_id = sqlc.arg(trigger_id);

-- name: InsertTriggerDelivery :execrows
-- One row per trigger, outcome and calendar minute for the throttled
-- outcomes: trigger_deliveries_throttle_idx is the partial unique index
-- for exactly those, and ON CONFLICT DO NOTHING makes the dedup atomic
-- under concurrent deliveries (Task 9 review, Minor 1). Every other
-- outcome never matches that index, so it always inserts. 0 rows affected
-- tells the caller nothing was written, so it can skip the trim (Minor 2).
INSERT INTO trigger_deliveries (id, org_id, trigger_id, at, source_ip, outcome, reason, job_group_id)
VALUES (sqlc.arg(id)::uuid, sqlc.arg(org_id)::uuid, sqlc.arg(trigger_id)::uuid, sqlc.arg(at)::timestamptz,
        sqlc.arg(source_ip)::text, sqlc.arg(outcome)::text, sqlc.arg(reason)::text, sqlc.narg(job_group_id)::uuid)
ON CONFLICT (trigger_id, outcome, (trigger_delivery_minute(at))) WHERE outcome IN (
  'rejected_ip', 'rejected_auth', 'disabled', 'rate_limited', 'invalid_payload'
) DO NOTHING;

-- name: TrimTriggerDeliveries :exec
DELETE FROM trigger_deliveries AS d
WHERE d.org_id = sqlc.arg(org_id) AND d.trigger_id = sqlc.arg(trigger_id)
  AND d.id NOT IN (
    SELECT k.id FROM trigger_deliveries AS k
    WHERE k.org_id = sqlc.arg(org_id) AND k.trigger_id = sqlc.arg(trigger_id)
    ORDER BY k.at DESC, k.id DESC
    LIMIT 500
  );

-- name: ListTriggerDeliveries :many
SELECT * FROM trigger_deliveries WHERE org_id = sqlc.arg(org_id) AND trigger_id = sqlc.arg(trigger_id)
ORDER BY at DESC, id DESC LIMIT sqlc.arg(lim);

-- name: NoteDeliveryRateLimited :exec
-- Receive's own per-address budget is checked after InsertTriggerDelivery
-- above already recorded a hidden outcome's real reason; when the budget
-- is spent, the sender's actual answer becomes 429, so the row needs to
-- say so too, or an operator reading it alone sees only the stale
-- original reason. Matched by (trigger, outcome, minute) — the same key
-- the throttle index dedups new inserts on — so repeated failures from
-- one address, which is exactly how a budget gets spent, land on the one
-- row every one of those calls shares, not a fresh id of their own that
-- was itself deduplicated away. The "not already noted" guard keeps a
-- second, third, ... call in the same minute from appending the same
-- sentence again.
UPDATE trigger_deliveries
SET reason = CASE WHEN reason = '' THEN 'also answered 429 (address rate limit spent)'
                  ELSE reason || '; also answered 429 (address rate limit spent)' END
WHERE org_id = sqlc.arg(org_id) AND trigger_id = sqlc.arg(trigger_id) AND outcome = sqlc.arg(outcome)::text
  AND trigger_delivery_minute(at) = trigger_delivery_minute(sqlc.arg(at)::timestamptz)
  AND reason NOT LIKE '%address rate limit spent%';
