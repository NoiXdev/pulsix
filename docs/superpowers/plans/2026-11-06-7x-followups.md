# 7.x Follow-ups (7.83–7.87) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The runs host filter reads an index under row-level security; a repo playbook finds the roles its `requirements.yml` installed, and the variable source read finds them too; the review leftovers of the 7.x package (7.85), the detail views (7.86) and the variable editor (7.87) are closed.

**Architecture:** A `job_hosts` link table (RLS, composite FKs, backfilled in migration 00082) is filled by the `CreateJob` statement itself and drives a new `ListJobsForHost` query; the GIN index of 00079 is dropped. The worker exports the galaxy directory of a clone; the runner names it in the generated `ansible.cfg` `roles_path`, and the sync hands it to `internal/varsource` as a second, separately confined `os.Root`. Everything else is local fixes behind the existing layers.

**Tech Stack:** Go 1.27 (pgx v5, sqlc, huma v2, chi v5), PostgreSQL 18 + TimescaleDB, ansible-core / ansible-runner on the worker, React/TypeScript SPA (TanStack Router/Query, Vitest), Playwright E2E.

**Spec:** Backlog rows 7.83, 7.84, 7.85, 7.86, 7.87 in `docs/BACKLOG.md`, the 7.75 spec `docs/superpowers/specs/2026-11-05-variable-editor-design.md` (for 7.83's parser half and 7.87), and the Rulings section below.

## Global Constraints

- Work on the checked-out branch `fix/7.83-7.87`; do not switch branches.
- Everything in the repository is English; user-visible SPA strings in English and German (`web/src/i18n/en|de/*.ts`).
- Conventional Commits; every commit ends with exactly `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. Commit only the task's own files.
- TDD: failing test first, watched failing, then the minimal change. A "verify only" step runs the named command and records its output in the task report instead of changing code.
- Tests per package with a timeout (`go test -race -count=1 -timeout 900s ./internal/<pkg>/`; `internal/api` needs `-timeout 1800s`). Never one whole-module `go test ./...`. Full suite: `make test`.
- Before every commit: `golangci-lint run` (0 issues) and `gofmt -s -l .` (no output).
- SPA: `PATH=$HOME/.nvm/versions/node/v24.14.0/bin:$PATH`; in `web/`: `npx vitest run <file>`, `npx eslint <files>`, `npx tsc -b`; OpenAPI types via `npm run api:gen` after every API shape change (Task 5 only). No prettier.
- E2E: in `web/`, `npx playwright test e2e/<spec>.ts`.
- sqlc is not installed as a binary: `go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1 generate`, then `... diff` must be clean.
- Exactly one new migration: `internal/db/migrations/00082_job_hosts.sql`, with a working Down. Never edit a released migration. Its index drop is documented in `docs/upgrades.md` (Task 1).
- Database tests use `internal/db/testdb`. Only touch Docker containers the tests start themselves.
- File splits are pure moves: no behaviour change, tests unchanged except moved. A file this plan grows past ~400 lines gets its new code in a new file instead (named in the task).
- Backlog rows and the end-user docs in `NoiXdev/noix-docs` are updated by the controller after the branch: `reference/variable-sources.md` for Task 3 (the `role_not_found` wording, roles installed from `requirements.yml`, the `(requirements)/` path prefix) and the template variables page for Task 10 (values not sent).

## Review Focus

1. After the upgrade the host page lists runs started **before** it: the backfill links every old job to every host of its `host_ids` that still exists (Task 1, `TestJobHostsBackfill`).
2. A job whose `host_ids` repeats a host or names a host deleted in the meantime is still created, with one link per existing host and no foreign-key failure (Task 1, `TestCreateJobLinksItsHosts`).
3. A host filter never crosses organizations: listing org A's host from org B's scope is empty, and a link row pairing org B's job with org A's host is refused by the composite FK (Task 1, `TestListJobsByHostStaysInItsOrganization`).
4. A role in the playbook's own `roles/` wins over a same-named role installed from `requirements.yml`, in the run and in the parser (Task 2 `TestGalaxyRoleResolvesInRealAnsible`, Task 3 `TestRepoRoleWinsOverInstalledRole`).
5. A symlink inside the galaxy directory that points outside it is not followed by the source read, and a clone without a galaxy directory reads exactly as before (Task 3, `TestSyncExecutorDoesNotFollowSymlinksOutOfTheInstalledRoles`, `TestParseRepoWithoutInstalledRoles`).

## Rulings

Binding decisions of the owner/controller, plus the choices this plan makes where they left one open:

1. **7.84 — link table.** `job_hosts(org_id, job_id, host_id, created_at)`, `PRIMARY KEY (job_id, host_id)`, `FOREIGN KEY (org_id, job_id) REFERENCES jobs (org_id, id) ON DELETE CASCADE`, `FOREIGN KEY (org_id, host_id) REFERENCES hosts (org_id, id) ON DELETE CASCADE`, `org_id` also `REFERENCES organizations(id) ON DELETE CASCADE`, the RLS policy pair with `ENABLE` and `FORCE`. `created_at` is the job's own (immutable) creation time, copied so that the list's order and keyset cursor are read from the index: `CREATE INDEX job_hosts_host_idx ON job_hosts (org_id, host_id, created_at DESC, job_id DESC)` — the list then reads the newest 51 links of one host instead of sorting every run that host ever had. The PK serves the cascade from `jobs`; the host index serves the cascade from `hosts`.
2. **7.84 — filling.** The `CreateJob` query becomes one statement with a data-modifying CTE that inserts the job and its links, joining `hosts` so that a duplicate id is linked once and an id that names no (longer existing) host is skipped. Every insert path — `insertJobs`, `SQLStore.CreateJob`, the rotation finalizer (`rotation.go`), and test fixtures that go through `gen.CreateJob` — uses that query, so nothing can forget the links. (A trigger would also cover raw `INSERT INTO jobs`; only two test fixtures do that, and a trigger hides the rule from `jobs.sql`.) `jobs.host_ids` stays: the claim path, the run view and `ListHostsDueForTest` read it.
3. **7.84 — index 00079.** After `ListJobs` loses its host condition nothing uses `@>` on `host_ids` (`ListHostsDueForTest` uses `= ANY`, which a GIN index cannot serve), so 00082 drops `jobs_host_ids_idx`: it costs every job insert a GIN update for no reader. Dropping an index removes no data a release reads; a previous-minor manager would still answer its `@>` query correctly, only by a scan. Documented in `docs/upgrades.md`.
4. **7.84 — backfill.** One `INSERT … SELECT DISTINCT … JOIN hosts` in the migration, before the host index is built. Not batched: goose runs a migration file in one transaction, so batches inside it buy nothing, and batches across transactions (`NO TRANSACTION`) could leave a half-filled table behind a failed upgrade. The statement only reads `jobs` (ACCESS SHARE) and runs at manager start before anything else writes; its cost is one row per (job, host) pair. The migration runs with no organization bound, so the bypass policy lets it see every organization.
5. **7.84 — first test.** `docs/superpowers/notes/7.84-list-host-plan-test.go.txt` moves (`git mv`) to `internal/jobs/list_host_plan_test.go` and is Task 1's first test, retargeted to `job_hosts_host_idx` and `listJobsForHost`; its seed creates real hosts and fills `job_hosts` with the backfill statement.
6. **7.83 — run.** The generated `ansible.cfg` gets `roles_path = <galaxy dir>:/usr/share/ansible/roles:/etc/ansible/roles` (Ansible's two system defaults kept; its home default is the run's empty private HOME) for a repo playbook whose galaxy directory exists. Ansible searches the playbook's own `roles/` before `roles_path`, so the order "after the playbook's own roles" is Ansible's. Config file only, no `ANSIBLE_ROLES_PATH`: `ANSIBLE_CONFIG` already points at the generated file. With a play uid the galaxy directory is shared read-only like the clone. Collections installed by the same `-p` are out of scope; Task 2 records where `ansible-galaxy install -r … -p` puts them so the controller can file a follow-up.
7. **7.83 — parser.** `varsource.ParseRepoWithRoles(ctx, fsys, roles, commit)` takes the galaxy directory as its own `fs.FS`; the worker opens it as a second `os.Root` with the clone's symlink rules (`cloneFS`). A plain role name is looked up in `<dir>/roles`, then `roles/`, then the installed roles. Files read from there are named `(requirements)/<role>/…` in notes and file lists; internally the walk keys them apart from repo paths, so a repo directory called `(requirements)` cannot be confused with them.
8. **7.85–7.87.** Every item. Interpretations: the dropped-forgot log line is asserted at the `accounts` level through an injected logger (the API test cannot see the global logger without a race); the "real screen-reader check" is a Playwright accessible-description assertion in the hosts journey plus a manual VoiceOver pass the agent cannot do (reported as open for the owner); 7.87's "a page-wide test flag" is read as the channel page's shared Send-test in-flight flag (7.86) — the controller confirms, otherwise it stays in the backlog.
9. **`web/e2e/ansible.spec.ts`.** Its labels are fixed so the journey would pass with a worker. Adding a worker to the compose E2E stack is **not trivial** and out of scope: the manager runs as a host binary, so a worker container needs host-gateway networking, enrolment, and the SSH, bastion and git fixtures (see the comment in `.github/workflows/ci.yml` after the `e2e-ansible` job).

---

### Task 1: The runs host filter reads `job_hosts` (7.84)

**Files:** Move `docs/superpowers/notes/7.84-list-host-plan-test.go.txt` → `internal/jobs/list_host_plan_test.go`. Create `internal/db/migrations/00082_job_hosts.sql`, `internal/db/job_hosts_backfill_test.go`, `internal/jobs/store_sql_list.go` (`ListJobs` moves here from `store_sql.go`, which is 452 lines). Modify `internal/db/queries/jobs.sql` (`CreateJob` ~1, `ListJobs` ~41, new `ListJobsForHost`) + `internal/db/gen`, `internal/jobs/store_sql.go`, `internal/jobs/rotation.go` (~489, only if the generated row type of `CreateJob` changes), `internal/jobs/store_sql_test.go` (`TestSQLStoreListJobsByHost` ~105), `internal/backup/fixture_test.go` (one `job_hosts` row after the jobs at ~55), `docs/upgrades.md` (Migrations section, after the 00079 paragraph).

**Interfaces:**
- Migration 00082 per Rulings 1, 3, 4, in this order: table, RLS pair, backfill, `job_hosts_host_idx`, `DROP INDEX jobs_host_ids_idx`. Down: recreate `jobs_host_ids_idx` (`USING gin (host_ids)`), drop the table. The backfill (also used by the plan test's seed):
  ```sql
  INSERT INTO job_hosts (org_id, job_id, host_id, created_at)
  SELECT DISTINCT j.org_id, j.id, h.id, j.created_at
  FROM jobs j
  CROSS JOIN LATERAL unnest(j.host_ids) AS t(host_id)
  JOIN hosts h ON h.org_id = j.org_id AND h.id = t.host_id;
  ```
- `CreateJob :one`: `WITH j AS (INSERT INTO jobs (…) VALUES ($1 … $15) RETURNING *), linked AS (INSERT INTO job_hosts … the same SELECT over j instead of jobs) SELECT * FROM j;` — same parameters as today. If sqlc now generates a `CreateJobRow`, callers keep discarding it.
- `ListJobs` loses its `host_id` parameter and condition (doc comment updated). `ListJobsForHost :many`: `SELECT sqlc.embed(j) FROM job_hosts jh JOIN jobs j ON j.org_id = jh.org_id AND j.id = jh.job_id WHERE jh.org_id = sqlc.arg(org_id) AND jh.host_id = sqlc.arg(host_id)` then the same optional filters as `ListJobs` on `j.` columns in the same order, the cursor as `(jh.created_at, jh.job_id) < (cursor_at, cursor_id)`, `ORDER BY jh.created_at DESC, jh.job_id DESC LIMIT sqlc.arg(row_limit)`. Its doc comment says why the host filter is its own query (RLS keeps the non-leakproof `@>` out of an index scan; uuid equality is leakproof, backlog 7.84).
- `SQLStore.ListJobs(ctx, orgID, f Filter, cursor, limit)` keeps its signature and calls `ListJobsForHost` when `f.HostID != nil`; one row mapper serves both.
- Test helper `seedHost(t *testing.T, d *db.DB, org, zone uuid.UUID, name string) uuid.UUID` in `store_sql_test.go` (raw insert like `seedZone`, address `10.0.0.1`).

- [ ] **Step 1: The measuring test, moved and retargeted.** `git mv` the note; rename the test `TestListJobsHostFilterUsesTheLinkIndexUnderAGenericPlan`; `hostFilterIndex = "job_hosts_host_idx"`; `generatedQuery(t, "listJobsForHost")`; the seed inserts the 200 host ids as real `hosts` rows (`name` `h-<n>`, the seed's zone) before the jobs, then runs the backfill statement and `ANALYZE jobs, job_hosts`; args comment and order follow `gen.ListJobsForHostParams` (org, host, zone, status, kind, template, run group, trigger, trigger ref, cursor at, cursor id, limit). Doc comment: backlog 7.84, why the GIN index never served the app role.
- [ ] **Step 2:** `go test -race -count=1 -timeout 900s -run TestListJobsHostFilter ./internal/jobs/` — FAIL (`const listJobsForHost not found`).
- [ ] **Step 3: More failing tests:**
  - `TestJobHostsBackfill` (internal/db, the `TestBackupNotifiedBackfill` pattern: fresh database, `MigrateTo(ctx, 81)`): one org, zone, hosts A and B; job 1 `{A,B}`, job 2 `{A,<random id>,A}`, job 3 `{}`; `Migrate`; `job_hosts` is exactly `{(1,A),(1,B),(2,A)}`, each `created_at` equal to its job's; `pg_indexes` has `job_hosts_host_idx` and no `jobs_host_ids_idx`.
  - `TestCreateJobLinksItsHosts` (jobs, DB): `store.CreateJob` with `HostIDs {A, A, B, ghost}` succeeds; links exactly `{A, B}` with the job's `created_at`; a job with no hosts has no links.
  - `TestSQLStoreListJobsByHost` rewritten on `seedHost` hosts, same assertions, plus paging: limit 1 returns the newest and a cursor, the next page returns the older one and no cursor.
  - `TestListJobsByHostStaysInItsOrganization`: org B's scope listing org A's host returns nothing; a superuser `INSERT INTO job_hosts` pairing org B's job with org A's host fails with SQLSTATE `23503`.
  - `TestJobHostsFollowTheirJobAndHost`: deleting host A (`gen.DeleteHost`) removes its links, keeps the job and its `host_ids`; a superuser `DELETE FROM jobs WHERE id = …` removes that job's links; deleting the organization leaves no `job_hosts` row of it.
- [ ] **Step 4:** run `./internal/jobs/` and `-run TestJobHostsBackfill ./internal/db/` — FAIL.
- [ ] **Step 5:** implement migration, queries, store; regenerate sqlc; add the backup fixture row; write the `docs/upgrades.md` paragraph: 00082 adds `job_hosts`, fills it once from the run history (time grows with runs × hosts, same advice as 00079: upgrade outside busy automation windows) and drops `jobs_host_ids_idx`, which nothing reads any more (an index drop removes no data; a previous-minor manager still lists by host correctly, only slower).
- [ ] **Step 6:** `./internal/jobs/` (plan test logs the generic plan and passes), `./internal/db/` (`TestEveryOrgOwnedTableForcesRowLevelSecurity` covers the new table unchanged), `./internal/backup/`, `./internal/api/` (1800 s) pass; sqlc diff clean; with `-short` the plan test is skipped.
- [ ] **Step 7: Commit** `perf(jobs): list a host's runs through a job_hosts link table` (migration, queries, store, tests, fixture, upgrades note).

### Task 2: Roles installed from `requirements.yml` are on the run's roles path (7.83, run)

**Files:** Modify `internal/worker/sync.go` (`installRequirements` ~238 uses the new helper), `internal/worker/runner/playbook.go` (`materializePlaybook`), `internal/worker/runner/inventory.go` (`materializeRunnerConfig` ~265), `internal/worker/runner/layout.go` (`ansibleCfgText` ~106), `internal/worker/runner/runner.go` (`Run` ~165-172), every test caller of the three changed signatures (`unsafe_test.go`, `runner_test.go`, …). Create `internal/worker/runner/galaxy_roles_test.go`.

**Interfaces:**
- `func GalaxyDir(cloneDir string) string` in `internal/worker/sync.go`: `<parent>/<base>.galaxy` (the path `installRequirements` already uses).
- `(r *Runner) materializePlaybook(ctx, job, env) (playbook, rolesDir string, err error)`: `rolesDir` is `worker.GalaxyDir(clone)` for a repo playbook when `os.Lstat` finds a directory there (a symlink or anything else: `""`); `""` for every other kind. With `env.Config.PlayUID != 0` and `rolesDir != ""` it calls `ShareRepoRead(env.RepoCacheDir, rolesDir)` after the clone's share.
- `(r *Runner) materializeRunnerConfig(job, env, rolesDir string) error`; `ansibleCfgText(workDir string, vaultFiles []vaultFile, rolesDir string) string` writes `roles_path = <rolesDir>:/usr/share/ansible/roles:/etc/ansible/roles` in `[defaults]` when `rolesDir != ""` and no `roles_path` line otherwise.

- [ ] **Step 1: Failing tests** in `galaxy_roles_test.go`:
  - `TestAnsibleCfgRolesPath` table: `rolesDir "/c/o/r.galaxy"` → the text contains the line `roles_path = /c/o/r.galaxy:/usr/share/ansible/roles:/etc/ansible/roles`; `""` → no `roles_path`.
  - `TestRepoRunNamesTheGalaxyDirInItsConfig` (`gitFixture`, `runJob`, `runEnv` from the existing tests): with `<cache>/<org>/<repo>.galaxy` created before `materializePlaybook`, the written `ansible.cfg` names it; without it, no `roles_path`; with a symlink at that path, no `roles_path`; an inline playbook never gets one.
  - `TestGalaxyRolesAreReadableByThePlayUID`: `PlayUID 12345` and a galaxy role file → every directory from the role file up to `env.RepoCacheDir` is group-readable and -traversable, none group-writable (the `TestRepoPlaybookIsReadableByThePlayUID` walk).
  - `TestGalaxyRoleResolvesInRealAnsible` (`requireAnsiblePlaybook`): repo `playbooks/site.yml` with `gather_facts: false` using roles `galaxy_only` and `shadowed`; `playbooks/roles/shadowed/tasks/main.yml` prints `from-repo`, the galaxy dir holds `galaxy_only` (prints `from-galaxy`) and `shadowed` (prints `from-galaxy-shadow`). After `materializePlaybook` and `materializeRunnerConfig`, `ansible-playbook -i localhost, -c local <playbook>` with `r.subprocessEnv(env, bin)` exits 0, its output has `from-galaxy` and `from-repo` and not `from-galaxy-shadow`. Control: the same run with the galaxy dir removed before `materializeRunnerConfig` exits non-zero naming `galaxy_only`.
- [ ] **Step 2:** `go test -race -count=1 -timeout 900s -run 'RolesPath|GalaxyDir|GalaxyRole' ./internal/worker/runner/` — FAIL (compile errors on the new signatures count).
- [ ] **Step 3:** implement; `Run` passes `rolesDir` from `materializePlaybook` to `materializeRunnerConfig`. Record in the task report where `ansible-galaxy install -r requirements.yml -p <dir>` (ansible-core 2.21) puts collections (Ruling 6).
- [ ] **Step 4:** `./internal/worker/...` pass (900 s), and `PULSIX_E2E_REAL_ANSIBLE=1 go test -race -count=1 -timeout 1800s -run 'UnsafeMarker|RealAnsible' ./internal/worker/runner/...` passes locally (CI runs this line).
- [ ] **Step 5: Commit** `fix(worker): put the roles installed from requirements.yml on the run's roles path`.

### Task 3: The source read finds roles installed from `requirements.yml` (7.83, parser)

**Files:** Modify `internal/varsource/discover.go` (`ParseRepo` ~33), `internal/varsource/playbook.go` (`parser` ~160), `internal/varsource/roles.go` (`findRole`, `useRole`, `spec`, `readRole`), `internal/varsource/files.go` (`walk.read`), `internal/varsource/doc.go`, `internal/worker/clonefs.go`, `internal/worker/sync_sources.go` (`parseRepoFunc`, `collectVarSources`), `internal/worker/sync.go` (`parseRepo` field doc). Create `internal/varsource/roles_installed_test.go`, `internal/worker/sync_sources_galaxy_test.go`.

**Interfaces:**
- `func ParseRepoWithRoles(ctx context.Context, fsys, roles fs.FS, commit string) Report`; `ParseRepo(ctx, fsys, commit)` calls it with `roles == nil`. `parser` gains `roles fs.FS`.
- `const InstalledRolesPrefix = "(requirements)/"` — how a path read from `roles` is shown.
- `findRole`: a plain name's candidates are `<dir>/roles/<name>`, `roles/<name>` (dir ≠ "."), then `<name>` in `roles` when non-nil (found there: `roleTarget.installed = true`); a slash name is never looked up in `roles`; the collection rule stays last. Not found with `roles` set: where reads `at <candidates> or among the roles installed from requirements.yml`.
- Reads of an installed role's spec go to `roles` (`w.read`/`w.spec` take the target's fs); the spec memo, `roleKey` and `FileUse`/note paths use the shown path `InstalledRolesPrefix + <role dir>/meta/<file>`.
- Worker: `type parseRepoFunc func(ctx context.Context, fsys, roles fs.FS, commit string) varsource.Report`; `func openInstalledRolesFS(cloneDir string) (*cloneFS, error)` opens `GalaxyDir(cloneDir)` as its own root — `(nil, nil)` when it does not exist; any other error: logged, `varsource.ReadFailed("the roles installed from requirements.yml could not be opened; roles found only there are reported as not found")` joins the report's notes and the read goes on without it.

