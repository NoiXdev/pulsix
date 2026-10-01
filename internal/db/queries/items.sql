-- name: InsertCheckItem :exec
-- Discovery (v0.3b-3 design, "Discovery and evaluation"): one row per
-- (host, check_key, item), written at most once in its life. ON CONFLICT
-- DO NOTHING is the whole mechanism — an item already known costs one
-- failed index probe on the primary key and nothing else; first_seen is
-- left exactly as it was on the existing row. first_seen is the
-- database's own now(), not a value the caller supplies — see migration
-- 00048's own comment on check_items for why no clock is threaded through
-- this statement.
INSERT INTO check_items (org_id, host_id, check_key, item, first_seen)
VALUES (sqlc.arg(org_id), sqlc.arg(host_id), sqlc.arg(check_key), sqlc.arg(item), now())
ON CONFLICT (host_id, check_key, item) DO NOTHING;

-- name: ListItemsForHost :many
-- Store.ItemsForHost: one host's discovered items, each with its own "last
-- reported" moment — a correlated scalar subquery computing max(at) over
-- that exact tuple's check_current rows, across every metric the tuple
-- carries (a disk item reports several: used_pct, free_bytes, ...).
-- check_items itself carries no last_seen column (00048's own comment
-- explains why), so this is the read side's only source for it.
--
-- max() over zero rows is SQL NULL — exactly "never reported": an item
-- ignored since the moment it was discovered, or an agent that has not
-- reported since a restart. reported is a separate EXISTS, the honest way
-- to tell that case apart from a real reading; last_seen itself is
-- COALESCEd to the sentinel 'epoch' (1970-01-01) so the column comes back
-- NOT NULL — sqlc's static analysis (no live database behind this schema
-- directory, see sqlc.yaml) cannot otherwise prove a subquery-derived
-- column's nullability, and a NULL scanned into Go's time.Time is a
-- runtime scan error, not a zero value (an earlier '-infinity' sentinel
-- failed for a related reason: pgx's plain time.Time cannot represent
-- timestamptz's own infinity values either).
-- The store (internal/items.SQLStore) discards the sentinel and reports
-- the Go zero time.Time whenever reported is false, so it never escapes
-- this query.
SELECT i.org_id, i.host_id, i.check_key, i.item, i.first_seen,
       COALESCE(
         (SELECT max(cur.at) FROM check_current cur
           WHERE cur.org_id = sqlc.arg(org_id) AND cur.host_id = i.host_id
             AND cur.check_key = i.check_key AND cur.item = i.item),
         'epoch'::timestamptz
       )::timestamptz AS last_seen,
       EXISTS (
         SELECT 1 FROM check_current cur
          WHERE cur.org_id = sqlc.arg(org_id) AND cur.host_id = i.host_id
            AND cur.check_key = i.check_key AND cur.item = i.item
       ) AS reported
FROM check_items i
WHERE i.org_id = sqlc.arg(org_id) AND i.host_id = sqlc.arg(host_id)
ORDER BY i.check_key, i.item;

-- name: ListItemsForOrg :many
-- Store.ItemsForOrg: the same per-item "last reported" subquery as
-- ListItemsForHost, across every host in the organization, filtered by
-- check_key and/or an item glob that the caller (internal/items.SQLStore)
-- has already translated to a LIKE pattern: '*' becomes '%', the LIKE
-- wildcard for "any run of characters, including none" — exactly
-- items.Match's own semantics for '*' — and every '%', '_' or backslash
-- literal in the OPERATOR's own pattern is escaped first, so it stays a
-- literal instead of becoming a second, accidental wildcard. The
-- translation is exact, not approximate: our glob is a strict subset of
-- LIKE (one metacharacter, always anchored at both ends, no '?' and no
-- character class), so the two can never drift apart without a change to
-- items.Match itself — if that ever happens, this comment and the
-- translation it describes are the first things to revisit.
SELECT i.org_id, i.host_id, i.check_key, i.item, i.first_seen,
       COALESCE(
         (SELECT max(cur.at) FROM check_current cur
           WHERE cur.org_id = sqlc.arg(org_id) AND cur.host_id = i.host_id
             AND cur.check_key = i.check_key AND cur.item = i.item),
         'epoch'::timestamptz
       )::timestamptz AS last_seen,
       EXISTS (
         SELECT 1 FROM check_current cur
          WHERE cur.org_id = sqlc.arg(org_id) AND cur.host_id = i.host_id
            AND cur.check_key = i.check_key AND cur.item = i.item
       ) AS reported
FROM check_items i
WHERE i.org_id = sqlc.arg(org_id)
  AND (sqlc.narg(check_key)::text IS NULL OR i.check_key = sqlc.narg(check_key)::text)
  AND (sqlc.narg(item_like)::text IS NULL OR i.item LIKE sqlc.narg(item_like)::text ESCAPE '\')
ORDER BY i.host_id, i.check_key, i.item;

-- name: CreateItemDecision :exec
-- created_at/updated_at are supplied by the caller (internal/items.Service's
-- injected clock, a later task), not DEFAULT now() — the same reasoning
-- CreateRule's own comment gives: Store.CreateDecision returns only an
-- error, so the timestamps this statement writes are the only ones the
-- caller ever sees again.
INSERT INTO check_item_decisions (
  id, org_id, scope, host_id, check_key, match_kind, pattern, decision, note,
  created_by, created_at, updated_at
) VALUES (
  sqlc.arg(id), sqlc.arg(org_id), sqlc.arg(scope), sqlc.arg(host_id), sqlc.arg(check_key),
  sqlc.arg(match_kind), sqlc.arg(pattern), sqlc.arg(decision), sqlc.arg(note),
  sqlc.arg(created_by), sqlc.arg(created_at), sqlc.arg(updated_at)
);

-- name: UpdateItemDecision :execrows
-- Full replace of every mutable column; created_by and created_at are
-- deliberately absent from the SET list — who created a decision and when
-- never changes on edit, the same posture UpdateRule's own comment takes
-- for origin/created_at.
UPDATE check_item_decisions
SET scope = sqlc.arg(scope), host_id = sqlc.arg(host_id), check_key = sqlc.arg(check_key),
    match_kind = sqlc.arg(match_kind), pattern = sqlc.arg(pattern), decision = sqlc.arg(decision),
    note = sqlc.arg(note), updated_at = sqlc.arg(updated_at)
WHERE org_id = sqlc.arg(org_id) AND id = sqlc.arg(id);

-- name: DeleteItemDecision :execrows
DELETE FROM check_item_decisions WHERE org_id = sqlc.arg(org_id) AND id = sqlc.arg(id);

-- name: GetItemDecision :one
SELECT * FROM check_item_decisions WHERE org_id = sqlc.arg(org_id) AND id = sqlc.arg(id);

-- name: ListItemDecisions :many
-- Store.ListDecisions: every decision in the organization, for the
-- operator-facing listing (the "Items" tab, a later task) — unfiltered by
-- scope or host, unlike ListItemDecisionsForEval below.
SELECT * FROM check_item_decisions WHERE org_id = sqlc.arg(org_id) ORDER BY created_at, id;

-- name: ListItemDecisionsForEval :many
-- Store.Decisions: the organization's decisions plus, when host_id is
-- given, that host's — one statement, not one per item, because the
-- evaluator (internal/rules, a later task) calls this once per host and
-- runs internal/items.Decide over the result for every item on that host.
-- When sqlc.narg(host_id) is NULL (Store.Decisions called with a nil
-- hostID), "host_id = NULL" is never true, so the second half of the OR
-- contributes nothing and only scope = 'org' rows come back — exactly
-- "the organization's decisions" with no host's.
SELECT * FROM check_item_decisions
WHERE org_id = sqlc.arg(org_id)
  AND (scope = 'org' OR (scope = 'host' AND host_id = sqlc.narg(host_id)::uuid))
ORDER BY created_at, id;
