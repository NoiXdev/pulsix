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

-- name: InsertEscalatedEvent :exec
-- Spec decision 7: an escalation is visible on the alert itself, not only
-- in the channel it reached — "the alert page shows an untouched timeline
-- while phones are ringing" is exactly the gap this closes. 'escalated' is
-- the eighth alert_events kind (migration 00056's widened CHECK); payload
-- is the step's own position and the channels it reached, the one piece
-- of this event nothing else already has a column for — the same reason
-- notification_deliveries' own payload exists, not a new table. actor_id
-- is left NULL, like every other system-caused event InsertAlertEvent
-- itself writes (alerts.sql's own comment): nobody acted, the clock did.
INSERT INTO alert_events (org_id, alert_id, at, kind, payload)
VALUES (@org_id, @alert_id, @at, 'escalated', @payload);

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
-- fired_up_to can no longer actually BE -1 by the time a row leaves this
-- query (Critical 1, below, excludes a pair with no base row at all from
-- the WHERE clause itself), but the SELECT list still reads it through
-- COALESCE(f.fired_up_to, -1) rather than the bare column: sqlc's own
-- static analysis (no live database behind it, same limitation
-- ListItemsForHost's own comment, queries/items.sql, already found) cannot
-- see that the WHERE clause rules NULL out, and would otherwise infer a
-- nullable Go type for a column the store already treats as plain int32.
-- ChainState's own doc comment (escalate.go) still documents -1 as the
-- general "no base" reading for the type as a whole — it is simply
-- unreachable through this particular query now, not a meaning this query
-- ever disagreed with.
--
-- last_event_at and snoozed_until together cover FOUR of spec decision 5's
-- FIVE anchors for LastUrgentAt (the fifth, the end of a maintenance
-- occurrence, is the one this query still deliberately does not cover —
-- see below):
--
--   1. the alert's own most recent severity_raised/ack_voided/unsnoozed
--      event (last_event_at/has_urgent_event);
--   2. its snoozed_until column, for a bounded snooze that simply runs out
--      rather than being cleared by a person;
--
-- The owning route's own updated_at was folded in here too for one round
-- (review, Critical 3 of migration 00058's review) — reasoning that a
-- disabled-then-re-enabled route needed an anchor the same way a cleared
-- snooze does. The final review (Important 2) reversed that: the likeliest
-- moment anyone edits a route is mid-incident — adding a channel precisely
-- because nobody answered — and folding updated_at in pushed every open
-- alert that route matches further out the instant that edit landed. An
-- operator trying to widen the alarm was quietening it instead. A
-- re-enabled or freshly edited route's next rung is now due promptly if
-- its delay has already elapsed, the same answer decision 8 already gives
-- a severity re-entry, and for the same reason: firing a little early is
-- the safe direction, firing late is not, and there is no state here that
-- would let a "resume with a fresh delay" rule tell the two cases apart
-- anyway.
--
-- alerts.severity_changed_at (migration 00058) exists and is honest data —
-- RaiseAlertSeverity (queries/alerts.sql) moves it on every severity
-- write, rise or fall — but nothing outside the alerts store's own row
-- mapping (internal/alerts/store_sql.go) ever reads it, and it is
-- DELIBERATELY NOT an anchor here either, after a round of review
-- genuinely got this wrong: the first version of this fold folded it in,
-- on the theory that a falling severity needed SOME anchor to resume a
-- chain from, same as a mute ending. That reasoning does not survive
-- contact with a severity that moves WITHIN a route's own set —
-- crit down to warn on a {warn, crit} route never leaves the set at all,
-- so there is no "resuming" to anchor, yet severity_changed_at still
-- moves, and the next rung's delay restarts on every wobble. An alert that
-- oscillates inside the set would never finish climbing the ladder — a
-- STALL, not the burst decision 5 is worried about, and the dangerous
-- direction: a burst pages someone a few minutes early, a stall pages
-- nobody at all, which is the one failure this whole feature exists to
-- prevent. The spec itself was amended (decision 5) to stop lumping
-- severity re-entry in with the end of a mute: a mute means nobody heard
-- anything, a severity excursion means other routes may well have been
-- shouting the whole time, so firing the next rung promptly on re-entry is
-- the right behaviour on its own terms, not a gap to patch.
--
-- 'unsnoozed' belongs in the SAME event filter as severity_raised/
-- ack_voided, not a filter of its own: Unsnooze (internal/alerts/
-- store_sql.go) clears snoozed_until back to NULL and records only that
-- event, so it is the ONLY trace a human-cleared snooze leaves anywhere —
-- a query that read snoozed_until but not this event would see nothing for
-- that case and fall back to the stale base fired_at, firing the next rung
-- immediately instead of giving it its own full delay from the moment the
-- alert actually became urgent again (TestChainsNeedingAttentionAnchorsOnUnsnoozeNotJustBase
-- is the regression test for exactly this gap — caught in review, not
-- before).
--
-- The store (escalate_sql.go), not this statement, takes the GREATEST of
-- the two: GREATEST(x, y) does not widen to a nullable Go type the way
-- COALESCE does — tried first, it left sqlc unable to infer a concrete
-- type for the column at all, and casting to force one made it infer a
-- non-nullable time.Time despite the arguments being capable of NULL,
-- which would panic the first time an alert carried neither of the
-- nullable ones.
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
-- The FIFTH anchor, the end of a maintenance occurrence, is the one this
-- query still deliberately does NOT cover: occurrences are computed in Go
-- from the maintenance package's own windows, this query has no view onto
-- them, and folding a half-known anchor in here would make the SQL look
-- complete when it is not. The follower is what actually finishes decision
-- 5's anchor list by adding that one on top of what this query already
-- returns — unlike the 'unsnoozed' gap above, this omission is a
-- deliberate deferral, not a miss.
--
-- "has an unfired step" (ladder.max_position > fired_up_to) is also what
-- keeps a route with no escalation steps at all out of this result
-- entirely: step 0 is never a row in alert_route_steps (migration 00056's
-- own comment), so a plain v0.4b route with no ladder joins to no ladder
-- row and is filtered out by the inner join, never reaching the predicate
-- at all.
--
-- `f.fired_up_to IS NOT NULL` and the acknowledgement predicate below are
-- Critical 1 of the final review, and both close the same hole: this
-- query's own ORDER BY/LIMIT window used to include pairs that could
-- NEVER become due and never would on their own — a pair whose base row
-- was never written (DueStep, escalate.go, always refuses one: FiredUpTo
-- < 0), or an alert an operator has already acknowledged at its current
-- severity. Neither case changes by itself, so each was a STABLE,
-- PERMANENT occupant of the window rather than a candidate that simply
-- lost a race one tick and would clear out the next. An organization with
-- more such pairs than the per-tick batch size saw its NEWEST alerts never
-- reach the window at all, and therefore never escalate — silent, and the
-- exact failure this whole feature exists to prevent
-- (TestChainsNeedingAttentionDoesNotStarveADueChainBehindBaselessOnes,
-- escalate_sql_test.go, is the regression test). Filtering both out here,
-- rather than leaving them to be rejected once they reach the follower's
-- own dueStep, also cuts the per-tick cost: each ineligible candidate used
-- to still pay for a GetAlert and up to three WindowsForHost calls inside
-- the advisory-locked transaction before being rejected.
--
-- The acknowledgement predicate is the same coverage comparison ListAlerts'
-- own `acknowledged` filter already uses (queries/alerts.sql) — severity
-- ranked no higher than acknowledged_severity — not a bare
-- `acknowledged_at IS NOT NULL`: decision 2's whole point is that an
-- acknowledgement given at warn does not cover a later rise to crit, and a
-- chain must resume the instant that happens, not stay silenced by a stale
-- ack. A voided acknowledgement (RaiseAlertSeverity clears
-- acknowledged_severity back to NULL on exactly that) therefore reads as
-- uncovered again here too, with no second predicate needed.
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
      WHERE ae.org_id = a.org_id AND ae.alert_id = a.id AND ae.kind IN ('severity_raised', 'ack_voided', 'unsnoozed')),
    'epoch'::timestamptz
  )::timestamptz AS last_event_at,
  EXISTS (
    SELECT 1 FROM alert_events ae
     WHERE ae.org_id = a.org_id AND ae.alert_id = a.id AND ae.kind IN ('severity_raised', 'ack_voided', 'unsnoozed')
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
  AND f.fired_up_to IS NOT NULL
  AND NOT (
    a.acknowledged_severity IS NOT NULL
    AND (CASE a.severity WHEN 'crit' THEN 3 WHEN 'warn' THEN 2 WHEN 'unknown' THEN 1 ELSE 0 END)
        <= (CASE a.acknowledged_severity WHEN 'crit' THEN 3 WHEN 'warn' THEN 2 WHEN 'unknown' THEN 1 ELSE 0 END)
  )
  AND l.max_position > f.fired_up_to
ORDER BY a.opened_at, a.id, r.id
LIMIT @lim;