- [ ] **Step 1: Failing tests:**
  - `TestInstalledRoleIsRead` (`fstest.MapFS` repo: `site.yml` uses role `acme.web`; roles fs: `acme.web/meta/argument_specs.yml` declaring `web_port` int): the playbook has field `web_port`, its `RoleUse` state `read`, `Files` contains `(requirements)/acme.web/meta/argument_specs.yml`.
  - `TestRepoRoleWinsOverInstalledRole`: `roles/common` in the repo declares `a`, the installed `common` declares `b` → fields `[a]` only.
  - `TestInstalledRolesNotSearchedForPaths`: role `x/y` exists only in the roles fs → `not_found`.
  - `TestRoleNotFoundNamesInstalledRoles`: the `role_not_found` message contains `requirements.yml` with a roles fs and does not without one.
  - `TestParseRepoWithoutInstalledRoles`: `ParseRepo` and `ParseRepoWithRoles(…, nil, …)` return equal reports for a repo with roles.
  - Worker: `TestSyncExecutorReadsRolesInstalledFromRequirements` (a clone dir and its `.galaxy` sibling holding the role spec; the real `varsource` parser) → the report's playbook has the role's field; `TestSyncExecutorDoesNotFollowSymlinksOutOfTheInstalledRoles` (an installed role's `meta/argument_specs.yml` is a symlink to a file outside the galaxy dir declaring `leaked`) → no field `leaked`, the role is `unreadable`; `TestSyncExecutorWithoutInstalledRoles` → same report as before the change.
- [ ] **Step 2:** `go test -race -count=1 -timeout 900s ./internal/varsource/ ./internal/worker/` — FAIL.
- [ ] **Step 3:** implement; `doc.go` and the `sync_sources.go` header name the installed roles and their own root.
- [ ] **Step 4:** both packages pass; `./internal/e2e/` passes (1800 s; its fixtures have no `requirements.yml`).
- [ ] **Step 5: Commit** `feat(varsource,worker): read the argument specs of roles installed from requirements.yml`.

### Task 4: 7.85 backend hardening

**Files:** Modify `internal/api/request_log.go` (`isInviteTokenPath`). Tests `internal/api/request_log_internal_test.go`, `internal/jobs/run_only_host_test.go`.

**Interfaces:** `isInviteTokenPath(p string) bool` is true when the cleaned path matches as today **or** the path's own segments (empty and `.` dropped, `..` kept as a segment, no resolution) are `invite` (case-insensitive) followed by at least one more segment.

- [ ] **Step 1: Failing tests:**
  - `TestRedactRequestURI` rows: `/invite/tok/..` → `/invite/[redacted]`; `/invite/tok/../` → same; `/invite/tok/..?x=1` → same; `/a/../invite/tok` → same; `/invite/..` → same; `/invite/./` → unchanged. `TestRequestLoggerNeverRecordsAnInviteToken` also serves `/invite/tok_secret_123/..` and finds no `tok_secret_123` in the log.
  - `TestEnqueueRunWithOnlyHost` subtests: `host in the excluded hosts under an exclusion-only group limit` (`newOnlyHostFixture(t, "!dbservers")`, `ExcludeHosts {A}`, `OnlyHost A`) → `ErrAllHostsExcluded`, no job stored; `exclusion-only group limit the host is not in` (`"!dbservers"`, `OnlyHost A`) → one job targeting A, sealed limit `!dbservers:&web-01`. Doc comment: the matcher decides on host names, so a group the host is in is Ansible's to decide at run time (limit.go's fail-open contract).
