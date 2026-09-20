-- name: CreateNotificationChannel :one
INSERT INTO notification_channels (id, org_id, kind, name, config, body_json, enabled, created_by)
VALUES (@id, @org_id, @kind, @name, @config, @body_json, @enabled, sqlc.narg(created_by))
RETURNING *;

-- name: GetNotificationChannel :one
SELECT * FROM notification_channels WHERE org_id = @org_id AND id = @id;

-- name: ListNotificationChannels :many
SELECT * FROM notification_channels WHERE org_id = @org_id ORDER BY name;

-- name: UpdateNotificationChannel :one
-- No row back means a stale revision or an unknown channel.
UPDATE notification_channels SET name = @name, config = @config, body_json = @body_json, enabled = @enabled,
  revision = revision + 1, updated_at = now()
WHERE org_id = @org_id AND id = @id AND revision = @revision
RETURNING *;

-- name: DeleteNotificationChannel :execrows
DELETE FROM notification_channels WHERE org_id = @org_id AND id = @id;

-- name: InsertNotificationDelivery :execrows
-- ON CONFLICT: the (org, event, channel) idempotency key — a retried
-- source (a finish retry, a re-decided schedule row) enqueues nothing new.
INSERT INTO notification_deliveries (id, org_id, channel_id, event_key, event_kind, payload, status, attempts,
  next_attempt_at, last_error, created_at, sent_at)
VALUES (@id, @org_id, @channel_id, @event_key, @event_kind, @payload, @status, @attempts,
  @next_attempt_at, @last_error, @created_at, sqlc.narg(sent_at))
ON CONFLICT (org_id, event_key, channel_id) DO NOTHING;

-- name: ListNotificationDeliveries :many
SELECT * FROM notification_deliveries WHERE org_id = @org_id AND channel_id = @channel_id
ORDER BY created_at DESC, id DESC LIMIT @lim;

-- name: ClaimNotificationDeliveries :many
-- The dispatcher's claim: runs on the raw pool without pulsix.org_id
-- (bypass policy), across every organization. SKIP LOCKED lets several
-- managers dispatch without a leader; the lease (locked_until) and the
-- attempts increment are written in the same statement, so a manager that
-- dies mid-send leaves a row that becomes due again once the lease ends.
UPDATE notification_deliveries AS d
SET locked_until = @lease_until::timestamptz, attempts = d.attempts + 1
FROM notification_channels AS c
WHERE c.org_id = d.org_id AND c.id = d.channel_id
  AND d.id IN (
    SELECT p.id FROM notification_deliveries AS p
    WHERE p.status = 'pending' AND p.next_attempt_at <= @now::timestamptz
      AND (p.locked_until IS NULL OR p.locked_until <= @now::timestamptz)
    ORDER BY p.next_attempt_at
    LIMIT @lim
    FOR UPDATE SKIP LOCKED
  )
RETURNING d.id, d.org_id, d.channel_id, d.event_key, d.event_kind, d.payload, d.attempts, d.created_at,
  c.kind AS channel_kind, c.name AS channel_name, c.config AS channel_config, c.enabled AS channel_enabled;

-- name: CompleteNotificationDelivery :execrows
-- attempts fences a late completion: a manager whose lease ran out and
-- whose row another manager re-claimed changes nothing.
UPDATE notification_deliveries SET status = 'sent', sent_at = @at, locked_until = NULL, last_error = ''
WHERE org_id = @org_id AND id = @id AND attempts = @attempts AND status = 'pending';

-- name: RetryNotificationDelivery :execrows
UPDATE notification_deliveries SET next_attempt_at = @next_attempt_at, locked_until = NULL, last_error = @last_error
WHERE org_id = @org_id AND id = @id AND attempts = @attempts AND status = 'pending';

-- name: FailNotificationDelivery :execrows
UPDATE notification_deliveries SET status = 'failed', locked_until = NULL, last_error = @last_error
WHERE org_id = @org_id AND id = @id AND attempts = @attempts AND status = 'pending';

-- name: TrimNotificationDeliveries :exec
-- Keeps the newest 500 finished rows of a channel; pending ones are never
-- trimmed.
DELETE FROM notification_deliveries AS d
WHERE d.org_id = @org_id AND d.channel_id = @channel_id AND d.status <> 'pending'
  AND d.id NOT IN (
    SELECT k.id FROM notification_deliveries AS k
    WHERE k.org_id = @org_id AND k.channel_id = @channel_id AND k.status <> 'pending'
    ORDER BY k.created_at DESC, k.id DESC
    LIMIT @keep
  );

-- name: ListScheduleNotifications :many
SELECT n.channel_id, n.events, c.name AS channel_name, c.kind AS channel_kind
FROM schedule_notifications AS n
JOIN notification_channels AS c ON c.org_id = n.org_id AND c.id = n.channel_id
WHERE n.org_id = @org_id AND n.schedule_id = @schedule_id
ORDER BY c.name;

-- name: DeleteScheduleNotifications :exec
DELETE FROM schedule_notifications WHERE org_id = @org_id AND schedule_id = @schedule_id;

-- name: InsertScheduleNotification :exec
INSERT INTO schedule_notifications (org_id, schedule_id, channel_id, events)
VALUES (@org_id, @schedule_id, @channel_id, @events);

-- name: NotifyRuleChannels :many
-- The enabled channels a schedule's rules route any of the categories to.
SELECT n.channel_id
FROM schedule_notifications AS n
JOIN notification_channels AS c ON c.org_id = n.org_id AND c.id = n.channel_id
WHERE n.org_id = @org_id AND n.schedule_id = @schedule_id AND c.enabled
  AND n.events && @categories::text[]
ORDER BY n.channel_id;

-- name: NotifyScheduleContext :one
-- The names a message carries, read when the event is enqueued.
SELECT o.slug AS org_slug, o.name AS org_name, s.name AS schedule_name, s.template_id, t.name AS template_name
FROM schedules AS s
JOIN organizations AS o ON o.id = s.org_id
JOIN job_templates AS t ON t.org_id = s.org_id AND t.id = s.template_id
WHERE s.org_id = @org_id AND s.id = @schedule_id;
