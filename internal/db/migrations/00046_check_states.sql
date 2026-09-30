-- +goose Up
-- State storage (v0.3b-2, spec docs/superpowers/specs/2026-09-29-v0.3b-2-
-- rules-design.md, "Data model"): the state machine's memory. check_rules
-- (00045) says what a value means; these three tables hold what the
-- evaluator (a later task) concluded from it. Nothing here decides a state
-- — internal/rules.Evaluate (Task 8) is pure, and internal/rules.EvalStore
-- (Task 9) is what writes these tables; this migration and the read-only
-- internal/rules.StateStore built on it (states_store.go) exist so the
-- evaluator has somewhere to put its conclusions and the API has somewhere
-- to read them back from.
--
-- check_states is the current state of one (host, check_key, item, metric)
-- tuple, upserted on every evaluation — the same "one row per tuple,
-- overwritten in place" shape check_current (00044) already uses for
-- samples, for the same reason: a host page or fleet view reads this
-- instead of replaying history. The primary key is exactly
-- (host_id, check_key, item, metric), no org_id in it (host_id already
-- determines the organization through the FK below) and no `at`, mirroring
-- check_current's own primary key exactly.
--
-- candidate_state/candidate_since are the half-ripened candidate a
-- `for_duration` rule is waiting on (spec's "Order of decision", step 4): a
-- worse target that has not yet held for its rule's `for_duration`. The
-- CHECK ties the pair together — a candidate is either fully absent or
-- fully present, never a `candidate_state` with no timestamp to ripen from
-- or a `candidate_since` with no target to ripen towards.
--
-- rule_id is `ON DELETE SET NULL`, deliberately not `CASCADE`, and
-- deliberately not a plain `rule_id uuid REFERENCES check_rules(id)`
-- either: the review focus this migration exists to satisfy (plan's review
-- focus 7) is that deleting the rule currently holding a tuple at `crit`
-- must leave the state row standing, not delete it — a cascade would erase
-- the row, and the tuple would silently reappear at the next read as if it
-- had never been anything but healthy, instead of surfacing as `crit` with
-- no deciding rule until the next tick re-evaluates it. The composite FK
-- `(org_id, rule_id) REFERENCES check_rules (org_id, id)` is the same
-- "can only reference this organization's own row" guard every other
-- composite reference in this schema carries (check_rules' own group_id/
-- host_id FKs, 00045) — but a plain `ON DELETE SET NULL` on a composite FK
-- would try to null every column the FK covers, including `org_id`, which
-- is `NOT NULL` and would make the delete fail instead of clearing the
-- rule. `ON DELETE SET NULL (rule_id)` is PostgreSQL 15's column-list form
-- of the same action (18 is deployed, see check_definitions' own note on
-- `NULLS NOT DISTINCT` for the same version floor): it nulls only the
-- column named, leaving org_id, host_id and everything else on the row
-- untouched.
CREATE TABLE check_states (
  org_id          uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  host_id         uuid NOT NULL,
  check_key       text NOT NULL,
  item            text NOT NULL DEFAULT '',
  metric          text NOT NULL,
  state           text NOT NULL CHECK (state IN ('ok', 'warn', 'crit', 'unknown')),
  since           timestamptz NOT NULL,
  rule_id         uuid,
  last_value      double precision,
  last_label      text,
  candidate_state text CHECK (candidate_state IN ('ok', 'warn', 'crit', 'unknown')),
  candidate_since timestamptz,
  flapping        boolean NOT NULL DEFAULT false,
  evaluated_at    timestamptz NOT NULL,
  PRIMARY KEY (host_id, check_key, item, metric),
  CHECK ((candidate_state IS NULL) = (candidate_since IS NULL)),
  FOREIGN KEY (org_id, host_id) REFERENCES hosts (org_id, id) ON DELETE CASCADE,
  FOREIGN KEY (org_id, rule_id) REFERENCES check_rules (org_id, id) ON DELETE SET NULL (rule_id)
);
-- check_current (00044) carries the same shape of index for the same
-- reason: the primary key leads with host_id, not org_id, so a plain scan
-- by organization (RLS, and any future "every tuple in this org" listing)
-- needs its own index.
CREATE INDEX check_states_org_idx ON check_states (org_id);

-- check_state_changes is the append-only history: one row per actual
-- transition, never per tick. It is a TimescaleDB hypertable on `at`,
-- one-day chunks and a 90-day, instance-wide retention policy — the exact
-- same incantation check_samples (00044) uses, for the same reason (see
-- that migration's own "Retention" note): per-organization retention and
-- continuous aggregates are both explicitly deferred.
--
-- It feeds two consumers today and a third in the next milestone: the
-- flapping detector (internal/rules.ChangeCounts, "how many times did this
-- tuple change in the last 30 minutes") reads it live, the state history a
-- host's own detail view can show reads it as a plain log, and it becomes
-- v0.4's alert history unmodified — that milestone adds routing on top,
-- never a second journal beside this one.
--
-- The index (org_id, host_id, check_key, item, metric, at DESC) exists for
-- exactly one query: ChangeCounts' own "count this tuple's rows with
-- at >= since", scoped to one host and grouped per tuple — the same
-- (org_id, host_id, ...) prefix check_samples_lookup_idx (00044) already
-- proves out for an identical access pattern. There is no second query
-- this index is shaped for; a future one that reads a different prefix
-- should get its own index rather than stretching this one to also serve
-- it.
--
-- from_state/to_state carry the same enum CHECK state does, plus
-- `from_state <> to_state`: this table is a log of transitions, not of
-- every tick's outcome, so a row where nothing actually changed cannot
-- occur. rule_id is nullable and `ON DELETE SET NULL (rule_id)` for the
-- same reason check_states' own is: a rule deleted after it decided a
-- historical transition must not take that transition's row down with it.
-- value/label are deliberately not constrained the way check_samples'
-- value/label are (exactly one of the two): a transition into `unknown`
-- from staleness or a collection error carries neither, which is a normal
-- transition, not a malformed row.
CREATE TABLE check_state_changes (
  at         timestamptz NOT NULL,
  org_id     uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  host_id    uuid NOT NULL,
  check_key  text NOT NULL,
  item       text NOT NULL DEFAULT '',
  metric     text NOT NULL,
  from_state text NOT NULL CHECK (from_state IN ('ok', 'warn', 'crit', 'unknown')),
  to_state   text NOT NULL CHECK (to_state IN ('ok', 'warn', 'crit', 'unknown')),
  rule_id    uuid,
  value      double precision,
  label      text,
  CHECK (from_state <> to_state),
  FOREIGN KEY (org_id, host_id) REFERENCES hosts (org_id, id) ON DELETE CASCADE,
  FOREIGN KEY (org_id, rule_id) REFERENCES check_rules (org_id, id) ON DELETE SET NULL (rule_id)
);
SELECT create_hypertable('check_state_changes', 'at', chunk_time_interval => INTERVAL '1 day');
SELECT add_retention_policy('check_state_changes', INTERVAL '90 days');
CREATE INDEX check_state_changes_lookup_idx
  ON check_state_changes (org_id, host_id, check_key, item, metric, at DESC);

-- check_host_states is the per-host roll-up: the worst tuple state on the
-- host (Worse's own ordering) plus how many tuples are at each severity,
-- one row per host. The fleet overview (a later task) reads this instead
-- of aggregating every tuple of every host on each page view — the same
-- "read the roll-up, not the detail" trade check_current already makes for
-- a single host's page. Primary key (host_id) alone, no org_id, mirroring
-- check_current and check_states above for the same reason.
CREATE TABLE check_host_states (
  org_id         uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  host_id        uuid PRIMARY KEY,
  state          text NOT NULL CHECK (state IN ('ok', 'warn', 'crit', 'unknown')),
  crit_count     integer NOT NULL DEFAULT 0 CHECK (crit_count >= 0),
  warn_count     integer NOT NULL DEFAULT 0 CHECK (warn_count >= 0),
  unknown_count  integer NOT NULL DEFAULT 0 CHECK (unknown_count >= 0),
  since          timestamptz NOT NULL,
  evaluated_at   timestamptz NOT NULL,
  FOREIGN KEY (org_id, host_id) REFERENCES hosts (org_id, id) ON DELETE CASCADE
);
CREATE INDEX check_host_states_org_idx ON check_host_states (org_id);

-- The standard row-level security pair (see e.g. migration 00037) on all
-- three tables — every org-owned table in this schema carries it,
-- including a hypertable (check_samples, 00044, whose own comment records
-- exactly what the pair does and does not protect on a hypertable, which
-- applies unchanged to check_state_changes here).
ALTER TABLE check_states ENABLE ROW LEVEL SECURITY;
ALTER TABLE check_states FORCE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON check_states
  USING (org_id = NULLIF(current_setting('pulsix.org_id', true), '')::uuid);
CREATE POLICY org_isolation_bypass ON check_states
  USING (current_setting('pulsix.org_id', true) IS NULL OR current_setting('pulsix.org_id', true) = '');

ALTER TABLE check_state_changes ENABLE ROW LEVEL SECURITY;
ALTER TABLE check_state_changes FORCE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON check_state_changes
  USING (org_id = NULLIF(current_setting('pulsix.org_id', true), '')::uuid);
CREATE POLICY org_isolation_bypass ON check_state_changes
  USING (current_setting('pulsix.org_id', true) IS NULL OR current_setting('pulsix.org_id', true) = '');

ALTER TABLE check_host_states ENABLE ROW LEVEL SECURITY;
ALTER TABLE check_host_states FORCE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON check_host_states
  USING (org_id = NULLIF(current_setting('pulsix.org_id', true), '')::uuid);
CREATE POLICY org_isolation_bypass ON check_host_states
  USING (current_setting('pulsix.org_id', true) IS NULL OR current_setting('pulsix.org_id', true) = '');

-- +goose Down
DROP TABLE check_host_states;
DROP TABLE check_state_changes;
DROP TABLE check_states;