- [ ] **Step 2:** `-run 'TestRedact|TestRequestLogger' ./internal/api/` (1800 s) and `-run TestEnqueueRunWithOnlyHost ./internal/jobs/` — the redaction rows fail. The two run subtests are expected to pass against today's code; if one starts a job, that is the bug: fix it in `resolveRunTargets` and say so.
- [ ] **Step 3:** implement.
- [ ] **Step 4:** both packages pass.
- [ ] **Step 5: Commit** `fix(api): keep an invitation token out of the log when a dot-dot segment follows it` and `test(jobs): pin a narrowed run under an exclusion-only group limit`.

### Task 5: 7.87 backend: values not sent, the legacy reseal, one read of a playbook's source

**Files:** Modify `internal/inventory/schema/secrets.go` (+ `secrets_test.go`), `internal/app/template_lookup.go` (`dropStraySealed` ~134, `topLevel` moves to schema), `internal/templates/review.go` (`Review`, `review`), `internal/templates/variables.go` (`buildKeepingValues` ~235), `internal/api/template_variables_handlers.go` (`templateVariablesReviewDTO` ~50, mapper ~70), `internal/db/queries/var_sources.sql` (+ gen), `internal/repos/sources_store_sql.go`, `internal/repos/service.go` (Store ~223), `internal/repos/sources.go` (`PlaybookVarSource` ~265), repos fakes, `web/src/api/schema.d.ts` (regenerated). Tests `internal/templates/settings_save_test.go`, `internal/templates/variables_test.go`, `internal/api/template_variables_readd_test.go`, `internal/repos/sources_store_sql_test.go`, `internal/repos/sources_test.go`.

