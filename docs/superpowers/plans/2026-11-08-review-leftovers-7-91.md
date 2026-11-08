# Review Leftovers (7.91) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Every leftover of the 7.89/7.90 reviews (backlog 7.91) is fixed or tested. The variable read finds roles where ansible-playbook finds them. The group page shows a save's problems only beside the draft they were reported for, and the settings dialog follows the page's own steps, which stay bounded. The worker resolves a relative data dir once at start. The parked tests assert real behaviour.

**Architecture:** The parser's role lookup becomes one candidate list per name, plain or with a slash, in ansible-core's order. A candidate that exists, as a directory or as a regular file, ends the search (Tasks 1–2). On the SPA, a save ticket names its draft (generation and revision). Reported problems carry that mark and are shown only while it matches (Task 3). The own-steps record moves its walk (`followOwnSteps`) next to the record, which the settings dialog then shares. Drafts and the dialog hold the revision they are pinned at, and a new step prunes every step below the oldest hold and the group's shown revision (Task 4). Everything else is local: `worker.Load` (Task 5), test hardening (Tasks 6–7), docs (Task 8).

**Tech Stack:** Go 1.27, Postgres test container (`internal/db/testdb`), ansible-core 2.21.5 / ansible-playbook for the real-Ansible pins, React 19 + TypeScript + TanStack Query, Vitest + Testing Library.

**Spec:** Backlog row 7.91 in `docs/BACKLOG.md` (background: rows 7.86, 7.89), and the owner-approved decisions of 2026-11-08 (package B). The decisions file was a scratch file. Its full content is carried in **Rulings** below, which are binding.

## Global Constraints

