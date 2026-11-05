# 7.x Review Leftovers (7.89, 7.90) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A group schema save never overwrites another person's change (7.89), and every leftover of the 7.83–7.87 reviews (7.90) is fixed, tested or recorded as already fine.

**Architecture:** One small SPA hook (`useRevisionDraft`) carries the revision model `ValuesForm` got in 7.86; the schema section uses it (Task 1) and the values form moves onto it so neither form needs a content key that remounts it (Task 9). `CreateJob` locks the hosts it links, so a host deleted meanwhile is skipped instead of failing the insert (Task 2). The worker checks the galaxy directory in one place and opens it without a check-then-open race (Task 3). The variable parser searches roles exactly where `ansible-playbook` does (Task 5). Everything else is local fixes behind the existing layers.

**Tech Stack:** Go 1.27 (pgx v5, sqlc, huma v2.39.1, chi v5), PostgreSQL 18 + TimescaleDB, ansible-core 2.21 / ansible-runner on the worker, React/TypeScript SPA (TanStack Router/Query 5.102.8, Vitest), Playwright E2E.

**Spec:** Backlog rows 7.89 and 7.90 in `docs/BACKLOG.md`, the 7.86 model in `web/src/features/inventory/sections/ValuesSection.tsx` and its tests `GroupDetailPage.revision.test.tsx`, and the Rulings below.

## Global Constraints