**Interfaces:**
- `func StrayTopLevel(fields []Field, values map[string]any) []string` in `schema`: the top-level names under which `StraySealed` finds a value, in field order, each once. `dropStraySealed` uses it (log line unchanged).
- `templates.Review.NotSent []string`; DTO `NotSent []string \`json:"not_sent" doc:"Variables a run leaves out until a person enters their value again: each holds a stored secret under a field that is no longer a secret"\``, always an array (`[]` when none).
- `func (s *Service) keepsValues(aad []byte, cur, next map[string]any) bool` replaces `sameJSON(t.ExtraVars, cur.ExtraVars)` in `buildKeepingValues`: equal JSON everywhere, except that a path where `cur` holds a plain string and `next` a sealed envelope counts as equal when the envelope opens under `aad` to that string (the legacy plaintext a save reseals, backlog 2.47).
- Query `GetPlaybookVarSource :one`: the `repo_var_sources` row `LEFT JOIN repo_playbook_sources p ON p.org_id = r.org_id AND p.repo_id = r.repo_id AND p.path = sqlc.arg(path)` in one statement. Store method `GetPlaybookVarSource(ctx context.Context, orgID, repoID uuid.UUID, path string) (SourcesRecord, *varsource.Playbook, error)` (ErrNotFound without a record, nil playbook without a row) replaces `GetPlaybookSource` in the Store interface; `PlaybookVarSource` makes this one call.

