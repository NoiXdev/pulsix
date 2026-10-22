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
--
-- attempts < 20 is the backstop for that re-claim path. The real cap lives
-- in Go (notify.MaxAttempts), but it is consulted only when the send
-- returned an error, so a row whose outcome write never landed at all -- a
-- manager that died after the receiver accepted the message, or whose
-- outcome write panicked -- used to be re-claimed forever, re-sending the
-- same message every two minutes. The number is deliberately far above
-- MaxAttempts: this only bounds a loop no Go-side decision ever reaches.
UPDATE notification_deliveries AS d
SET locked_until = @lease_until::timestamptz, attempts = d.attempts + 1
FROM notification_channels AS c
WHERE c.org_id = d.org_id AND c.id = d.channel_id
  AND d.id IN (
    SELECT p.id FROM notification_deliveries AS p
    WHERE p.status = 'pending' AND p.next_attempt_at <= @now::timestamptz
      AND (p.locked_until IS NULL OR p.locked_until <= @now::timestamptz)
      AND p.attempts < 20
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

-- name: ListOrgScheduleNotifyEvents :many
-- Every (schedule, events) row of the organization, for the schedules list
-- (GUI "Notify" column, backlog 2.73): one query for the whole org rather
-- than one per schedule row, same reasoning 2.94 gives for keeping a list
-- endpoint's per-row work to a single round trip. The caller unions each
-- schedule's rows into its set of distinct categories — a schedule can
-- have several channels, each with its own events, which is why this is
-- :many and not a pre-aggregated array.
SELECT schedule_id, events FROM schedule_notifications WHERE org_id = @org_id;

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

-- name: NotifyOrgContext :one
SELECT slug, name FROM organizations WHERE id = @org_id;

-- name: GetNotificationDigest :one
SELECT * FROM notification_digests WHERE org_id = @org_id;

-- name: ListNotificationDigestChannels :many
SELECT d.channel_id, c.name AS channel_name, c.kind AS channel_kind
FROM notification_digest_channels AS d
JOIN notification_channels AS c ON c.org_id = d.org_id AND c.id = d.channel_id
WHERE d.org_id = @org_id
ORDER BY c.name;

-- name: UpsertNotificationDigest :one
INSERT INTO notification_digests (org_id, enabled, weekday, at_time, timezone, next_run_at, updated_at)
VALUES (@org_id, @enabled, @weekday, @at_time, @timezone, sqlc.narg(next_run_at), now())
ON CONFLICT (org_id) DO UPDATE SET enabled = EXCLUDED.enabled, weekday = EXCLUDED.weekday,
  at_time = EXCLUDED.at_time, timezone = EXCLUDED.timezone, next_run_at = EXCLUDED.next_run_at, updated_at = now()
RETURNING *;

-- name: DeleteNotificationDigestChannels :exec
DELETE FROM notification_digest_channels WHERE org_id = @org_id;

-- name: InsertNotificationDigestChannel :exec
INSERT INTO notification_digest_channels (org_id, channel_id) VALUES (@org_id, @channel_id);

-- name: LockDueDigests :many
-- The digest ticker's due scan: raw pool, bypass policy, every org, SKIP
-- LOCKED like the schedule ticker.
SELECT * FROM notification_digests
WHERE enabled AND next_run_at <= @now::timestamptz
ORDER BY next_run_at
LIMIT @lim
FOR NO KEY UPDATE SKIP LOCKED;

-- name: DigestChannelsForSend :many
SELECT d.channel_id
FROM notification_digest_channels AS d
JOIN notification_channels AS c ON c.org_id = d.org_id AND c.id = d.channel_id
WHERE d.org_id = @org_id AND c.enabled
ORDER BY d.channel_id;

-- name: AdvanceNotificationDigest :exec
UPDATE notification_digests SET next_run_at = @next_run_at, last_period = @last_period
WHERE org_id = @org_id;
