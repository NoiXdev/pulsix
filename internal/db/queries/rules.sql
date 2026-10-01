-- name: CreateRule :exec
-- created_at/updated_at are supplied by the caller (internal/rules.Service's
-- injected clock), not DEFAULT now(): Store.Create returns only an error,
-- never a round-tripped row (internal/rules.Store's own doc comment), so
-- the timestamps this statement writes are the only ones the service ever
-- sees again.
INSERT INTO check_rules (
  id, org_id, name, enabled, origin, scope, group_id, host_id, item,
  check_key, metric, severity, mode, ast, clear_ast, source, for_duration,
  created_at, updated_at
) VALUES (
  sqlc.arg(id), sqlc.arg(org_id), sqlc.arg(name), sqlc.arg(enabled), sqlc.arg(origin),
  sqlc.arg(scope), sqlc.arg(group_id), sqlc.arg(host_id), sqlc.arg(item),
  sqlc.arg(check_key), sqlc.arg(metric), sqlc.arg(severity), sqlc.arg(mode),
  sqlc.arg(ast)::jsonb, sqlc.arg(clear_ast)::jsonb, sqlc.arg(source), sqlc.arg(for_duration),
  sqlc.arg(created_at), sqlc.arg(updated_at)
);

-- name: GetRule :one
SELECT * FROM check_rules WHERE org_id = sqlc.arg(org_id) AND id = sqlc.arg(id);

-- name: UpdateRule :execrows
-- Full replace of every mutable column; origin, created_at are deliberately
-- absent from the SET list — origin never changes after creation and
-- created_at is not this statement's to touch (the service carries both
-- through from the row it fetched before calling this). updated_at is the
-- caller's clock value, the same reasoning as CreateRule's own comment.
UPDATE check_rules
SET name = sqlc.arg(name), enabled = sqlc.arg(enabled), scope = sqlc.arg(scope),
    group_id = sqlc.arg(group_id), host_id = sqlc.arg(host_id), item = sqlc.arg(item),
    check_key = sqlc.arg(check_key), metric = sqlc.arg(metric), severity = sqlc.arg(severity),
    mode = sqlc.arg(mode), ast = sqlc.arg(ast)::jsonb, clear_ast = sqlc.arg(clear_ast)::jsonb,
    source = sqlc.arg(source), for_duration = sqlc.arg(for_duration), updated_at = sqlc.arg(updated_at)
WHERE org_id = sqlc.arg(org_id) AND id = sqlc.arg(id);

-- name: DeleteRule :execrows
DELETE FROM check_rules WHERE org_id = sqlc.arg(org_id) AND id = sqlc.arg(id);

-- name: ListRules :many
-- The optional-parameter idiom hosts.sql's ListHosts and audit.sql already
-- use ("param IS NULL OR <condition>"): one statement serves an unfiltered
-- list and every combination of Filter's three fields, rather than the
-- service assembling SQL text per call.
SELECT * FROM check_rules
WHERE org_id = sqlc.arg(org_id)
  AND (sqlc.narg(scope)::text IS NULL OR scope = sqlc.narg(scope)::text)
  AND (sqlc.narg(check_key)::text IS NULL OR check_key = sqlc.narg(check_key)::text)
  AND (sqlc.narg(host_id)::uuid IS NULL OR host_id = sqlc.narg(host_id)::uuid)
ORDER BY created_at, id;

-- name: ListCheckStates :many
-- Task 7 (Store.StatesForHost): every tuple's current state for one host,
-- read straight off check_states' own primary key prefix (host_id) with
-- org_id re-asserted in the WHERE clause the same way every other store in
-- this codebase double-checks org scope alongside row-level security (see
-- e.g. GetRule above). Ordered by (check_key, item, metric) so two calls
-- against the same, unchanged data always agree on order.
SELECT * FROM check_states
WHERE org_id = sqlc.arg(org_id) AND host_id = sqlc.arg(host_id)
ORDER BY check_key, item, metric;