- Work on the checked-out branch `fix/7.89-7.90`; do not switch branches.
- Everything in the repository is English; user-visible SPA strings in English and German (`web/src/i18n/en|de/*.ts`). This plan adds no new user-visible string.
- Conventional Commits; every commit ends with exactly `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. Commit only the task's own files.
- TDD: failing test first, watched failing, then the minimal change. A "verify only" step runs the named command and records its output in the task report instead of changing code.
- Tests per package with a timeout (`go test -race -count=1 -timeout 900s ./internal/<pkg>/`; `internal/api` needs `-timeout 1800s`). Never one whole-module `go test ./...`. Full suite: `make test`.
- Before every commit: `golangci-lint run` (0 issues) and `gofmt -s -l .` (no output).
- SPA: `PATH=$HOME/.nvm/versions/node/v24.14.0/bin:$PATH`; in `web/`: `npx vitest run <file>`, `npx eslint <files>`, `npx tsc -b`; OpenAPI types via `npm run api:gen` after the API shape change (Task 7 only). No prettier.
- E2E: in `web/`, `npx playwright test e2e/<spec>.ts`.
- sqlc is not installed as a binary: `go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1 generate`, then `... diff` must be clean.
- No migration. Database tests use `internal/db/testdb`. Only touch Docker containers the tests start themselves.
- File splits are pure moves: no behaviour change, tests unchanged except moved. A file this plan grows past ~400 lines gets its new code in a new file instead (named in the task).
- Backlog rows and the end-user docs in `NoiXdev/noix-docs` are updated by the controller after the branch: the runs page (and the host page's runs section) say that filtering by a deleted host lists nothing — the host's links go with it, the runs stay in the unfiltered list (7.90 docs item); `reference/variable-sources.md` names the role search order of Task 5 (`<playbook dir>/roles`, the roles installed from `requirements.yml`, the playbook's own directory; never the repository root's `roles/` for a playbook in a subdirectory); the worker configuration page says `PULSIX_WORKER_DATA_DIR` must not contain `:` or a line break (Task 4).

## Review Focus

1. Two people edit the same group's schema: the one who saves second gets the stale notice, never a silent overwrite — also when a background refetch brought the first save's revision in between (Task 1, `a refetch that only bumps the revision keeps the schema save on the revision the draft started from`).
2. A person with an unsaved schema draft saves the group values (or renames the group) on the same page first: the schema save then goes through on the revision that own save stored — but if someone else saved in between, it is still refused (Task 1, `follows the page's own values save with an edited schema draft` and `does not follow an own save that came after someone else's`; Ruling 3).
3. A run started while one of its hosts is being deleted: the run is created, the deleted host is not linked, nothing answers 500 (Task 2, `TestCreateJobSkipsAHostDeletedMeanwhile`).
4. A repository laid out as `playbooks/site.yml` + root `roles/`: the variable editor reports the role not found with the paths Ansible searched, exactly as the run fails (Task 5, `TestRolesAreSearchedWhereAnsiblePlaybookLooks` and `TestRoleSearchFromASubdirectoryInRealAnsible`).
5. A worker whose data dir contains `:` refuses to start with a message naming `PULSIX_WORKER_DATA_DIR`, instead of running plays with a broken `roles_path` (Task 4, `TestLoadRefusesADataDirThatWouldSplitAnsibleCfg`).

## Rulings

Binding decisions of the owner/controller, plus the choices this plan makes where they left one open:

1. **7.89 — model.** Exactly `ValuesForm`'s 7.86 model, extracted into `useRevisionDraft`: an unedited draft follows `group.revision` (and, now, the stored content); the first edit pins the revision it was made on; an own successful save rebuilds the draft from the response and resets to the response revision; Reload keeps an edited draft edited and pinned to the reloaded revision, and starts over when the reload brings other content; an edit typed while the save was in flight stays. The schema section is keyed by `group.id` only, because a key on `group.schema` remounts the section when its own save lands and loses an edit typed during that save.
2. **7.89 — the rest of the group page.** `GroupSettingsDialog` already fills its form and `revision` once when it opens (`useState(group.revision)`); `AssignHostDialog` gets the member object `MembersSection` stored when Edit values was chosen (`setDialog({ member })`), so `member.revision` is the one the form was filled from. Neither moves with a refetch: Task 1 pins both with a test, no code change. The group delete sends no revision at all (the API takes none); out of scope.
3. **7.89 — the page's own saves (controller ruling, replaces "known cost").** A pinned draft may follow the page's *own* revision steps: when a write made from this group page sent `If-Match: "r"` and was answered with revision `r'`, nobody else wrote between `r` and `r'`, so a draft pinned at `r` moves to `r'` (and on along a chain of such steps). A step that does not start at the pinned revision is not followed, so someone else's write in between still ends in a 412. `GroupDetailPage` keeps the page-level record of own steps (`{ from, to }`, collected from every group write the page makes: the settings dialog save, the values save, the schema save, and any member write that moves the group revision) and passes it to `useRevisionDraft`. This also applies to `ValuesForm` through Task 9.
4. **7.90 — `CreateJob` 23503.** Reproduced on PostgreSQL: with the host's `DELETE` open in another transaction, the `CreateJob` statement waits on the foreign-key check and then fails with `job_hosts_org_id_host_id_fkey`. Neither of the offered options is taken: the link insert reads the hosts it links through a subquery with `FOR KEY SHARE` instead of a plain join. A host deleted meanwhile is then skipped exactly like a host deleted just before (the 7.84 rule "an id that names no host is skipped"), and a host deleted just after cascades its link away — so no 23503 can arise, nothing answers 500, no transaction is replayed. Verified in a scratch PostgreSQL 17: the locked subquery waits for the delete, skips the row and commits. This is smaller than a server-side retry (which would have to replay the whole `CreateJobGroup`/`CreateRotationGroup` transaction) and safer than a 409 (a multi-zone run whose second zone's insert fails has already created the first zone's job — backlog 2.60 — so "start it again" would run that zone twice). `KEY SHARE` blocks only deletes and key changes of the hosts, which the foreign-key check already blocks; a host edit (`FOR NO KEY UPDATE`) is not blocked. `insertJobs` already runs inside one transaction (`WithOrg`), so a failing job leaves no partial batch; Task 2 extends `TestCreateJobGroupIsAtomic` to the links. Fallback if the race test still sees 23503 after the change: map `db.IsForeignKeyViolation(err) && db.ConstraintName(err) == "job_hosts_org_id_host_id_fkey"` to a new `jobs.ErrTargetsChanged` → 409 in `mapJobsErr`, `mapAdhocErr`, `mapHostopsErr`, `mapCredentialsErr` and the schedules mapper, and report it.
5. **7.90 — role search.** The run is right, the parser is wrong. Checked with ansible-core 2.21.5: for `repo/playbooks/site.yml` Ansible searches `repo/playbooks/roles`, then `roles_path`, then `repo/playbooks` itself — never `repo/roles` (its error lists exactly those three). The run invokes the playbook in place with only the galaxy dir and the system paths on `roles_path`, so it finds the same. The parser drops the root `roles/` candidate and gains the playbook-directory fallback after the installed roles. Fixing the run instead would make Pulsix resolve roles plain `ansible-playbook` does not, and needs a claim-payload change (the run payload's `subpath` already joins the repository subpath with the playbook path). The 7.75 spec's sentence (line 68) is corrected in Task 5.
6. **7.90 — `not_sent`.** huma 2.39.1 honours `nullable:"false"` on a slice field (`boolTag(f, "nullable", …)`, schema.go:622); `toReviewDTO` already always sends `[]`. So the field gets the tag and the generated type becomes `string[]`. No other array in the API sets it; this one does because its mapper guarantees the list.
7. **7.90 — `qc.fetchQuery`.** The installed `@tanstack/query-core` 5.102.8 marks `fetchQuery` deprecated with "Use queryClient.query(options) instead" and ships `query()` with the same fetch-or-cached semantics (`queryClient.js:159`). All four call sites switch.
8. **7.90 — already fine.** The warn contrast of the not-sent notice is 10.32:1 (`#FBBF24` on `--px-surface` `#161B26`; the app is dark-only), above 4.5:1 for 12px text: Task 11 records it, no change.

---

### Task 1: The group schema save keeps the revision its draft started from (7.89)

**Files:** Create `web/src/features/inventory/sections/useRevisionDraft.ts`, `web/src/features/inventory/sections/useRevisionDraft.test.ts`, `web/src/features/inventory/GroupDetailPage.schemaRevision.test.tsx`. Modify `web/src/features/inventory/sections/SchemaSection.tsx`, `web/src/features/inventory/GroupDetailPage.tsx` (SchemaSection key + the comment above the sections), `web/src/features/inventory/groupPageTestKit.tsx` (gains the shared helpers below; `GroupDetailPage.drafts.test.tsx` and `GroupDetailPage.revision.test.tsx` import them instead of their local copies), `web/src/features/inventory/GroupDetailPage.test.tsx` (two dialog tests).

**Interfaces:**
- Produces (Task 9 consumes):
  ```ts
  export interface SaveTicket { readonly edits: number }
  export interface RevisionDraft<D> {
    draft: D;            // what the form shows
    revision: number;    // what a save sends as If-Match
    generation: number;  // bumps whenever the draft is replaced wholesale (editor key, problem reset)
    edit(next: D): void;
    startSave(): SaveTicket;
    saved(ticket: SaveTicket, stored: unknown, revision: number): void;
    reloaded(stored: unknown, revision: number): void;
    discard(): void;
  }
  export interface RevisionStep { readonly from: number; readonly to: number }
  export function useRevisionDraft<D>(stored: unknown, revision: number, build: (stored: unknown) => D, ownSteps: readonly RevisionStep[]): RevisionDraft<D>;
  ```
  Semantics (the 7.86 comment in `ValuesSection.tsx` moves to this hook's doc comment and is extended): content is compared as `JSON.stringify(stored)`; `base` is the content the draft was built from. Initially `draft = build(stored)`, untouched, pinned = `revision`. Rendering with other content while untouched rebuilds the draft from it and bumps `generation` (an unedited draft follows the group). `revision` is `touched ? follow(pinned, ownSteps) : Math.max(pinned, revisionArg)`, where `follow` walks own steps whose `from` equals the current value (Ruling 3). `edit` counts the edit, pins the current `revision` on the first one and sets the draft. `saved` pins the response revision and makes the response content the base; when no edit came after `startSave` it rebuilds the draft from the response, untouched, `generation + 1`. `reloaded` pins the reloaded revision; other content than `base` starts over (draft rebuilt, untouched, `generation + 1`), the same content keeps the draft and its touched state. `discard` rebuilds from the current `stored`, untouched, pinned to the current `revision`, `generation + 1`.
- Test kit exports: `stale(): Response` (the 412 problem), `typeEnv(user, text)`, `addField(user)`, `fieldCount(): number`, `schemaSaved(revision: number, schema: unknown)` → `{ ...group({ revision, schema }), incomplete_members: 0 }`.
- `SchemaSection` uses `useRevisionDraft(group.schema, group.revision, toVarSchema)`; `VariableEditor key={generation}`; Discard calls `discard()` and clears problems; Save sends `{ schema: draft, revision }` after `startSave()` and calls `saved(ticket, res.group.schema, res.group.revision)`; `onReload` calls `reloaded(fresh.schema, fresh.revision)`. `GroupDetailPage` renders `<SchemaSection key={group.id} …>`.

- [ ] **Step 1: Failing tests.**
  - `useRevisionDraft.test.ts` (`renderHook`, `rerender` with new props, `act`): `follows new content and revision while untouched` (rerender stored B rev 4 → draft `build(B)`, revision 4, generation 1); `pins the revision of the first edit` (edit at rev 3, rerender rev 4 same content → revision 3; rerender other content → draft unchanged); `a save without a later edit rebuilds from the response` (edit, ticket, `saved(ticket, C, 5)` → draft `build(C)`, revision 5 after a rerender at rev 6 → 6, i.e. untouched); `an edit typed during the save stays, pinned to the response revision` (edit, ticket, edit, `saved(ticket, C, 5)`, rerender rev 6 → draft is the second edit, revision 5); `reload with the same content keeps an edited draft on the reloaded revision`; `reload with other content starts over`; `discard follows the group again`.
  - `GroupDetailPage.schemaRevision.test.tsx` (mirrors `GroupDetailPage.revision.test.tsx`: `let current = group()`, `GET …/groups/g1` returns `current`, PUT `…/groups/g1/schema` pushes to `puts`; refetch with `act(async () => { await qc.refetchQueries(); })`; `OTHER = { fields: [...SCHEMA.fields, { name: 'region', type: 'string' }] }`):
    - `a refetch that only bumps the revision keeps the schema save on the revision the draft started from`: addField; `current = group({ revision: 4 })`; refetch; Save schema → `puts[0]` `If-Match: "3"` answered `stale()`; Reload in the region `Variable schema` → alert gone, `fieldCount()` still before + 1; Save → `puts[1]` `If-Match: "4"`, `Schema saved` shown.
    - `lets an unedited schema draft follow a refetched revision`: refetch to rev 4 first, then addField, Save → `If-Match: "4"`.
    - `follows a schema someone else saved while the draft is unedited`: `current = group({ revision: 4, schema: OTHER })`, refetch → `fieldCount()` is 3; Save → `If-Match: "4"`, body schema has `region`.
    - `keeps an edited draft over someone else's schema, and Reload starts it over`: addField; `current = group({ revision: 4, schema: OTHER })`, refetch → `fieldCount()` still 3 (own field, not `region`: `screen.queryByText('region')` absent); Save → `If-Match: "3"`, `stale()`; Reload → `region` shown, alert gone; Save → `If-Match: "4"`.
    - `rebuilds a saved draft from the response, unedited again`: addField; PUT answers `schemaSaved(4, req.body.schema)` and sets `current` to it; after `Schema saved`, `current = group({ revision: 5, schema: current.group.schema })`, refetch; Save → `puts[1]` `If-Match: "5"`.
    - `keeps an edit typed while a schema save was in flight, on the revision that save stored`: PUT 1 held on a promise; addField, Save, addField again, release with `schemaSaved(4, puts[0].body.schema)` → `fieldCount()` is before + 2; refetch to rev 5 with that schema; Save → `If-Match: "4"`, body has both new fields.
    - `saves the next schema on the revision the previous save stored`: Save twice unchanged (PUT answers rev 3 + n) → `"3"`, then `"4"`.
    - `discard lets the draft follow the group again`: addField; refetch to rev 4 (same schema); Discard; Save → `If-Match: "4"`.
    - `follows the page's own values save with an edited schema draft` (Ruling 3): addField; typeEnv `staging`; Save values (PUT vars sent `If-Match: "3"`, answers `group({ revision: 4, vars })`, sets `current`); Save schema → `If-Match: "4"`, `Schema saved`.
    - `follows the page's own rename with an edited schema draft`: addField; rename through the settings dialog (PATCH `If-Match: "3"` answers rev 4); Save schema → `If-Match: "4"`.
    - `does not follow an own save that came after someone else's`: addField (pinned 3); `current = group({ revision: 4 })`, refetch (someone else); Save values (untouched values follow 4 → PUT `If-Match: "4"` answers rev 5); Save schema → `If-Match: "3"`, answered `stale()`.
    - `useRevisionDraft.test.ts` adds: `follows a chain of own steps from the pinned revision` (pinned 3, steps [{3,4},{4,5}] → 5) and `ignores own steps that do not start at the pinned revision` (pinned 3, steps [{4,5}] → 3).
  - `GroupDetailPage.test.tsx`: `the settings dialog saves on the revision it was opened on through a refetch` (open Edit, refetch to rev 4, rename, Save → PATCH `If-Match: "3"`); `the member values dialog saves on the member revision it was opened on through a refetch` (open Edit values of web-01, members refetch answers revision 8, Save → `If-Match: "7"`). Both are expected to pass on today's code (Ruling 2); a failure is a lost update: fix it the same way and say so.
- [ ] **Step 2:** `npx vitest run src/features/inventory/sections/useRevisionDraft.test.ts src/features/inventory/GroupDetailPage.schemaRevision.test.tsx src/features/inventory/GroupDetailPage.test.tsx` — the hook tests fail (no module), the first schema test fails with `If-Match: "4"` (or the remount), the two dialog tests pass.
- [ ] **Step 3:** implement the hook, `SchemaSection`, the key; move the shared test helpers into the kit.
- [ ] **Step 4:** `npx vitest run src/features/inventory` passes (including the drafts and 7.86 revision tests unchanged in substance); eslint on the touched files; `npx tsc -b`; `npx playwright test e2e/inventory.spec.ts`.
- [ ] **Step 5: Commit** `fix(web): save a group schema draft on the revision it started from`.

### Task 2: A host deleted while its run is created is skipped (7.90 backend)

**Files:** Modify `internal/db/queries/jobs.sql` (`CreateJob`, its comment) + `internal/db/gen`. Create `internal/jobs/job_hosts_race_test.go`. Modify `internal/jobs/rotation_store_test.go` (`TestCreateJobGroupIsAtomic`).

**Interfaces:** `CreateJob`'s `linked` CTE becomes
```sql
INSERT INTO job_hosts (org_id, job_id, host_id, created_at)
SELECT j.org_id, j.id, h.id, j.created_at
FROM j, (SELECT id FROM hosts WHERE org_id = $2 AND id = ANY ($6::uuid[]) FOR KEY SHARE) AS h
```
(same parameters, no `DISTINCT` needed: `hosts` has one row per id). Its comment adds why (Ruling 4, backlog 7.90). Go signatures unchanged.

- [ ] **Step 1: Failing tests.**
  - `TestCreateJobSkipsAHostDeletedMeanwhile` (jobs, DB; helpers from `job_hosts_store_test.go`): hosts A, B; `superuser(t)` begins a transaction and deletes A (not committed); a goroutine runs `store.CreateJob(scoped, j, nil)` with `HostIDs {A, B}`; poll (10 s deadline) `SELECT count(*) FROM pg_stat_activity WHERE datname = current_database() AND wait_event_type = 'Lock' AND query LIKE '%job_hosts%'` until 1; commit the delete; `CreateJob` returns nil; `jobHostLinks` is exactly `{(j, B)}`; `GetJob` keeps `HostIDs {A, B}`.
  - `TestCreateJobGroupIsAtomic`: `first` gets `HostIDs` of a seeded host; after the failed group insert there is no `job_hosts` row for `first.ID`.
- [ ] **Step 2:** `go test -race -count=1 -timeout 900s -run 'TestCreateJobSkipsAHostDeletedMeanwhile|TestCreateJobGroupIsAtomic' ./internal/jobs/` — the race test fails with SQLSTATE 23503 `job_hosts_org_id_host_id_fkey`; the atomic test passes (the transaction already covers the links).
- [ ] **Step 3:** change the query; regenerate sqlc.
- [ ] **Step 4:** `./internal/jobs/` and `./internal/backup/` pass (900 s); sqlc diff clean; `-run TestCreateJobSkipsAHostDeletedMeanwhile -count=20` passes.
- [ ] **Step 5: Commit** `fix(jobs): skip a host deleted while its run is created instead of failing the insert`.

### Task 3: The galaxy directory: one check, a race-free open, named refusals (7.90 backend + tests)

> **DROPPED by controller ruling (2026-11-05):** backlog 7.88 (approved by the owner) replaces `<clone>.galaxy` with a per-commit requirements cache shared by sync and run. These items — one place that checks the directory, open-then-verify instead of check-then-open, a named refusal for a symlink, a logged reason when a run leaves it out, the open-error tests and a play-uid test that can fail — move into the 7.88 spec for the new cache. Do not implement this task.

**Files:** Modify `internal/worker/sync.go` (next to `GalaxyDir`), `internal/worker/clonefs.go` (`errNotGalaxyDir`, `openInstalledRolesFS`), `internal/worker/runner/playbook.go` (`galaxyRolesDir` and its caller). Tests: create `internal/worker/galaxy_dir_test.go`; modify `internal/worker/sync_sources_galaxy_test.go`, `internal/worker/runner/galaxy_roles_test.go`.

**Interfaces:**
- `var errGalaxySymlink = errors.New("a symlink, which is not followed")` beside `errNotGalaxyDir` (`"not a directory"`, now only for a non-directory).
- `func InstalledRolesDir(cloneDir string) (string, error)` in `sync.go`: `os.Lstat(GalaxyDir(cloneDir))`; does not exist → `("", nil)`; any other Lstat error, a symlink (`errGalaxySymlink`) or a non-directory (`errNotGalaxyDir`) → `("", fmt.Errorf("installed roles %s: %w", dir, …))`; a real directory → `(dir, nil)`.
- `openInstalledRolesFS(cloneDir string) (*cloneFS, error)` opens first, then checks: `os.OpenRoot(dir)`; on an open error, the `InstalledRolesDir` error if it has one (a dangling symlink, a file), else `(nil, nil)` for does-not-exist, else the open error wrapped; on success, `os.Lstat(dir)` must be a real directory that `os.SameFile` matches `root.Stat(".")` — otherwise close and return `errGalaxySymlink` / `errNotGalaxyDir` / `errGalaxyReplaced` (`errors.New("replaced while it was opened")`). No check-then-open: what is read is the directory that was at the path after the open.
- `(r *Runner) galaxyRolesDir(ctx context.Context, clone string) string` uses `worker.InstalledRolesDir`; an error is logged `r.log().WarnContext(ctx, "installed roles left off the roles path", "err", err)` and gives `""`.

- [ ] **Step 1: Failing tests.**
  - `galaxy_dir_test.go` (package worker): `TestInstalledRolesDir` table — none → `"", nil`; directory → the path; symlink to a directory → `errors.Is(err, errGalaxySymlink)` and the message contains `symlink`; dangling symlink → `errGalaxySymlink`; file → `errNotGalaxyDir`; parent without search permission (`0o600`, skip when `os.Geteuid() == 0`) → `errors.Is(err, fs.ErrPermission)`. `TestOpenInstalledRolesFS` — the same kinds: none → `nil, nil`; directory → reads `acme.web/meta/argument_specs.yml`; symlink, dangling symlink, file → the same sentinels; a directory with mode `0o000` (skip as root) → `fs.ErrPermission`.
  - `TestSyncExecutorInstalledRolesThatCannotBeOpened` gains the kind `dangling symlink`.
  - `TestRepoRunNamesTheGalaxyDirInItsConfig`: the runner gets `Log: slog.New(slog.NewTextHandler(&buf, nil))`; the symlink case's log contains `installed roles left off the roles path` and `symlink`; the case without a galaxy dir logs nothing of it.
  - `TestGalaxyRolesAreReadableByThePlayUID` (the half that cannot fail): after `writeRoleFile`, chmod the role file `0o620` and every directory from it up to the galaxy dir `0o770`, so the "none group-writable" assertion has something to clear.
- [ ] **Step 2:** `go test -race -count=1 -timeout 900s ./internal/worker/ ./internal/worker/runner/` — the new tests fail (undefined `InstalledRolesDir`, `errGalaxySymlink`; no log line). Then, to see the play-uid half able to fail, temporarily drop `&^ 0o020` from `chmodReadShared`: `-run TestGalaxyRolesAreReadableByThePlayUID ./internal/worker/runner/` fails with `is group-writable`; restore it. Record both in the report.
- [ ] **Step 3:** implement; update the `clonefs.go` header comment (the galaxy root is opened first and then verified).
- [ ] **Step 4:** both packages pass (900 s); `./internal/e2e/` passes (1800 s).
- [ ] **Step 5: Commit** `fix(worker): check the galaxy directory in one place, open it without a race and log why a run leaves it out`.

### Task 4: Worker config and sync report (7.90 backend + tests)

**Files:** Modify `internal/worker/config.go` (`Load`, after the `DataDir` default), `internal/worker/sync_sources.go` (every `r.Notes = append(notes, r.Notes...)`), `docs/upgrades.md` ("Behaviour changes to check after an upgrade"). Tests `internal/worker/config_test.go`, `internal/worker/sync_sources_galaxy_test.go` (`TestSyncExecutorWithoutInstalledRoles`).

**Interfaces:**
- `Load` refuses a `DataDir` containing `:`, `\n` or `\r`: `fmt.Errorf("%s %q must not contain ':' or a line break: the worker writes paths below it into each run's ansible.cfg, where ':' separates roles_path entries and a line break ends a setting", envDataDir, cfg.DataDir)`. This covers `RepoCacheDir` (`roles_path`) and the work dir (`local_tmp`), both below it.
- `func prependNotes(front, rest []varsource.Note) []varsource.Note` in `sync_sources.go`: `front` then `rest`, never nil (`[]varsource.Note{}` when both are empty). Every prepend uses it.
- `docs/upgrades.md` bullet: **7.90, worker data dir:** a worker refuses to start when `PULSIX_WORKER_DATA_DIR` contains `:` or a line break (a run's `roles_path` and `local_tmp` would split there); move the data dir before upgrading such a worker.

- [ ] **Step 1: Failing tests.** `TestLoadRefusesADataDirThatWouldSplitAnsibleCfg` table: `/data/a:b`, `"/data/a\nb"`, `"/data/a\rb"` → error containing `PULSIX_WORKER_DATA_DIR`; `/data/a b` → accepted (the `TestLoadOverrides` env otherwise). `TestSyncExecutorWithoutInstalledRoles`: delete the `if len(r.Notes) == 0 && len(want.Notes) == 0 { … }` normalisation and assert `r.Notes != nil`.
- [ ] **Step 2:** `-run 'TestLoadRefusesADataDir|TestSyncExecutorWithoutInstalledRoles' ./internal/worker/` — FAIL (accepted; `"notes":null`).
- [ ] **Step 3:** implement; the upgrades bullet.
- [ ] **Step 4:** `./internal/worker/` and `./cmd/worker/` pass.
- [ ] **Step 5: Commit** `fix(worker): refuse a data dir that would split ansible.cfg settings and keep empty sync notes a list`.

### Task 5: The parser searches roles where `ansible-playbook` does (7.90 backend)

**Files:** Modify `internal/varsource/roles.go` (`target` doc comment, `findRole`), `internal/varsource/doc.go` (if it names the root `roles/`), `docs/superpowers/specs/2026-11-05-variable-editor-design.md` (line 68: replace the sentence with the search order of Ruling 5 and "(corrected by backlog 7.90: the run never searched the subpath's root `roles/` for a playbook in a subdirectory)"), `docs/upgrades.md` (behaviour bullet). Fixture: `git mv internal/varsource/testdata/basic/roles/{pre,nospecs,inblock,inrescue,inalways,post,handler} internal/varsource/testdata/basic/sub/roles/` (`roles/shared` stays at the root). Tests `internal/varsource/roles_test.go` (`TestRoleResolution`), `internal/varsource/discover_test.go` (only the expectations for `sub/play.yml` that change), create `internal/varsource/role_search_test.go`, `internal/worker/runner/role_search_test.go`.

**Interfaces:** `findRole(name, dir string) roleTarget` for a plain name: `<dir>/roles/<name>` in the repository, then `<name>` among the installed roles, then `<dir>/<name>` in the repository (`path.Join`), then the collection rule. A slash name is unchanged. Not found: `where` is `at <dir>/roles/<name> or <dir>/<name>`, plus ` or among the roles installed from requirements.yml` when installed roles were given. `docs/upgrades.md` bullet: **7.90, variable editor:** a role a playbook in a subdirectory uses is no longer read from the repository subpath's root `roles/` — runs never found it there — and shows as not found after the next sync, as the run reports it; a role directory next to the playbook is now read, as the run finds it.

- [ ] **Step 1: Failing tests.**
  - `TestRolesAreSearchedWhereAnsiblePlaybookLooks` (`fstest.MapFS` via `mapFS`, installed roles via `parseWithRoles`): repo `playbooks/site.yml` using `inroles`, `beside`, `shadowed`, `rootonly`; `playbooks/roles/inroles`, `playbooks/beside`, `playbooks/shadowed`, `roles/rootonly` each declare one option named after the role (`oneOption`); installed roles hold `shadowed` declaring `installed_shadowed`. Fields `[inroles_var, beside_var, installed_shadowed]`; roles `rootonly` → `RoleNotFound`; its note message is exactly `role "rootonly" not found at playbooks/roles/rootonly or playbooks/rootonly or among the roles installed from requirements.yml; its variables are not read`.
  - `TestRoleResolution` after the fixture move: fields `pre_x, local_sub, inblock_x, inrescue_x, inalways_x, post_x, handler_x` (no `shared_x`); `shared` → `RoleNotFound`; notes `warning role_not_found sub/play.yml:7`, `warning role_not_found sub/play.yml:8`, `hint role_collection sub/play.yml:9`, `hint role_without_specs sub/play.yml:10`.
  - `TestRoleSearchFromASubdirectoryInRealAnsible` (`requireAnsiblePlaybook`, the `TestGalaxyRoleResolvesInRealAnsible` harness): subtest `order` — `playbooks/site.yml` uses `inroles`, `beside`, `galaxy_only`; `playbooks/roles/inroles`, `playbooks/beside` and galaxy `galaxy_only`, `beside` print `from-inroles`, `from-beside`, `from-galaxy`, `from-galaxy-beside` → exit 0, output has `from-inroles`, `from-galaxy`, `from-galaxy-beside` and not `"msg": "from-beside"` (roles_path before the playbook dir). Subtest `root roles` — `playbooks/site.yml` uses `rootonly`, only `roles/rootonly` exists → non-zero, output contains `rootonly` and `was not found`. Doc comment: this is the order the parser mirrors (backlog 7.90).
- [ ] **Step 2:** `go test -race -count=1 -timeout 900s ./internal/varsource/` — FAIL; `go test -race -count=1 -timeout 900s -run RoleSearch ./internal/worker/runner/` — passes against today's run (it pins the run, which is right; Ruling 5); with `ansible-playbook` missing it skips: then run it under `PULSIX_E2E_REAL_ANSIBLE=1` where it is installed and record which.
- [ ] **Step 3:** implement; spec correction and upgrades bullet.
- [ ] **Step 4:** `./internal/varsource/`, `./internal/worker/` pass; `./internal/e2e/` passes (1800 s; its fixture playbook sits at the root).
- [ ] **Step 5: Commit** `fix(varsource): search roles where ansible-playbook does`.

### Task 6: Accounts log through the service's logger; the reset and token assertions (7.90 backend + tests)

**Files:** Modify `internal/accounts/notices.go` (~135), `internal/accounts/reset.go` (~88, ~94, ~229, ~347). Tests `internal/accounts/reset_test.go` (fixture, `captureLog` deleted), `internal/accounts/reset_redeem_test.go` (~195), `internal/accounts/actions_test.go`, `internal/auth/token_test.go` (`TestTamperTokenAlwaysChangesTheSecret`).

**Interfaces:** every log line of the accounts service goes through `s.logger()`. `resetFixture` gains `logs *bytes.Buffer`; `newResetFixture` builds the service with `accounts.WithLogger(slog.New(slog.NewTextHandler(f.logs, nil)))`; no accounts test calls `slog.SetDefault` any more.

- [ ] **Step 1: Failing tests.** Every former `captureLog(t)` reader reads `f.logs` (TestRequestResetCapsWorkInFlight, …RecoversFromPanic, …StoreFailure, …SendFailureIsSilent, the `a failed send` subtest); `TestRequestResetLogsADroppedRequest` uses the fixture. `TestRequestResetCapsWorkInFlight` asserts `password reset request dropped: too many in flight` and `limit=` followed by `accounts.MaxResetJobs`. New `TestNoticeFailureLogsThroughTheServiceLogger` (actions_test.go, the `mail fails` setup of `SetTemporaryPassword`): `newStore()`, `&fakeMailer{sendErr: errors.New("smtp down")}`, the service built like `actionService` plus `accounts.WithLogger` over a buffer; `SetTemporaryPassword(ctx, true, actor, target)` gives `NoticeFailed` and the buffer has `account notice not sent` and `smtp down`. `TestTamperTokenAlwaysChangesTheSecret`: for each of the 1 000 created tokens `tk.Resolve(ctx, tamperToken(p))` fails with `errors.Is(err, auth.ErrUnauthenticated)`.
- [ ] **Step 2:** `./internal/accounts/`, `-run TestTamperToken ./internal/auth/` — the accounts log tests fail (empty buffer); the token test passes (it asserts behaviour that holds) — record it.
- [ ] **Step 3:** implement.
- [ ] **Step 4:** `./internal/accounts/`, `./internal/auth/` pass; `go test -race -count=50 -run 'TestRequestReset' ./internal/accounts/` passes.
- [ ] **Step 5: Commit** `fix(accounts): log every line through the service's logger` and `test(auth): assert that a tampered token is refused`.

### Task 7: Templates: one stray scan, an unopenable envelope, `not_sent` never null (7.90 backend + SPA)

**Files:** Modify `internal/inventory/schema/secrets.go` (+ `secrets_test.go`), `internal/app/template_lookup.go` (`dropStraySealed`), `internal/api/template_variables_handlers.go` (`NotSent` tag), `web/src/api/schema.d.ts` (regenerated), `web/src/features/templates/variables/ReviewBadges.tsx` (~18), `web/src/features/templates/variables/TemplateVariablesPage.tsx` (~184), `web/src/features/templates/TemplatesPage.test.tsx` (~443: `not_sent: []`). Create `internal/templates/keeps_values_internal_test.go`, `internal/api/template_variables_spec_internal_test.go`.

**Interfaces:**
- `func StrayNames(stray []StrayValue) []string` in `schema`: the top-level names of `stray`, in order, each once, never nil. `StrayTopLevel(fields, values)` is `StrayNames(StraySealed(fields, values))`. `dropStraySealed` calls `StraySealed` once and `StrayNames` on its result.
- `NotSent []string \`json:"not_sent" nullable:"false" doc:"…"\`` (doc unchanged); the SPA drops `?.`/`?? []` on `not_sent`.

- [ ] **Step 1: Failing tests.**
  - `TestStrayNames` (schema): for every row of `TestStrayTopLevel`, `StrayNames(StraySealed(f, v))` equals `StrayTopLevel(f, v)`; `StrayNames(nil)` is `[]string{}` (non-nil).
  - `TestKeepsValuesOverAnEnvelopeThatDoesNotOpen` (package templates; `NewService(nil, nil, nil, nil, nil, box)`; envelopes via `schema.Sealed(box.SealAAD(...))`): cur `{"token": "hunter2"}` against next `{"token": <hunter2 sealed under aad>}` → true; sealed under another template's `ExtraVarsAAD` → false; sealed by another key's box → false; `{"$sealed": base64("garbage")}` → false; a repeater `{"vhosts": [{"password": "x"}]}` against the item's password sealed under aad → true.
  - `TestReviewNotSentIsNeverNullInTheSpec` (package api, the `registeredMutatingOps` construction): `hapi.OpenAPI().Components.Schemas.Map()["TemplateVariablesReviewDTO"].Properties["not_sent"]` has `Type == "array"` and `Nullable == false`.
- [ ] **Step 2:** `./internal/inventory/schema/`, `-run TestKeepsValues ./internal/templates/`, `-run TestReviewNotSent ./internal/api/` (1800 s) — `StrayNames` undefined, the spec test fails (`Nullable` true); the keepsValues test passes (it pins existing behaviour) — record it.
- [ ] **Step 3:** implement; `npm run api:gen` (the diff of `schema.d.ts` is `not_sent: string[]` only); fix the three SPA spots; `npx tsc -b`.
- [ ] **Step 4:** `./internal/inventory/schema/`, `./internal/app/`, `./internal/templates/`, `./internal/api/` pass; `npx vitest run src/features/templates`; eslint.
- [ ] **Step 5: Commit** `refactor(app): find a template's stray sealed values once per lookup`, `test(templates): pin a settings save over an envelope that does not open`, `fix(api): type review.not_sent as a list that is never null`.

### Task 8: Jobs test hygiene (7.90 tests)

**Files:** Modify `internal/jobs/list_host_plan_test.go` (`explainGeneric`). Split `internal/jobs/store_sql_test.go` (434 lines): `TestQueueClaimIsScopedToTheWorkersOrganization` and `TestQueueClaimConcurrency` move to new `internal/jobs/store_sql_claim_test.go`.

**Interfaces:** `func explainGenericOn(ctx context.Context, conn *pgx.Conn, org uuid.UUID, query, execArgs string, analyze bool) (plan any, millis float64, err error)` does today's work in a transaction on `conn`; its deferred cleanup rolls the transaction back first and then runs `DEALLOCATE list_jobs` on `conn` outside it (error ignored: the prepare may not have happened). `explainGeneric(t, app, …)` acquires a connection from `app.Pool()`, calls it, releases, and `t.Fatal`s on an error.

- [ ] **Step 1: Failing test.** `TestExplainGenericFreesItsStatementAfterAFailure`: `testdb.Open`, `seedOrg`, one `Acquire`d connection; `explainGenericOn` with the plan test's query and args string but `'not-a-uuid'` as the organization argument fails; a second call on the same connection with valid arguments succeeds.
- [ ] **Step 2:** write `explainGenericOn` first with today's cleanup (DEALLOCATE inside the transaction) and run `-run TestExplainGenericFrees ./internal/jobs/` — the second call fails with `prepared statement "list_jobs" already exists`; record it.
- [ ] **Step 3:** move the DEALLOCATE after the rollback; the split as a pure move.
- [ ] **Step 4:** `./internal/jobs/` passes; `go test -list . ./internal/jobs/ | wc -l` is the count before plus one; every touched file ≤ ~400 lines.
- [ ] **Step 5: Commit** `test(jobs): free the plan test's statement after a failure and split the store tests`.

### Task 9: A values edit typed during its save survives a change of the stored values (7.90 SPA)

**Files:** Modify `web/src/features/inventory/sections/ValuesSection.tsx` (`ValuesForm` on `useRevisionDraft`), `web/src/features/inventory/GroupDetailPage.tsx` (`ValuesSection key={JSON.stringify([group.id, group.schema])}` — the values leave the key; the comment says why). Tests `web/src/features/inventory/GroupDetailPage.revision.test.tsx`.

**Interfaces:** Consumes Task 1's `useRevisionDraft(group.vars, group.revision, draftOf)`; `draftOf` builds keep markers as today. `ValuesForm` resets its problems to `group.problems` whenever `generation` changes (render-time check against the last seen generation); Save runs `startSave()`, sends `revision`, calls `saved(ticket, res.group.vars, res.group.revision)` then `save.reset()`; Reload calls `reloaded(fresh.vars, fresh.revision)`.

- [ ] **Step 1: Failing tests** (`withSecret(revision, env = 'prod')`):
  - `keeps an edit typed while a save that changes the stored values was in flight`: PUT 1 held; typeEnv `staging`, Save, typeEnv `typed meanwhile`, release with `withSecret(4, 'staging')` (also `current`) → after `Group values saved` the field still reads `typed meanwhile`; refetch to `withSecret(5, 'staging')`; Save → `If-Match: "4"`, body `env: 'typed meanwhile'`.
  - `an unedited values draft follows values someone else saved`: refetch to `withSecret(4, 'staging')` → the field reads `staging`; Save → `If-Match: "4"`.
  - `Reload starts the values over when someone else changed them`: typeEnv `mine`; PUT answers `stale()` and sets `current = withSecret(4, 'staging')`; Save, Reload → field `staging`, alert gone; Save → `If-Match: "4"`.
- [ ] **Step 2:** `npx vitest run src/features/inventory/GroupDetailPage.revision.test.tsx` — the first test fails (field reads `staging` after the remount); the other two pass today through the remount and must keep passing.
- [ ] **Step 3:** implement.
- [ ] **Step 4:** `npx vitest run src/features/inventory`; eslint; tsc; `npx playwright test e2e/inventory.spec.ts`.
- [ ] **Step 5: Commit** `fix(web): keep a values edit typed while its save changes the stored values`.

### Task 10: Offline cold load, `queryClient.query`, list-row names (7.90 SPA)

**Files:** Modify `web/src/api/hooks/useSetupStatus.ts`, `web/src/api/hooks/useMe.ts` (`refetchMe` + its doc comment), `web/src/api/hooks/useHosts.ts` (`useReloadHost` ~125), `web/src/api/hooks/useInventory.ts` (`useReloadInventory` ~54), `web/src/ui/list-row.tsx` (`ListRowName`), `web/src/features/inventory/GroupsPage.tsx` (~52), `web/src/features/agents/AgentRow.tsx` (~46). Tests `web/src/routes/RootRedirect.test.tsx`, `web/src/ui/list-row.test.tsx`, `web/src/features/inventory/GroupsPage.test.tsx`, `web/src/features/agents/AgentsPage.test.tsx`.

**Interfaces:**
- `const setupStatusQuery = { queryKey: setupStatusQueryKey, queryFn: setupStatusQueryFn, networkMode: 'always' } as const`; `useSetupStatus()` is `useQuery(setupStatusQuery)` and `refetchSetupStatus(qc)` is `qc.query({ ...setupStatusQuery, staleTime: 0 })`. Every reader of the key shares the options, so no reader's fetch pauses offline (the paused-reader case `useSettledMe` handles cannot arise here).
- `refetchMe`, `useReloadHost`, `useReloadInventory`: `qc.fetchQuery(` → `qc.query(` with the same options (Ruling 7); the `refetchMe` comment names `query`.
- `ListRowName({ className, style, children }: { className?: string; style?: CSSProperties; children: ReactNode })`, `style` on the span. `GroupsPage`: `<ListRowName className="truncate font-mono text-[13px] text-foreground" style={{ paddingLeft: depth * 16 }}>` with no wrapping span; `AgentRow`: `<ListRowName className="truncate text-[13px]">` with no wrapping span (its `data-row` has no reader: `grep -rn "data-row" web/src web/e2e` shows none for agents — record it).

- [ ] **Step 1: Failing tests.** RootRedirect `offline cold load, shows the failure instead of waiting`: `onlineManager.setOnline(false)`, a fresh `QueryClient` (no cached setup status), `GET /api/v1/setup/status` rejects `new TypeError('Failed to fetch')` → heading `Something went wrong`; no `LOGIN`, no `SETUP`. `list-row.test.tsx` `ListRowName passes its style`: `paddingLeft: 16px`. `GroupsPage.test.tsx` `indents a child group's name without wrapping it`: `screen.getByText('web_eu')` has `aria-hidden="true"`, `paddingLeft: 16px`, and its parent has class `contents`. `AgentsPage.test.tsx` `the host name cell is the row's name cell`: the host name element has `aria-hidden="true"` and its parent has class `contents`.
- [ ] **Step 2:** run the four files — FAIL.
- [ ] **Step 3:** implement.
- [ ] **Step 4:** `npx vitest run src/routes src/api src/ui src/features/inventory src/features/agents src/features/hosts src/features/setup src/features/account`; `grep -rn "fetchQuery\|prefetchQuery\|ensureQueryData" web/src` prints nothing; eslint; tsc.
- [ ] **Step 5: Commit** `fix(web): fail an offline cold load, use queryClient.query, keep list-row names unwrapped`.

### Task 11: SPA tests and the contrast check (7.90 tests)

**Files:** Tests `web/src/ui/detail-layout.test.tsx`, `web/src/features/variables/VariableEditor.test.tsx`.

- [ ] **Step 1: Tests.**
  - `detail-layout.test.tsx` `a nav click on another page is kept for that page`: at `/groups/a#members` (scrolled once), `history.replaceState(null, '', '/groups/b')`, rerender; click the nav link `a` (hash `#a`, marked handled for `/groups/b`); rerender → `scroll` still called once (the click's hash is not acted on). It fails if `withHandled` keeps the old path (then the handled list reads empty for `/groups/b`).
  - `VariableEditor.test.tsx` `a viewer can change none of an opened field's inputs`: read-only `group` editor with `{ name: 'port', type: 'int', required: true, min: 1, max: 65535, default: 8080, description: 'Listen port' }`, `{ name: 'level', type: 'enum', options: ['a', 'b'] }` and a repeater with one sub-field; open each field: every `textbox`, `combobox`, `checkbox`, `switch` and `spinbutton` in the dialog is disabled or `readonly`; the only enabled buttons are Cancel and ones that open a nested field for viewing (list them in the report); typing into Name changes nothing; `onChange` never called.
- [ ] **Step 2:** `npx vitest run src/ui/detail-layout.test.tsx src/features/variables/VariableEditor.test.tsx` — expected to pass (they cover branches that work). For the first, temporarily make `withHandled` return `{ path: prev.path, ids: [...prev.ids, id] }` and watch it fail, then restore; for the second, an enabled input is a bug: fix it in the dialog and say so.
- [ ] **Step 3 (verify only):** the not-sent notice contrast (Ruling 8): `node -e` with the WCAG relative-luminance formula on `#FBBF24` against `#161B26` prints `10.32`; `grep -n "px-warn\|px-surface" web/src/styles/tokens.css` and the notice's classes (`TemplateVariablesPage.tsx` ~234: `text-[var(--px-warn)] bg-card`) recorded.
- [ ] **Step 4:** eslint; tsc.
- [ ] **Step 5: Commit** `test(web): pin the per-page nav handling and a read-only field dialog`.

---

## Self-review

- **Coverage:** 7.89 → Task 1 (schema section; settings and member dialogs checked and pinned, Ruling 2). 7.90 backend: `CreateJob` 23503 → 2; galaxy `Lstat` logging and check-then-open, `errNotGalaxyDir` for a symlink → 3; `:`/newline in the data dir → 4; parser vs run role search → 5; accounts logger → 6; `StraySealed` twice → 7. Tests: `explainGeneric`, `store_sql_test.go` split → 8; play-uid half, galaxy open-error branches → 3; notes `[]`/`null` → 4; unopenable envelope → 7; "too many in flight" line, `Resolve` on tampered tokens → 6; `DetailLayout` path-change, viewer dialog inputs → 11. SPA: offline `RootRedirect`, `qc.fetchQuery`, `ListRowName` nesting → 10; values edit lost on remount → 9; warn contrast → 11 (verify only); `not_sent` nullable → 7. Docs → Global Constraints (noix-docs) plus `docs/upgrades.md` bullets in Tasks 4 and 5.
- **Types:** `useRevisionDraft`/`RevisionDraft`/`SaveTicket` (Task 1) are used by Task 9; the test-kit helpers (Task 1) by Task 9's tests; `InstalledRolesDir` and `errGalaxySymlink` (Task 3) by the runner and `openInstalledRolesFS`; `StrayNames` (Task 7) by `dropStraySealed`.
- **Review Focus:** 1 and 2 in Task 1, 3 in Task 2, 4 in Task 5, 5 in Task 4.
