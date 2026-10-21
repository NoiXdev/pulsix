-- Task 2, spec docs/superpowers/specs/2026-10-20-v0.2f-installation-owned-
-- resources-design.md: the installation-owned repository definition
-- (migration 00060's shared_repos) and its projection into every
-- organization's own git_repos row (decisions 1, 2, 4, 5 and 7). Modelled
-- on internal/db/queries/vault_identities.sql's own comment style and its
-- delete-then-insert rule, and on alert_routes.sql's lockstep-unnest shape
-- for writing one row per something in a single round trip.
--
-- Every statement here runs on a connection with NO organization bound
-- (db.WithOrgScope never called) — shared_repos carries no org_id and no
-- RLS at all (the instance_settings posture since migration 00009), and an
-- unscoped connection is exactly what lets git_repos' own
-- org_isolation_bypass policy (migration 00018) admit every organization's
-- row instead of just one. shared.SQLStore's own doc comment says why that
-- is deliberate rather than an oversight.

-- name: CreateSharedRepo :one
-- The definition's own row. A duplicate name here is two DEFINITIONS
-- wanting the same name — shared_repos.name's own UNIQUE constraint
-- (migration 00060) catches it, and the store maps it to the plain
-- ErrNameTaken. That is a different collision from decision 7's (an
-- organization's own unrelated repository already using the name), which
-- ListGitRepoNamesExcludingDefinition below exists to catch BEFORE this
-- ever runs.
INSERT INTO shared_repos (id, name, url, branch, subpath)
VALUES (@id, @name, @url, @branch, @subpath)
RETURNING *;

-- name: GetSharedRepo :one
SELECT * FROM shared_repos WHERE id = @id;

-- name: ListSharedRepos :many
-- Every repository definition, oldest first (uuid v7's own time ordering,
-- the same reliance internal/db/queries/hostkeys.sql's ORDER BY id takes) —
-- ProjectAllInto's raw material when a new organization is created (spec
-- decision 4): every definition that existed before this organization did,
-- projected into it once, in the order they were made.
SELECT * FROM shared_repos ORDER BY id;

-- name: UpdateSharedRepo :one
-- The definition's own row, run inside the same transaction as
-- UpdateProjectedRepo below (shared.SQLStore.UpdateAndPropagate) so the
-- definition's new value and every projection's either land together or
-- neither does (Review Focus 2 — propagation is atomic per definition).
-- :one with RETURNING, not :execrows: a row that does not exist and a
-- genuine zero-row update are the same thing here (there is no optimistic
-- lock to tell them apart, unlike alert_routes' own Update), so pgx.ErrNoRows
-- mapping straight to ErrNotFound is all the store needs.
UPDATE shared_repos SET name = @name, url = @url, branch = @branch, subpath = @subpath, updated_at = now()
WHERE id = @id
RETURNING *;

-- name: DeleteSharedRepo :execrows
-- The definition's own row, run only after every one of its projections is
-- already gone (DeleteProjectedRepo below, looped over in the same
-- transaction) — shared_repo_id's ON DELETE RESTRICT (migration 00060)
-- would refuse this otherwise, the backstop spec decision 5 relies on if a
-- caller ever got the order wrong.
DELETE FROM shared_repos WHERE id = @id;

-- name: ListGitRepoNamesExcludingDefinition :many
-- Every organization's git_repos name, decision 7's raw material: a
-- definition cannot be created or renamed to a name some organization's
-- OWN existing repository already carries, whether that repository is a
-- projection of a different definition or an ordinary row of its own.
-- exclude is the definition being renamed — its own current projections
-- are about to be renamed to the very name being checked, so they must not
-- flag themselves as colliding with themselves. A brand new definition has
-- no projections yet and passes uuid.Nil, which excludes nothing: no row's
-- shared_repo_id is ever the nil UUID, so IS DISTINCT FROM is true for
-- every one of them.
SELECT org_id, name FROM git_repos WHERE shared_repo_id IS DISTINCT FROM @exclude::uuid;

-- name: ListOrganizationIDs :many
-- Every organization, oldest first (uuid v7 ordering, hostkeys.sql's own
-- ORDER BY id) — for projecting a brand new definition into all of them at
-- once (spec decision 4). organizations carries no RLS (it is tenancy's
-- own root, migration 00001), so this never needs an organization scope
-- either.
SELECT id FROM organizations ORDER BY id;

-- name: InsertProjectedRepos :exec
-- One projection per organization, landing in the SAME transaction as the
-- definition's own CreateSharedRepo (shared.SQLStore.CreateWithProjections)
-- so a brand new definition appears everywhere at once or not at all (spec
-- decision 4) — never some organizations with it and others still waiting
-- because a later statement in a loop failed. InsertRepoVaultCredentials'
-- own lockstep-unnest shape (queries/vault_identities.sql) over the one
-- pair of arrays that actually varies per row: ids and org_ids, built from
-- the same slice by the caller in one pass so the two are always the same
-- length. Every other column is one literal value shared by every row,
-- which is what makes this a single round trip instead of one INSERT per
-- organization. credential_id is left out of the column list (NULL by
-- omission): a fresh projection's binding is the organization's own to
-- fill in (spec decision 2), not something this statement could ever know.
INSERT INTO git_repos (id, org_id, name, url, branch, subpath, shared_repo_id)
SELECT unnest(@ids::uuid[]), unnest(@org_ids::uuid[]), @name, @url, @branch, @subpath, @shared_repo_id;

-- name: InsertProjectedRepo :exec
-- InsertProjectedRepos' single-organization counterpart: ProjectInto's own
-- statement for projecting one ALREADY EXISTING definition into ONE newly
-- created organization (ProjectAllInto), the mirror image of
-- InsertProjectedRepos' one-definition-into-every-organization.
INSERT INTO git_repos (id, org_id, name, url, branch, subpath, shared_repo_id)
VALUES (@id, @org_id, @name, @url, @branch, @subpath, @shared_repo_id);

-- name: ListProjectedRepos :many
-- Every organization currently holding a projection of one definition, with
-- the projection's own id (GitRepoHasJobTrigger, GitRepoHasPlaybook and
-- DeleteProjectedRepo all need it, the last only for its org_id half) —
-- oldest projection first
-- (ORDER BY the projection's own id, uuid v7 ordering again), which is
-- UpdateAndPropagate's own iteration order: "the second organization" in a
-- propagation failure is the second one actually projected, not an
-- arbitrary one.
SELECT id, org_id FROM git_repos WHERE shared_repo_id = @shared_repo_id::uuid ORDER BY id;

-- name: ListProjectedReposForUpdate :many
-- ListProjectedRepos' own shape, with FOR UPDATE, for
-- DeleteWithProjections alone: locking each projection row before checking
-- it for dependants is what actually closes the race DeleteProjectedRepo's
-- own backstop comment used to merely gesture at. A concurrent INSERT into
-- job_triggers or playbooks takes a FOR KEY SHARE lock on the git_repos
-- row it references, to verify that row still exists; this FOR UPDATE
-- conflicts with that and blocks it until this transaction ends. By the
-- time it can proceed, either this transaction has committed (the row is
-- gone, so the blocked INSERT then fails on the very foreign key it was
-- checking) or this transaction rolled back (the row is still there to
-- reference, and the INSERT proceeds normally). UpdateAndPropagate does
-- not need this: it only ever changes columns, never removes the row a
-- concurrent insert depends on existing.
SELECT id, org_id FROM git_repos WHERE shared_repo_id = @shared_repo_id::uuid ORDER BY id FOR UPDATE;

-- name: GitRepoHasJobTrigger :one
-- Whether a job trigger still names one organization's projection — one of
-- the two tables DeleteWithProjections checks explicitly, in the same
-- pre-write loop as GitRepoHasPlaybook, before touching anything. See
-- DeleteWithProjections' own doc comment (store_sql.go) for the full
-- inventory of every table that references git_repos and why these two
-- are the ones checked here: job triggers are a customer's own webhook
-- configuration that would otherwise silently disappear (job_triggers' own
-- ON DELETE CASCADE, migration 00026) — exactly spec decision 5's own
-- example ("a schedule, a trigger or a job"). git_repo_vault_credentials
-- is the one table deliberately NOT checked; its own cascade (migration
-- 00059) is left to run, because a vault password binding is part of the
-- projection rather than a dependant on it.
SELECT EXISTS (SELECT 1 FROM job_triggers WHERE org_id = @org_id::uuid AND repo_id = @repo_id::uuid);

-- name: GitRepoHasPlaybook :one
-- Whether a playbook still names one organization's projection — the
-- second table DeleteWithProjections checks explicitly, beside
-- GitRepoHasJobTrigger. A playbook is registered by an organization
-- itself (playbooks.Service.CreateRepo, POST /orgs/{org}/playbooks under
-- playbooks.write) — sync only clones a repository's checkout
-- (internal/worker/runner/gitrepo.go) and never writes a playbooks row —
-- so a playbook is customer-owned configuration exactly like a job
-- trigger, not derived data a delete may clear on the customer's behalf.
-- playbooks.repo_id's own foreign key to git_repos (migration 00018)
-- carries no ON DELETE clause at all (NO ACTION) and would already refuse
-- DeleteProjectedRepo below on its own; checking here is what turns that
-- into the same named, machine-readable InUseError the job-trigger case
-- gets, instead of a bare foreign-key violation surfacing from whichever
-- organization the loop happens to reach first.
SELECT EXISTS (SELECT 1 FROM playbooks WHERE org_id = @org_id::uuid AND repo_id = @repo_id::uuid);

-- name: UpdateProjectedRepo :execrows
-- One organization's projection, propagating the definition's new shared
-- columns (name, url, branch, subpath — spec decision 2) onto it. One
-- statement per organization in a loop, not a single bulk
-- UPDATE ... WHERE shared_repo_id = @shared_repo_id: a repository's own
-- propagation needs no per-row data, but Tasks 6 and 7 resolve some of
-- their own shared columns per organization (a job template's playbook_id,
-- resolved against each organization's own synced clone) and cannot be a
-- single bulk statement at all. shared.SQLStore.UpdateAndPropagate runs
-- this whole loop inside one transaction (s.d.Begin) precisely because a
-- per-organization loop does NOT get a bulk statement's built-in
-- atomicity for free — without that transaction, a failure on one
-- organization would leave every organization already updated this call
-- on the NEW value and everyone after it on the old one (Review Focus 2's
-- exact "two new and one old").
UPDATE git_repos SET name = @name, url = @url, branch = @branch, subpath = @subpath, updated_at = now()
WHERE org_id = @org_id AND shared_repo_id = @shared_repo_id::uuid;

-- name: DeleteProjectedRepo :execrows
-- One organization's projection, looped over by
-- shared.SQLStore.DeleteWithProjections inside one transaction — after
-- GitRepoHasJobTrigger and GitRepoHasPlaybook have already refused the
-- whole call if either still names any organization's projection, and
-- after ListProjectedReposForUpdate's own FOR UPDATE has held a lock on
-- every projection row since before those checks ran. That lock is what
-- makes this statement's own foreign-key violation (IsForeignKeyViolation,
-- mapped to InUseError) a backstop for a future referencing table this
-- method does not yet know to check, and nothing more: a job_triggers or
-- playbooks row inserted concurrently cannot slip in between the checks
-- and this DELETE, because inserting either one requires a FOR KEY SHARE
-- lock on this row that the FOR UPDATE already holds. Looping (rather than
-- a single bulk DELETE) is what lets that backstop, when it fires, name
-- "this organization" rather than a bulk failure naming none of them; the
-- transaction around the loop is what rolls back any organization's row
-- already deleted this call when a later one is refused.
DELETE FROM git_repos WHERE org_id = @org_id AND shared_repo_id = @shared_repo_id::uuid;

-- Task 6, migration 00061: the second application of this file's own
-- mechanism, for job templates. shared_job_templates' own column split
-- (name, shared_repo_id, playbook_path, limit, extra_vars,
-- extra_vars_schema) differs from shared_repos', so every statement below
-- is its own — nothing above is reused verbatim — but the shape (a
-- definition table, a bulk project-into-every-org insert, a single
-- project-into-one-org insert, a propagate loop, a delete loop with an
-- explicit dependant check first) is identical, by design: tx.go's own
-- comment is why withTx and propagate are plain functions over
-- *gen.Queries rather than generics, and the same reasoning is why these
-- queries do not try to parametrize over "which table" either.
--
-- playbook_id is the one column that is NOT a straight copy from the
-- definition to every projection (ProjectedTemplateColumns' own doc
-- comment, definition.go): it is resolved PER ORGANIZATION, by joining
-- that organization's own projection of shared_repo_id (its own git_repos
-- row) to its own playbooks row at playbook_path. An organization whose
-- clone has not synced that path yet (or whose shared repository
-- projection does not exist at all, which should not happen but is not
-- assumed away) simply gets NULL — migration 00061's own comment on why
-- job_templates.playbook_id had to become nullable.

-- name: CreateSharedTemplate :one
INSERT INTO shared_job_templates (id, name, shared_repo_id, playbook_path, "limit", extra_vars, extra_vars_schema)
VALUES (@id, @name, @shared_repo_id, @playbook_path, @lim, @extra_vars, @extra_vars_schema)
RETURNING *;

-- name: GetSharedTemplate :one
SELECT * FROM shared_job_templates WHERE id = @id;

-- name: ListSharedTemplates :many
-- Oldest first (uuid v7 ordering) — ProjectAllTemplatesInto's raw material
-- for a newly created organization, exactly like ListSharedRepos.
SELECT * FROM shared_job_templates ORDER BY id;

-- name: UpdateSharedTemplate :one
-- Run inside the same transaction as UpdateProjectedTemplate below
-- (shared.SQLStore.UpdateTemplateAndPropagate), for the same atomicity
-- reason UpdateSharedRepo's own comment gives.
UPDATE shared_job_templates SET
  name = @name, shared_repo_id = @shared_repo_id, playbook_path = @playbook_path,
  "limit" = @lim, extra_vars = @extra_vars, extra_vars_schema = @extra_vars_schema, updated_at = now()
WHERE id = @id
RETURNING *;

-- name: DeleteSharedTemplate :execrows
-- Only after every projection is already gone (DeleteProjectedTemplate
-- below, looped first) — shared_template_id's own ON DELETE RESTRICT
-- (migration 00061) is the backstop if a caller ever got the order wrong,
-- exactly like DeleteSharedRepo.
DELETE FROM shared_job_templates WHERE id = @id;

-- name: ListJobTemplateNamesExcludingDefinition :many
-- Decision 7's raw material for job templates: every organization's own
-- job_templates name, excluding rows that are already a projection of
-- exclude — ListGitRepoNamesExcludingDefinition's own comment, same
-- reasoning, different table.
SELECT org_id, name FROM job_templates WHERE shared_template_id IS DISTINCT FROM @exclude::uuid;

-- name: InsertProjectedTemplates :exec
-- One projection per organization, in the SAME transaction as the
-- definition's own CreateSharedTemplate (spec decision 4, atomic). The
-- lockstep-unnest shape InsertProjectedRepos uses for (ids, org_ids) is
-- joined here, per row, to that organization's own git_repos projection of
-- shared_repo_id and then to that organization's own playbooks row at
-- playbook_path — the per-organization playbook_id resolution this query
-- comment block's own header describes. LEFT JOIN, not JOIN: an
-- organization with no matching playbook yet still gets its row, with
-- playbook_id NULL (migration 00061 is what makes that legal). scope is
-- the projection's own (spec decision 2) and starts empty — "the whole
-- organization" — exactly like a repository projection starts with no
-- credential: the organization's to fill in, not something this statement
-- could ever know.
-- sqlc's own analyzer (not real Postgres, which does support it) does not
-- know the two-argument form of unnest, so the (ids, org_ids) pair is
-- zipped with WITH ORDINALITY instead of InsertProjectedRepos' own
-- single-call unnest(a), unnest(b) shape — equivalent, just spelled so
-- sqlc's catalog accepts it.
INSERT INTO job_templates (id, org_id, name, playbook_id, scope, "limit", extra_vars, extra_vars_schema, shared_template_id)
SELECT i.id, o.org_id, @name, pb.id, '{}'::jsonb, @lim, @extra_vars, @extra_vars_schema, @shared_template_id
FROM unnest(@ids::uuid[]) WITH ORDINALITY AS i(id, n)
JOIN unnest(@org_ids::uuid[]) WITH ORDINALITY AS o(org_id, n) ON i.n = o.n
LEFT JOIN git_repos g ON g.org_id = o.org_id AND g.shared_repo_id = @shared_repo_id::uuid
LEFT JOIN playbooks pb ON pb.org_id = o.org_id AND pb.repo_id = g.id AND pb.path = @playbook_path AND pb.source = 'repo';

-- name: InsertProjectedTemplate :exec
-- InsertProjectedTemplates' single-organization counterpart —
-- ProjectTemplateInto's own statement for projecting one already-existing
-- definition into one newly created organization, the scalar-subquery
-- mirror of the LEFT JOIN above.
INSERT INTO job_templates (id, org_id, name, playbook_id, scope, "limit", extra_vars, extra_vars_schema, shared_template_id)
VALUES (
  @id, @org_id, @name,
  (SELECT pb.id FROM git_repos g JOIN playbooks pb ON pb.org_id = g.org_id AND pb.repo_id = g.id
     WHERE g.org_id = @org_id::uuid AND g.shared_repo_id = @shared_repo_id::uuid
       AND pb.path = @playbook_path AND pb.source = 'repo' LIMIT 1),
  '{}'::jsonb, @lim, @extra_vars, @extra_vars_schema, @shared_template_id
);

-- name: ListProjectedTemplates :many
-- Every organization currently holding a projection of one template
-- definition, oldest projection first — ListProjectedRepos' own comment
-- and iteration-order reasoning, unchanged.
SELECT id, org_id FROM job_templates WHERE shared_template_id = @shared_template_id::uuid ORDER BY id;

-- name: ListProjectedTemplatesForUpdate :many
-- ListProjectedTemplates' own shape, with FOR UPDATE, for
-- DeleteTemplateWithProjections alone — ListProjectedReposForUpdate's own
-- comment and locking argument, unchanged.
SELECT id, org_id FROM job_templates WHERE shared_template_id = @shared_template_id::uuid ORDER BY id FOR UPDATE;

-- name: JobTemplateHasSchedule :one
-- Whether a schedule still names one organization's projection — the
-- first of the two tables DeleteTemplateWithProjections checks explicitly
-- (store_sql.go's own doc comment on that method has the full inventory,
-- including why job_template_vault_credentials is deliberately NOT checked
-- here). schedules.template_id carries its own ON DELETE CASCADE
-- (migration 00025) and would otherwise let a customer's own scheduled run
-- disappear silently along with the projection.
SELECT EXISTS (SELECT 1 FROM schedules WHERE org_id = @org_id::uuid AND template_id = @template_id::uuid);

-- name: JobTemplateHasTrigger :one
-- Whether a job trigger still names one organization's projection — the
-- second table DeleteTemplateWithProjections checks explicitly, beside
-- JobTemplateHasSchedule. job_triggers.template_id carries its own ON
-- DELETE CASCADE (migration 00026), the same silent-loss shape schedules
-- has.
SELECT EXISTS (SELECT 1 FROM job_triggers WHERE org_id = @org_id::uuid AND template_id = @template_id::uuid);

-- name: UpdateProjectedTemplate :execrows
-- One organization's projection, propagating the definition's new shared
-- columns AND re-resolving playbook_id against this organization's own
-- git_repos/playbooks rows (the definition's playbook_path or
-- shared_repo_id may have just changed, so a previously-resolved — or
-- previously-NULL — playbook_id must be re-derived, not merely copied).
-- One statement per organization in a loop inside one transaction
-- (shared.SQLStore.UpdateTemplateAndPropagate calls s.d.Begin), for
-- exactly the reason UpdateProjectedRepo's own comment gives: without that
-- transaction, a failure on one organization would leave every
-- organization already updated this call on the NEW value and the rest on
-- the old one.
UPDATE job_templates SET
  name = @name,
  playbook_id = (SELECT pb.id FROM git_repos g JOIN playbooks pb ON pb.org_id = g.org_id AND pb.repo_id = g.id
                   WHERE g.org_id = @org_id::uuid AND g.shared_repo_id = @shared_repo_id::uuid
                     AND pb.path = @playbook_path AND pb.source = 'repo' LIMIT 1),
  "limit" = @lim, extra_vars = @extra_vars, extra_vars_schema = @extra_vars_schema, updated_at = now()
WHERE org_id = @org_id::uuid AND shared_template_id = @shared_template_id::uuid;

-- name: DeleteProjectedTemplate :execrows
-- One organization's projection, looped over by
-- DeleteTemplateWithProjections inside one transaction — after
-- JobTemplateHasSchedule and JobTemplateHasTrigger have already refused
-- the whole call if either still names any organization's projection, and
-- after ListProjectedTemplatesForUpdate's own FOR UPDATE has held a lock
-- on every projection row since before those checks ran —
-- DeleteProjectedRepo's own locking argument, unchanged. The foreign-key
-- backstop (IsForeignKeyViolation, mapped to InUseError) covers a future
-- referencing table this method does not yet know to check.
DELETE FROM job_templates WHERE org_id = @org_id AND shared_template_id = @shared_template_id::uuid;

-- name: ResolveTemplatePlaybooksForNewPath :exec
-- The ORGANIZATION-side half of playbook_id resolution — a review finding
-- on Task 6's first draft, which only ever resolved on a DEFINITION-side
-- event (CreateTemplateWithProjections/UpdateTemplateAndPropagate above).
-- A provider defining a shared template before any customer has synced its
-- matching playbook is the NORMAL order (configure centrally, then
-- customers catch up, spec decision 4), not an edge case, so leaving that
-- projection's playbook_id NULL until the definition's next edit — which
-- may never come — left the feature unusable on exactly its main path.
--
-- playbooks.Service.CreateRepo calls this once a new (org, repo, path)
-- playbook is safely stored, so this organization's own shared template
-- projections waiting on exactly that pair resolve immediately, without
-- the provider touching the definition at all. repo_id is this
-- organization's OWN git_repos row (not a shared_repos definition id);
-- joining it to shared_job_templates through git_repos.shared_repo_id is
-- the same join InsertProjectedTemplate/UpdateProjectedTemplate already
-- perform, run here in the opposite direction — from a newly known
-- playbook back to every definition it satisfies, rather than from a
-- definition out to every organization.
--
-- Touches zero rows, harmlessly, when repo_id names an ordinary
-- (non-shared) repository, or no shared template happens to use this exact
-- path: both are the common case for most playbook registrations, not an
-- error.
UPDATE job_templates jt
SET playbook_id = @playbook_id::uuid, updated_at = now()
FROM shared_job_templates st, git_repos g
WHERE jt.shared_template_id = st.id
  AND jt.org_id = @org_id::uuid
  AND jt.playbook_id IS NULL
  AND g.id = @repo_id::uuid
  AND g.org_id = @org_id::uuid
  AND st.shared_repo_id = g.shared_repo_id
  AND st.playbook_path = @playbook_path::text;

-- Task 7, migration 00062: the third and last application of this file's
-- own mechanism, for check definitions. shared_check_definitions' own
-- column split (key, plugin, params, interval_seconds) differs from the
-- other two kinds', and UNLIKE BOTH of them, nothing about this one needs
-- per-organization resolution at write time: a check definition's binding
-- (binding_kind, group_id, host_id) and whether it is enabled are entirely
-- the PROJECTION's own (spec decision 2) — never derived from the
-- definition, in either direction — so ProjectedCheckColumns
-- (internal/shared/definition.go) is a pure function of the definition
-- alone, exactly like ProjectedRepoColumns, not ProjectedTemplateColumns'
-- narrower shape.
--
-- A fresh projection's binding defaults to ('org', NULL, NULL) with
-- enabled = true — the SAME default internal/checks.SeedDefinitions()
-- already uses for the built-in "Linux base" set: organization-wide and
-- immediately active, needing no per-organization state to be a complete,
-- runnable row (unlike a repository's credential or a template's resolved
-- playbook_id). The organization is free to narrow the binding to a group
-- or a host, or disable it, through the ordinary
-- PUT /orgs/{org}/checks/{id} — the same write path an organization's own
-- custom definitions already use, since binding_kind/group_id/host_id/
-- enabled are the projection's columns on every row regardless of origin.
-- This is also why check definitions need no organization-side resolution
-- trigger the way ResolveTemplatePlaybooksForNewPath exists for job
-- templates above: there is no per-organization state for a check
-- definition's projection to wait on in the first place.

-- name: CreateSharedCheckDefinition :one
INSERT INTO shared_check_definitions (id, key, plugin, params, interval_seconds)
VALUES (@id, @key, @plugin, @params::jsonb, @interval_seconds)
RETURNING *;

-- name: GetSharedCheckDefinition :one
SELECT * FROM shared_check_definitions WHERE id = @id;

-- name: ListSharedCheckDefinitions :many
-- Oldest first (uuid v7 ordering) — ProjectAllCheckDefinitionsInto's raw
-- material for a newly created organization, exactly like ListSharedRepos.
SELECT * FROM shared_check_definitions ORDER BY id;

-- name: UpdateSharedCheckDefinition :one
-- Run inside the same transaction as UpdateProjectedCheckDefinition below
-- (shared.SQLStore.UpdateCheckDefinitionAndPropagate), for the same
-- atomicity reason UpdateSharedRepo's own comment gives.
UPDATE shared_check_definitions SET
  key = @key, plugin = @plugin, params = @params::jsonb, interval_seconds = @interval_seconds, updated_at = now()
WHERE id = @id
RETURNING *;

-- name: DeleteSharedCheckDefinition :execrows
-- Only after every projection is already gone (DeleteProjectedCheckDefinition
-- below, looped first) — shared_definition_id's own ON DELETE RESTRICT
-- (migration 00062) is the backstop if a caller ever got the order wrong,
-- exactly like DeleteSharedRepo.
DELETE FROM shared_check_definitions WHERE id = @id;

-- name: ListCheckDefinitionKeysAtOrgScopeExcludingDefinition :many
-- Decision 7's raw material for check definitions, narrower than
-- ListGitRepoNamesExcludingDefinition/ListJobTemplateNamesExcludingDefinition:
-- a fresh projection ALWAYS lands at binding_kind = 'org', group_id and
-- host_id NULL (this file's own comment block above), so that is the only
-- scope a newly-created or newly-renamed shared definition could ever
-- collide at on the common path, and it is what the preflight check in
-- shared.Service.CreateCheckDefinition/UpdateCheckDefinition compares a
-- candidate key against.
--
-- This is NOT a complete collision oracle, and is documented here as a
-- known, accepted gap rather than a bug: an organization that has since
-- REBOUND its own projection to a group or a host (an ordinary write
-- through PUT /orgs/{org}/checks/{id}, which this package never sees) now
-- sits at a DIFFERENT (binding_kind, group_id, host_id) scope this query
-- does not examine, so propagating a renamed key to THAT organization could
-- still collide there without this preflight predicting it. The
-- authoritative check is, as always, the database's own UNIQUE constraint
-- (migration 00043); UpdateProjectedCheckDefinition's own unique-violation
-- branch (store_sql.go) is the backstop that turns a collision at a rebound
-- scope into the same *NameCollisionError this preflight produces for the
-- common case, one organization at a time, exactly the role
-- collisionFromRace already plays for repositories' own, narrower race
-- window. Reported as a finding about Task 2's mechanism (task-7-report.md):
-- CollidingOrgs/ExistingNames model a single flat (org, name) scope, which
-- fits a repository or a job template exactly (every projection of either
-- one always lives at the same scope), but not a check definition, whose
-- scope is itself a customer-editable column.
SELECT org_id, key FROM check_definitions
WHERE binding_kind = 'org' AND group_id IS NULL AND host_id IS NULL
  AND shared_definition_id IS DISTINCT FROM @exclude::uuid;

-- name: InsertProjectedCheckDefinitions :exec
-- One projection per organization, in the SAME transaction as the
-- definition's own CreateSharedCheckDefinition (spec decision 4, atomic) —
-- InsertProjectedRepos' own lockstep-unnest shape, unchanged. binding_kind,
-- group_id and host_id are literal ('org', NULL, NULL) rather than columns
-- this statement reads from anywhere else — this file's own comment block
-- above explains why a check definition needs no per-organization
-- resolution at all, unlike InsertProjectedTemplates' own LEFT JOIN.
-- origin is the literal 'shared' migration 00062 added to the CHECK
-- constraint.
INSERT INTO check_definitions (
  id, org_id, key, plugin, params, interval_seconds, enabled, origin, binding_kind, group_id, host_id, shared_definition_id
)
SELECT unnest(@ids::uuid[]), unnest(@org_ids::uuid[]), @key, @plugin, @params::jsonb, @interval_seconds,
       true, 'shared', 'org', NULL, NULL, @shared_definition_id;

-- name: InsertProjectedCheckDefinition :exec
-- InsertProjectedCheckDefinitions' single-organization counterpart —
-- ProjectCheckDefinitionInto's own statement for projecting one
-- already-existing definition into one newly created organization.
INSERT INTO check_definitions (
  id, org_id, key, plugin, params, interval_seconds, enabled, origin, binding_kind, group_id, host_id, shared_definition_id
)
VALUES (@id, @org_id, @key, @plugin, @params::jsonb, @interval_seconds, true, 'shared', 'org', NULL, NULL, @shared_definition_id);

-- name: ListProjectedCheckDefinitions :many
-- Every organization currently holding a projection of one check
-- definition, oldest projection first — ListProjectedRepos' own comment and
-- iteration-order reasoning, unchanged.
SELECT id, org_id FROM check_definitions WHERE shared_definition_id = @shared_definition_id::uuid ORDER BY id;

-- name: ListProjectedCheckDefinitionsForUpdate :many
-- ListProjectedCheckDefinitions' own shape, with FOR UPDATE, for
-- DeleteCheckDefinitionWithProjections. UNLIKE git_repos and job_templates,
-- nothing in this schema references check_definitions by foreign key at all
-- (migration 00062's own comment, re-derived from the live catalog, not
-- merely grepped: check_samples and check_current are deliberately keyed by
-- (org_id, host_id, check_key), never by check_definitions.id), so this
-- lock protects nothing today — DeleteCheckDefinitionWithProjections has no
-- dependant table to check before removing a projection, a legitimately
-- simpler case than the other two kinds, not an oversight. Kept anyway, for
-- the same reason ListProjectedReposForUpdate's own lock argument gives: a
-- future table that DOES reference check_definitions must find this call
-- already holding the row before it can check anything, not a second
-- migration away from being safe to add.
SELECT id, org_id FROM check_definitions WHERE shared_definition_id = @shared_definition_id::uuid ORDER BY id FOR UPDATE;

-- name: UpdateProjectedCheckDefinition :execrows
-- One organization's projection, propagating the definition's new shared
-- columns (key, plugin, params, interval_seconds — spec decision 2) onto
-- it. Deliberately does NOT touch binding_kind, group_id, host_id or
-- enabled: those are the projection's own, set by the organization itself
-- through the ordinary PUT /orgs/{org}/checks/{id}, and must survive a
-- provider's edit to the definition untouched — UpdateProjectedRepo's own
-- comment on why credential_id is left out of ITS SET list, same
-- reasoning, different columns. A unique violation here is
-- ListCheckDefinitionKeysAtOrgScopeExcludingDefinition's own documented
-- backstop for an organization that has rebound its projection away from
-- the default ('org', NULL, NULL) scope; store_sql.go's Go caller turns it
-- into a single-organization *NameCollisionError.
UPDATE check_definitions SET
  key = @key, plugin = @plugin, params = @params::jsonb, interval_seconds = @interval_seconds, updated_at = now()
WHERE org_id = @org_id AND shared_definition_id = @shared_definition_id::uuid;

-- name: DeleteProjectedCheckDefinition :execrows
-- One organization's projection, looped over by
-- DeleteCheckDefinitionWithProjections inside one transaction — after
-- ListProjectedCheckDefinitionsForUpdate's own FOR UPDATE has held a lock on
-- every projection row, even though (this file's own comment above)
-- nothing currently references check_definitions to be caught by it.
DELETE FROM check_definitions WHERE org_id = @org_id AND shared_definition_id = @shared_definition_id::uuid;