- [ ] **Step 1: Failing tests:**
  - `TestStrayTopLevel` table: a field `token` now `string` holding an envelope → `["token"]`; a repeater `vhosts` whose sub-field `password` is now `string`, two items with envelopes → `["vhosts"]`; a secret field's own envelope → `[]`.
  - `TestVariablesReviewNamesValuesNotSent` (templates, fakes): `Variables(...)` of such a template → `Review.NotSent == ["token"]`.
  - API `TestTemplateVariablesShowNotSentValues`: GET variables → `review.not_sent == ["token"]`; a template without strays → `[]`.
  - `TestSettingsSaveKeepsAMissingRequiredValueOverALegacySecret`: the 7.87 fixture of `TestSettingsSaveKeepsAMissingRequiredValue`, but the store holds `token: "hunter2"` in plaintext; a rename echoing the keep marker succeeds, and the stored `token` is now an envelope that opens to `hunter2`; a rename sending `token: "other"` is refused with problems `["release"]`.
  - `TestGetPlaybookVarSource` (DB): record and row → both; record without that path → record and nil; no record → `ErrNotFound`. `TestPlaybookVarSourceReadsOnce` (fake store counting calls): exactly one `GetPlaybookVarSource`, no `GetVarSources`.
- [ ] **Step 2:** run `./internal/inventory/schema/`, `./internal/templates/`, `./internal/repos/`, `./internal/app/`, `-run TemplateVariables ./internal/api/` — FAIL.
- [ ] **Step 3:** implement; regenerate sqlc; `npm run api:gen`; `npx tsc -b`.
- [ ] **Step 4:** the packages pass; sqlc diff clean.
- [ ] **Step 5: Commit** `fix(templates,api): name the variables a run leaves out`, `fix(templates): keep a settings save working over a legacy plaintext secret`, `fix(repos): read a playbook's source and its record in one statement`.