-- name: CountStateChangesSince :many
-- Task 7 (Store.ChangeCounts): the flapping count, one row per tuple that
-- changed at least once since `since`. This is check_state_changes_lookup_
-- idx's one named query (00046's own comment): org_id and host_id pin the
-- index's leading columns, `at >= since` is the range condition the index's
-- trailing `at DESC` column serves, and the GROUP BY reduces to exactly
-- the map ChangeCounts hands back — a tuple absent from the result changed
-- zero times, not an error.
SELECT check_key, item, metric, count(*)::bigint AS change_count
FROM check_state_changes
WHERE org_id = sqlc.arg(org_id) AND host_id = sqlc.arg(host_id) AND at >= sqlc.arg(since)
GROUP BY check_key, item, metric;

-- name: MarkHostDirty :exec
-- Task 9, the ingest path's one contribution to evaluation (v0.3b-2 spec,
-- "Trigger"): one single-row upsert per ingest batch, run inside
-- internal/checks' own RecordObservations transaction so a cursor mark can
-- never be lost by a batch that committed. dirty_at is the database's own
-- now(), never the manager's wall clock, because it is compared against
-- evaluated_at — which the ticker writes from the same clock — and two
-- managers with skewed clocks must not disagree about whether a host has
-- unevaluated samples (backlog 2.80). Inside a transaction now() is the
-- transaction's start time, which is conservative in the only direction
-- that matters: it is never later than the samples this batch is writing.
--
-- The ON CONFLICT target is (host_id), the table's primary key, so the
-- statement is one index probe and one in-place update on the hot path.
INSERT INTO check_eval_cursor (org_id, host_id, dirty_at)
VALUES (sqlc.arg(org_id), sqlc.arg(host_id), now())
ON CONFLICT (host_id) DO UPDATE SET dirty_at = excluded.dirty_at;

-- name: ClaimDueEvalCursors :many
-- Task 9, the ticker's claim (v0.3b-2 spec, "Trigger"), verbatim. The
-- reading side of both comparisons is the database's own clock: dirty_at
-- and evaluated_at are both stamped by now() in this file, and now() here
-- is this statement's own (backlog 2.80).
--
-- next_due_at is the one value that crosses clocks, and it does so by
-- design: it is Result.NextDue, which the pure evaluator computes from the
-- ticker's injected wall clock (Snapshot.Now) and which this statement
-- compares against the database's now(). The consequence is worth naming
-- because it is silent: a manager whose clock runs X ahead writes every
-- next_due_at X too late, so every host it evaluates is looked at X late
-- (by any manager, not only that one); a clock X behind writes them X
-- early, so those hosts come back due on every tick until real time
-- catches up, which costs evaluations, never correctness. Keeping it on
-- the database clock would mean either a second round trip per host to
-- read now() or a duration-from-now instead of an instant, and neither is
-- worth it while the manager's own clock is already what decides staleness
-- inside the evaluation itself.
--
-- FOR UPDATE SKIP LOCKED is the entire distribution mechanism between
-- managers: a row another manager is already EVALUATING is skipped rather
-- than waited for, so two tickers firing at the same instant take largely
-- disjoint sets and neither blocks. This statement's own transaction is
-- opened for it alone and released immediately — the lock that matters is
-- LockOneEvalCursor's, taken per host inside the transaction that
-- evaluates it, and this one only keeps the scan from listing a host
-- somebody is already busy with. Two managers scanning in the same instant
-- can therefore still list the same host; the per-host lock is what makes
-- exactly one of them evaluate it.
--
-- ORDER BY next_due_at drains a backlog oldest-first
-- (check_eval_cursor_due_idx serves it), and the LIMIT bounds one tick — a
-- manager that has fallen behind catches up over several ticks instead of
-- in one pass long enough to matter to everything else sharing the pool.
SELECT org_id, host_id FROM check_eval_cursor
WHERE dirty_at > evaluated_at OR next_due_at <= now()
ORDER BY next_due_at
LIMIT sqlc.arg(lim)
FOR UPDATE SKIP LOCKED;

