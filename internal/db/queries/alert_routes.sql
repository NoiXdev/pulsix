-- Task 1, spec docs/superpowers/specs/2026-10-12-v0.4b-alert-routes-
-- design.md, "Data model" and "The follower": the statements a later
-- task's internal/alertroutes.SQLStore issues for route CRUD and their
-- three selection tables, plus the ledger reads and writes the per-org
-- follower tick drives. Modelled on internal/db/queries/alerts.sql's own
-- shape and comment style throughout.

-- name: CreateAlertRoute :one
-- revision is left off the column list, exactly like
-- CreateNotificationChannel leaves it off for notification_channels: the
-- migration's own DEFAULT 1 is what a brand new route starts at, and a
-- caller-supplied value could only ever disagree with that by mistake.
-- created_at/updated_at, by contrast, have no DEFAULT on this table (unlike
-- notification_channels' own now()), so the caller's own clock supplies
-- both — the same posture InsertAlert already takes for opened_at.
INSERT INTO alert_routes (id, org_id, name, enabled, scope, scope_group_id, scope_host_id, created_at, updated_at)
VALUES (@id, @org_id, @name, @enabled, @scope, sqlc.narg(scope_group_id), sqlc.narg(scope_host_id), @created_at, @updated_at)
RETURNING *;

-- name: GetAlertRoute :one
SELECT * FROM alert_routes WHERE org_id = @org_id AND id = @id;

-- name: ListAlertRoutes :many
SELECT * FROM alert_routes WHERE org_id = @org_id ORDER BY name;

-- name: UpdateAlertRoute :execrows
-- :execrows, not :one with RETURNING, the same choice UpdateCheckRule and
-- UpdateMaintenanceWindow already make for the same reason: zero rows
-- affected is the one signal the caller needs (a stale revision per
-- backlog 1.31's own If-Match, or an id that no longer exists), and the
-- Store is the layer that turns that into ErrConflict vs ErrNotFound by
-- re-reading the row, not this statement's job to distinguish.
UPDATE alert_routes SET
  name = @name, enabled = @enabled, scope = @scope,
  scope_group_id = sqlc.narg(scope_group_id), scope_host_id = sqlc.narg(scope_host_id),
  revision = revision + 1, updated_at = @updated_at
WHERE org_id = @org_id AND id = @id AND revision = @revision;

-- name: DeleteAlertRoute :execrows
-- The three selection tables and any alert_route_channels links cascade
-- off alert_routes' own id (migration 00052's composite FKs), so this is
-- the only statement a route deletion ever needs.
DELETE FROM alert_routes WHERE org_id = @org_id AND id = @id;

-- name: ReplaceRouteSeverities :exec
-- The whole selection in one round trip, the same shape
-- ReplaceRecoveryCodes/InsertRecoveryCode split across two statements for
-- recovery codes — collapsed here into one, because unlike a recovery
-- code's hash a severity carries no identity worth preserving across the
-- replace: the caller always has the complete new set in hand (the
-- editor's own multi-select), never a single value to add or remove.
--
-- The DELETE runs as a CTE ahead of the INSERT rather than as a separate
-- statement for the same reason SweepStaleAlerts' own WITH swept AS
-- (UPDATE ...) is one statement rather than two: one round trip, and
-- nothing else can observe the selection half-replaced between them.
-- "deleted" is never selected from — Postgres still executes a
-- data-modifying CTE even when nothing references its output, the same
-- as SweepStaleAlerts' own swept would if nothing read it either.
--
-- unnest(@severities::text[]) is InsertCheckItems' own one-statement-not-
-- one-per-row shape; ON CONFLICT DO NOTHING absorbs a value repeated
-- twice in the same call, the same defensive reason InsertCheckItems'
-- own comment gives. An empty array is a legal no-op that clears the
-- selection to nothing, which is exactly what "replace with zero
-- severities" means.
WITH deleted AS (
  DELETE FROM alert_route_severities WHERE org_id = @org_id AND route_id = @route_id
)
INSERT INTO alert_route_severities (org_id, route_id, severity)
SELECT @org_id, @route_id, unnest(@severities::text[])
ON CONFLICT (route_id, severity) DO NOTHING;

-- name: ReplaceRouteEvents :exec
-- ReplaceRouteSeverities' own shape, for the event-kind selection.
WITH deleted AS (
  DELETE FROM alert_route_events WHERE org_id = @org_id AND route_id = @route_id
)
INSERT INTO alert_route_events (org_id, route_id, kind)
SELECT @org_id, @route_id, unnest(@kinds::text[])
ON CONFLICT (route_id, kind) DO NOTHING;

-- name: ReplaceRouteChannels :exec
-- ReplaceRouteSeverities' own shape, for the channel selection. A route
-- with no channels is rejected by the Service before this is ever called
-- (the design's own "A route with no channels is rejected at creation"),
-- not by anything in this statement or the schema — an empty array is
-- still accepted here so that a channel deleted out from under an
-- otherwise-valid route (the design's "reported ineffective" case) can
-- still be replaced down to zero without a special case.
WITH deleted AS (
  DELETE FROM alert_route_channels WHERE org_id = @org_id AND route_id = @route_id
)
INSERT INTO alert_route_channels (org_id, route_id, channel_id)
SELECT @org_id, @route_id, unnest(@channel_ids::uuid[])
ON CONFLICT (route_id, channel_id) DO NOTHING;

-- name: ListRouteSelections :many
-- Every route's severities, event kinds and channels in org, in one
-- round trip — the same reasoning ListAlertRoutes' own list page needs:
-- hydrating N routes' three selection tables with three queries per row
-- would be the N+1 ListMaintenanceWindowsForHost's own comments warn
-- about elsewhere. route_id is in every row so the caller can group by
-- it without a fourth query; exactly one of severity/event_kind/channel_id
-- is non-NULL per row, by construction of which branch of the UNION
-- produced it.
--
-- Three dedicated nullable columns, not a kind+value discriminator pair —
-- an earlier version of this query had one, and sqlc's nullability
-- inference for a compound SELECT takes each output column's
-- nullability from its FIRST arm only, not an OR across every arm the
-- way Postgres itself actually evaluates the merged column: a single
-- shared "value" column came back as a plain non-nullable string even
-- though the 'channel' arm (listed last) provided it as NULL::text, and
-- scanning that arm's own row crashed on cannot scan NULL into *string
-- (caught by a throwaway test against the real container before this
-- shipped, not by sqlc or go vet).
--
-- Splitting into three columns fixes two of them for free: event_kind's
-- and channel_id's own real values sit in the second and third arms, so
-- their first arm is a bare NULL::<type> literal and sqlc infers them
-- nullable correctly. severity's real value sits in the FIRST arm
-- (alert_route_severities.severity, NOT NULL in the schema), which is
-- exactly the case the bug above hits again — a plain `s.severity`
-- there came back non-nullable despite the later arms needing NULL.
-- CASE and COALESCE wrappers were both tried and both still resolved to
-- the underlying column's own NOT NULL catalog type; a self LEFT JOIN
-- back onto the same row (sv, matched on the table's own primary key) is
-- what actually forces it, because join-side nullability is a real SQL
-- property sqlc does honour, independent of the catalog's own NOT NULL
-- on the joined column. sv always matches exactly the row s already
-- found — the join narrows nothing — it exists only to make this
-- column's Go type agree with the other two.
SELECT s.route_id, sv.severity, NULL::text AS event_kind, NULL::uuid AS channel_id
FROM alert_route_severities s
LEFT JOIN alert_route_severities sv ON sv.route_id = s.route_id AND sv.severity = s.severity
WHERE s.org_id = @org_id
UNION ALL
SELECT e.route_id, NULL::text AS severity, e.kind AS event_kind, NULL::uuid AS channel_id
FROM alert_route_events e WHERE e.org_id = @org_id
UNION ALL
SELECT c.route_id, NULL::text AS severity, NULL::text AS event_kind, c.channel_id
FROM alert_route_channels c WHERE c.org_id = @org_id
ORDER BY route_id;

-- name: UndecidedAlertEvents :many
-- The follower's forward pass (spec "The follower"): every event in org
-- with no row in alert_notify_state yet. The anti-join is the whole
-- idempotency mechanism migration 00052's own comment on alert_notify_state
-- explains — alert_events.id is a gen_random_uuid(), not monotonic, so a
-- watermark over `at` could skip an event whose transaction took its
-- timestamp early and committed late; NOT EXISTS has no such gap, because
-- it asks "was this exact id ever decided", not "has time moved past it".
--
-- Joined to alerts and hosts for the subject fields Match (a later task)
-- needs: the design's own "Resolution" section is explicit that severity
-- is "the one on the alerts row at the time the follower processes the
-- event, not a value frozen into the event" — so this reads a.severity
-- live rather than anything carried in ae.payload. Every joined column is
-- NOT NULL in its own table, so there is no UNION-style nullability
-- hazard here (see ListRouteSelections' own doc comment for what that
-- looks like when it does bite).
SELECT ae.id AS event_id, ae.alert_id, a.host_id, h.name AS host_name,
       a.check_key, a.item, a.metric, a.severity, ae.kind, ae.at
FROM alert_events ae
JOIN alerts a ON a.id = ae.alert_id
JOIN hosts h ON h.id = a.host_id
WHERE ae.org_id = @org_id
  AND NOT EXISTS (SELECT 1 FROM alert_notify_state s WHERE s.alert_event_id = ae.id)
ORDER BY ae.at, ae.id
LIMIT @lim;

-- name: SuppressedAlertEvents :many
-- The follower's catch-up pass: every event still ledgered 'suppressed'
-- whose alert is still open. alert_notify_state_pending_idx (migration
-- 00052) is a partial index on exactly (org_id) WHERE state =
-- 'suppressed', so the first join condition is cheap regardless of how
-- much decided history has piled up; closed_at IS NULL on alerts is what
-- distinguishes a row this pass should retry from one ExpireClosedSuppressed
-- below should retire instead — the same alert state, read for two
-- different purposes. Same join and the same live-severity reasoning as
-- UndecidedAlertEvents above.
SELECT ae.id AS event_id, ae.alert_id, a.host_id, h.name AS host_name,
       a.check_key, a.item, a.metric, a.severity, ae.kind, ae.at
FROM alert_notify_state s
JOIN alert_events ae ON ae.id = s.alert_event_id
JOIN alerts a ON a.id = ae.alert_id
JOIN hosts h ON h.id = a.host_id
WHERE s.org_id = @org_id AND s.state = 'suppressed' AND a.closed_at IS NULL
ORDER BY ae.at, ae.id
LIMIT @lim;

-- name: RecordAlertNotifyState :exec
-- The one write both passes above share (the design's own "This is
-- decision 4, in the same table rather than as a second mechanism"): the
-- forward pass inserts a fresh row, the catch-up pass turns an existing
-- 'suppressed' row into 'sent'. ON CONFLICT DO UPDATE is what lets one
-- statement serve both — alert_event_id is the table's own primary key,
-- so a conflict can only mean "this event was already decided once",
-- which is exactly the catch-up pass's own case. decided_at is the
-- caller's own clock, not now(), the same posture CloseAlert and
-- SweepStaleAlerts already take for their own @now/@closed_at.
INSERT INTO alert_notify_state (org_id, alert_event_id, state, decided_at)
VALUES (@org_id, @alert_event_id, @state, @decided_at)
ON CONFLICT (alert_event_id) DO UPDATE SET state = excluded.state, decided_at = excluded.decided_at;

-- name: ExpireClosedSuppressed :execrows
-- "Retiring suppressed rows" (spec "The follower"): a 'suppressed' row
-- whose alert has since closed can never be caught up, because
-- SuppressedAlertEvents above only ever offers the catch-up pass an open
-- alert. Moving it to 'expired' here is what keeps that pass's own
-- working set bounded by rows genuinely still pending rather than
-- growing with every suppressed event the organization's history ever
-- produced. decided_at is overwritten to @now — the moment of retirement,
-- not the original suppression decision, the same "this column means
-- when this state became true" reading RaiseAlertSeverity's own
-- peak_severity gives its column.
--
-- The terminal state is a parameter rather than the literal 'expired',
-- for the same reason RecordAlertNotifyState above takes one: the store
-- passes alertroutes.StateExpired, so that Go constant is the single
-- name for this state rather than a second copy of a string this
-- statement would otherwise own alone and nothing in Go would ever read.
UPDATE alert_notify_state s SET state = @state, decided_at = @now
FROM alert_events ae
JOIN alerts a ON a.id = ae.alert_id
WHERE s.alert_event_id = ae.id AND s.org_id = @org_id AND s.state = 'suppressed' AND a.closed_at IS NOT NULL;