### Task 6: Backend test hygiene (7.85, 7.87)

**Files:** Modify `internal/accounts/service.go` (`NewService` ~194), `internal/accounts/reset.go` (~69, ~76), `internal/auth/token_test.go` (~137). Tests `internal/accounts/reset_test.go`. Splits: `internal/worker/runner/gitrepo_test.go` → `TestFetchRepoEnforcesPinnedGitServerKey`, `TestFetchRepoRejectsBadSpecs`, `TestFetchRepoErrorTextNeverNamesTheURL`, `TestFetchRepoNeverPassesOptionShapedArguments` move to new `gitrepo_guard_test.go`; `internal/accounts/store_sql_actions_test.go` → `TestResetFlowSQL`, `TestResetConcurrentWithLinkRequestsSQL` move to new `store_sql_reset_test.go`.

**Interfaces:** `type Option func(*Service)`; `func WithLogger(l *slog.Logger) Option`; `NewService(store, mailer, publicURL, now, rnd, opts ...Option)` (callers unchanged); the two drop lines log through the service's logger (`slog.Default()` when none). Test helper `tamperToken(plain string) string` in `token_test.go`: replaces the first character after `pxt_` with `A`, or `B` when it is `A` — every bit of that character is significant, while the 43rd base64url character of 32 bytes carries two unused bits, which is why "zz" at the end could decode to the same secret.

- [ ] **Step 1: Failing tests:** `TestRequestResetLogsADroppedRequest`: a service with `WithLogger` over a buffer, mail configured, `Shutdown`, then `RequestReset(ctx, "drop@example.test")` returns nil; the buffer has `password reset request dropped: shutting down` and not `drop@example.test`. `TestTamperTokenAlwaysChangesTheSecret`: for `pxt_A…`, `pxt_B…` and 1 000 freshly created tokens, the base64url-decoded secret of `tamperToken(p)` differs from `p`'s; `TestTokenCreateResolveRevoke` uses `tamperToken`.
- [ ] **Step 2:** `./internal/accounts/`, `./internal/auth/` — the logger test fails to compile.
- [ ] **Step 3:** implement; the splits as pure moves.
- [ ] **Step 4:** `./internal/accounts/`, `./internal/auth/`, `./internal/worker/runner/` pass with the same test count as before for the split packages (`go test -list . <pkg> | wc -l`); every touched file ≤ ~400 lines; `go test -race -count=200 -run TestTokenCreateResolveRevoke ./internal/auth/` passes.
- [ ] **Step 5: Commit** `test(accounts,auth): assert the dropped reset's log line and tamper tokens where every bit counts` and `refactor(test): split the git fetch and reset store tests`.

### Task 7: 7.85 SPA

**Files:** `web/src/features/hosts/HostDialog.tsx` (~33, ~47), `web/src/features/hosts/HostsListPage.tsx` (~184), `web/src/features/hosts/useHostActions.tsx` (~81), `web/src/api/hooks/useMe.ts` (`useSettledMe`), `web/src/routes/RootRedirect.tsx`, `web/src/routes/EnrolmentLayout.tsx`, `web/src/features/account/ChangePasswordPage.tsx`, `web/src/ui/list-row.tsx`, every `ListRow` caller (`AgentRow`, `GroupsPage`, `ChildrenSection`, `MemberRow`, `UsersPage`, `OrganizationsPage`, `SharedListFrame`, `MembersPage`, `HostRow`, `HostRunsSection`, `RunGroupSection`, `RunRow`), `web/src/features/settings/SettingsLayout.test.tsx` (~49), `web/src/features/members/MemberPage.test.tsx`, delete `web/src/test/waitForQuery.ts`, `web/e2e/hosts.spec.ts`. Tests `HostDialog.test.tsx`, `RootRedirect.test.tsx`, `EnrolmentLayout.test.tsx`, `ChangePasswordPage.test.tsx`, `list-row.test.tsx`.

**Interfaces:**
- `HostDialog({ org, role, open, onOpenChange, host }: HostDialogProps & { role: string | undefined })`; the loader takes `role` and no longer calls `useOrg`.
- `useSettledMe` adds `networkMode: 'always'`, so an offline fetch fails instead of pausing. `ChangePasswordPage` and `EnrolmentLayout` render `<QueryError error={me.error} />` when `settled && me.isError`; `RootRedirect` keeps `/login` for `errorStatus(me.error) === 401` and renders `QueryError` for any other error.
- `export function ListRowName({ className, children }: { className?: string; children: ReactNode })` in `ui/list-row.tsx` — `<span aria-hidden="true" className={className}>`: the visible name cell, hidden from the link's description because the label already names the link. Every caller wraps exactly its name text in it.