-- name: LockOneEvalCursor :one
-- Task 9, the second half of the claim (see ClaimDueEvalCursors). The scan
-- above lists the batch and releases its locks straight away; this
-- statement is what actually claims ONE host, inside the short transaction
-- that evaluates it and nothing else.
--
-- Splitting the claim in two is what keeps a row lock on this table short.
-- The lock lives until its transaction commits, MarkHostDirty takes the
-- same lock on the ingest path, and ingest is the 7 000-samples-a-second
-- hot path the cursor design exists to keep free — so a single transaction
-- spanning a whole 200-host batch, each host costing a dozen round trips,
-- would block every agent whose host it had claimed for the length of the
-- batch, and pin Postgres' xmin horizon for just as long. It is also what
-- the spec asks for in so many words: "Each host is evaluated in its own
-- transaction, so one host whose data is broken cannot roll back the
-- others."
--
-- SKIP LOCKED, not a wait: another manager may have taken this host
-- between the scan and now, and the honest answer to that is to skip it,
-- exactly as the scan itself would have. The due predicate is repeated for
-- the same reason — a host another manager has finished evaluating in the
-- meantime is no longer due, and re-evaluating it would be wasted work.
-- Both cases return no row, which the caller reads as "skip, not an
-- error".
SELECT org_id, host_id FROM check_eval_cursor
WHERE host_id = sqlc.arg(host_id)
  AND (dirty_at > evaluated_at OR next_due_at <= now())
FOR UPDATE SKIP LOCKED;

-- name: AdvanceEvalCursor :exec
-- Task 9: the claim's own write-back, in the transaction that holds the
-- row lock. next_due_at is the evaluation's own conclusion
-- (internal/rules.Result.NextDue): the earliest of a ripening candidate's
-- deadline and a value's staleness deadline, or a 15-second heartbeat when
-- the host has nothing pending at all.
--
-- evaluated_at is now(), which inside a transaction is the transaction's
-- START time — earlier than every read the evaluation made. That is the
-- conservative direction: an ingest batch that commits after the claim
-- began carries a dirty_at at or after this evaluated_at, so it leaves the
-- host dirty rather than silently clean.
--
-- One narrow case remains, and it delays an evaluation rather than losing
-- one: an ingest transaction that BEGAN before the claim and commits after
-- the claimed host was read stamped its dirty_at before this evaluated_at,
-- while its rows were invisible to the read. That host is therefore not
-- marked dirty by those samples. What brings it back is whichever comes
-- first of its own next_due_at and the next batch of samples for it — so
-- the bound is "the earliest pending deadline, or the host's next sample",
-- NOT the 15-second heartbeat: the heartbeat only applies to a host with
-- nothing pending at all. That earliest pending deadline is itself bounded
-- in minutes, not hours: internal/rules.Evaluate adds every measured
-- tuple's staleness deadline to its pending set, so next_due_at is never
-- later than three check intervals past the oldest reading, however long
-- a ripening candidate's own for_duration may be. In practice a host being
-- ingested for is a host ingesting again shortly, which is the short half
-- of that minimum.
UPDATE check_eval_cursor
SET evaluated_at = now(), next_due_at = sqlc.arg(next_due_at)
WHERE org_id = sqlc.arg(org_id) AND host_id = sqlc.arg(host_id);

-- name: GetHostFacts :one
-- Task 9: the facts a rule's fact() node reads (v0.3b-2 spec, "Facts are
-- strings"). Only the one jsonb column, never the whole host row — this
-- runs once per host per evaluation.
SELECT facts FROM hosts WHERE org_id = sqlc.arg(org_id) AND id = sqlc.arg(host_id);

-- name: GetCheckHostState :one
-- Task 9: the roll-up the previous evaluation wrote, which is the only
-- thing the next one can carry HostState.Since from (see
-- internal/rules.Snapshot.PreviousHost). No row means "never evaluated".
SELECT * FROM check_host_states
WHERE org_id = sqlc.arg(org_id) AND host_id = sqlc.arg(host_id);

