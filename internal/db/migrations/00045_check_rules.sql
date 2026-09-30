-- +goose Up
-- Rules (v0.3b-2, spec docs/superpowers/specs/2026-09-29-v0.3b-2-rules-
-- design.md, "Data model"): what a value means. One row judges exactly one
-- metric with exactly one severity — "disk" warn and "disk" crit are two
-- rows, not one row with two thresholds — so that precedence (a later
-- task's resolver) resolves independently per severity, never having to
-- untangle a row that won on warn from the same row losing on crit.
--
-- The binding is the same shape check_definitions (00043) already uses,
-- widened by one scope: org, group and host as before, plus item — a rule
-- may bind to one (host, check_key, item) tuple, the architecture spec's
-- fourth precedence level. Two nullable foreign keys plus a CHECK that ties
-- scope to exactly the columns it requires, rather than a single
-- polymorphic id column, keeps referential integrity inside the database
-- instead of an application-level promise: group_id is set exactly for
-- 'group', host_id for 'host' and 'item', and item itself is non-empty
-- exactly for 'item' (never NULL — see check_samples' own item column,
-- 00044, for why: '' keeps equality comparisons simple everywhere else).
--
-- Both composite FKs are ON DELETE CASCADE, the same as check_definitions':
-- deleting a group or host takes its rules with it. group_id/host_id being
-- nullable does not weaken the FK — PostgreSQL's default MATCH SIMPLE skips
-- the check whenever any column of a composite FK is NULL, which is exactly
-- the org-scoped row's shape (both NULL).
--
-- check_rules references check_key as text, never a check_definitions row,
-- for the same reason check_samples does (00044's own comment): a
-- definition may be edited or deleted without orphaning the rules written
-- against its key. check_key is derived by the service from the metric
-- name via the catalogue (internal/rules.Catalogue()), never accepted
-- directly from a caller — see internal/rules' own service for why.
--
-- ast/clear_ast store the same tree shape (internal/rules.Node) both editor
-- modes compile into, as jsonb — "the evaluator never sees the difference"
-- (spec's Decisions table, "Both editor modes"). clear_ast is nullable: a
-- rule with no clear condition clears as soon as the trigger predicate is
-- false (spec's "Order of decision", step 3), and has nothing to store.
-- mode exists for the editor alone, to know which shape to offer back;
-- source is what the operator typed (expression mode) or a Format()-
-- rendered summary of ast (form mode) — see internal/rules.Format's own
-- doc comment, which is also what a form-mode rule's source is regenerated
-- through on every save, never stored as anything the operator "typed".
--
-- for_duration is a real interval, not an integer count of seconds like
-- check_definitions.interval_seconds: nothing here ever needs to do
-- arithmetic on it in SQL (the evaluator, a later task, reads it back into
-- a Go time.Duration and compares in-process), so there is no reason to
-- give up the self-documenting unit. The CHECK bounds it to [0, 24h] —
-- the same bound internal/rules.Service validates before ever reaching
-- this table, kept here too as the same belt-and-braces the interval_seconds
-- CHECK on check_definitions already models.
--
-- UNIQUE (org_id, id): the same reason monitoring_groups (00042) and
-- check_definitions (00043) both carry it — check_states (migration 00046,
-- Task 7) needs a composite foreign key (org_id, rule_id) REFERENCES
-- check_rules (org_id, id), so a state can never reference a rule
-- belonging to another organization.
--
-- Task 5's own backfill INSERT sits below, after the RLS pair and before
-- the goose Down marker — the same place check_definitions' backfill
-- (00043) sits in its migration: it seeds the same five-row starter set
-- SeedForOrg gives a newly created organization (internal/rules/seed.go)
-- into every organization that already existed when this migration ran.
-- Seeded rows carry origin = 'seed'; internal/rules refuses to change a
-- seeded row's metric, scope or severity (copy it instead), but its
-- thresholds, for_duration and enabled flag are ordinary mutable columns.
CREATE TABLE check_rules (
  id           uuid PRIMARY KEY,
  org_id       uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
  name         text NOT NULL,
  enabled      boolean NOT NULL DEFAULT true,
  origin       text NOT NULL DEFAULT 'user' CHECK (origin IN ('seed', 'user')),
  scope        text NOT NULL CHECK (scope IN ('org', 'group', 'host', 'item')),
  group_id     uuid,
  host_id      uuid,
  item         text NOT NULL DEFAULT '',
  check_key    text NOT NULL,
  metric       text NOT NULL,
  severity     text NOT NULL CHECK (severity IN ('warn', 'crit')),
  mode         text NOT NULL CHECK (mode IN ('form', 'expression')),
  ast          jsonb NOT NULL,
  clear_ast    jsonb,
  source       text NOT NULL,
  for_duration interval NOT NULL DEFAULT '0'
    CHECK (for_duration >= interval '0' AND for_duration <= interval '24:00:00'),
  created_at   timestamptz NOT NULL DEFAULT now(),
  updated_at   timestamptz NOT NULL DEFAULT now(),
  CHECK (
    (scope = 'org'   AND group_id IS NULL     AND host_id IS NULL     AND item = '') OR
    (scope = 'group' AND group_id IS NOT NULL AND host_id IS NULL     AND item = '') OR
    (scope = 'host'  AND group_id IS NULL     AND host_id IS NOT NULL AND item = '') OR
    (scope = 'item'  AND group_id IS NULL     AND host_id IS NOT NULL AND item <> '')
  ),
  UNIQUE (org_id, id),
  FOREIGN KEY (org_id, group_id) REFERENCES monitoring_groups (org_id, id) ON DELETE CASCADE,
  FOREIGN KEY (org_id, host_id) REFERENCES hosts (org_id, id) ON DELETE CASCADE
);
CREATE INDEX check_rules_org_idx ON check_rules (org_id);
-- Supports Store.List's Filter (scope, check_key, host_id) and the
-- precedence resolver (a later task), which loads every rule for a given
-- check_key across scopes to decide a winner.
CREATE INDEX check_rules_lookup_idx ON check_rules (org_id, check_key, scope, host_id);

-- The standard row-level security pair (see e.g. migration 00037): every
-- org-owned table admits only the bound organization, or every row when no
-- organization is bound at all.
ALTER TABLE check_rules ENABLE ROW LEVEL SECURITY;
ALTER TABLE check_rules FORCE ROW LEVEL SECURITY;
CREATE POLICY org_isolation ON check_rules
  USING (org_id = NULLIF(current_setting('pulsix.org_id', true), '')::uuid);
CREATE POLICY org_isolation_bypass ON check_rules
  USING (current_setting('pulsix.org_id', true) IS NULL OR current_setting('pulsix.org_id', true) = '');

-- Backfill: every organization that existed before this migration gets the
-- same five-row starter set a newly created one gets from
-- internal/rules.Service.SeedForOrg. The values below — including the ast/
-- clear_ast jsonb, which must decode to exactly the Node tree
-- Form.Compile() builds for the same operator/value pair — must stay in
-- sync with SeedRules() (internal/rules/seed.go) by hand: a migration
-- cannot call Go code. service.state deliberately has no row here either —
-- see SeedRules()'s own doc comment for why.
INSERT INTO check_rules (
  id, org_id, name, enabled, origin, scope, check_key, metric, severity,
  mode, ast, clear_ast, source, for_duration
)
SELECT gen_random_uuid(), o.id, v.name, true, 'seed', 'org', v.check_key, v.metric, v.severity,
  'form', v.ast::jsonb, v.clear_ast::jsonb, v.source, v.for_duration::interval
FROM organizations o
CROSS JOIN (VALUES
  (
    'Disk usage warning', 'disk', 'disk.used_pct', 'warn',
    '{"kind":"compare","op":">=","args":[{"kind":"metric","name":"disk.used_pct"},{"kind":"number","num":85}]}',
    '{"kind":"compare","op":"<","args":[{"kind":"metric","name":"disk.used_pct"},{"kind":"number","num":80}]}',
    'disk.used_pct >= 85', '0'
  ),
  (
    'Disk usage critical', 'disk', 'disk.used_pct', 'crit',
    '{"kind":"compare","op":">=","args":[{"kind":"metric","name":"disk.used_pct"},{"kind":"number","num":95}]}',
    '{"kind":"compare","op":"<","args":[{"kind":"metric","name":"disk.used_pct"},{"kind":"number","num":90}]}',
    'disk.used_pct >= 95', '0'
  ),
  (
    'Memory usage warning', 'memory', 'mem.used_pct', 'warn',
    '{"kind":"compare","op":">=","args":[{"kind":"metric","name":"mem.used_pct"},{"kind":"number","num":90}]}',
    NULL,
    'mem.used_pct >= 90', '10 minutes'
  ),
  (
    'Memory usage critical', 'memory', 'mem.used_pct', 'crit',
    '{"kind":"compare","op":">=","args":[{"kind":"metric","name":"mem.used_pct"},{"kind":"number","num":97}]}',
    NULL,
    'mem.used_pct >= 97', '5 minutes'
  ),
  (
    'CPU usage warning', 'cpu', 'cpu.used_pct', 'warn',
    '{"kind":"compare","op":">=","args":[{"kind":"metric","name":"cpu.used_pct"},{"kind":"number","num":90}]}',
    NULL,
    'cpu.used_pct >= 90', '15 minutes'
  )
) AS v(name, check_key, metric, severity, ast, clear_ast, source, for_duration);

-- +goose Down
DROP TABLE check_rules;