- [ ] **Step 1: Failing tests:**
  - `HostDialog.test.tsx` `asks nothing about the organization`: rendered with `role="admin"`, no request to `/api/v1/orgs/acme`; the jump host field is offered; with `role="viewer"` it is not.
  - The three settled-me screens: `offline, shows the failure instead of waiting` — `onlineManager.setOnline(false)`, `/auth/me` rejects with `new TypeError('Failed to fetch')` → the `QueryError` alert is shown; RootRedirect does not land on `/login`. `afterEach` restores online.
  - `list-row.test.tsx`: a row with label `web-01` and cells `<ListRowName>web-01</ListRowName>` + `10.0.0.1` → `getByRole('link', { name: 'web-01' })` has accessible description `10.0.0.1` (not containing `web-01`).
  - `hosts.spec.ts`: the created host's row link `toHaveAccessibleName(host)` and its description does not contain the host name but contains its zone.
- [ ] **Step 2:** run the files — FAIL.
- [ ] **Step 3:** implement; replace `waitForQuery` in the two tests with waiting for what the answer renders (e.g. `await screen.findByRole(...)` of an element the gating query brings), then the absence assertions; delete the helper.
- [ ] **Step 4:** `npx vitest run src/features src/routes src/ui` (ListRow is everywhere); eslint; tsc; `npx playwright test e2e/hosts.spec.ts`. The manual VoiceOver pass over the hosts list is listed in the report for the owner.
- [ ] **Step 5: Commit** `fix(web): host dialog takes the role, settled screens fail offline, list rows read their name once`.

### Task 8: 7.86 inventory and lists

**Files:** `web/src/api/hooks/useInventory.ts` (`useReloadInventory` ~48), `web/src/features/inventory/sections/ValuesSection.tsx` (`ValuesForm`), `web/src/api/hooks/useNotifications.ts` (`useTestChannel` ~54), `web/src/features/notifications/ChannelsPage.tsx` (~36, ~113), `web/src/features/checks/CheckRow.tsx`, `web/src/i18n/en|de/checks.ts`, `web/src/features/fleet/FleetPage.tsx` (`FleetRow`), `web/e2e/fleet.spec.ts` (row lookup by `data-list-row`, if it reads the old grid row). Tests new `web/src/api/hooks/useReloadInventory.test.tsx`, `GroupDetailPage.test.tsx`, `ChannelsPage.test.tsx`, `ChecksPage.rows.test.tsx`, `FleetPage.test.tsx`.

**Interfaces:**
- `useReloadInventory(org)` → `async (groupId)`: starts `invalidateQueries(['inventory', org])` without awaiting or throwing, and returns `(await qc.fetchQuery({ queryKey: groupKey(org, groupId), queryFn: () => fetchGroup(org, groupId), staleTime: 0 })).group` — only the group's own failure rejects.
- `ValuesForm` keeps `baseRevision` (state, initially `group.revision`) and saves with it; `onReload` sets it to the reloaded group's revision. A background refetch never moves it.
- `useTestChannel(org)` gets `mutationKey: ['notification-channel-test', org]`; `ChannelsPage` reads the pending ids with `useMutationState({ filters: { mutationKey: [...], status: 'pending' }, select: (m) => m.state.variables as string })` and passes `testing={pending.includes(c.id)}`.
- `export function bindingText(check: CheckDefinition, groups: MonitoringGroup[], t: Messages): string` in `CheckRow.tsx` (host name, group name or fallback, `t.checks.organization`); `Binding` shows it; the menu label is `t.checks.rowActions(c.key, bindingText(...))` — en `Actions for ${key} (${binding})`, de `Aktionen für ${key} (${binding})`.
- `FleetRow` is a `ListRow` to `/o/$org/hosts/$id` with `label={host.name}`, its name in `ListRowName`, no inner `Link`.

- [ ] **Step 1: Failing tests:** `useReloadInventory.test.tsx` — the groups list answering 500 still resolves the fresh group; the group itself answering 500 rejects. `GroupDetailPage.test.tsx` `a refetch that only bumps the revision keeps the save on the revision the draft started from` — group at revision 3 with a masked secret; a refetch answers revision 4 with the same masked values; edit a plain value, Save → PATCH `If-Match: "3"`; answer 412 → stale notice; Reload, Save → `If-Match: "4"`. `ChannelsPage.test.tsx` — while channel A's test request is held, A's item reads the in-flight label and B's "Send test" is enabled. `ChecksPage.rows.test.tsx` — two `cpu` checks bound to the organization and to host `web-01` have menus `Actions for cpu (Organization)` and `Actions for cpu (web-01)`. `FleetPage.test.tsx` — `getByRole('link', { name: 'web-01' })` points at `/o/acme/hosts/<id>`; each row holds exactly one `<a>`.
- [ ] **Step 2:** run the files — FAIL.
- [ ] **Step 3:** implement.
- [ ] **Step 4:** the files plus `src/features/inventory`, `src/features/notifications`, `src/features/checks`, `src/features/fleet` pass; eslint; tsc; `npx playwright test e2e/fleet.spec.ts`.
- [ ] **Step 5: Commit** `fix(web): inventory reload and stale saves, per-channel test state, unique check menus, fleet rows as list rows`.

### Task 9: 7.86 layout and test noise

**Files:** `web/src/ui/detail-layout.tsx` (`useCurrentSection` ~27, `DisabledHint` ~231), `web/src/test/setup.ts`. Tests `web/src/ui/detail-layout.test.tsx`.