- Work on the checked-out branch `fix/7.91-review-leftovers`; do not switch branches.
- Everything in the repository is English: code, comments, commits, docs. This package adds no user-visible SPA string. If one becomes necessary, it gets en and de i18n entries.
- Conventional Commits. Every commit ends with exactly `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. Commit only the task's own files.
- TDD: each fix comes with a test that fails first. Watch it fail, then make the minimal change. A test that pins existing behaviour (or Ansible's) says so and is expected to pass at once.
- Go tests run per package with a timeout:
  - `go test -race -count=1 -timeout 900s ./internal/<pkg>/`;
  - `internal/api` needs `-timeout 1800s`;
  - `internal/e2e` runs on its own invocation;
  - never one whole-module `go test ./...`.
- Before every Go commit: `golangci-lint run` (0 issues) and `gofmt -s -l .` (no output).
- SPA:
  - single files with `cd web && npx vitest run <files>`;
  - before the last SPA commit, the full `npx vitest run`, `npx tsc -b` and `npx eslint src`, all clean.
- A file over ~400 lines is a signal to split. A file this plan would grow past that gets its new tests in the new file the task names.
- No migration, no HTTP API change.
- End-user docs (`NoiXdev/noix-docs`) are the controller's, not a task's. That includes the role-lookup wording in the variable-read reference.

## Review Focus

1. A slash role name whose first component is a regular file in `<dir>/roles` (`roles/p` is a file, the role is `p/q`). On disk the lookup gets `ENOTDIR`, not `fs.ErrNotExist`. Ansible searches on and finds `<dir>/p/q`, so the parser must too (Task 2, the `os.DirFS` subtest of `TestAFileAtARoleCandidateEndsTheSearch`).
2. A role name with `..` that stays inside the repository through `<dir>/roles/..`. From `pb/`, `../../etc` is found as `etc`, as Ansible finds it. One that leaves the repository on every candidate is not found "inside the repository" (Task 2, `roles_test.go` rows and `TestRoleCandidatesFollowAnsiblesOrder`).
3. A save refused with 412 after a foreign write already replaced the untouched draft. The fresh draft shows no stale notice, since its save would succeed (Task 3, `drops the stale notice of a refused values save …`).
4. A long session: an edited schema draft pinned at r, then more own values saves than any cap would keep. The draft still saves on the last own revision, because pruning never drops a step at or above a held revision (Task 4, `keeps a chain from a held revision however long it grows`).
5. A worker started with a relative data dir from a working directory whose path holds a space. It is refused at start, and the error names the resolved path; it does not fail at the first run's `ansible.cfg` (Task 5, `TestLoadRefusesADataDirThatResolvesToAnUnsafePath`).

## Rulings

The owner's decisions (binding, copied in full in substance), then the choices this plan makes where they left one open (marked **plan**).

**Variable read (role lookup, `internal/varsource`).**
1. **Slash role names:** a slash role name (`a/b`) is looked up the way Ansible does: as `<dir>/roles/a/b` and under the roles path (`roles_path/a/b`), not only as `<dir>/a/b`. Mirror Ansible's search order exactly, read from ansible-core 2.21.5's role path resolution (`RoleDefinition._load_role_path`).
2. **Dotted three-part names:** Ansible tries a dotted three-part name (`ns.col.role`) as a collection role first. That belongs to backlog 7.96 (roles inside installed collections), so it is out of scope here. The current behaviour stays (reported as `role_collection`), and the 7.91 row notes that this point moved to 7.96.
3. **A regular file at a role candidate:** Ansible stops there, the parser does not. Make the parser behave like Ansible: a regular file at a candidate path ends the search for that role the way Ansible ends it. Check whether Ansible errors or treats it as not found, and mirror that.

**Group page (SPA: `useRevisionDraft`, the values form, the schema section, the settings dialog).**

4. **Problems belong to their draft.** A refetch can rebuild an untouched draft while its save is in flight. A 422 that follows then shows problems for a draft no longer on screen. The problems of a save are shown only while the draft they were reported for is still on screen (same generation); otherwise they are dropped.
5. **Problems keep their revision.** A foreign write between our save and its refetch can show the save's problems next to newer values. Keep the answer's revision with the problems and show them only while the draft is on that revision; otherwise drop them.
6. **Settings dialog and own steps.** The dialog does not follow the page's own steps, so it hits a false 412 when it is opened while a save is in flight. It follows the page's own revision steps by the same mechanism as the page.
7. **Prune the own-steps record.** It grows by one entry per page save. Drop the steps that can no longer be followed: those whose `from` is below the oldest revision any draft is pinned at. A simple cap is fine only if it is provably safe.

**Worker.**

8. **Relative data dir.** `PULSIX_WORKER_DATA_DIR` need not be absolute today. At start the worker resolves it to an absolute path (`filepath.Abs`) before anything uses it, and logs the resolved path once. A relative value keeps working.

**Tests.**

9. A real-Ansible test case for a role found only beside the playbook.
10. The `pg_stat_activity` poll matches on query text. It must match something robust, `application_name` or the backend pid, whichever the test can set.
11. `TestStrayNames` is near-tautological. Make it assert real behaviour, or delete it if another test covers that.
12. `TestExplainGenericFrees…` asserts only `err != nil`. Assert the specific error or message.
13. The `internal/api` tests still swap `slog.Default`. Inject a logger instead where the code allows. Their absence checks must also assert that the captured log is non-empty, so a check cannot pass vacuously.
14. Split `internal/accounts/actions_test.go` (407 lines) and `internal/worker/config_test.go` (515 lines) by topic.

**Docs.**

- `docs/BACKLOG.md`: 7.91 done, noting that the dotted three-part name moved to 7.96.
- `docs/upgrades.md`, "Behaviour changes":
  - a line for the role lookup: more roles can be found, so variable sources can change after the next sync;
  - a line for the relative data dir if it is user-visible.

**Plan rulings.**

- **P1 (item 1): Ansible's search, from the source.** `ansible/playbook/role/definition.py` `RoleDefinition._load_role_path` (ansible-core 2.21.5, `/opt/homebrew/Cellar/ansible/14.5.0_1/libexec/lib/python3.14/site-packages/ansible`) runs these steps:
  1. It tries the collection lookup first for an FQCR (Ruling 2).
  2. For **every** name, plain or with a slash, it tests `unfrackpath(os.path.join(p, role_name))` for `p` in `[<playbook dir>/roles, *DEFAULT_ROLES_PATH, role_basedir (a dependency's only), <playbook dir>]`, and takes the first for which `self._loader.path_exists` (that is `os.path.exists`) is true.
  3. Last, it tests `unfrackpath(role_name)` against the process's working directory.

  The parser mirrors step 2 with the candidates `<dir>/roles/<name>`, the installed roles' `<name>`, then `<dir>/<name>`. It leaves out:
  - `role_basedir`: the parser reads no role dependencies, unchanged;
  - the system entries of `roles_path` (`/usr/share/ansible/roles:/etc/ansible/roles`): outside the repository, as today;
  - the working-directory step: `newRunnerCmd` runs ansible-runner with `cmd.Dir = env.WorkDir`, the run's work dir, which holds no file of the clone.

  `os.path.exists` is false on any error (missing, `ENOTDIR`, `EACCES`), so any `fs.Stat` error means "not there, go on". Candidates are joined lexically (`path.Join`), which is what `unfrackpath`'s `realpath` gives for every path without a symlink ahead of a `..`. A candidate that leaves its tree is skipped (repository: `..` or `../…`; installed roles: not `fs.ValidPath`). An absolute name stays "not found inside the repository". Confirmed with ansible-playbook 2.21.5:
  - `x/y`, present only in `roles_path`, ran from there;
  - `m/n`, present in both `<pb>/roles/m/n` and `<pb>/m/n`, ran from `roles/`;
  - `p/q`, with a regular file at `<pb>/roles/p`, ran from `<pb>/p/q`.
- **P2 (item 3): a regular file is a role without content.** Ansible neither errors nor reports "not found". A regular file at `<pb>/roles/fileonly`, with a role directory `fileonly` in `roles_path`, gives a successful play that runs nothing of either role and warns `Ignoring invalid path provided to plugin path: '<pb>/roles/fileonly' is not a directory`. A regular file at `roles_path/fs` likewise shadows the directory `<pb>/fs`. The parser mirrors this: the role use is `RoleNoSpecs`, with a `role_without_specs` hint that names the file. There is no new note code: the codes are the user docs' contract, and this is a role that adds no fields.
- **P3 (item 1 consequence):** `../../etc` from `pb/` is now found as `etc` through `pb/roles/../../etc`, as Ansible finds it. The `roles_test.go` row "path climbing out of the repo" becomes `../../../etc`, and `../../etc` gets a row of its own (found).
- **P4 (item 2):** the dotted check stays after the search and applies only to names without a slash, unchanged.
- **P5 (items 4, 5): problems carry a draft mark.**
  - `SaveTicket` gains the draft's `generation` and `revision` at `startSave`.
  - The sections keep `reported: Reported | null`, where `Reported = { on: DraftMark, problems }`, and show them via `shownProblems`:
    - `null` shows the group's problems (values) or none (schema), today's semantics for a draft built from the group;
    - a mark equal to the draft's `{generation, revision}` shows its problems;
    - any other mark shows none.
  - A save sets `{on: ticket, problems: []}`. Its 422 replaces that only while `reported.on === ticket` (identity, through a state updater). Any rebuild, answer or newer save in between wins.
  - An answer (save 200 or Reload) is applied where the draft it rebuilt shows up, as today (the `answered` hand-off). It carries its revision:
    - while that equals the draft's revision, its problems are reported on `{generation, revision}`;
    - otherwise `reported` becomes `null`, so the group's problems show. An untouched draft whose revision is not the answer's is on `group.revision`, built from the group's content.
- **P6 (not in the decisions; same defect as item 4):** the stale notice of a 412 is tied to the ticket's generation (`staleOn === generation`). It no longer shows beside a draft that a foreign write already replaced, and the per-generation reset code goes.
- **P7 (item 6):** `follow` moves to `ownSteps.ts` as `followOwnSteps`, and `RevisionStep` moves with it. The dialog sends `followOwnSteps(revision, own.steps)` as its If-Match and records the step from that revision. Its form state is not refilled, as today. Following is safe for the same reason as for a draft: an own step means nobody else wrote, and no page write other than the dialog's own changes its fields.
- **P8 (item 7): prune below the floor, no cap.** `record` drops every step with `from < floor`, where `floor = min(group.revision as last rendered, every held revision)`. Edited drafts hold their pin, and the settings dialog holds its revision while it is open (`useHeldRevision`). A cap is not provably safe: an edited schema draft follows any number of own values saves (Review Focus 4).

  Safety: `followOwnSteps` walks only forward (`to > from`), from a start that is either a held pin or a pin made later. A later pin is `max(pinned, group.revision)` of an untouched draft, a dialog's opening revision, or a Reload's fresh revision, all at least the group's revision when it is made. The group's revision on the page does not decrease: our writes invalidate, which cancels any older fetch in flight. So every step a walk can still take has `from ≥ floor`, and a dropped step is one no walk reaches. The window between a pin and the effect that registers its hold is covered by the group-revision term. Pruning only ever removes a step, which at worst turns a follow into a 412 and a stale notice, never a silent overwrite. Even a wrong bound fails safe.
- **P9 (item 8):** `worker.Load` resolves the data dir right after the default and before `dataDirSafe`, so the safety check sees the path that is actually used. A relative value under an unsafe working directory is refused at start, naming both values. `IdentityDir` derives from the resolved path. The log line: `cmd/worker`'s existing `pulsix-worker starting … data_dir=` now prints the resolved path, and the embedded worker's `embedded worker running` line gains `data_dir`. Both are existing lines, so the path is logged once per worker. It is user-visible (the start log; a relative value now works for runs, whose `ansible.cfg` previously got relative paths), so `docs/upgrades.md` gets a line.
- **P10 (item 10):** the backend pid. The test owns the blocking transaction, so it reads that transaction's `pg_backend_pid()` and polls `wait_event_type = 'Lock' AND $1 = ANY(pg_blocking_pids(pid))`, as `internal/auth`'s `waitForLockWait` does. `CreateJob`'s pool cannot be given an `application_name` without touching `db.Open`.
- **P11 (item 11):** keep and rewrite `TestStrayNames`. `TestStrayTopLevel` covers `StraySealed`'s scan, but nothing pins `StrayNames`' own contract on hand-built input: top-level name, first-seen order, each once, empty and non-nil.
- **P12 (item 12):** the first call must fail at the `EXPLAIN EXECUTE`, after the `PREPARE`, which is the only failure that exercises the clean-up. So the test asserts `pgconn.PgError` code `22P02` and the `explain: ` prefix.
- **P13 (item 13): inject where it is local, swap where it is not.**
  - `streamError` is unexported and takes a `*slog.Logger`. Its four callers pass `slog.Default()`, and its test injects a logger and no longer swaps the default.
  - The password routes log through the package-level `slog.*Context` calls that the whole package uses (about 100 call sites; `Deps` has no logger). Injecting there is a package-wide API change, out of scope, so these tests keep the swap.
  - The success path logs nothing while the audit store works, and the refusals log nothing at all. The three absence checks therefore cannot assert a non-empty log as they stand. They move into one test, `TestChangePasswordLogsNoPassword`, on a server with `apitest.WithFailingAudit()`. Its successful change writes the line `audit: failed to record event … action="POST changePassword"` through the same default logger. That line must be present, and no password may be.
- **P14 (item 14):** split as named in Tasks 5 and 6, pure moves, no test changed in the move.

---

### Task 1: Real-Ansible pins of the role search (item 9; evidence for items 1, 3)

**Files:** Modify `internal/worker/runner/role_search_test.go`.

**Interfaces:** none. Uses `runRepoPlaybook`, `requireAnsiblePlaybook`, and the test's `play`/`debug` helpers.

- [ ] **Step 1: Subtests** of `TestRoleSearchFromASubdirectoryInRealAnsible`. They pin Ansible, so they are expected to pass at once. Each checks `err == nil` and the `"msg": "<x>"` lines:
  - `beside only`: `playbooks/site.yml` = `play("besideonly")`, `playbooks/besideonly/tasks/main.yml` = `debug("from-beside-only")`, nothing installed → `from-beside-only` ran.
  - `slash names`: `play("a/inroles", "b/installed", "c/beside")`. Files: `playbooks/roles/a/inroles/tasks/main.yml` → `from-a-inroles`; `playbooks/a/inroles/tasks/main.yml` → `from-a-beside`; installed `b/installed/tasks/main.yml` → `from-b-installed`; `playbooks/c/beside/tasks/main.yml` → `from-c-beside`. Ran: `from-a-inroles`, `from-b-installed`, `from-c-beside`. Not run: `from-a-beside`.
  - `a file at a candidate`: `play("fileonly", "p/q", "after")`. Files:
    - `playbooks/roles/fileonly` = `"not a role\n"` (a regular file);
    - installed `fileonly/tasks/main.yml` → `from-installed-fileonly`;
    - `playbooks/roles/p` = `"not a directory\n"`;
    - `playbooks/p/q/tasks/main.yml` → `from-p-q`;
    - `playbooks/roles/after/tasks/main.yml` → `from-after`.

    Ran: `from-p-q`, `from-after`. Not run: `from-installed-fileonly` (the file ends the search, and the play does not fail).

  The test's doc comment gains one sentence: these subtests are the evidence for P1/P2, which `internal/varsource` mirrors (backlog 7.91).
- [ ] **Step 2:** `PULSIX_E2E_REAL_ANSIBLE=1 go test -race -count=1 -timeout 900s -run TestRoleSearchFromASubdirectoryInRealAnsible ./internal/worker/runner/` → PASS, no SKIP. A failure is a finding about Ansible: report it, and do not bend the subtest to fit.
- [ ] **Step 3: Commit** `test(runner): pin slash role names, a role beside the playbook and a file at a candidate in real Ansible`.

### Task 2: The parser looks a role up where Ansible does (items 1, 3)

**Files:** Modify `internal/varsource/roles.go`, `doc.go`, `roles_test.go` (rows). Test: `internal/varsource/role_search_test.go` (66 lines now).

**Interfaces (package-internal):**
- `type roleCandidate struct { path string; installed bool }`.
- `func roleCandidates(name, dir string, withInstalled bool) []roleCandidate` returns, in order:
  - `path.Join(dir, "roles", name)`;
  - when `withInstalled` and `fs.ValidPath(path.Clean(name))`, `{path.Clean(name), true}`;
  - `path.Join(dir, name)`.

  A repository path equal to `..` or starting with `../` is left out.
- `roleTarget` gains `file bool`: the candidate is not a directory.

- [ ] **Step 1: Failing tests** in `role_search_test.go`.
  - `TestRoleCandidatesFollowAnsiblesOrder` table (`name, dir, withInstalled` → paths with `(i)` for installed):
    - `x, pb, true` → `pb/roles/x, (i)x, pb/x`;
    - `a/b, pb, true` → `pb/roles/a/b, (i)a/b, pb/a/b`;
    - `a/b, "", true` → `roles/a/b, (i)a/b, a/b`;
    - `../x, pb, true` → `pb/x, x`;
    - `../../x, pb, true` → `x`;
    - `../../../x, pb, true` → none;
    - `./roles/x, pb, false` → `pb/roles/roles/x, pb/roles/x`.
  - `TestSlashRoleNamesAreSearchedLikeAPlainName`:
    - repository: `playbooks/site.yml` with `roles: [a/inroles, b/installed, c/beside, a/shadowed, d/nowhere]`; `playbooks/roles/a/inroles/meta/argument_specs.yml` = `oneOption("inroles_var")`; `playbooks/c/beside/…` = `beside_var`; `playbooks/a/shadowed/…` = `beside_shadowed`;
    - installed: `b/installed/…` = `installed_var`; `a/shadowed/…` = `installed_shadowed`;
    - expected fields `[inroles_var installed_var beside_var installed_shadowed]`;
    - expected states: four `RoleRead`, then `RoleNotFound`;
    - expected note `warning role_not_found playbooks/site.yml:2` with exactly `role "d/nowhere" not found at playbooks/roles/d/nowhere or playbooks/d/nowhere or among the roles installed from requirements.yml; its variables are not read`.
  - `TestAFileAtARoleCandidateEndsTheSearch`:
    - repository: `playbooks/site.yml` with `roles: [fileonly, p/q, instfile]`;
      - `playbooks/roles/fileonly` = `"x"`;
      - `playbooks/fileonly/meta/argument_specs.yml` = `beside_var`;
      - `playbooks/roles/p` = `"x"`;
      - `playbooks/p/q/meta/argument_specs.yml` = `pq_var`;
      - `playbooks/instfile/meta/argument_specs.yml` = `beside_inst`;
    - installed: a regular file `instfile`;
    - expected fields `[pq_var]`;
    - expected states `fileonly` `RoleNoSpecs`, `p/q` `RoleRead`, `instfile` `RoleNoSpecs`;
    - expected notes: two `hint role_without_specs playbooks/site.yml:2`, with messages exactly `role "fileonly" is the file playbooks/roles/fileonly, which Ansible takes for a role without content; it adds no fields` and `role "instfile" is the file (requirements)/instfile, which Ansible takes for a role without content; it adds no fields`.

    Two subtests run it: `mapFS`, and the same tree written under `t.TempDir()` and read through `os.DirFS`, where `Stat("playbooks/roles/p/q")` fails with `ENOTDIR`.
  - `roles_test.go` `TestRoleLookupVariants` (P3):
    - row `path climbing out of the repo` becomes `../../../etc` (`RoleNotFound`, `warning role_not_found pb/play.yml:2`);
    - new row `path through the roles directory` `../../etc` with `etc/meta/argument_specs.yml` → `RoleRead`, fields `[x_var]`.
- [ ] **Step 2:** `go test -race -count=1 -timeout 900s -run 'RoleCandidates|SlashRoleNames|AFileAtARoleCandidate|RoleLookupVariants' ./internal/varsource/` → FAIL. `roleCandidates` is undefined: comment the first test out to watch the others fail on today's lookup.
- [ ] **Step 3: Implement.**
  - `findRole` keeps two guards: an absolute name → `RoleNotFound`, `where: "inside the repository"`; `""`, `.` and `..` → `(not a role name)`.
  - It then walks `roleCandidates(name, dir, w.p.roles != nil)`, calling `fs.Stat` on the repository or installed FS:
    - an error → next candidate;
    - a directory → `RoleRead`;
    - anything else → `RoleRead` with `file: true`.
  - Nothing found:
    - a name without a slash and with ≥ 2 dots → `RoleCollection` (P4);
    - else `RoleNotFound`, with `where`:
      - `"at " + <the repository candidates joined by " or ">`, plus `" or among the roles installed from requirements.yml"` when an installed candidate was listed, clipped as today;
      - `"inside the repository"` when no repository candidate is left.
  - `useRole`'s default branch: a `file` target → `RoleNoSpecs`, and the P2 hint (path shown with `InstalledRolesPrefix` when installed). It reads no spec.
  - `isDir` goes if unused.
  - `target`'s comment and the "plain name" comment state P1 in brief: candidates, order, function name, the left-out steps and why.
  - `doc.go`: "a plain role name" → "a role name, plain or with a slash, …".
- [ ] **Step 4:** `go test -race -count=1 -timeout 900s ./internal/varsource/ ./internal/worker/` → PASS.
- [ ] **Step 5: Commit** `fix(varsource): look a role up where ansible-playbook does, a file at a candidate included`.

### Task 3: Problems and the stale notice belong to their draft (items 4, 5; P5, P6)

**Files:**
- Create `web/src/features/inventory/sections/draftProblems.ts` and `draftProblems.test.ts`.
- Modify `sections/useRevisionDraft.ts`, `sections/ValuesSection.tsx`, `sections/SchemaSection.tsx`, `sections/useRevisionDraft.test.ts`.
- Test: create `web/src/features/inventory/GroupDetailPage.problems.test.tsx`.

**Interfaces:**
- Produces in `useRevisionDraft.ts`: `export interface DraftMark { readonly generation: number; readonly revision: number }`; `SaveTicket extends DraftMark` (`edits` stays). `startSave()` returns the draft's current generation and revision (`current(s)`).
- Produces in `draftProblems.ts`:
  - `export interface Reported { readonly on: DraftMark; readonly problems: readonly Problem[] }`;
  - `export function shownProblems(reported: Reported | null, draft: DraftMark, fromGroup: readonly Problem[]): readonly Problem[]`, implementing P5's three cases;
  - `export function refusedWith(ticket: SaveTicket, problems: readonly Problem[]): (cur: Reported | null) => Reported | null`, which returns `{on: ticket, problems}` when `cur?.on === ticket`, else `cur`.

- [ ] **Step 1: Failing tests.**
  - `draftProblems.test.ts`:
    - `shownProblems`: `null` → `fromGroup`; same generation and revision → the problems; other generation → `[]`; other revision → `[]`.
    - `refusedWith`: replaces a `Reported` whose `on` is the ticket; keeps one with another `on`, and `null`.
  - `useRevisionDraft.test.ts` `a save ticket names the draft it was started on`: after an edit at revision 3 → `{generation: 0, revision: 3}`; after a rebuild to revision 4 → `{generation: 1, revision: 4}`.
  - `GroupDetailPage.problems.test.tsx`. Each test holds the PUT (`await new Promise<void>((r) => (release = r))` inside the handler) and waits for `puts` to have length 1 before the refetch.
    - `drops the problems of a refused values save when someone else's values replaced the draft meanwhile`:
      - setup: `group()`; click Save values unedited; while held, `current = group({ revision: 4, vars: { env: 'staging' } })`, `refetch(qc)`;
      - release with 422 `{ status: 422, errors: [{ location: 'body.vars.env', message: 'is required' }] }`;
      - expected after Save is enabled again: `env` has value `staging`, and there is no `is required` in the values region.
    - `drops the problems of a refused schema save when someone else's schema replaced the draft meanwhile`:
      - setup: the same with the schema PUT, and `current = group({ revision: 4, schema: OTHER })` (`OTHER` as in the schema revision test, defined locally);
      - release with 422 at `body.schema.fields[0].name`;
      - expected: `region` is shown, and there is no `/The schema has/`.
    - `drops the problems a values save answered when someone else's values landed before that answer`:
      - setup: while held, `current = group({ revision: 5, vars: { env: 'other' } })`, `refetch(qc)`;
      - release with `group({ revision: 4, vars: {}, problems: [{ path: 'env', message: 'is required' }] })`;
      - expected after `Group values saved`: `env` is `other`, and there is no `is required`.
    - `drops the stale notice of a refused values save when someone else's values replaced the draft meanwhile`:
      - setup: as the first test, but release with `stale()`;
      - expected: no `Reload` button in the values region after Save is enabled again.
- [ ] **Step 2:** `cd web && npx vitest run src/features/inventory/sections/draftProblems.test.ts src/features/inventory/sections/useRevisionDraft.test.ts src/features/inventory/GroupDetailPage.problems.test.tsx` → FAIL (module missing; the ticket has no mark; the four page tests show the problem or notice).
- [ ] **Step 3: Implement** P5/P6.
  - `ValuesForm`:
    - state:
      - `reported` (`null` at mount);
      - `answered: { revision: number; problems: Problem[] } | null`;
      - `seen`;
      - `staleOn: number | null`.
    - On a generation change: `setReported(answered && answered.revision === revision ? { on: { generation, revision }, problems: answered.problems } : null)`. `answered` is cleared on every render that holds one, as today.
    - Save: `const ticket = startSave(); setReported({ on: ticket, problems: [] })`. On 200, `setAnswered({ revision: res.group.revision, problems })`; on 412, `setStaleOn(ticket.generation)`; on 422 with problems, `setReported(refusedWith(ticket, ps))`.
    - Reload: sets `answered` with `fresh.revision` and clears `staleOn`.
    - Rendering: `shownProblems(reported, { generation, revision }, toProblems(group.problems))`; the notice when `staleOn === generation`.
  - `SchemaSection`: the same with `fromGroup = []` and no `answered`. The per-generation reset block goes. Discard calls `setReported(null)`.
  - Both doc comments state the rule in two sentences.
- [ ] **Step 4:** the Step 2 command, then all of `src/features/inventory/` → PASS. The existing `valuesDraft`, `schemaRevision` and `drafts` tests pass unchanged.
- [ ] **Step 5: Commit** `fix(inventory): show a save's problems and stale notice only beside the draft they were reported for`.

### Task 4: The settings dialog follows the page's own steps, and the record stays bounded (items 6, 7; P7, P8)

**Files:** Modify `web/src/features/inventory/ownSteps.ts`, `ownSteps.test.ts`, `sections/useRevisionDraft.ts`, `sections/useRevisionDraft.test.ts`, `sections/ValuesSection.tsx`, `sections/SchemaSection.tsx`, `GroupSettingsDialog.tsx`, `GroupDetailPage.tsx`, `GroupDetailPage.dialogRevision.test.tsx`.

**Interfaces:**
- Produces in `ownSteps.ts`:
  - `export interface RevisionStep` (moved from `useRevisionDraft.ts`);
  - `export interface OwnSteps { steps: readonly RevisionStep[]; record(step: RevisionStep): void; hold(revision: number): () => void }`;
  - `export function followOwnSteps(pinned: number, steps: readonly RevisionStep[]): number`, today's `follow`;
  - `export function useHeldRevision(hold: OwnSteps['hold'], revision: number | null): void`, which holds `revision` while it is non-null, re-holds on change and releases on unmount (a `useEffect` returning the release);
  - `export function useOwnStepsRecord(groupId: string, revision: number): OwnSteps`.

  The default context's `hold` returns a no-op release.
- Changes: `useRevisionDraft(stored, revision, build, own: Pick<OwnSteps, 'steps' | 'hold'>)`. It holds `pinned` while the draft is touched (`useHeldRevision(own.hold, s.touched ? s.pinned : null)`). The sections pass `own`, and `GroupDetail` passes `group.revision`.

- [ ] **Step 1: Failing tests.**
  - `ownSteps.test.ts`. The record is rendered as `renderHook(({ rev }) => useOwnStepsRecord('g1', rev), …)`, and the two existing tests pass `rev: 3`.
    - `drops on record the steps below the group's revision`: rev 3, record `3→4`, rerender rev 4, record `4→5`, rerender rev 5, record `5→6` → `[{5,6}]`.
    - `keeps the steps from the shown revision while its refetch has not landed`: rev 3, record `3→4` and `4→5` → both kept.
    - `keeps a chain from a held revision however long it grows`: `hold(3)`, then 60 rounds of record `r→r+1` and rerender `r+1` → 60 steps from `3→4`. After the release and one more record → only that last step.
    - `useHeldRevision holds, re-holds and releases`: a `vi.fn` hold whose release is a `vi.fn`. Expected calls: mount 3 → `hold(3)`; rerender 4 → release, then `hold(4)`; rerender `null` → release; unmount → nothing more.
  - `useRevisionDraft.test.ts`: `setup` passes `{ steps: steps ?? [], hold }` with a spy. New `holds its pinned revision while edited and releases it when rebuilt`: edit at 3 → `hold(3)`; `saved(ticket, C, 5)` untouched → released, and no further hold.
  - `GroupDetailPage.dialogRevision.test.tsx` `the settings dialog opened during a values save follows that save`:
    1. The values PUT is held. Its answer sets `current = group({ revision: 4 })` and returns it.
    2. The PATCH records the request and returns `group({ revision: 5 })`.
    3. Click Save values unedited, wait for the PUT, open Edit (`dialog` `Edit group`).
    4. Release, and `await screen.findByText('Group values saved')`.
    5. Rename to `web_fe` and save → the PATCH's `If-Match` is `"4"` (today `"3"`).

    The existing test (a foreign refetch stays on `"3"`) is the counter-case.
- [ ] **Step 2:** `cd web && npx vitest run src/features/inventory/ownSteps.test.ts src/features/inventory/sections/useRevisionDraft.test.ts src/features/inventory/GroupDetailPage.dialogRevision.test.tsx` → FAIL.
- [ ] **Step 3: Implement** P7/P8.
  - `useOwnStepsRecord` keeps:
    - `holds = useRef(new Map<symbol, number>())`;
    - `shown = useRef(revision)`, updated in a `useEffect`.
  - `hold` is a stable `useCallback`: it adds `Symbol()` → revision, and its release deletes that key.
  - `record` computes `floor = Math.min(shown.current, ...holds.current.values())`, then `setRecord((r) => (r.groupId === groupId ? { groupId, steps: [...r.steps.filter((s) => s.from >= floor), step] } : r))`. No ref is read during render.
  - `useRevisionDraft` imports `followOwnSteps` and the types from `ownSteps.ts` (no value import back).
  - `GroupSettingsDialog`:
    - `useHeldRevision(own.hold, revision)`;
    - the save computes `const from = followOwnSteps(revision, own.steps)`, sends it, and records `{ from, to: res.group.revision }`;
    - its doc comment's last sentence becomes "… never a newer one, except along the page's own steps (backlog 7.91)".
  - `ownSteps.ts`'s doc carries P8's safety paragraph in three sentences.
- [ ] **Step 4:** `npx vitest run src/features/inventory/`, then the full `npx vitest run`, `npx tsc -b` and `npx eslint src` → clean.
- [ ] **Step 5: Commit** `fix(inventory): let the settings dialog follow the page's own steps and prune the steps no draft can follow`.

### Task 5: A relative worker data dir is resolved at start (item 8; config tests split, item 14)

**Files:**
- Modify `internal/worker/config.go`, `internal/app/embedded_worker.go` (log field).
- Split `internal/worker/config_test.go` into:
  - `config_test.go`: `TestLoadDefaults`, `ServerNameOverride`, `Overrides`, `IgnoresAliasedEnv`, `IdentityDirAndToken`, `envSlice`, `mustZone`;
  - `config_errors_test.go`: `TestLoadErrors`;
  - `config_play_test.go`: `PlaySupervisor`, `PlaySupervisorIgnoresEnvironment`, `PlayUID`;
  - `config_datadir_test.go`: `TestLoadAllowsOnlySafeDataDirCharacters` plus the new tests.

**Interfaces:** none new. `Config.DataDir` is documented as absolute ("Load resolves a relative value against the working directory once, at start").

- [ ] **Step 1:** pure move into the four files (each under 300 lines), imports trimmed. `go test -race -count=1 -timeout 900s ./internal/worker/ -run TestLoad` → PASS. Commit `test(worker): split the config tests by topic`.
- [ ] **Step 2: Failing tests** in `config_datadir_test.go`, both using `t.Chdir`:
  - `TestLoadResolvesARelativeDataDir`: `t.Chdir(t.TempDir())`, `wd, _ := os.Getwd()`, no certificate paths. For `data` and `./x/../data`: `cfg.DataDir == filepath.Join(wd, "data")`, `cfg.IdentityDir == filepath.Join(wd, "data", "identity")`, and `cfg.CertPath` is below it.
  - `TestLoadRefusesADataDirThatResolvesToAnUnsafePath`: chdir into a fresh `a b` directory below `t.TempDir()` → the error contains `PULSIX_WORKER_DATA_DIR`, `"data"` and `(resolved to "<wd>/data")`.
- [ ] **Step 3:** `go test -race -count=1 -timeout 900s -run 'RelativeDataDir|ResolvesToAnUnsafePath' ./internal/worker/` → FAIL.
- [ ] **Step 4: Implement** P9.
  - After the default: `abs, err := filepath.Abs(raw)`; an error → `fmt.Errorf("%s %q: resolve to an absolute path: %w", envDataDir, raw, err)`.
  - `dataDirSafe(abs)`. The refusal message names `(resolved to %q)` only when `abs != raw`; the existing text stays otherwise.
  - The embedded worker's `embedded worker running` log gains `"data_dir", wcfg.DataDir`.
- [ ] **Step 5:** `go test -race -count=1 -timeout 900s ./internal/worker/ ./cmd/worker/` and `go test -race -count=1 -timeout 900s ./internal/app/` → PASS.
- [ ] **Step 6: Commit** `fix(worker): resolve a relative data dir once at start`.

### Task 6: Parked tests assert what they claim (items 10, 11, 12; accounts split, item 14)

**Files:**
- Modify `internal/jobs/job_hosts_race_test.go`, `internal/inventory/schema/secrets_test.go`, `internal/jobs/list_host_plan_test.go`.
- Split `internal/accounts/actions_test.go` into:
  - `actions_test.go`: helpers and the per-action tests (`SetTemporaryPassword`, `ResetSecondFactor`, `LockOutVoidsResetLinks`, `EndSessions`, `Reactivate`, `ReactivateAnAccountWhoseInvitationIsPending`);
  - `actions_notices_test.go`: `NoticeFailureLogsThroughTheServiceLogger`, `NoticesSkipDeactivatedAccounts`, `NotifyDeactivated`;
  - `actions_refusals_test.go`: `adminActions` and the three `TestAdminActionsRefuse…`.

- [ ] **Step 1 (item 10, P10):**
  - Right after `Begin`, `tx.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&blockerPID)`.
  - The poll becomes `SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE datname = current_database() AND wait_event_type = 'Lock' AND $1 = ANY(pg_blocking_pids(pid)))` into a `bool`. The comment says why: it matches the blocker, not query text.
  - `go test -race -count=5 -timeout 900s -run TestCreateJobSkipsAHostDeletedMeanwhile ./internal/jobs/` → PASS. This pins existing behaviour.
- [ ] **Step 2 (item 11, P11):** `TestStrayNames` becomes a table over hand-built `[]schema.StrayValue`, asserting `got != nil` and `slices.Equal`:
  - `nil` → `[]`;
  - `{Path: "token"}` → `[token]`;
  - `vhosts[0].password`, `vhosts[1].password` → `[vhosts]`;
  - `db.auth.password` → `[db]`;
  - `{Path: "creds", Container: true}` → `[creds]`;
  - `b.x`, `a`, `b[2]`, `a.y` → `[b a]`.

  Its comment states the contract (first-seen order, each once, empty and non-nil). To watch it bite, mutate `topLevel` to split only at `.` → the `vhosts[0]` row FAILs; restore. `go test -race -count=1 -timeout 900s ./internal/inventory/schema/` → PASS.
- [ ] **Step 3 (item 12, P12):** the first call's error must satisfy:
  - `errors.As(err, &pgErr)` with `pgErr.Code == "22P02"` (`github.com/jackc/pgx/v5/pgconn`);
  - `strings.HasPrefix(err.Error(), "explain: ")`.

  Otherwise `t.Fatalf` names the error. To watch it bite, temporarily break the `PREPARE` (append `x` to `query`) → the error is no longer `explain: … 22P02` and the assertion FAILs; restore. `go test -race -count=1 -timeout 900s -run TestExplainGenericFreesItsStatementAfterAFailure ./internal/jobs/` → PASS.
- [ ] **Step 4 (item 14):** the accounts split, a pure move, each file under 300 lines. `go test -race -count=1 -timeout 900s ./internal/accounts/` → PASS, with the same test count as before (`go test -list . ./internal/accounts/ | wc -l` before and after).
- [ ] **Step 5: Commit** `test: match the lock wait by pid, pin StrayNames and the explain clean-up, split the account action tests`.

### Task 7: The `internal/api` log tests cannot pass vacuously (item 13, P13)

**Files:**
- Modify `internal/api/job_sse.go` (`streamError` and its 4 callers), `internal/api/job_sse_internal_test.go`, `internal/api/password_handlers_test.go`.
- Create `internal/api/password_logs_test.go`.

**Interfaces:** `func streamError(ctx context.Context, log *slog.Logger, writeFrame func(string, any) error, stage string, err error)`. Callers pass `slog.Default()`.

- [ ] **Step 1: Tests.**
  - `TestStreamErrorFrameIsGenericAndLogged` builds `log := slog.New(slog.NewTextHandler(&logs, nil))`, passes it, and no longer touches `slog.Default`. Its assertions stay.
  - `password_logs_test.go` takes `captureLog` (moved), with a comment: the API package logs through the package-level `slog` functions (`Deps` has no logger), so the default is swapped; no test in `internal/api` runs `t.Parallel`, which this relies on.
  - `TestChangePasswordLogsNoPassword` uses `apitest.New(t, apitest.WithFailingAudit())`, then `captureLog`.
    - User A has password `P` and a passkey. Its refusals: wrong current (`not the password`, next `N1`), too short (`elevenchars`), same as current. Then a success `P` → `N2` (204).
    - User B is `markMustChange`d with an expired temporary password `T`, and is refused with `T` (`temporary_password_expired`).
    - The log must contain `audit: failed to record event` and `POST changePassword`, so it is not empty. It must not contain `P`, `N1`, `N2`, `T` or `elevenchars`.
  - `password_handlers_test.go`: the three tests drop `captureLog` and their log checks. Each gets a one-line comment pointing to `TestChangePasswordLogsNoPassword`. Their audit and status assertions stay.
- [ ] **Step 2:** `go test -race -count=1 -timeout 1800s -run 'StreamErrorFrame|ChangePassword' ./internal/api/` → the stream test does not compile until Step 3. To watch the log test bite, temporarily drop `WithFailingAudit()` → it FAILs on the missing audit line; restore.
- [ ] **Step 3:** change `streamError`'s signature and its callers.
- [ ] **Step 4:** `go test -race -count=1 -timeout 1800s ./internal/api/` → PASS. `grep -n 'slog.SetDefault' internal/api/*_test.go` lists only `password_logs_test.go`.
- [ ] **Step 5: Commit** `test(api): inject the stream error logger and prove the password log check captured a line`.

### Task 8: Upgrade notes and backlog (docs)

**Files:** Modify `docs/upgrades.md`, `docs/BACKLOG.md`.

- [ ] **Step 1:** `docs/upgrades.md`, top of "Behaviour changes to check after an upgrade", two bullets:
  - **7.91, role lookup in the variable read:** a role named with a slash (`roles: [a/b]`) is now looked up as `<playbook dir>/roles/a/b`, then among the roles installed from `requirements.yml`, then as `<playbook dir>/a/b`, the order ansible-playbook uses for every role name. A regular file where a role is looked for ends the lookup, as in Ansible, and the role adds no fields. After the next sync, a playbook can gain fields from a role it did not find before, or lose those of a role that a file or an earlier candidate now shadows.
  - **7.91, relative worker data dir:** a relative `PULSIX_WORKER_DATA_DIR` is resolved against the worker's working directory once at start. The start log (`data_dir`) shows the absolute path, and runs get absolute paths in their `ansible.cfg`. A value whose resolved path holds a character outside `letters, digits and . _ / + @ = -` is refused at start.
- [ ] **Step 2:** `docs/BACKLOG.md`:
  - Row 7.91 → `done`. Its text: package `docs/superpowers/plans/2026-11-08-review-leftovers-7-91.md`, branch `fix/7.91-review-leftovers` (2026-11-08), then one sentence per group as in Tasks 1–7:
    - P1/P2's findings (a file at a candidate is a role without content; ansible-core 2.21.5 `RoleDefinition._load_role_path`);
    - P8's floor rule;
    - P13's ruling (the password routes keep the default swap; no logger in `Deps`).

    It ends with: "The dotted three-part name (Ansible tries `ns.col.role` as a collection role first) moved to 7.96."
  - Row 7.96 gains: "From 7.91: ansible-playbook tries a dotted three-part role name as a collection role before the role search; the variable read still searches first and reports `role_collection` only when nothing is found."
- [ ] **Step 3:** `git diff --check` is clean. `grep -cE '^\| 7\.9[16] ' docs/BACKLOG.md` prints 2.
- [ ] **Step 4: Commit** `docs: record 7.91`.

---

## Self-review

- **Coverage:**
  - Ruling 1 → Task 2, evidenced by Task 1.
  - Ruling 2 → P4 (no code) and Task 8.
  - Ruling 3 → Task 2 (P2), evidenced by Task 1.
  - Rulings 4, 5 → Task 3; P6 → Task 3.
  - Rulings 6, 7 → Task 4.
  - Ruling 8 → Task 5.
  - Ruling 9 → Task 1.
  - Rulings 10–12 → Task 6.
  - Ruling 13 → Task 7.
  - Ruling 14 → Tasks 5 (config) and 6 (accounts).
  - Docs → Task 8.
- **Types:**
  - `DraftMark` and `SaveTicket` (Task 3) are used by `draftProblems.ts` and the sections.
  - `OwnSteps.hold`, `followOwnSteps`, `useHeldRevision` and `RevisionStep` (Task 4) are used by `useRevisionDraft`, the dialog and the tests. After Task 4, `RevisionStep` is imported from `ownSteps.ts` everywhere.
  - `roleCandidate`, `roleCandidates` and `roleTarget.file` live in `roles.go` only.
  - `streamError`'s new parameter order is used by its 4 callers.
- **Review Focus:**
  - 1 → Task 2 (the `os.DirFS` subtest);
  - 2 → Task 2 (`roles_test.go` rows, candidates table);
  - 3 → Task 3 (stale test);
  - 4 → Task 4 (`keeps a chain from a held revision …`);
  - 5 → Task 5 (`TestLoadRefusesADataDirThatResolvesToAnUnsafePath`).
