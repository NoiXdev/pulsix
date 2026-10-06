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
INSERT INTO alerts (
  org_id, host_id, scope, check_key, item, metric,
  opened_at, severity, peak_severity, was_flapping
) VALUES (
  @org_id, @host_id, @scope, @check_key, @item, @metric,
  @opened_at, @severity, @severity,
  COALESCE(
    (SELECT flapping FROM check_states s
     WHERE @scope::text = 'tuple' AND s.host_id = @host_id AND s.check_key = @check_key
       AND s.item = @item AND s.metric = @metric),
    false)
)
RETURNING id;

-- name: GetAlertSeverity :one
-- Execute's own read before applying an ActionSeverity: whether the
-- severity actually rose — and therefore whether a severity_raised event
-- belongs in the history — can only be answered against the row's current
-- severity, which Action itself does not carry.
SELECT severity FROM alerts WHERE id = @id AND org_id = @org_id;

-- name: RaiseAlertSeverity :exec
-- The brief's own statement, verbatim. The nested CASE's literal ranking
-- (warn=1, unknown=2, crit=3) is deliberately NOT what ships here — see
-- store_sql.go's own comment on raiseAlertSeverityRank for why, and
-- TestPeakSeverityRankMatchesRulesStateRank in store_sql_test.go for the
-- test that would fail if the two ever drifted apart again. Everything
-- else — the three SET targets, @severity_rank as the caller-computed
-- rank of the new severity, @clears_snooze clearing snooze_until_change
-- only when asked — is exactly as given.
UPDATE alerts SET severity = @severity,
  peak_severity = CASE WHEN @severity_rank::int > (
    CASE peak_severity WHEN 'crit' THEN 3 WHEN 'warn' THEN 2 WHEN 'unknown' THEN 1 ELSE 0 END)
    THEN @severity ELSE peak_severity END,
  snooze_until_change = CASE WHEN @clears_snooze::boolean THEN false ELSE snooze_until_change END
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
-- states, because a host whose last check was removed never becomes due
-- again and a per-host sweep would miss exactly that case. scope = 'tuple'
-- excludes host-scope alerts, which carry no check_key/item/metric by
-- construction and would otherwise never match any check_states row
-- either way — excluding them here is what makes that exclusion a fact of
-- this statement rather than an accident of the NOT EXISTS always
-- happening to hold for them.
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
      WHERE s.host_id = a.host_id AND s.check_key = a.check_key
        AND s.item = a.item AND s.metric = a.metric)
  RETURNING id, org_id
)
INSERT INTO alert_events (org_id, alert_id, at, kind)
SELECT org_id, id, @now, 'closed' FROM swept;
