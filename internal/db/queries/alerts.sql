-- Task 5, spec docs/superpowers/specs/2026-10-06-v0.4a-alerts-design.md,
-- "Lifecycle": these are the statements internal/alerts.SQLStore (Task 2's
-- Reconcile only decides, never writes) issues to carry out one
-- Reconcile verdict and to run the sweep. OpenForHost and the write
-- statements below all run on the caller's own transaction — the
-- evaluator's per-host claim, unscoped and carrying no pulsix.org_id
-- (internal/rules' own EvalSQLStore.conn is the precedent) — which is why
-- every one of them names org_id in its own WHERE clause rather than
-- leaning on row-level security to supply it. SweepStaleAlerts is the
-- opposite case: it runs in its own transaction, across every
-- organization at once, the same posture internal/rules' due-table scan
-- already uses for the same reason (cheap, instance-wide, no per-org loop
-- needed).

-- name: ListOpenAlertsForHost :many
-- Store.OpenForHost: every alert org/host has open right now — the
-- `open []Alert` Reconcile's own signature asks for. The partial unique
-- index (migration 00049) guarantees at most one row per subject, so the
-- ORDER BY exists only to make the result deterministic across runs, not
-- to break a tie.
SELECT * FROM alerts
WHERE org_id = @org_id AND host_id = @host_id AND closed_at IS NULL
ORDER BY scope, check_key, item, metric;

-- name: InsertAlert :one
-- ActionOpen: a brand new row, never an update of a closed one — the
-- partial unique index is what makes that legal once the old row has
-- closed_at set, and this INSERT is the only thing that ever creates an
-- alert, so a reopened subject always starts with every ack/snooze column
-- at its default (unacknowledged, unsnoozed). severity and peak_severity
-- both start at the opening severity (the Task 2 review's own ruling:
-- Action.Severity on an ActionOpen feeds both columns).
--
-- was_flapping is seeded from check_states.flapping for the same subject,
-- not carried on Action (the brief's own ruling: widening Action to carry
-- a Flapping field would touch every one of its four consumers for a
-- value the store already has in hand, inside the same transaction the
-- evaluator just wrote that row in). A scope='host' subject's check_key,
-- item and metric are all '' by construction (migration 00049's own
-- CHECK on alerts), but check_states carries no such CHECK on its own
-- columns — nothing stops a row shaped (check_key='', item='', metric='')
-- from existing there too. @scope::text = 'tuple' is therefore load-bearing,
-- not redundant with the empty-string columns: it is what makes a
-- scope='host' open consult check_states not at all, rather than merely
-- "usually" matching nothing because real tuples happen to have non-empty
-- keys (see TestExecuteOpenHostScopeNeverFlaps's own adversarial row).
--
-- severity_changed_at (v0.4d, migration 00058) starts at @opened_at, the
-- same reading that migration's own backfill gives every row that
-- predates the column: a brand new alert's severity has never changed, so
-- the moment it last did is the moment it opened.
INSERT INTO alerts (
  org_id, host_id, scope, check_key, item, metric,
  opened_at, severity, peak_severity, was_flapping, severity_changed_at
) VALUES (
  @org_id, @host_id, @scope, @check_key, @item, @metric,
  @opened_at, @severity, @severity,
  COALESCE(
    (SELECT flapping FROM check_states s
     WHERE @scope::text = 'tuple' AND s.org_id = @org_id AND s.host_id = @host_id
       AND s.check_key = @check_key AND s.item = @item AND s.metric = @metric),
    false),
  @opened_at
)
RETURNING id;

-- name: GetAlertSeverity :one
-- Execute's own read before applying an ActionSeverity: whether the
-- severity actually rose — and therefore whether a severity_raised event
-- belongs in the history — can only be answered against the row's current
-- severity, which Action itself does not carry.
SELECT severity FROM alerts WHERE id = @id AND org_id = @org_id;

-- name: RaiseAlertSeverity :exec
-- The brief's own statement, verbatim, plus v0.4d's own fourth SET target.
-- The nested CASE's literal ranking (warn=1, unknown=2, crit=3) is
-- deliberately NOT what ships here — see store_sql.go's own comment on
-- raiseAlertSeverityRank for why, and TestPeakSeverityRankMatchesRulesStateRank
-- in store_sql_test.go for the test that would fail if the two ever
-- drifted apart again. The three original SET targets — @severity_rank as
-- the caller-computed rank of the new severity, @clears_snooze clearing
-- snooze_until_change only when asked — are exactly as given.
--
-- acknowledged_severity is the v0.4d addition (backlog 4.36): raiseSeverity
-- (store_sql.go) already writes an ack_voided event exactly when
-- @voids_ack is true, but until now that event was the only record of the
-- void — acknowledged_severity itself was left standing. Since
-- "acknowledged" is never a stored flag but the live comparison
-- `severity <= acknowledged_severity`, a voided ack whose severity later
-- fell back below the old acknowledged_severity read as acknowledged
-- again, while its own timeline still showed the ack_voided event: the row
-- and the history disagreed. Escalation is what turns that disagreement
-- from cosmetic into material — a voided ack must keep a chain calling,
-- not go quiet because severity happened to dip. Clearing it in this same
-- UPDATE, gated on the identical @voids_ack the event write already uses,
-- is what makes the column and the event move together by construction
-- rather than by two call sites happening to agree today.
-- acknowledged_at/acknowledged_by are deliberately left alone: they record
-- that an acknowledgement happened at all, which stays true even once it
-- no longer covers.
--
-- severity_changed_at (v0.4d escalation-chains review, Critical 2) is the
-- fifth SET target, unconditional — raiseSeverity (store_sql.go) only ever
-- calls this statement when the severity column itself is about to move
-- (its own doc comment: "the only reason Reconcile emits this action at
-- all"), rise or fall alike, so every call here is a real severity change
-- and every one of them moves this column. A FALLING severity writes no
-- alert_events row at all — this is the only trace it leaves anywhere.
--
-- escalatePass (internal/alertroutes) does NOT read this column as an
-- anchor, despite an earlier round of review arguing it should (migration
-- 00058's own doc comment has the full reasoning): this column moves on
-- every severity write, including one that never leaves a route's own
-- severity set at all, so folding it into a chain's resume timing could
-- stall a chain oscillating inside that set forever. The column is kept
-- regardless — it is honest data about the alert, true on its own terms —
-- but nothing outside the alerts store's own row mapping
-- (internal/alerts/store_sql.go) reads it today.
UPDATE alerts SET severity = @severity,
  peak_severity = CASE WHEN @severity_rank::int > (
    CASE peak_severity WHEN 'crit' THEN 3 WHEN 'warn' THEN 2 WHEN 'unknown' THEN 1 ELSE 0 END)
    THEN @severity ELSE peak_severity END,
  snooze_until_change = CASE WHEN @clears_snooze::boolean THEN false ELSE snooze_until_change END,
  acknowledged_severity = CASE WHEN @voids_ack::boolean THEN NULL ELSE acknowledged_severity END,
  severity_changed_at = @at
WHERE id = @id AND org_id = @org_id;

-- name: SetAlertFlapping :exec
-- ActionFlapped: latches was_flapping once a tuple is seen flapping. There
-- is no "flapped" kind in alert_events' own CHECK (migration 00049) — this
-- is the one action kind Execute applies with no event to match.
UPDATE alerts SET was_flapping = true WHERE id = @id AND org_id = @org_id;

-- name: CloseAlert :exec
-- ActionClose, both reasons (resolved from Reconcile, stale from the
-- sweep's own SweepStaleAlerts below — this statement is Execute's own
-- path, the sweep writes its UPDATE directly since it closes many rows at
-- once with no per-row event to pair it with).
UPDATE alerts SET closed_at = @closed_at, closed_reason = @reason
WHERE id = @id AND org_id = @org_id;

-- name: InsertAlertEvent :exec
-- Every event Execute writes is system-caused (opened, severity_raised,
-- ack_voided, closed) — actor_id is left NULL, the acknowledge/snooze
-- events a human causes belong to Task 6's Service, not here. payload is
-- always the default '{}'::jsonb: nothing downstream reads one yet.
INSERT INTO alert_events (org_id, alert_id, at, kind)
VALUES (@org_id, @alert_id, @at, @kind);

-- name: SweepStaleAlerts :execrows
-- The design doc's own UPDATE, driven by open alerts rather than by
-- states. scope = 'tuple' excludes host-scope alerts, which carry no
-- check_key/item/metric by construction and would otherwise never match
-- any check_states row either way — excluding them here is what makes
-- that exclusion a fact of this statement rather than an accident of the
-- NOT EXISTS always happening to hold for them.
--
-- What this statement does and does NOT catch (v0.4a final review,
-- Critical): "no check_states row exists" is true of exactly one of
-- decision 5's four paths, the item dropped by an operator's decision —
-- rules.Result.Dropped is the only thing outside ON DELETE CASCADE that
-- deletes such a row. A check taken off a host or disabled leaves the row
-- standing on purpose (rules.Result.Unmeasured's own doc comment), and is
-- closed inside the host's own evaluation transaction instead, by
-- alerts.Reconcile's staleActions. A deleted rule leaves the tuple judged
-- ok, which closes `resolved` — the honest word for a tuple that is
-- genuinely healthy, which is why the design doc's decision 5 was
-- corrected rather than the code. A decommissioned host takes its alerts
-- with it through the composite FK. This sweep therefore remains the
-- central net decision 5 asks for — it is what a FIFTH deletion site
-- added next year would be caught by without knowing alerts exist — but
-- it is no longer the only mechanism, because it cannot be.
--
-- An earlier version of this comment justified the sweep by "a host whose
-- last check was removed never becomes due again". That is false:
-- rules.nextDue returns a 15-second heartbeat for a host with nothing
-- pending, and ClaimDueEvalCursors (rules.sql) takes any cursor whose
-- next_due_at has passed, so such a host keeps being evaluated until it
-- is deleted. The sweep is still right to be instance-wide rather than
-- per host — it is cheap, needs no org loop, and does not depend on any
-- host being looked at — but not for that reason.
--
-- The design doc's own text stopped at the UPDATE; the CTE and INSERT
-- below are a ruling made after review, not part of the brief: the design
-- predates alert_events, and without this every alert the sweep closes
-- would read as an episode that opened and never ended on Task 10's own
-- timeline (closed_at would be set with no matching 'closed' event).
-- RETURNING feeds straight into the event insert so the two can never
-- drift apart — a swept row and its event are the same statement, not two
-- that could partially fail — and :execrows now counts the INSERT, since
-- it runs one row per swept alert and "how many did the sweep swept"
-- means the same thing either way.
WITH swept AS (
  UPDATE alerts a SET closed_at = @now, closed_reason = 'stale'
  WHERE a.closed_at IS NULL AND a.scope = 'tuple'
    AND NOT EXISTS (
      SELECT 1 FROM check_states s
      WHERE s.org_id = a.org_id AND s.host_id = a.host_id AND s.check_key = a.check_key
        AND s.item = a.item AND s.metric = a.metric)
  RETURNING id, org_id
)
INSERT INTO alert_events (org_id, alert_id, at, kind)
SELECT org_id, id, @now, 'closed' FROM swept;

-- Task 6, spec docs/superpowers/specs/2026-10-06-v0.4a-alerts-design.md,
-- "Suppression": the read and operator-action statements alerts.Service
-- issues directly on the bare pool (never the evaluator's tx above) —
-- List, Get, Acknowledge, Snooze and their reversals. Unlike the
-- lifecycle statements above, these run from the API's own request
-- handling, so RLS already does its job; org_id is still named explicitly
-- in every WHERE clause anyway, the convention every other store in this
-- schema keeps regardless (see e.g. maintenance.sql's own GetMaintenanceWindow).

-- name: GetAlert :one
-- Joined to hosts on the composite (org_id, host_id) — never host_id
-- alone — the same guard every other reference in this schema gives a
-- cross-table lookup (GetMaintenanceWindow's own comment above), even
-- though alerts.host_id is already a single table's primary key and could
-- not actually resolve to a different organization's row: the lifecycle's
-- own transaction (store_sql.go's package doc) is unscoped and bypasses
-- RLS by design, so every join here names org_id on principle, not only
-- where a collision could occur. LEFT JOIN, not INNER: the composite FK
-- (migration 00049) with ON DELETE CASCADE means an alert cannot outlive
-- its host, so host_name is never actually missing — the LEFT JOIN plus
-- COALESCE is defensive, not load-bearing, the same posture
-- GetMaintenanceWindow already takes for a target that in its case
-- genuinely can be absent.
SELECT a.*, COALESCE(h.name, '')::text AS host_name
FROM alerts a
LEFT JOIN hosts h ON h.org_id = a.org_id AND h.id = a.host_id
WHERE a.org_id = @org_id AND a.id = @id;

-- name: ListAlerts :many
-- Service.List's own Filter, forwarded almost verbatim: each optional
-- field matches everything when NULL, the same "param IS NULL OR
-- <condition>" idiom hosts.sql's own ListHosts and rules.sql's own
-- ListRules already use. Muted has no column to filter on — Service.List
-- applies it in Go, after Mute has run. host_name is joined the same way
-- GetAlert's own comment explains — composite (org_id, host_id), LEFT
-- JOIN defensively though the FK makes a miss impossible in practice.
--
-- Paginated on (opened_at, id), the keyset the ORDER BY already provided
-- (v0.4a final review, Important). The alerts table has no retention
-- policy by design — an alert must outlive the 90 days of
-- check_state_changes — so the set this reads only ever grows, and the
-- feature's own front page defaults to no state filter at all. Unbounded,
-- it read every alert the organization had ever opened on every page
-- load, and Service.List then ran Mute over all of them. Hosts have been
-- cursor-paginated here since v0.1; this follows the same shape.
--
-- `acknowledged` filters by COVERAGE, not by "has acknowledged_at ever
-- been set" (review finding, Important, corrected from an earlier version
-- of this query that used exactly that weaker test): design decision 2 —
-- an acknowledgement given at warn does not cover a later rise to crit —
-- is the whole reason alerts.go's own isAckCovering (and this page's own
-- "Unacknowledged — overtaken" label) exists, and a presence-only filter
-- put an overtaken alert in Acknowledged while the row right next to it
-- labelled the same alert Unacknowledged. An operator filtering for
-- unhandled work needs exactly the covering comparison here, not merely
-- "was this ever touched". The two CASE expressions are the same literal
-- severity ranking (crit=3, warn=2, unknown=1, else 0) RaiseAlertSeverity's
-- own peak_severity column already uses — kept in sync by hand for the
-- same reason that query's own comment gives: SQL cannot call
-- rules.StateRank. acknowledged_severity reads NULL both for a row that
-- has never been acknowledged and for one whose acknowledgement was later
-- voided (migration 00057 relaxed the table's own CHECK to allow exactly
-- that combination — acknowledged_at can stand with acknowledged_severity
-- NULL now, where it once could not) — either way this ELSE 0 reads 0,
-- below every real severity, so neither case can ever read as covered even
-- without the explicit acknowledged_at IS NOT NULL alongside it.
SELECT a.*, COALESCE(h.name, '')::text AS host_name
FROM alerts a
LEFT JOIN hosts h ON h.org_id = a.org_id AND h.id = a.host_id
WHERE a.org_id = @org_id
  AND (sqlc.narg(open)::boolean IS NULL
       OR (sqlc.narg(open)::boolean AND a.closed_at IS NULL)
       OR (NOT sqlc.narg(open)::boolean AND a.closed_at IS NOT NULL))
  AND (sqlc.narg(severity)::text IS NULL OR a.severity = sqlc.narg(severity)::text)
  AND (sqlc.narg(host_id)::uuid IS NULL OR a.host_id = sqlc.narg(host_id)::uuid)
  AND (sqlc.narg(scope)::text IS NULL OR a.scope = sqlc.narg(scope)::text)
  AND (sqlc.narg(acknowledged)::boolean IS NULL
       OR (sqlc.narg(acknowledged)::boolean AND a.acknowledged_at IS NOT NULL
           AND (CASE a.severity WHEN 'crit' THEN 3 WHEN 'warn' THEN 2 WHEN 'unknown' THEN 1 ELSE 0 END)
               <= (CASE a.acknowledged_severity WHEN 'crit' THEN 3 WHEN 'warn' THEN 2 WHEN 'unknown' THEN 1 ELSE 0 END))
       OR (NOT sqlc.narg(acknowledged)::boolean
           AND (a.acknowledged_at IS NULL
                OR (CASE a.severity WHEN 'crit' THEN 3 WHEN 'warn' THEN 2 WHEN 'unknown' THEN 1 ELSE 0 END)
                    > (CASE a.acknowledged_severity WHEN 'crit' THEN 3 WHEN 'warn' THEN 2 WHEN 'unknown' THEN 1 ELSE 0 END))))
  AND (
    sqlc.narg(cursor_opened_at)::timestamptz IS NULL
    OR (a.opened_at, a.id) < (sqlc.narg(cursor_opened_at)::timestamptz, sqlc.narg(cursor_id)::uuid)
  )
-- id breaks a tie between two alerts one Execute call opened at the
-- identical opened_at (review finding, Minor): without it, two runs of
-- this same query could disagree on which of the two comes first. It is
-- DESC, matching opened_at, because it is now also half of the keyset the
-- cursor condition above resumes from — a row comparison is only a valid
-- keyset when every column of it runs the same way as the ORDER BY, which
-- is exactly the shape ListAuditEventsForOrg already uses.
ORDER BY a.opened_at DESC, a.id DESC
LIMIT sqlc.arg(row_limit);

-- name: ListAlertEvents :many
-- Get's own timeline, the event half (timeline.go's mergeTimeline merges
-- this with ListStateChangesForTuple below). Ordered by (at, id): id only
-- ever breaks a tie between two events this package itself wrote at the
-- very same instant (raiseSeverity's own severity_raised + ack_voided
-- pair), which carries no further meaning worth encoding.
SELECT * FROM alert_events WHERE org_id = @org_id AND alert_id = @alert_id ORDER BY at, id;

-- name: ListStateChangesForTuple :many
-- Get's own timeline, the state-change half: the tuple's own
-- check_state_changes rows inside the alert's lifetime, [from_at, to_at]
-- inclusive at both ends (the brief's own "[opened_at, closed_at]").
-- Service.Get never calls this for a scope='host' alert — see its own
-- doc comment for why. check_state_changes carries no unique id (it is a
-- hypertable with no single-row PK, 00046's own comment), so `at` alone
-- leaves two rows sharing an instant to whatever order Postgres felt like
-- that run, undermining mergeTimeline's own determinism (timeline.go);
-- from_state/to_state break that tie (review correction: not ctid — a
-- system column stable only within one execution, not across a VACUUM or
-- an update, and not worth a project-wide strict_order_by exception for a
-- defensive case this guards against: the evaluator writes at most one
-- change per tuple per tick, so two rows tying on `at` for the very same
-- tuple are already close to impossible). A total order is not needed
-- either — two rows identical in at/from_state/to_state can still differ
-- in value/label, but nothing distinguishes them as a reader, so there is
-- no order left worth specifying.
SELECT * FROM check_state_changes
WHERE org_id = @org_id AND host_id = @host_id AND check_key = @check_key AND item = @item AND metric = @metric
  AND at >= @from_at AND at <= @to_at
ORDER BY at, from_state, to_state;

-- name: ListMaintenanceWindowsForHost :many
-- Store.WindowsForHost's own candidate set: every maintenance window
-- whose target could cover host_id — the organization, the host itself,
-- or an inventory group host_id is a member of or descends into through
-- any chain of parent_id (the recursive CTE walks from host_id's own
-- direct memberships up to the root, so a window on an ancestor group
-- still matches). A recurring window's own cron cannot be evaluated in
-- SQL, so every row with one is returned regardless of time —
-- occurrenceAt (store_sql.go) is what decides whether it is active right
-- now, the same split ActiveAt/Occurrences already draw in
-- internal/maintenance. A one-off window, by contrast, *is* filtered here
-- by @now: review finding (Important) — List ran this query once per
-- alert in a loop, and an unbounded scan of every one-off window the
-- organization has ever created (most of them long expired) was pure
-- waste on the feature's main read path; occurrenceAt's own one-off
-- branch would have excluded them anyway, so filtering here changes no
-- result, only how much gets shipped out of Postgres to do it. Monitoring-
-- group targets are deliberately not resolved here, the same gap
-- internal/maintenance.Service's own HostsInMaintenance leaves for the
-- same target kind today.
--
-- An inventory-group target covers EVERY member here, not only the
-- managed ones HostsInMaintenance counts (backlog 4.30, settled rather
-- than left undocumented): an unmanaged host still reports checks and
-- still raises alerts, so a window covering its group must mute it, while
-- a scheduled Ansible run that could never touch it has no reason to be
-- guarded on its account. The two answers differ because the two
-- questions do; see HostsInMaintenance's own doc comment for the other
-- half of this note. Both reasons are returned — a 'silence' window is
-- exactly what this read exists for, and alerts.Mute labels the two
-- apart; that is the opposite of HostsInMaintenance, which filters
-- silence out.
WITH RECURSIVE ancestors AS (
  SELECT g.id, g.parent_id FROM inventory_groups g
    JOIN inventory_memberships m ON m.group_id = g.id
    WHERE m.org_id = @org_id AND m.host_id = @host_id
  UNION
  SELECT p.id, p.parent_id FROM inventory_groups p
    JOIN ancestors a ON p.id = a.parent_id
)
SELECT w.* FROM maintenance_windows w
WHERE w.org_id = @org_id
  AND (w.target_kind = 'org'
       OR (w.target_kind = 'host' AND w.host_id = @host_id)
       OR (w.target_kind = 'inventory_group' AND w.group_id IN (SELECT id FROM ancestors)))
  AND (w.cron IS NOT NULL OR (w.starts_at <= @now::timestamptz AND w.ends_at > @now::timestamptz));

-- name: AcknowledgeAlert :execrows
-- Service.Acknowledge's own write: acknowledged_severity is set to
-- `severity` — the row's own column, unqualified, which standard SQL
-- resolves to the pre-update value of the row being written, never a
-- caller-supplied parameter (review finding, Important). Service's own
-- GetAlert reads the alert once to decide ErrNotFound/ErrInvalidInput
-- (closed) before calling this, and that round trip is stale the instant
-- it returns; Reconcile's own advance can raise the severity in the gap
-- between it and this statement committing. Reading it fresh, in the same
-- statement, under the row's own lock, is what makes "acknowledge the
-- severity as of right now" true rather than "as of whenever Service
-- happened to look" — anything else reopens exactly the silent-ack gap
-- decision 2 exists to close. closed_at IS NULL in the WHERE is Service's
-- same validation, re-asserted here: zero rows affected means the alert
-- closed in the gap between Service's check and this statement, which
-- Store.Acknowledge reports as ErrInvalidInput, not silently succeeding.
UPDATE alerts SET acknowledged_at = @at, acknowledged_by = @actor, acknowledged_severity = severity, ack_note = @note
WHERE org_id = @org_id AND id = @id AND closed_at IS NULL;

-- name: UnacknowledgeAlert :execrows
UPDATE alerts SET acknowledged_at = NULL, acknowledged_by = NULL, acknowledged_severity = NULL, ack_note = ''
WHERE org_id = @org_id AND id = @id;

-- name: SnoozeAlert :execrows
-- Service.Snooze's own write, after it has already rejected the
-- combinations migration 00049 leaves the table itself unable to reject
-- (both until and until_change, or neither, or an until in the past) —
-- this statement trusts its caller the way RaiseAlertSeverity already
-- trusts Execute's. closed_at IS NULL (review finding, Minor): without
-- it, a closed alert could be snoozed and List would then report it
-- Muted — a mute with nothing left to mute.
UPDATE alerts SET snoozed_until = @until, snooze_until_change = @until_change
WHERE org_id = @org_id AND id = @id AND closed_at IS NULL;

-- name: UnsnoozeAlert :execrows
UPDATE alerts SET snoozed_until = NULL, snooze_until_change = false
WHERE org_id = @org_id AND id = @id;

-- name: InsertAlertActorEvent :exec
-- The human-caused counterpart to InsertAlertEvent above: acknowledged,
-- snoozed and unsnoozed all name their actor (migration 00049's own
-- ON DELETE SET NULL on alert_events.actor_id is exactly for this column).
-- There is no 'unacknowledged' kind in the CHECK — Store.Unacknowledge
-- writes no event at all, so this is never called for it.
INSERT INTO alert_events (org_id, alert_id, at, kind, actor_id)
VALUES (@org_id, @alert_id, @at, @kind, @actor_id);
