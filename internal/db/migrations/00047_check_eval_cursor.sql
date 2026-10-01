-- +goose Up
-- The evaluation cursor (v0.3b-2, spec docs/superpowers/specs/2026-09-29-
-- v0.3b-2-rules-design.md, "Trigger"): one row per host, and the whole
-- scheduling substrate of the rule engine. There is no queue table, no
-- lease column, no attempt counter and no worker identity — deliberately.
--
-- Why a cursor at all, rather than evaluating inside the ingest
-- transaction: ingest is the hot path (the fleet target is roughly 7 000
-- samples a second) and evaluation is not a constant-cost operation —
-- window aggregates scan the hypertable. Paying that inside the batch that
-- delivered the samples would tie an agent's stream to the cost of the
-- rules written against it. So ingest contributes exactly one single-row
-- upsert per batch (MarkHostDirty, in internal/checks' own ingest
-- transaction) and the ticker does the rest. It also buys the one thing
-- ingest-triggered evaluation could never give: a host whose agent has
-- gone silent sends nothing at all, so nothing would ever mark it dirty —
-- only next_due_at brings the ticker back to it, which is what makes
-- `unknown` reachable and what a ripening `for_duration` candidate needs.
--
-- The three timestamps, and why each exists:
--
--   dirty_at     the last moment samples arrived for this host. Written
--                by ingest, read by nothing else.
--   evaluated_at the last moment the ticker evaluated it. "Dirty" is
--                therefore `dirty_at > evaluated_at` — a comparison of two
--                stored moments rather than a boolean flag somebody has to
--                remember to clear. A flag would have a lost-update race
--                (clear it after evaluating and the samples that arrived
--                mid-evaluation are lost); two timestamps cannot.
--   next_due_at  when this host must be looked at again even if nothing
--                arrives: a ripening candidate's own deadline, the moment
--                a value goes stale, or a 15-second heartbeat when the
--                host has nothing pending at all (internal/rules.nextDue
--                computes it).
--
-- dirty_at and evaluated_at are both written by now() (MarkHostDirty,
-- AdvanceEvalCursor), so the comparison between them never crosses two
-- machines' clocks. next_due_at is the exception, deliberately: it is the
-- pure evaluator's conclusion, computed from the ticker's own injected
-- clock, and the claim compares it against the database's now(). A manager
-- whose clock runs ahead therefore delays every host it evaluates by that
-- much; one whose clock runs behind makes them due again on every tick
-- until real time catches up. Both cost evaluations, neither costs
-- correctness — see ClaimDueEvalCursors' own comment for why that trade
-- was taken.
--
-- Both default to 'epoch' so that a row created by ingest — which sets
-- only dirty_at — is immediately both dirty and due, and gets evaluated on
-- the very next tick rather than waiting for a first advance to happen.
--
-- The claim (internal/rules/due_sql.go) is
--
--   SELECT ... WHERE dirty_at > evaluated_at OR next_due_at <= now()
--   ORDER BY next_due_at LIMIT :batch FOR UPDATE SKIP LOCKED
--
-- and check_eval_cursor_due_idx is the index it orders by: an overdue
-- backlog drains oldest-first instead of starving its oldest rows, and the
-- ORDER BY costs a sorted scan of the whole table without it. The index
-- deliberately does NOT lead with org_id: the ticker is one per manager
-- and claims across every organization at once (it runs unscoped, where
-- the org_isolation_bypass policy applies, exactly like the schedule
-- ticker's own due scan), so a per-organization prefix would only make
-- every claim read more index pages. check_eval_cursor_org_idx exists for
-- the other direction — the two ON DELETE CASCADE paths into this table
-- (an organization, and a host) both look rows up by org_id, and
-- check_host_states (00046) carries its own for the same reason.
--
-- Nothing here is per-manager. `SKIP LOCKED` is the whole distribution
-- mechanism: two managers ticking at the same moment each take a disjoint
-- set of rows because the second one skips what the first has locked, and
-- neither waits for the other. The row lock lives exactly as long as the
-- evaluating transaction, so a manager that dies mid-batch releases every
-- host it held when its connection drops — which is why there is no lease
-- to expire and no reaper to sweep.
--
-- That lifetime is why the claim is in two parts (ClaimDueEvalCursors then
-- LockOneEvalCursor, internal/rules/due_sql.go): the scan lists a batch and
-- lets its locks go at once, and the lock that is actually held belongs to
-- one host's own transaction. MarkHostDirty takes the same row lock from
-- the ingest path, so a lock held for the length of a 200-host batch would
-- block every agent in that batch on its ingest commit — the hot path this
-- whole table exists to keep free.
CREATE TABLE check_eval_cursor (
  org_id       uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  host_id      uuid PRIMARY KEY,
  dirty_at     timestamptz NOT NULL,
  evaluated_at timestamptz NOT NULL DEFAULT 'epoch',
  next_due_at  timestamptz NOT NULL DEFAULT 'epoch',
  FOREIGN KEY (org_id, host_id) REFERENCES hosts (org_id, id) ON DELETE CASCADE
);
CREATE INDEX check_eval_cursor_due_idx ON check_eval_cursor (next_due_at);
CREATE INDEX check_eval_cursor_org_idx ON check_eval_cursor (org_id);

-- The standard row-level security pair (migration 00037's shape), the same
-- as every other org-owned table in this schema. The ticker itself runs
-- outside any organization scope, where the bypass policy applies — it has
-- to, since it claims across all of them — and every statement it issues
-- names org_id in its own WHERE clause regardless, the same posture the
-- schedule ticker's due scan already has. The pair is what keeps an
-- org-scoped request (the API) from ever seeing another tenant's cursor.
ALTER TABLE check_eval_cursor ENABLE ROW LEVEL SECURITY;
ALTER TABLE check_eval_cursor FORCE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON check_eval_cursor
  USING (org_id = NULLIF(current_setting('pulsix.org_id', true), '')::uuid);
CREATE POLICY org_isolation_bypass ON check_eval_cursor
  USING (current_setting('pulsix.org_id', true) IS NULL OR current_setting('pulsix.org_id', true) = '');

-- +goose Down
DROP TABLE check_eval_cursor;