**Interfaces:** `useCurrentSection` keeps `handled` together with the pathname it belongs to (`{ path: string; ids: string[] }`); a render whose `window.location.pathname` differs starts a fresh list, so the same hash on another page is acted on again. `DisabledHint` is `role="note"` with `aria-labelledby` on its title (`useId`). `setup.ts` stubs `window.scrollTo` (and `Element.prototype.scrollTo` when missing) as no-ops, with a comment like the other jsdom gaps (TanStack Router's scroll restoration calls it).

- [ ] **Step 1: Failing tests:** `acts on the same hash again on another page` — at `/groups/a#members` the section is scrolled into view once; after `history.replaceState(null, '', '/groups/b#members')` and a rerender it is scrolled again; a rerender on `/groups/b` does not scroll a third time. `DisabledHint` — `getByRole('note', { name: 'No agent' })`.
- [ ] **Step 2:** `npx vitest run src/ui/detail-layout.test.tsx` — FAIL.
- [ ] **Step 3:** implement.
- [ ] **Step 4:** the file passes; `npx vitest run src/features/runs src/features/checks 2>&1 | grep -c "Not implemented"` prints `0` (73 in the runs suite before); eslint; tsc.
- [ ] **Step 5 (verify only):** the viewer can browse the schema: `npx vitest run src/features/variables/VariableEditor.test.tsx src/features/inventory/GroupDetailPage.test.tsx` passes and covers a read-only editor whose field opens a read-only dialog (the group page uses `VariableEditor readOnly={!canWrite}` since 7.75); if no test opens a field read-only, add `a viewer opens a field and sees its attributes without editing them` to `VariableEditor.test.tsx`. The WebKit container-query item is obsolete: `grep -rn "@container\|container-type\|@\[" web/src` finds nothing (the 7.75 editor replaced the builder that had it); record the output.
- [ ] **Step 6: Commit** `fix(web): act on a deep link per page, give the disabled hint a role, silence jsdom's scrollTo`.

### Task 10: 7.87 SPA

**Files:** `web/src/features/templates/variables/TemplateVariablesPage.tsx`, `web/src/features/templates/variables/ReviewBadges.tsx`, `web/src/i18n/en|de/templates.ts` (`review`, `variables`), `web/src/features/variables/origin.ts` (`editLocked` ~61), `web/src/features/variables/fieldEditor.ts` (`formatAttribute` ~27). Tests `TemplateVariablesPage.test.tsx`, `ReviewBadges` cases in the template list test, `origin.test.ts`, `VariableEditor.subfields.test.tsx`, `fieldEditor.test.ts`.

**Interfaces:**
- Strings: `t.templates.review.notSent(n)` en `${n} not sent to runs` / de `${n} nicht an Läufe übergeben`; `t.templates.variables.notSent(names: string)` en `Not sent to runs until a value is entered again: ${names}. Each holds a stored secret under a field that is no longer a secret.` / de `Wird erst wieder an Läufe übergeben, wenn ein Wert neu eingegeben wird: ${names}. Jede hält ein gespeichertes Geheimnis unter einem Feld, das kein Geheimnis mehr ist.` The page shows the notice (warning style of its other notices) when `review.not_sent` is non-empty, names joined with `, `; `ReviewBadges` adds a warn pill.
- `editLocked(prev, next)`: additionally, when `prev` is `adjusted` with a `source_field` and `next`'s content equals that source version (`sameContent(next, { ...content(prev.source_field), name: next.name })`), it returns `resetToSource({ ...next, source_field: prev.source_field })` — so reverting inline and reverting in the dialog both end at `source`.
- `formatAttribute('item', v)`: an `enum` item with options → `enum (a, b)`; otherwise its type as today.

- [ ] **Step 1: Failing tests:** variables page with `review.not_sent: ['token']` shows the notice naming `token`; `[]` shows none; the badge shows `1 not sent to runs`. `origin.test.ts` — adjusted field whose content is reverted → origin `source`, no `source_field`, `active` and `source` kept. `VariableEditor.subfields.test.tsx` — change a source field's sub-field inline and change it back → the emitted field is `source` without `source_field`; the same through the field dialog gives the same field. `fieldEditor.test.ts` — `formatAttribute('item', { name: '', type: 'enum', options: ['a', 'b'] })` is `enum (a, b)`; `{ type: 'int' }` stays `int`.
- [ ] **Step 2:** run the files — FAIL.
- [ ] **Step 3:** implement.
- [ ] **Step 4:** `npx vitest run src/features/variables src/features/templates`; eslint; tsc.
- [ ] **Step 5: Commit** `fix(web): name the variables a run leaves out, revert a sub-field edit to the source either way, show an enum item's options`.

### Task 11: `web/e2e/ansible.spec.ts` follows today's labels (7.87)

**Files:** `web/e2e/ansible.spec.ts`.

- [ ] **Step 1:** for every locator name, dialog title and toast text in the spec, find the rendering component and its `web/src/i18n/en` string and correct what changed. Known: the repository dialog is `New repository` (`t.repos.dialog.newTitle`), not `Add repository` (the button keeps that name); a new editor playbook toasts `Playbook created` (`t.playbooks.editor.created`), not `/playbook saved/i` (both occurrences). Check the rest the same way (credential and jump host dialogs, host dialog fields, the SSH card, run view tables, the variables page steps at ~216-241). Keep a scratch list of every string checked and where it lives for the report.
- [ ] **Step 2:** `npx tsc --noEmit -p e2e/tsconfig.json`; `npx eslint e2e/ansible.spec.ts`; `npx playwright test e2e/ansible.spec.ts` reports the spec skipped with the worker reason (no worker in the stack, Ruling 9).
- [ ] **Step 3: Commit** `test(e2e): follow today's labels in the ansible journey`.

---

## Self-review

- **Coverage:** 7.84 → Task 1. 7.83 → Tasks 2, 3. 7.85: invite `..` and narrowed-run tests → 4; HostDialog org, `networkMode`, list-row description, `waitForQuery` → 7; drop log line, file splits → 6. 7.86: schema browsing (verify), WebKit container query (verify, obsolete) → 9; `useReloadInventory`, secret overwrite, Send test, check labels, fleet rows → 8; handled hashes, `DisabledHint`, `scrollTo` → 9. 7.87: values not sent → 5 + 10; legacy reseal, `PlaybookVarSource` → 5; revert asymmetry, enum "Source says" → 10; page-wide test flag → 8 (Ruling 8 interpretation); `ansible.spec.ts` → 11; flaky token test → 6.
- **Types:** `GalaxyDir` (Task 2) is used by Task 3's `openInstalledRolesFS`; `ListRowName` (Task 7) by Task 8's `FleetRow`; `review.not_sent` (Task 5) by Task 10.
- **Review Focus:** items 1–3 in Task 1, 4 in Tasks 2 and 3, 5 in Task 3.
