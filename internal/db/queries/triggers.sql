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
UPDATE trigger_buckets
SET tokens = LEAST(sqlc.arg(capacity)::float8,
      tokens + GREATEST(EXTRACT(EPOCH FROM (sqlc.arg(now)::timestamptz - updated_at))::float8, 0) * sqlc.arg(refill)::float8) - 1,
    updated_at = sqlc.arg(now)::timestamptz
WHERE org_id = sqlc.arg(org_id) AND trigger_id = sqlc.arg(trigger_id)
  AND LEAST(sqlc.arg(capacity)::float8,
      tokens + GREATEST(EXTRACT(EPOCH FROM (sqlc.arg(now)::timestamptz - updated_at))::float8, 0) * sqlc.arg(refill)::float8) >= 1
RETURNING tokens;

-- name: GetTriggerBucket :one
SELECT tokens, updated_at FROM trigger_buckets WHERE org_id = sqlc.arg(org_id) AND trigger_id = sqlc.arg(trigger_id);

-- name: InsertTriggerDelivery :exec
-- throttle: at most one row per trigger, outcome and minute.
INSERT INTO trigger_deliveries (id, org_id, trigger_id, at, source_ip, outcome, reason, job_group_id)
SELECT sqlc.arg(id)::uuid, sqlc.arg(org_id)::uuid, sqlc.arg(trigger_id)::uuid, sqlc.arg(at)::timestamptz,
       sqlc.arg(source_ip)::text, sqlc.arg(outcome)::text, sqlc.arg(reason)::text, sqlc.narg(job_group_id)::uuid
WHERE NOT sqlc.arg(throttle)::boolean OR NOT EXISTS (
  SELECT 1 FROM trigger_deliveries d
  WHERE d.org_id = sqlc.arg(org_id)::uuid AND d.trigger_id = sqlc.arg(trigger_id)::uuid
    AND d.outcome = sqlc.arg(outcome)::text AND d.at > sqlc.arg(at)::timestamptz - interval '1 minute'
);

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