-- name: CountStateChangesInFlappingWindow :many
-- Task 9: CountStateChangesSince with the window computed in SQL instead
-- of handed in. The flapping count is a staleness comparison like any
-- other, so its boundary comes from the database's clock rather than from
-- whichever manager happens to be ticking (backlog 2.80) — otherwise two
-- managers would count different windows for the same host and the flag
-- would depend on who got there first. Same index, same shape as
-- CountStateChangesSince (see its comment).
SELECT check_key, item, metric, count(*)::bigint AS change_count
FROM check_state_changes
WHERE org_id = sqlc.arg(org_id) AND host_id = sqlc.arg(host_id)
  AND at >= now() - sqlc.arg(flapping_window)::interval
GROUP BY check_key, item, metric;

-- name: UpsertCheckState :exec
-- Task 9: one tuple's state, written in place on every evaluation — the
-- "one row per tuple, overwritten" shape check_current already uses. Every
-- column except the key is replaced, candidate_state/candidate_since
-- included: a candidate that stopped ripening has to be cleared by the
-- same statement that would have set it, or a stale one would ripen later
-- against a wait that has long since ended.
INSERT INTO check_states (
  org_id, host_id, check_key, item, metric, state, since, rule_id,
  last_value, last_label, candidate_state, candidate_since, flapping, evaluated_at
) VALUES (
  sqlc.arg(org_id), sqlc.arg(host_id), sqlc.arg(check_key), sqlc.arg(item), sqlc.arg(metric),
  sqlc.arg(state), sqlc.arg(since), sqlc.arg(rule_id), sqlc.arg(last_value), sqlc.arg(last_label),
  sqlc.arg(candidate_state), sqlc.arg(candidate_since), sqlc.arg(flapping), sqlc.arg(evaluated_at)
)
ON CONFLICT (host_id, check_key, item, metric) DO UPDATE SET
  state = excluded.state, since = excluded.since, rule_id = excluded.rule_id,
  last_value = excluded.last_value, last_label = excluded.last_label,
  candidate_state = excluded.candidate_state, candidate_since = excluded.candidate_since,
  flapping = excluded.flapping, evaluated_at = excluded.evaluated_at;

-- name: InsertCheckStateChange :exec
-- Task 9: one transition appended to the history. Only actual transitions
-- reach this statement (Evaluate emits a Change only when the state
-- really moved), which is what the table's own CHECK (from_state <>
-- to_state) enforces on the other side.
INSERT INTO check_state_changes (
  at, org_id, host_id, check_key, item, metric, from_state, to_state, rule_id, value, label
) VALUES (
  sqlc.arg(at), sqlc.arg(org_id), sqlc.arg(host_id), sqlc.arg(check_key), sqlc.arg(item),
  sqlc.arg(metric), sqlc.arg(from_state), sqlc.arg(to_state), sqlc.arg(rule_id),
  sqlc.arg(value), sqlc.arg(label)
);

-- name: UpsertCheckHostState :exec
-- Task 9: the per-host roll-up the fleet overview reads, one row per host,
-- replaced on every evaluation that judged at least one tuple.
INSERT INTO check_host_states (
  org_id, host_id, state, crit_count, warn_count, unknown_count, since, evaluated_at
) VALUES (
  sqlc.arg(org_id), sqlc.arg(host_id), sqlc.arg(state), sqlc.arg(crit_count),
  sqlc.arg(warn_count), sqlc.arg(unknown_count), sqlc.arg(since), sqlc.arg(evaluated_at)
)
ON CONFLICT (host_id) DO UPDATE SET
  state = excluded.state, crit_count = excluded.crit_count, warn_count = excluded.warn_count,
  unknown_count = excluded.unknown_count, since = excluded.since, evaluated_at = excluded.evaluated_at;

-- name: DeleteCheckHostState :exec
-- Task 9: what an evaluation that judged no tuple at all asks for (see
-- internal/rules.Result's own doc comment). A host whose every check lost
-- its resolved interval must not keep showing the colour it had when its
-- last check was deleted — the roll-up is derived state, and the history
-- it was derived from lives in check_state_changes and is untouched here.
DELETE FROM check_host_states
WHERE org_id = sqlc.arg(org_id) AND host_id = sqlc.arg(host_id);
