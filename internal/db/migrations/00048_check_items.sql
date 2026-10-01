-- +goose Up
-- Discovery and item decisions (v0.3b-3, spec docs/superpowers/specs/2026-
-- 10-01-v0.3b-3-discovery-design.md, "Data model"): the things a check
-- measures — a mount like `/var`, a service unit like `sshd` — stop being
-- strings an operator types from memory and become rows the product knows
-- about. Two tables, two lifecycles, deliberately kept apart ("Discovery
-- and decisions" in the design's own decision table): check_items is
-- written by the machine and belongs in no audit log; check_item_decisions
-- is an operator's decision and belongs in every audit log. Neither
-- references the other — a decision may name an item that does not exist
-- yet, and survives one that disappears.
--
-- check_items is what the machine found: one row per (host, check_key,
-- item) ever discovered, written by the evaluation ticker (internal/rules,
-- a later task) inside the same per-host transaction it already opens to
-- load that host's current tuples. The primary key leads with host_id, not
-- org_id — the same shape check_current and check_states (00044, 00046)
-- already use, for the same reason: host_id already determines the
-- organization through the FK below, and the read paths (one host's items,
-- the evaluator's own lookup) always know the host.
--
-- first_seen is the ONLY timestamp this table carries. There is
-- deliberately no last_seen column: storing it would mean one write per
-- item per host per tick — twenty mounts, every fifteen seconds, for
-- ever — on a table whose entire point is that an item costs exactly one
-- write in its lifetime. check_current (00044) already carries the latest
-- timestamp per (host, check_key, item, metric) tuple, so "last seen" is
-- max(at) over that tuple's check_current rows, read when it is needed
-- (internal/items.Store.ItemsForHost/ItemsForOrg), never a column here.
-- The insert itself is `ON CONFLICT (host_id, check_key, item) DO
-- NOTHING`, which is what makes first_seen immovable: a second discovery
-- of the same tuple is a no-op, not an update.
--
-- first_seen is stamped with the database's own now(), not a value
-- Discover's caller supplies: unlike check_eval_cursor's dirty_at/
-- evaluated_at pair (00047, MarkHostDirty's own comment), nothing here
-- ever compares first_seen against a second, independently clock-stamped
-- column, so there is no cross-machine skew to reason about and no reason
-- to thread a clock through the Store interface for a value written
-- exactly once.
--
-- item is NOT NULL and the CHECK below also refuses '': a whole-host
-- metric (cpu, memory) has no item to be, and the design is explicit that
-- those are not rows ("Items with an empty name" in the decision table) —
-- a row per host per check would be noise every query on this table
-- carries. This is enforced here, not only trusted of the ticker, the same
-- belt-and-braces posture check_rules' own item CHECK (00045) takes for
-- its 'item' scope.
--
-- Rows are never deleted — "absence shows as a stale last_seen" (the
-- design's own decision table), the same convention check_current and
-- check_states already carry for a mount that was unmounted or a service
-- uninstalled: nothing observes a disappearance, the agent simply stops
-- sending it.
CREATE TABLE check_items (
  org_id     uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  host_id    uuid NOT NULL,
  check_key  text NOT NULL,
  item       text NOT NULL CHECK (item <> ''),
  first_seen timestamptz NOT NULL,
  PRIMARY KEY (host_id, check_key, item),
  FOREIGN KEY (org_id, host_id) REFERENCES hosts (org_id, id) ON DELETE CASCADE
);
-- check_current and check_states (00044, 00046) carry the same shape of
-- index for the same reason: the primary key leads with host_id, not
-- org_id, so a plain scan by organization (RLS, and ItemsForOrg's
-- fleet-wide listing) needs its own index.
CREATE INDEX check_items_org_idx ON check_items (org_id);

-- check_item_decisions is what a person decided: an operator override of
-- how an item's measurements are judged — ignore it, or watch it again
-- having overruled a broader ignore. internal/items.Decide (Task 1,
-- decide.go) is the pure resolver this table's rows feed; this table only
-- stores what Decide consumes.
--
-- scope is 'org' or 'host', exactly as check_rules' own scope column
-- distinguishes a binding's breadth, and the CHECK below ties it to
-- host_id in the exact shape check_rules (00045) uses for its own binding:
-- host_id is set for 'host' and NULL for 'org', never the other way round.
-- The composite FK (org_id, host_id) REFERENCES hosts (org_id, id) is the
-- same "can only reference this organization's own host" guard every other
-- composite reference in this schema carries, and ON DELETE CASCADE
-- matches check_rules' own host binding: deleting a host takes its
-- decisions with it, because a decision naming a host that no longer
-- exists is a row no query can ever reach again. MATCH SIMPLE (Postgres'
-- default) skips the FK check whenever either column is NULL, which is
-- exactly the org-scoped row's shape (host_id NULL).
--
-- match_kind and pattern are Decide's own vocabulary (items.MatchKind,
-- items.Match): 'exact' compares the pattern to an item name literally,
-- 'glob' runs it through the one-character '*' syntax Match implements.
-- decision is 'ignore' or 'watch' — internal/items.Verdict in Go, kept as
-- `decision` in the schema because that is the word an operator uses,
-- while `Decision` is already the Go name for the row itself (see
-- items.Verdict's own doc comment). watch is a stored value, not merely
-- the absence of ignore, because storing it is what lets the read side
-- tell "somebody looked at this and said watch it" from "nobody ever has"
-- (the design's own "The third value" decision).
--
-- note is NOT NULL DEFAULT '' but load-bearing all the same: an operator
-- six months later needs to read why a volume was ignored before they dare
-- switch it back on, and created_by/created_at alone record who and when,
-- never why.
--
-- created_by is a plain uuid with no foreign key, the same choice
-- check_definitions (00043) already makes for its own created_by: the
-- audit log is the durable record of who did this, not a referential
-- identity this table has to keep valid as users come and go.
--
-- created_at/updated_at carry a DEFAULT now() as a fallback only, exactly
-- like check_rules' own pair (00045): internal/items.Service (a later
-- task) always supplies both explicitly from its injected clock, the same
-- "caller's clock, not the database's" convention CreateRule documents.
CREATE TABLE check_item_decisions (
  id          uuid PRIMARY KEY,
  org_id      uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  scope       text NOT NULL CHECK (scope IN ('org', 'host')),
  host_id     uuid,
  check_key   text NOT NULL,
  match_kind  text NOT NULL CHECK (match_kind IN ('exact', 'glob')),
  pattern     text NOT NULL,
  decision    text NOT NULL CHECK (decision IN ('ignore', 'watch')),
  note        text NOT NULL DEFAULT '',
  created_by  uuid,
  created_at  timestamptz NOT NULL DEFAULT now(),
  updated_at  timestamptz NOT NULL DEFAULT now(),
  CHECK (
    (scope = 'org'  AND host_id IS NULL) OR
    (scope = 'host' AND host_id IS NOT NULL)
  ),
  FOREIGN KEY (org_id, host_id) REFERENCES hosts (org_id, id) ON DELETE CASCADE
);
-- Serves both RLS's own org-wide scan and Store.Decisions' own lookup
-- ("the organization's decisions plus, when hostID is non-nil, that
-- host's" — one query, since the evaluator calls it per host and must not
-- pay per item): org_id leads, scope and host_id follow, the same
-- "lookup index mirrors the one query it exists for" convention
-- check_rules_lookup_idx (00045) documents.
CREATE INDEX check_item_decisions_lookup_idx ON check_item_decisions (org_id, scope, host_id);

-- The standard row-level security pair (see e.g. migration 00037) on both
-- tables: every org-owned table in this schema admits only the bound
-- organization, or every row when no organization is bound at all.
ALTER TABLE check_items ENABLE ROW LEVEL SECURITY;
ALTER TABLE check_items FORCE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON check_items
  USING (org_id = NULLIF(current_setting('pulsix.org_id', true), '')::uuid);
CREATE POLICY org_isolation_bypass ON check_items
  USING (current_setting('pulsix.org_id', true) IS NULL OR current_setting('pulsix.org_id', true) = '');

ALTER TABLE check_item_decisions ENABLE ROW LEVEL SECURITY;
ALTER TABLE check_item_decisions FORCE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON check_item_decisions
  USING (org_id = NULLIF(current_setting('pulsix.org_id', true), '')::uuid);
CREATE POLICY org_isolation_bypass ON check_item_decisions
  USING (current_setting('pulsix.org_id', true) IS NULL OR current_setting('pulsix.org_id', true) = '');

-- +goose Down
DROP TABLE check_item_decisions;
DROP TABLE check_items;
