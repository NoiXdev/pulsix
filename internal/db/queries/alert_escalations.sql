-- Task 4, spec docs/superpowers/specs/2026-10-16-v0.4d-escalation-chains-
-- design.md, "Data" and "The sweep": the escalation ladder's own CRUD
-- (migration 00056's alert_route_steps/alert_route_step_channels) and the
-- anti-join that tells the follower's second pass which (alert, route)
-- chains still have a rung nobody has climbed to. Modelled on
-- internal/db/queries/alert_routes.sql's own shape and comment style
-- throughout, including its delete-then-insert split and its unnest-based
-- bulk channel insert.

-- name: DeleteRouteSteps :exec
-- The first half of ReplaceSteps' delete-then-insert (escalate_sql.go).
-- alert_route_step_channels cascades off (org_id, route_id, position)
-- (migration 00056's own FK), so deleting here also clears every step's
-- channel list. Two statements sent in sequence on the caller's own
-- transaction, never one WITH-based statement: DeleteRouteSeverities' own
-- comment in alert_routes.sql already explains why a data-modifying CTE's
-- DELETE and INSERT have no defined relative order against each other, and
-- v0.4b lost a row to exactly that mistake before it was caught.
DELETE FROM alert_route_steps WHERE org_id = @org_id AND route_id = @route_id;

-- name: InsertRouteStep :exec
-- The second half. One call per step, not an unnest-style bulk insert: a
-- route's ladder rarely runs past a handful of rungs, so there is none of
-- the arbitrary-length pressure InsertRouteChannels' own bulk shape exists
-- for. rotation_id is sqlc.narg — a step names either a rotation or a
-- fixed channel list (InsertRouteStepChannels, below), the same either/or
-- the route itself already offers.
INSERT INTO alert_route_steps (org_id, route_id, position, delay_seconds, rotation_id)
VALUES (@org_id, @route_id, @position, @delay_seconds, sqlc.narg(rotation_id));

-- name: InsertRouteStepChannels :exec
-- InsertRouteChannels' own unnest shape (alert_routes.sql), for one step's
-- fixed channel list. ON CONFLICT DO NOTHING absorbs a channel id repeated
-- twice in the same call, the same defensive reason that query's own
-- comment gives; an empty @channel_ids is a legal no-op for a step that
-- names a rotation instead, exactly like that query's own "empty array
-- clears the selection to nothing" reading.
INSERT INTO alert_route_step_channels (org_id, route_id, position, channel_id)
SELECT @org_id, @route_id, @position, unnest(@channel_ids::uuid[])
ON CONFLICT (route_id, position, channel_id) DO NOTHING;

-- name: ListEscalationStepsForRoutes :many
-- Every named route's own ladder, steps and channels together in one round
-- trip — ListRouteSelections' own reasoning (alert_routes.sql) for why a
-- LEFT JOIN beats one query per route per caller here too: ChainsNeedingAttention
-- below already knows exactly which routes it needs steps for, and reading
-- them one route at a time would be the same N+1 that query's own comment
-- warns against. One row per (step, channel) pair; a step with a rotation
-- instead of fixed channels, or no channels at all, surfaces once with
-- channel_id NULL — the store groups by (route_id, position) rather than
-- counting rows.
SELECT st.route_id, st.position, st.delay_seconds, st.rotation_id, ch.channel_id
FROM alert_route_steps st
LEFT JOIN alert_route_step_channels ch
  ON ch.org_id = st.org_id AND ch.route_id = st.route_id AND ch.position = st.position
WHERE st.org_id = @org_id AND st.route_id = ANY(@route_ids::uuid[])
ORDER BY st.route_id, st.position;

-- name: ClaimEscalationStep :execrows
-- The ledger write. ON CONFLICT DO NOTHING against alert_escalations' own
-- primary key (alert_id, route_id, position) is the whole idempotency
-- mechanism: two managers ticking at once, or one tick overlapping the
-- next, both attempt the identical row, and only one of them affects it.
-- :execrows, not :exec, is what lets the store turn "zero rows" into "this
-- step was already claimed" (false, no error) rather than mistaking a
-- harmless race for a failure that should abort the whole pass — the same
-- shape RecordAlertNotifyState's own row count already gives the ledger's
-- other table for a different kind of conflict.
INSERT INTO alert_escalations (org_id, alert_id, route_id, position, fired_at)
VALUES (@org_id, @alert_id, @route_id, @position, @fired_at)
ON CONFLICT (alert_id, route_id, position) DO NOTHING;

-- name: ChainsNeedingAttentionBase :many
-- The follower's second pass (spec "The sweep"): every open alert paired
-- with every enabled route whose ladder has at least one step nobody has
-- climbed to yet. ChainsNeedingAttention (escalate_sql.go) is the method
-- that also attaches each pair's own Steps, in a second round trip
-- (ListEscalationStepsForRoutes, above) rather than a join here — one more
-- reason this query stays a plain WHERE and not the place to repeat
-- match.go's own scope/severity logic (next paragraph).
--
-- Deliberately NOT repeated here: whether the alert's severity is actually
-- in the route's own severity selection, or the route's scope actually
-- covers the alert's host. match.go already owns that decision for the
-- forward pass, and the follower (a later task) re-runs it over every pair
-- this query returns before ever calling DueStep — this statement only
-- answers the structural question "does the ladder have an unfired rung",
-- the one fact no other part of the system already computes.
--
-- fired AS is a read-only CTE (MAX() over alert_escalations, grouped),
-- never a data-modifying one — the delete-then-insert warning on
-- DeleteRouteSteps above does not apply here, because nothing here writes.
-- Both CTEs key off (alert_id, route_id) or route_id alone, never off
-- alert_id by itself: a shared alert_id between two routes is exactly
-- where an over-eager WHERE would let one route's fired step answer for
-- the other's ladder (TestTwoRoutesOnOneAlertKeepSeparateChains), so every
-- join below carries both columns.
--
-- fired_up_to is -1 when the ledger has no row at all for this pair — not
-- even the base at position 0 — the same sentinel ChainState's own doc
-- comment gives. last_fired_at is NULL in that same case; the store reads
-- a NULL there as the zero time.Time.
--
-- last_event_at and snoozed_until are two of the three anchors spec
-- decision 5 names for LastUrgentAt — the alert's own most recent
-- severity_raised/ack_voided event, and its snoozed_until column. The
-- store (escalate_sql.go), not this statement, takes the later of the two:
-- GREATEST(x, y) does not widen to a nullable Go type the way COALESCE
-- does — tried first, it left sqlc unable to infer a concrete type for the
-- column at all, and casting to force one made it infer a non-nullable
-- time.Time despite both arguments being capable of NULL, which would
-- panic the first time an alert carried neither anchor.
--
-- last_event_at itself has the same problem one level down: a bare
-- correlated subquery's nullability is exactly what ListItemsForHost's own
-- comment (queries/items.sql) already found sqlc cannot prove — there is
-- no live database behind sqlc's static analysis (sqlc.yaml's schema is a
-- migrations directory, not a connection), so a MAX() that can
-- legitimately return no rows is indistinguishable from one that can't.
-- That query's own fix is reused here: COALESCE the subquery down to the
-- 'epoch' sentinel so the column comes back NOT NULL, and
-- has_urgent_event is the EXISTS flag that tells the store whether to
-- trust it or to use the Go zero time.Time instead — ChainState's own
-- "zero if none" promise, not epoch, which an untouched alert must never
-- read as having been "urgent" at 1970-01-01.
--
-- The third anchor, the end of a maintenance occurrence, is deliberately
-- left to the follower: occurrences are computed in Go from the
-- maintenance package's own windows, this query has no view onto them, and
-- folding a half-known anchor in here would make the SQL look complete
-- when it is not — the follower is what actually finishes decision 5's
-- anchor list.
--
-- "has an unfired step" (ladder.max_position > fired_up_to) is also what
-- keeps a route with no escalation steps at all out of this result
-- entirely: step 0 is never a row in alert_route_steps (migration 00056's
-- own comment), so a plain v0.4b route with no ladder joins to no ladder
-- row and is filtered out by the inner join, never reaching the predicate
-- at all.
WITH fired AS (
  SELECT alert_id, route_id, MAX(position) AS fired_up_to
  FROM alert_escalations
  WHERE org_id = @org_id
  GROUP BY alert_id, route_id
), ladder AS (
  SELECT route_id, MAX(position) AS max_position
  FROM alert_route_steps
  WHERE org_id = @org_id
  GROUP BY route_id
)
SELECT
  a.id AS alert_id,
  r.id AS route_id,
  a.severity AS severity,
  COALESCE(f.fired_up_to, -1)::int AS fired_up_to,
  lf.fired_at AS last_fired_at,
  COALESCE(
    (SELECT MAX(ae.at) FROM alert_events ae
      WHERE ae.org_id = a.org_id AND ae.alert_id = a.id AND ae.kind IN ('severity_raised', 'ack_voided')),
    'epoch'::timestamptz
  )::timestamptz AS last_event_at,
  EXISTS (
    SELECT 1 FROM alert_events ae
     WHERE ae.org_id = a.org_id AND ae.alert_id = a.id AND ae.kind IN ('severity_raised', 'ack_voided')
  ) AS has_urgent_event,
  a.snoozed_until AS snoozed_until
FROM alerts a
JOIN alert_routes r ON r.org_id = a.org_id AND r.enabled
JOIN ladder l ON l.route_id = r.id
LEFT JOIN fired f ON f.alert_id = a.id AND f.route_id = r.id
LEFT JOIN alert_escalations lf
  ON lf.org_id = a.org_id AND lf.alert_id = a.id AND lf.route_id = r.id AND lf.position = f.fired_up_to
WHERE a.org_id = @org_id
  AND a.closed_at IS NULL
  AND l.max_position > COALESCE(f.fired_up_to, -1)
ORDER BY a.opened_at, a.id, r.id
LIMIT @lim;
