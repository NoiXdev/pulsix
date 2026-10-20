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
