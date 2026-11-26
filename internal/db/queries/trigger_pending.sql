-- name: CollectTriggerPendingRun :one
-- A delivery to a settling trigger (backlog 2.137): the first one opens the
-- trigger's pending run (id, first_at and deadline_at are kept from then
-- on), every one moves due_at to settle after it — never past the deadline
-- — and replaces the collected values with its own. The upsert locks the
-- row, so a ticker starting this window (FOR UPDATE SKIP LOCKED) either
-- holds it first, and this waits and then opens the next window once the
-- started one is deleted, or skips it until this delivery committed.
INSERT INTO trigger_pending_runs (trigger_id, org_id, id, first_at, last_at, due_at, deadline_at, deliveries, last_delivery_id, overrides)
VALUES (sqlc.arg(trigger_id), sqlc.arg(org_id), sqlc.arg(id), sqlc.arg(at), sqlc.arg(at),
        LEAST(sqlc.arg(due)::timestamptz, sqlc.arg(deadline)::timestamptz), sqlc.arg(deadline)::timestamptz, 1,
        sqlc.arg(delivery_id), sqlc.narg(overrides))
ON CONFLICT (trigger_id) DO UPDATE SET
  last_at = GREATEST(trigger_pending_runs.last_at, EXCLUDED.last_at),
  due_at = LEAST(sqlc.arg(due)::timestamptz, trigger_pending_runs.deadline_at),
  deliveries = trigger_pending_runs.deliveries + 1,
  last_delivery_id = EXCLUDED.last_delivery_id,
  overrides = EXCLUDED.overrides
WHERE trigger_pending_runs.org_id = EXCLUDED.org_id
RETURNING *;

-- name: GetTriggerPendingRun :one
SELECT * FROM trigger_pending_runs WHERE org_id = sqlc.arg(org_id) AND trigger_id = sqlc.arg(trigger_id);

-- name: LockDueTriggerPendingRun :one
-- The settle ticker's claim, one pending run per transaction so a delivery
-- to a trigger never waits behind other triggers' starts: runs in a
-- raw-pool transaction without pulsix.org_id (bypass policy), across every
-- organization; SKIP LOCKED hands each row to one manager, and skip leaves
-- out the rows this pass already kept. A row is claimed when its trigger is
-- disabled (to drop it) or its settle delay was turned off (it starts at
-- once, active run or not: a later delivery would otherwise supersede it,
-- and older values must never start after newer ones), or when it is due
-- and no run the trigger started is still active (owner decision 3: the
-- collected run waits for that one; jobs_trigger_ref_idx finds them). Only
-- the pending row is locked, never the trigger.
SELECT sqlc.embed(p), sqlc.embed(t)
FROM trigger_pending_runs p
JOIN job_triggers t ON t.org_id = p.org_id AND t.id = p.trigger_id
WHERE NOT (p.trigger_id = ANY(sqlc.arg(skip)::uuid[]))
  AND (NOT t.enabled OR t.settle_seconds = 0
   OR (p.due_at <= sqlc.arg(now)::timestamptz
       AND NOT EXISTS (
         SELECT 1 FROM jobs j
         WHERE j.org_id = p.org_id AND j.trigger_ref = p.trigger_id AND j.trigger = 'webhook'
           AND j.status NOT IN ('succeeded', 'failed', 'cancelled', 'error'))))
ORDER BY p.due_at
LIMIT 1
FOR UPDATE OF p SKIP LOCKED;

-- name: LockTriggerPendingRun :one
-- A person's "run now": the trigger's pending run, waiting for a ticker
-- that is starting it (then there is none left).
SELECT sqlc.embed(p), sqlc.embed(t)
FROM trigger_pending_runs p
JOIN job_triggers t ON t.org_id = p.org_id AND t.id = p.trigger_id
WHERE p.org_id = sqlc.arg(org_id) AND p.trigger_id = sqlc.arg(trigger_id)
FOR UPDATE OF p;

-- name: DeleteTriggerPendingRun :exec
DELETE FROM trigger_pending_runs WHERE org_id = sqlc.arg(org_id) AND trigger_id = sqlc.arg(trigger_id) AND id = sqlc.arg(id);

-- name: DiscardTriggerPendingRun :one
-- A person's "discard": waits for a ticker that is starting the run (then
-- nothing is left to discard).
DELETE FROM trigger_pending_runs WHERE org_id = sqlc.arg(org_id) AND trigger_id = sqlc.arg(trigger_id)
RETURNING *;

-- name: SettleTriggerDeliveries :exec
-- What became of a window's deliveries: the run they collapsed into
-- (job_group_id), a failed start (outcome failed) or a drop, with note
-- appended to each reason.
UPDATE trigger_deliveries
SET outcome = sqlc.arg(outcome)::text,
    job_group_id = COALESCE(sqlc.narg(job_group_id)::uuid, job_group_id),
    reason = CASE WHEN sqlc.arg(note)::text = '' THEN reason
                  WHEN reason = '' THEN sqlc.arg(note)::text
                  ELSE reason || '; ' || sqlc.arg(note)::text END
WHERE org_id = sqlc.arg(org_id) AND trigger_id = sqlc.arg(trigger_id) AND pending_id = sqlc.arg(pending_id)::uuid;

-- name: SupersedeTriggerPendingRun :one
-- A delivery to a trigger whose settle delay is off takes the leftover
-- pending run out before it starts its own run; waits for a ticker that is
-- starting it (then the leftover started first and nothing is left).
DELETE FROM trigger_pending_runs WHERE org_id = sqlc.arg(org_id) AND trigger_id = sqlc.arg(trigger_id)
RETURNING id;

-- name: LaterTriggerDeliveries :one
-- The window's deliveries recorded after its run was (first) started: on a
-- replay they open a new window instead of being counted into that run.
SELECT count(*)::int AS n, COALESCE(min(at), sqlc.arg(after)::timestamptz)::timestamptz AS first_at,
       COALESCE(max(at), sqlc.arg(after)::timestamptz)::timestamptz AS last_at
FROM trigger_deliveries
WHERE org_id = sqlc.arg(org_id) AND trigger_id = sqlc.arg(trigger_id) AND pending_id = sqlc.arg(pending_id)::uuid
  AND at > sqlc.arg(after)::timestamptz;

-- name: ReopenTriggerPendingRun :exec
-- A replayed window keeps its row (and its newest collected values, which
-- belong to the latest delivery) as a new window of the later deliveries.
UPDATE trigger_pending_runs
SET id = sqlc.arg(new_id), first_at = sqlc.arg(first_at)::timestamptz, last_at = sqlc.arg(last_at)::timestamptz,
    deliveries = sqlc.arg(deliveries)::int, deadline_at = sqlc.arg(deadline)::timestamptz,
    due_at = LEAST(sqlc.arg(due)::timestamptz, sqlc.arg(deadline)::timestamptz)
WHERE org_id = sqlc.arg(org_id) AND trigger_id = sqlc.arg(trigger_id) AND id = sqlc.arg(id);

-- name: MoveLaterTriggerDeliveries :exec
UPDATE trigger_deliveries SET pending_id = sqlc.arg(new_id)::uuid
WHERE org_id = sqlc.arg(org_id) AND trigger_id = sqlc.arg(trigger_id) AND pending_id = sqlc.arg(pending_id)::uuid
  AND at > sqlc.arg(after)::timestamptz;
