# Per-Commit Requirements Cache (7.88) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Every repository run (and every repo sync) has exactly the roles and collections its commit's requirements files name, installed once per commit and worker into a verified, immutable cache entry, and the run log says what was installed or reused.

**Architecture:** A new package `internal/worker/requirements` holds the rules: `OpenDir` (open, then verify, named refusals), `Find` (which files count), the shape and local-source checks, the key, the lock, the take-over of what galaxy wrote, the atomic fill and the eviction, behind a `Galaxy` interface and a `Clock` (Tasks 1–6). `runner.Galaxy` runs ansible-galaxy like the play — through the setuid helper as the play uid, under the play supervisor — with write access to its temp entry only (Task 7, owner decision 2026-11-06). The manager ships `repo.repo_subpath` in a run's claim (Task 8); the runner reads it, installs before the run files are written and names both keys in `ansible.cfg` (Tasks 9–10); the sync uses the same `Ensure` and its variable read opens the entry's roles (Task 11). Real Ansible proves it offline (Tasks 12, 14) and the worker image proves the isolation (Task 13).

**Tech Stack:** Go 1.27 (`os.Root`, `syscall.Flock`, yaml.v3), ansible-core 2.21.5 (`ansible-galaxy role|collection install`, `ansible-playbook`), ansible-runner 2.4.3 (CI), PostgreSQL 18 testcontainer for `internal/jobs` and `internal/e2e`.

**Spec:** `docs/superpowers/specs/2026-11-06-requirements-cache-design.md` (binding, including "Owner answers", "Calls made after the builder check" and "Owner decision 2026-11-06: galaxy runs under the play uid", which supersedes goal 4's paths). Read it before any task; the goal numbers below are its sections.

## Global Constraints

- Work on the checked-out branch `feat/7.88-requirements-cache`; do not switch branches.
- Everything in the repository is English. No SPA change and no new UI string; the run log lines are English (spec goal 6). (SPA tooling, if ever needed: `PATH=$HOME/.nvm/versions/node/v24.14.0/bin:$PATH`, in `web/`.)
- Conventional Commits; every commit ends with exactly `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. Commit only the task's own files.
- TDD: failing test first, watched failing, then the minimal change. A "verify only" step records the command's output in the task report.
- Tests per package with a timeout: `go test -race -count=1 -timeout 900s ./internal/<pkg>/`; `internal/e2e` only on its own invocation with `-timeout 1800s`. Never one whole-module `go test ./...`. Full suite: `make test`. Coverage: `internal/worker/requirements` ≥ 80 %.
- Before every commit: `golangci-lint run` (0 issues) and `gofmt -s -l .` (no output).
- Real-Ansible tests are gated exactly like `requireAnsiblePlaybook` (`internal/worker/runner/unsafe_test.go`): skip when the binary is missing, fail when `PULSIX_E2E_REAL_ANSIBLE=1`. Locally ansible-core 2.21.5 is at `/opt/homebrew/bin`; ansible-runner is not installed locally, so the `internal/e2e` real pass runs in CI — record which ran.
- The isolation's kernel half is tested only inside the worker image: `internal/e2e/uidboundary`, compiled and run by `TestPlaybookUIDBoundary` (needs docker and the image `noixdev/pulsix-worker:dev` or `PULSIX_E2E_WORKER_IMAGE`; CI's docker job). Outside the image those tests skip; unit tests prove the command shape and the modes.
- No test reaches the network and nothing is installed outside a test's temp dirs: galaxy sources are vendored in the fixture repository (tarballs built with `archive/tar` + `compress/gzip`, collections as `type: dir`).
- No migration, no sqlc change, no proto change: the claim field is JSON inside `kind_payload` (Ruling 1). The wire change follows `docs/upgrades.md` (additive; a new worker derives the field from an older manager's claim).
- The `requirements` package imports nothing from `internal/worker` or `internal/worker/runner` (both import it). Package-level state only for constants, sentinel errors and compiled regexps.
- A file over ~400 lines gets no new code: `runner/runner.go` (835), `runner/inventory.go` (600) and `runner/runner_test.go` (1741) only get edits of existing lines; new code goes into the new files each task names.
- Backlog row 7.88 and the end-user docs in `NoiXdev/noix-docs` (branch `pulsix`) are updated by the controller after the branch: `user/playbooks-and-jobs.md` "Sources" (runs install roles and collections from the three files per commit; the first run of a commit takes longer; pin versions for identical installs across workers; the worker must reach the galaxy source; private servers unsupported; what the log shows; a role name must be a plain directory name), `reference/variable-sources.md` (installed roles are those installed for the synced commit; roles inside collections still not read), `admin/installation/requirements.md` "Worker host" (outbound HTTPS to galaxy.ansible.com or the git hosts the requirements name; disk for three installs per repository), `admin/operations.md` (`<repo>.requirements/` next to each clone; a leftover `<repo>.galaxy` may be removed by hand), `reference/changelog.md`. The worker configuration page also says that with `PULSIX_WORKER_PLAY_UID` set requirements install as the play uid, and that without it the install is not isolated (a warning in the worker log). Follow-ups from the spec's "Found while specifying" are filed by the controller.

## Review Focus

1. A role or collection that names a path outside its install target — a role dependency `name: ../../../ESCAPED_DEP` inside a tarball, a collection tarball whose `MANIFEST.json` name climbs to an absolute path (both verified to escape with ansible-core 2.21.5): under the play uid galaxy gets "Permission denied" and nothing exists at the target (Task 13 `TestRequirementsCannotWriteOutsideTheirEntry`; its fixtures are proven to attack by Task 12 `TestTraversalFixturesEscapeWithoutIsolationInRealAnsible`); a top-level role `name` with a path is refused before galaxy runs (Task 2).
2. What galaxy (as the play uid) left in the temp entry — a symlink out, a hard link, a FIFO, a setuid file — never reaches a published entry, and nothing in a published entry is owned by or writable for the play uid (Task 5 `TestTakeOverRefuses`, Task 10 `TestPublishedEntryIsTheWorkersOwn`, Task 13 ownership check).
3. A worker with `PULSIX_WORKER_PLAY_UID` set whose isolation does not work (no supervisor, missing or refusing helper): the install fails by name and galaxy never runs as the worker (Task 7 `TestGalaxyRefusesThePlayUIDWithoutTheSupervisor`, `TestGalaxyNamesAHelperThatDoesNotStart`, `TestGalaxyReportsAHelperRefusal`).
4. A run after a push, on a worker that synced the previous commit: it installs for its own commit and never reads the previous commit's roles (Task 6 `TestEnsureKeysTheEntryByCommitAndContent`, Task 10 `TestRepoRunInstallsAgainForANewCommit`).
5. A worker killed in the middle of an install, or a run cancelled while galaxy hangs: the half-filled `.tmp-*` (partly owned by the play uid) is never read and is removed (Task 4 `TestSweepTemp`, Task 6 `TestEnsureRemovesAStaleTempDir`, Task 7 `TestGalaxyStopsItsProcessGroupOnCancel` and `TestGalaxyRemoveAllAsThePlayUID`, Task 10 `TestRepoRunCancelledDuringTheInstall`).

## Rulings

Choices this plan makes where the spec left one, each verified where it touches Ansible (ansible-core 2.21.5, local fixtures only):

1. **Claim field.** `repo.repo_subpath` is a key of the run claim's JSON kind payload (`repoKindPayload`), always present (`""` without a repo subpath). The worker reads it as `*string`: absent means an older manager, then it is derived (spec goal 8); a claim `subpath` that does not end in `/<playbook path>` (or equal it) fails the run closed: `the claim's subpath %q does not end with the playbook path %q`.
2. **Exports.** `OpenDir` and its sentinels are exported (`ErrCacheSymlink`, `ErrCacheNotDirectory`, `ErrCacheReplaced`): the runner and the sync use and test them. The swap hook is a parameter (`openDirWith(path, afterOpen func())`), not a package variable. Entries are opened with `OpenDir(<cache>/<key>)` — the one function — and the cache directory's root is used for `Mkdir`, `Rename`, `RemoveAll`, `Chtimes` and the lock file.
3. **Find, then Ensure.** `Find(clone, subpath)` is exported; callers call it first and run `HeadCommit` only when files count, then `Ensure(Request{Files, Commit, …})`. The sync's variable read keeps its own `HeadCommit` call (its time-limit tests depend on that). A counted path the clone's confined root refuses (a link out, `.git`) fails the step; a directory at a counted location is not counted.
4. **Nothing to install never touches the cache.** No counted file: no line, zero `Entry`. Counted files that list nothing: one line, zero `Entry`, no lock, no legacy removal. So `<clone>.galaxy` of a repository that dropped its requirements stays until removed by hand (`docs/upgrades.md`, Task 11).
5. **Play-uid share per run** (spec left it open): `ShareRepoRead(RepoCacheDir, entry.Dir)` on every run with a play uid, like the clone. The sync shares no entry; under a play uid its install opens the clone read-only to the play group (`prepareInstall`, Task 7), as runs already do.
6. **Lines.** `Request.Line func(string)`; `Ensure` removes URL userinfo from every galaxy line and from the error tail (same pattern as `runner.urlCredPattern`), caps each command at 500 lines then `… N more lines`; the runner sends each line through its redactor as a `stdout` event `{"text": …}`; the sync sends them the same way with userinfo removal only (galaxy never receives the sync claim's one secret, the git credential). Lines stream **before** `run_start`: the step sits right after `materializePlaybook`, before any run file is written. On failure the caller sends its own error as the last line, `Requirements: <error text>` (e.g. `Requirements: install requirements: open requirements cache …: a symlink, which is not followed`); on cancel no failure line.
7. **Exec wrapper in `internal/worker/runner`** (`runner.Galaxy`), because the setuid helper spawn, the play supervisor and `stopGroup` live there (owner decision 2026-11-06: reuse, do not reinvent). `runner.NewRequirements(runExec, cfg)` is the single wiring for `cmd/worker`, the embedded worker, both e2e harnesses and the in-image harness; the runner and the sync share one instance.
8. **Shapes beyond the spec table** (galaxy's behaviour verified): `roles:` / `collections:` with a null value count as empty (galaxy skips them); another top-level key is refused like galaxy does (`only roles and collections are allowed at the top level, found foo`); `include:` is refused in the list form and in a mapping's `roles` list; a top-level role `name` (or the third part of `src,version,name`) that is not one plain directory name (`""`, `.`, `..`, contains `/` or `\`) is refused — galaxy extracted `name: ../../escaped` two levels above `-p`.
9. **Local sources.** Every source string without `://`, and every `file://` URL, is checked as a path: relative to the file's own directory, absolute only when inside the clone directory. Lexical escape or absolute outside → refused; inside and exists → fine; does not exist → left to galaxy (it is then a galaxy name, or galaxy fails); any other refusal of the clone's confined root (a link out, `.git`) → refused. Other URLs, `git+file://` included, are left to galaxy and git (`GIT_ALLOW_PROTOCOL`). Checked fields: role `src` (or `name` without `src`), a plain role string's first comma part, collection `name` and `source`.
10. **Messages** (file names are relative to the requirements root): `<file> is not a requirements file: <reason>`; `<file>: include: is not supported; list the roles in this file`; `<file>: the source "<x>" is outside the repository`; `ansible-galaxy <role|collection> install -r <file>: exit N: <tail>` / `: timed out after 10m0s` / `: context canceled`; lines `Requirements: installing for commit <7 hex> from <files>`, `Requirements: installed in %.1f s`, `Requirements: using the install for commit <7 hex> on this worker`, `Requirements: <file> lists nothing to install` (several files: `a, b list nothing to install`). The "installing … from" line names only files that install something.
11. **Lock** polls `flock(LOCK_EX|LOCK_NB)` every 20 ms until it gets the lock or the context ends, so a waiting ensure is cancellable and time-bounded by the step's 10 minutes.
12. **Fail closed** when the runner or the sync has counted files but no `Requirements` wired: `no requirements installer wired`.
13. **Galaxy under the play uid** (owner decision 2026-11-06; design in the spec's section of that name). Writable for the play uid: only `home/`, `tmp/`, `roles/`, `collections/` of the temp entry (2770, the worker's group, which only the worker and the play uid hold); the temp entry itself, `input/`, the cache directory and everything above are group read+traverse at most, so a `../` lands on "Permission denied". The work dir is not used by the install (it stays owner-only until `ShareWorkDir`, backlog 7.21). The worker then copies `roles/` and `collections/` into a worker-owned entry (Task 5) — `chown` is not available to it, and an entry the play uid owned could be changed by a later play — and removes the original as the play uid where it must (`RemoveAsPlayUID`, generalised from the loop). With a play uid set and no supervisor, galaxy is refused: the take-over needs the guarantee that no process of the install still writes. Without a play uid galaxy runs as the worker with a warn line per command. The take-over runs in both cases, so there is one code path.
14. **Cost accepted** (spec): one copy per install on a cache miss; a role or collection whose files the worker cannot read fails the install by name.

---

### Task 1: `OpenDir` — open, then verify, named refusals (goal 1a)

**Files:** Create `internal/worker/requirements/doc.go` (package comment: what the package holds, the layout of spec goal 1, "no repair of what the worker did not put there"), `internal/worker/requirements/opendir.go`, `internal/worker/requirements/opendir_test.go` (package `requirements`).

**Interfaces:**
- Produces:
  ```go
  var (
  	ErrCacheSymlink      = errors.New("a symlink, which is not followed")
  	ErrCacheNotDirectory = errors.New("not a directory")
  	ErrCacheReplaced     = errors.New("replaced while it was opened")
  )
  func OpenDir(path string) (*os.Root, error)                    // openDirWith(path, nil)
  func openDirWith(path string, afterOpen func()) (*os.Root, error) // afterOpen runs after os.OpenRoot, before the Lstat
  ```
  Every error is `fmt.Errorf("open requirements cache %s: %w", path, reason)`. `os.OpenRoot(path)` first; on its error, `os.Lstat(path)` decides: a symlink (dangling too) → `ErrCacheSymlink`; exists and is no directory → `ErrCacheNotDirectory`; both say not-exist → the `OpenRoot` error (so `errors.Is(err, fs.ErrNotExist)`); else the `OpenRoot` error. On success: `Lstat(path)` must be a real directory (symlink → `ErrCacheSymlink`, other → `ErrCacheNotDirectory`) and `os.SameFile(lstat, root.Stat("."))`, else `ErrCacheReplaced`; the root is closed on every refusal.

- [ ] **Step 1: Failing tests.**
  - `TestOpenDir` — a table run over two layouts, `cache` (`<tmp>/org/repo.requirements`) and `entry` (`<tmp>/org/repo.requirements/<40 hex>-<16 hex>`): `missing` → `errors.Is(err, fs.ErrNotExist)`, nil root; `directory` → `root.Stat(".")` is `os.SameFile` with `os.Stat(path)`; `symlink to a directory` and `dangling symlink` → `errors.Is(err, ErrCacheSymlink)`, message contains `symlink` and the path; `regular file` → `ErrCacheNotDirectory`, message contains `not a directory`, never `symlink`; `parent without permission` (parent `0o000`, restored in `t.Cleanup`; skip when `os.Geteuid() == 0`) → error is none of `fs.ErrNotExist`, the three sentinels, and contains the path. After every refusal the symlink is still a symlink (`Lstat`) and its target's contents are unchanged.
  - `TestOpenDirRefusesADirectorySwappedAfterTheOpen` — `openDirWith(path, swap)`: swap renames `path` to `path+".old"` and makes a new directory at `path` → `ErrCacheReplaced`, nil root; a second case swaps in a symlink to `path+".old"` → `ErrCacheSymlink`.
- [ ] **Step 2:** `go test -race -count=1 -timeout 900s ./internal/worker/requirements/` — FAIL (undefined).
- [ ] **Step 3:** implement.
- [ ] **Step 4:** the package passes; `go test -cover` ≥ 90 % for `opendir.go` (`go tool cover -func`).
- [ ] **Step 5: Commit** `feat(worker): open the requirements cache through one verified function`.

### Task 2: Which files count, their shapes and the key (goal 3, goal 1 key)

**Files:** Create `internal/worker/requirements/find.go`, `shape.go`, `key.go` and `find_test.go`, `shape_test.go`, `key_test.go` (package `requirements`).

**Interfaces:**
- Produces:
  ```go
  // Files are the counted requirements files of a commit (spec goal 3).
  type Files struct {
  	Root  string   // the requirements root relative to the clone: the repo subpath or ""
  	Paths []string // clone-relative slash paths, in goal 3 order
  }
  func (f Files) Empty() bool
  func Find(clone fs.StatFS, subpath string) (Files, error)

  type plan struct {
  	name               string   // the file relative to the requirements root (log lines, errors)
  	roles, collections bool     // which install runs
  	sources            []string // source strings for checkSources (Task 3), in file order
  }
  func parseFile(name string, data []byte) (plan, error)

  type fileData struct{ path string; data []byte } // path clone-relative
  func key(commit, root string, files []fileData) string
  ```
- `Find`: candidates `requirements`, `roles/requirements`, `collections/requirements`, each `.yml` if it is a regular file, else `.yaml`. Root = `subpath` when any candidate exists there (`subpath != ""`), else `""`. `fs.Stat` not-exist → not counted; a directory → not counted; any other error → `fmt.Errorf("%s: %w", <clone-relative path>, err)`.
- `parseFile` (yaml.v3 into `yaml.Node`): empty / comments / `null` / `[]` → `plan{}`; a sequence → roles; a mapping with only `roles`/`collections` keys, each a sequence or null → roles when its list is non-empty, collections likewise; Ruling 8 refusals; anything else → `fmt.Errorf("%s is not a requirements file: %s", name, reason)` with reasons `invalid YAML: <err>`, `the top level must be a list of roles or a mapping of roles and collections`, `only roles and collections are allowed at the top level, found <key>`, `roles must be a list`, `collections must be a list`, `roles entry <n> is neither a name nor a mapping` (same for collections), `the role name "<x>" is not a plain directory name`; `include` anywhere in a role list → `fmt.Errorf("%s: include: is not supported; list the roles in this file", name)`. `sources` collects the Ruling 9 fields.
- `key` — the algorithm, so every worker and test computes the same thing:
  ```go
  h := sha256.New()
  fmt.Fprintf(h, "root %q\n", root)
  for _, f := range files { fmt.Fprintf(h, "file %q %d\n", f.path, len(f.data)); h.Write(f.data) }
  return commit + "-" + hex.EncodeToString(h.Sum(nil))[:16]
  ```

- [ ] **Step 1: Failing tests.**
  - `TestFind` (`fstest.MapFS`): none → `Empty()`; root `requirements.yml`, subpath `""` → `{"", ["requirements.yml"]}`; subpath `ansible` with `ansible/requirements.yml` and a root `requirements.yml` → `{"ansible", ["ansible/requirements.yml"]}`; subpath `ansible` with nothing there and a root file → `{"", ["requirements.yml"]}`; all three at the root → `["requirements.yml", "roles/requirements.yml", "collections/requirements.yml"]`; only `requirements.yaml` → counted; both `.yml` and `.yaml` → `.yml` only; subpath holding only `collections/requirements.yaml` → `{"ansible", ["ansible/collections/requirements.yaml"]}`; a directory named `requirements.yml` → not counted.
  - `TestFindReportsAStatError`: an `fs.StatFS` fake whose `Stat("requirements.yml")` returns `errors.New("outside the repository's files")` → error contains `requirements.yml` and that text.
  - `TestParseFile` table (`name` = `roles/requirements.yml`): `""`, `"# only a comment\n"`, `"null"`, `"[]"`, `"roles: []\ncollections: []\n"`, `"roles:\ncollections:\n"` → `plan{name}` with neither flag; `"- src: ./r.tar.gz\n"` → roles; `"roles:\n  - name: a.b\n"` → roles only; `"collections:\n  - community.general\n"` → collections only; both lists → both; `"42"` → `is not a requirements file: the top level must be …`; `"roles: x\n"` → `roles must be a list`; `"collections: {a: 1}\n"` → `collections must be a list`; `"roles: []\nfoo: 1\n"` → `found foo`; `"roles: [\n"` → `invalid YAML`; `"- include: other.yml\n"` and `"roles:\n  - include: other.yml\n"` → exactly `roles/requirements.yml: include: is not supported; list the roles in this file`; `role name with a path`: `"- src: ./r.tar.gz\n  name: ../../escaped\n"` → `the role name "../../escaped" is not a plain directory name`; also `name: a/b`, `name: ..`, and `"- ./r.tar.gz,1.0,../x\n"`; `roles entry 1 is neither a name nor a mapping` for `"- [a]\n"`. Sources: `"roles:\n  - src: ./r.tar.gz\n  - name: acme.web\n  - geerlingguy.apache,1.2.0\ncollections:\n  - name: ../c\n    type: dir\n  - name: ns.col\n    source: https://hub.example.org\n"` → `[./r.tar.gz acme.web geerlingguy.apache ../c ns.col https://hub.example.org]`.
  - `TestKey`: the same inputs give the same key; the key matches `^[0-9a-f]{40}-[0-9a-f]{16}$` for a SHA-1 commit and `^[0-9a-f]{64}-[0-9a-f]{16}$` for a SHA-256 one; another commit, another root (`""` vs `ansible`), one changed byte, another path with the same bytes, and the same two files in swapped order each change the hash part.
- [ ] **Step 2:** run the package — FAIL (undefined).
- [ ] **Step 3:** implement.
- [ ] **Step 4:** the package passes.
- [ ] **Step 5: Commit** `feat(worker): find a commit's requirements files, check their shape and key their install`.

### Task 3: Local sources stay inside the repository (Constraints, owner answer 2)

**Files:** Create `internal/worker/requirements/source.go`, `source_test.go` (package `requirements`).

**Interfaces:**
- Consumes `plan` (Task 2).
- Produces:
  ```go
  func checkSource(clone fs.StatFS, cloneDir, fileDir, src string) error // nil or errOutside
  func checkSources(clone fs.StatFS, cloneDir, fileDir string, p plan) error
  var errOutside = errors.New("is outside the repository")
  ```
  `fileDir` is the file's clone-relative directory (`path.Dir` of its path, `"."` → `""`). Ruling 9 decides; `checkSources` returns the first refusal as `fmt.Errorf("%s: the source %q is outside the repository", p.name, src)`.

- [ ] **Step 1: Failing tests.**
  - `TestCheckSource` (clone fake: `fstest.MapFS` with `vendor/r.tar.gz` and `vendor/c/galaxy.yml`, wrapped so `Stat` of any path under `vendor/link/` or `.git/` returns `errors.New("outside the repository's files")`; `cloneDir = "/cache/org/repo"`): accepted — `geerlingguy.apache`, `https://github.com/x/y.git`, `git+https://github.com/x/y.git`, `git+file:///srv/x` (left to git), `git@github.com:x/y.git`, `./vendor/r.tar.gz`, `vendor/r.tar.gz`, `../vendor/r.tar.gz` with `fileDir` `roles`, `../vendor/c` with `fileDir` `collections`, `/cache/org/repo/vendor/r.tar.gz`, `file:///cache/org/repo/vendor/r.tar.gz`, `./missing.tar.gz` (left to galaxy); refused (`errors.Is(err, errOutside)`) — `../x.tar.gz` with `fileDir` `""`, `../../x.tar.gz` with `fileDir` `roles`, `/etc/passwd`, `/cache/org/other/r.tar.gz`, `file:///etc/x.tar.gz`, `vendor/link/r.tar.gz`, `.git/config`.
  - `TestCheckSourcesNamesTheFileAndTheSource`: a plan `{name: "collections/requirements.yml", sources: ["ns.col", "../../c"]}` with `fileDir` `collections` → exactly `collections/requirements.yml: the source "../../c" is outside the repository`.
- [ ] **Step 2:** run the package — FAIL.
- [ ] **Step 3:** implement.
- [ ] **Step 4:** the package passes.
- [ ] **Step 5: Commit** `feat(worker): refuse requirement sources outside the repository`.

### Task 4: Cache mechanics — lock, temp dirs, eviction, the legacy directory (goal 1, goal 8)

**Files:** Create `internal/worker/requirements/cache.go`, `lock.go`, `cache_test.go`, `lock_test.go` (package `requirements`).

**Interfaces:**
- Produces:
  ```go
  const maxEntries = 3
  var entryName = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})-[0-9a-f]{16}$`)
  func CacheDir(cloneDir string) string // <parent>/<base>.requirements
  func lock(ctx context.Context, root *os.Root) (unlock func(), err error) // ".lock", 0600, Ruling 11
  func mkTemp(root *os.Root) (string, error) // ".tmp-" + 16 hex (crypto/rand), Mkdir 0700, retried on ErrExist
  func sweepTemp(root *os.Root, fallback func(name string) error) error // RemoveAll of every ".tmp-*" name; when that fails, fallback(name) (Galaxy.RemoveAll: what the play uid owns), then RemoveAll again
  func evict(root *os.Root, keep int) error   // real directories named like entryName, newest mtime first (ties: name), RemoveAll beyond keep
  func removeLegacy(cloneDir string) error    // os.OpenRoot(parent).RemoveAll(base + ".galaxy")
  ```
  Nothing but `.tmp-*`, entry-named real directories and `<base>.galaxy` is ever removed; a symlink is removed as the link only.

- [ ] **Step 1: Failing tests.**
  - `TestCacheDir`: `/c/o/r` → `/c/o/r.requirements`.
  - `TestLockExcludesASecondHolder`: two roots over the same directory; first `lock` holds; second `lock` with a 100 ms context fails with `errors.Is(err, context.DeadlineExceeded)`; after `unlock()` a third `lock` returns within 100 ms; `.lock` has mode `0600`.
  - `TestMkTemp`: two calls give two different `.tmp-` names matching `^\.tmp-[0-9a-f]{16}$`, both directories with mode `0700`.
  - `TestSweepTemp`: `.tmp-dead/roles/x`, a `.tmp-link` symlink to an outside dir, an entry and `.lock` → only the two `.tmp-*` are gone; the outside dir's contents remain; the fallback is not called. A `.tmp-stuck` whose `roles/` is 0500 with a file inside (skip as root) → the fallback is called with `.tmp-stuck` (the test's fallback chmods and removes) and it is gone; a fallback that fails → the error names `.tmp-stuck`.
  - `TestEvictKeepsTheThreeMostRecentlyUsed`: five entries with mtimes t1..t5 (`Chtimes`), plus `.lock`, `.tmp-x`, a file `notes` and an entry-named symlink to an outside dir with mtime t0 → after `evict(root, 3)` exactly t3, t4, t5 entries remain, the four others untouched, the outside dir intact.
  - `TestRemoveLegacyGalaxyDir`: `<clone>.galaxy/acme.web/x` → gone; `<clone>.galaxy` as a symlink to an outside dir → the link gone, the target intact; absent → nil.
- [ ] **Step 2:** run the package — FAIL.
- [ ] **Step 3:** implement (`syscall.Flock` on the file's fd).
- [ ] **Step 4:** the package passes.
- [ ] **Step 5: Commit** `feat(worker): lock, sweep and evict the per-commit requirements cache`.

### Task 5: Take-over — verify and copy what galaxy wrote (owner decision 2026-11-06)

**Files:** Create `internal/worker/requirements/takeover.go`, `takeover_test.go` (package `requirements`).

**Interfaces:**
- Consumes `mkTemp` (Task 4).
- Produces:
  ```go
  // takeOver copies roles/ and collections/ of the temp entry src (written by galaxy, as the play uid
  // when one is set) into a fresh worker-owned temp entry and returns that one's name.
  func takeOver(cache *os.Root, src string) (string, error)
  ```
  Walk `src/roles` and `src/collections` through `cache.OpenRoot(src)` with `Lstat` types: a directory → `Mkdir` 0700; a regular file → must have `Nlink == 1`, opened with `O_RDONLY|syscall.O_NOFOLLOW` through the root, `os.SameFile` with the walked `Lstat`, copied, mode 0700 when any execute bit was set else 0600; a symlink → its target must be relative and `path.Join(dir of the link, target)` must stay inside `src` (lexically), recreated with `Symlink`; anything else refuses. Errors: `take over the install: <path relative to src>: <reason>` with reasons `a hard link`, `a symlink leaving the install`, `not a regular file, directory or symlink`, `changed while it was copied`, or the open/read error. On any error the partial copy is removed (`RemoveAll`) and no name is returned.

- [ ] **Step 1: Failing tests.**
  - `TestTakeOverCopiesALegitimateInstall`: `src/roles/acme.web/{tasks/main.yml 0664, files/run.sh 0755, files/suid 04755, meta/link → ../tasks/main.yml}`, `src/collections/ansible_collections/acme/tools/README.md 0644`, plus `src/input/1.yml` and `src/home/x` → the copy has the same bytes for every file under `roles/` and `collections/`; modes `main.yml` 0600, `run.sh` 0700, `suid` 0700 (no setuid bit), every directory 0700; `meta/link` is a symlink with target `../tasks/main.yml`; neither `input/` nor `home/` was copied; `src` is unchanged.
  - `TestTakeOverRefuses` table (each → error exactly `take over the install: <path>: <reason>`, no new `.tmp-*` left besides `src`): `roles/abs` → `/etc/passwd` (`a symlink leaving the install`); `roles/a/up` → `../../../x` (same); `roles/hard` hard-linked to `src/input/1.yml` (`a hard link`); `collections/fifo` made with `syscall.Mkfifo` (`not a regular file, directory or symlink`); `roles/secret` mode 0000 (skip as root; the open error, path named).
- [ ] **Step 2:** run the package — FAIL.
- [ ] **Step 3:** implement.
- [ ] **Step 4:** the package passes.
- [ ] **Step 5: Commit** `feat(worker): verify and take over what an ansible-galaxy install wrote`.

### Task 6: `Ensure` — one install per commit, published atomically (goals 1, 4–6)

**Files:** Create `internal/worker/requirements/ensure.go`, `lines.go`, `internal/worker/requirements/requirementstest/fake.go`; tests `ensure_test.go`, `ensure_refusal_test.go`, `lines_test.go` (package `requirements_test`, using `requirementstest`).

**Interfaces:**
- Consumes Tasks 1–5.
- Produces:
  ```go
  type Kind string
  const (KindRole Kind = "role"; KindCollection Kind = "collection")
  const DefaultTimeout = 10 * time.Minute

  // The temp entry an install fills (spec, owner decision 2026-11-06):
  // <cache>/.tmp-<x>/{input/<n>.yml, input/galaxy.cfg, home/, tmp/, roles/, collections/}, all created 0700/0600 here.
  type Install struct {
  	Kind         Kind
  	File         string // <Entry>/input/<n>.yml, n from 1 in Files order
  	Dir          string // galaxy's working directory: the file's own directory in the clone
  	Entry        string // the temp entry; -p is <Entry>/roles or <Entry>/collections
  	CloneDir     string // opened read-only to the play group with RepoCacheDir as root
  	RepoCacheDir string
  }
  type Galaxy interface {
  	Install(ctx context.Context, in Install, line func(string)) error
  	RemoveAll(dir string) error // removes dir and what the install wrote into it, also files the play uid owns
  }
  type ExitError struct { Code int; Stderr string } // Error() = "exit <Code>"
  type Clock interface { Now() time.Time }
  type SystemClock struct{}

  type Request struct {
  	CloneDir string    // <RepoCacheDir>/<org>/<repo>
  	Clone    fs.StatFS // the clone's confined root (worker.OpenCloneFS)
  	Files    Files
  	Commit   string    // full SHA-1 or SHA-256
  	RepoCacheDir string
  	Line     func(string) // nil: discarded
  }
  type Entry struct { Dir, RolesDir, CollectionsDir string }
  func (e Entry) IsZero() bool

  type Service struct{ /* galaxy, clock, timeout */ }
  func NewService(galaxy Galaxy, clock Clock, timeout time.Duration) *Service // 0 → DefaultTimeout
  func (s *Service) Ensure(ctx context.Context, req Request) (Entry, error)
  ```
- `requirementstest`:
  ```go
  type Galaxy struct {
  	Roles, Collections map[string]string // written below <Entry>/roles or <Entry>/collections (dirs 0700, files 0600)
  	Lines     []string                    // sent through line on every call
  	Err       error                       // returned by every call after the lines
  	Hold      chan struct{}               // when set, every call waits for it or ctx (then returns ctx.Err())
  	OnInstall func(in requirements.Install) error // runs first (seed modes, symlinks; signal tests)
  }
  func (g *Galaxy) Install(ctx context.Context, in requirements.Install, line func(string)) error // mutex-safe
  func (g *Galaxy) RemoveAll(dir string) error   // os.RemoveAll, recorded
  func (g *Galaxy) Calls() []requirements.Install
  func (g *Galaxy) Removed() []string
  type Clock struct { T time.Time; Step time.Duration } // Now returns T, then advances by Step (mutex-safe)
  ```
- `Ensure` in this order: (1) `Files.Empty()` → zero, nil; (2) commit must match `^(?:[0-9a-f]{40}|[0-9a-f]{64})$`, else `commit %q is not a full commit id`; (3) read each file with `fs.ReadFile(req.Clone, path)`, `parseFile`, `checkSources` — all before the cache is touched; (4) nothing to install → the lists-nothing line, zero; (5) `OpenDir(CacheDir)`, on not-exist `os.Mkdir(…, 0o700)` (an `ErrExist` is fine) and `OpenDir` again; (6) `ctx` gets the timeout, then `lock`; (7) `sweepTemp(root, fallback = galaxy.RemoveAll of the path)`, `removeLegacy`; (8) `OpenDir(<cache>/<key>)`: ok → the "using" line; not-exist → install; else that error; (9) install: `mkTemp`, `Mkdir` `input`, `home`, `tmp`, `roles`, `collections` (0700), write `input/<n>.yml` copies and `input/galaxy.cfg` = `"[defaults]\n"` (0600), the "installing" line, then per file roles before collections `galaxy.Install` with a line func that removes URL userinfo and caps at 500 lines (`… N more lines` after the command); on error `galaxy.RemoveAll(<cache>/<tmp>)` and return it formatted (Ruling 10: `ExitError` → userinfo removed from `Stderr` first, then its last 4 KiB; deadline of the step's context with the caller's still alive → `timed out after <timeout>`; the caller's context ended → wrap `ctx.Err()`); `pub := takeOver(root, tmp)` (error → `galaxy.RemoveAll` of the temp entry, return it), `galaxy.RemoveAll(<cache>/<tmp>)`, `Rename(pub, key)`; the "installed in" line from two `Clock.Now()` calls; (10) `OpenDir` the entry (a refusal returns), `Chtimes(key, now, now)`, `evict(root, maxEntries)`; return `Entry{Dir, Dir/roles, Dir/collections}`.

- [ ] **Step 1: Failing tests** (helper: a clone `<tmp>/org/repo` written with `os.DirFS`-backed files; `commitA`/`commitB` 40-hex constants; the entry is the one directory in `CacheDir(clone)` matching the entry pattern).
  - `TestEnsureNothingCountedTouchesNothing`: empty `Files` → zero `Entry`, no line, no `CacheDir`, no galaxy call.
  - `TestEnsureListsNothing`: `requirements.yml` = `"# none\n"` → zero `Entry`, lines exactly `["Requirements: requirements.yml lists nothing to install"]`, no `CacheDir`; two such files → `requirements.yml, roles/requirements.yml list nothing to install`.
  - `TestEnsureInstallsAndPublishes`: `requirements.yml` with roles and collections, `collections/requirements.yml` with collections; fake writes `acme.web/tasks/main.yml` and `ansible_collections/acme/tools/README.md`, `Lines: ["- acme.web was installed successfully"]` → `Entry.Dir` is `CacheDir(clone)/<commitA>-<16 hex>`, `RolesDir`/`CollectionsDir` hold the files; three calls: kinds `role, collection, collection`, `File` `<Entry>/input/1.yml`, `…/1.yml`, `…/2.yml` with the bytes of the clone files, `Dir` `<clone>` and `<clone>/collections`, `Entry` the same `.tmp-*` path under `CacheDir` for all three, `CloneDir` and `RepoCacheDir` passed through; `<Entry>/input/galaxy.cfg` read `[defaults]\n` during the calls; the published files have mode 0600 and the fake's temp entry is in `Removed()`; lines: first `Requirements: installing for commit <commitA[:7]> from requirements.yml, collections/requirements.yml`, then the galaxy line three times, last matches `^Requirements: installed in \d+\.\d s$`; no `.tmp-*` remains; `CacheDir` mode 0700.
  - `TestEnsureReusesTheEntryOfItsCommit`: a second `Ensure` → same `Entry`, no new call, lines exactly `["Requirements: using the install for commit <commitA[:7]> on this worker"]`.
  - `TestEnsureKeysTheEntryByCommitAndContent`: `commitB` with the same files → a second entry; `commitA` with `Files.Root` changed (`ansible` holding the same bytes) → a third; neither reuses another.
  - `TestEnsureFailureLeavesNoEntry`: `Err: &requirements.ExitError{Code: 1, Stderr: "fetching https://bob:s3cret@git.example.org/r.git\nERROR! boom"}` → error exactly `ansible-galaxy role install -r requirements.yml: exit 1: fetching https://[redacted]@git.example.org/r.git\nERROR! boom`; no entry, no `.tmp-*`; a 10 KiB `Stderr` gives a tail of exactly 4096 bytes.
  - `TestEnsureTimesOut`: `NewService(fake{Hold}, clock, 50*time.Millisecond)` → error ends with `timed out after 50ms`, not `errors.Is(err, context.Canceled)`; no entry, no `.tmp-*`.
  - `TestEnsureCancelled`: `Hold`, cancel from `OnInstall` → `errors.Is(err, context.Canceled)`; no entry, no `.tmp-*`.
  - `TestEnsureRemovesAStaleTempDir`: `CacheDir/.tmp-0123456789abcdef/roles/x` before → gone after (hit and miss both).
  - `TestEnsureRemovesTheLegacyGalaxyDir`: `<clone>.galaxy/acme.web/x` → gone after an `Ensure` that installs; untouched after one whose files list nothing.
  - `TestEnsureEvictsTheLeastRecentlyUsed`: `requirementstest.Clock{Step: time.Hour}`; commits c1..c4 → c1's entry gone, c2..c4 kept; `Ensure` c2 again (hit), then c5 → c3 gone, c2, c4, c5 kept.
  - `TestEnsureInstallsOnceForTwoConcurrentCalls`: `Hold`; two goroutines `Ensure` commitA; release after the first `OnInstall` → both return the same `Entry`, `len(Calls())` is one per file kind (not doubled), one result has the "using" line.
  - `TestEnsureRefusesAnInstallItCannotTakeOver`: `OnInstall` plants `roles/x` → an absolute symlink → error `take over the install: roles/x: a symlink leaving the install`; no entry, no `.tmp-*`.
  - `TestEnsureRefusesBadFilesBeforeTheCache`: `- include: x.yml` and `- src: ../../x.tar.gz` → their Task 2/3 messages; commit `"abc"` → `commit "abc" is not a full commit id`; each with no call and no `CacheDir`.
  - `ensure_refusal_test.go` `TestEnsureRefusesWhatItDidNotPutThere` table: at `CacheDir(clone)` a symlink to a dir with a file, a dangling symlink, a regular file; at the entry path (key from a first, successful `Ensure` then `RemoveAll` and replaced) the same three → `errors.Is` the matching sentinel, message contains the path and `a symlink, which is not followed` / `not a directory`, no new call, the symlink/file still there, the link target's contents unchanged.
  - `lines_test.go` `TestEnsureCapsAndCleansGalaxyLines`: 503 fake lines, line 1 `cloning https://bob:tok3n@git.example.org/r.git` → 500 galaxy lines reported, the first `cloning https://[redacted]@git.example.org/r.git`, followed by exactly `… 3 more lines`.
- [ ] **Step 2:** run the package — FAIL.
- [ ] **Step 3:** implement.
- [ ] **Step 4:** the package passes; `go test -race -count=20 -run 'TestEnsureInstallsOnce|TestLock' ./internal/worker/requirements/` passes; package coverage ≥ 80 %.
- [ ] **Step 5: Commit** `feat(worker): install a commit's requirements once into a shared cache entry`.

### Task 7: `runner.Galaxy` — ansible-galaxy under the play uid (goals 4–5, owner decision 2026-11-06)

**Files:** Create `internal/worker/runner/galaxy.go` (the `requirements.Galaxy` implementation), `internal/worker/runner/galaxy_share.go` (`prepareInstall`), `internal/worker/runner/galaxy_test.go`, `internal/worker/runner/galaxy_share_test.go`. Modify `internal/worker/runner/runner.go` (`newRunnerCmd` becomes a call of `newPlayCmd`; `resolveRunnerBin` takes the variable it names — edits of existing lines only), `internal/worker/runner/share.go` (the ancestor loop of `ShareRepoRead` moves into `shareAncestors`, unchanged in behaviour), `internal/worker/workdir.go` (`removeAsPlayUID` becomes the exported `RemoveAsPlayUID`; the loop calls it).

**Interfaces:**
- Consumes `requirements.Install`, `requirements.ExitError`, `requirements.NewService`, `requirements.SystemClock`; the runner's `stopGroup`, `ShareWorkDir`, `ShareRepoRead`, `chmodReadShared`, `supervisor.Command`.
- Produces:
  ```go
  // internal/worker
  func RemoveAsPlayUID(ctx context.Context, cfg Config, dir string) error // rm -rf of dir's entries through the helper as the play uid

  // internal/worker/runner
  type Galaxy struct {
  	Runner *Runner        // kill grace, supervisor backstop, logger, stopGroup
  	Config worker.Config  // PlayUID, SetuidHelper, PlaySupervisor, AnsibleBin
  }
  func NewRequirements(r *Runner, cfg worker.Config) *requirements.Service // NewService(&Galaxy{r, cfg}, requirements.SystemClock{}, 0)
  func galaxyBin(ansibleBin string) string // ansible-galaxy next to ansibleBin when it holds a path separator, else "ansible-galaxy"
  func (g *Galaxy) Install(ctx context.Context, in requirements.Install, line func(string)) error
  func (g *Galaxy) RemoveAll(dir string) error
  func (g *Galaxy) command(in requirements.Install) (*exec.Cmd, error)
  func newPlayCmd(bin, binVar string, args []string, cfg worker.Config, dir string, env []string, grace time.Duration) (*exec.Cmd, error)
  func shareAncestors(root, dir string) error // ShareRepoRead's ancestor walk: group r-x, never w, a symlink refused
  func prepareInstall(in requirements.Install) error
  ```
- `newPlayCmd` is today's `newRunnerCmd` body with the binary's variable named by `binVar` (`PULSIX_ANSIBLE_RUNNER_BIN` for the runner, `PULSIX_ANSIBLE_BIN` for galaxy): helper `[uid, supervisor, supervisor.Command(grace, abs bin, args)…]` or the subset that applies, `Setpgid`, `Dir = dir`. `newRunnerCmd(…)` = `newPlayCmd(bin, envRunnerBin, args, env.Config, env.WorkDir, …)`.
- `Install`: with `PlayUID != 0` and `PlaySupervisor == ""` → `ansible-galaxy cannot be isolated: PULSIX_WORKER_PLAY_UID is set, but the play supervisor is not available on this system`, nothing started. With a play uid: `prepareInstall(in)` first (its error returned). Without one: warn `ansible-galaxy is not isolated: PULSIX_WORKER_PLAY_UID is not set, so it runs as the worker's own uid` with `file` = `in.File`. argv `<kind> install -r <File> -p <Entry>/roles|collections`; env `stripPrivateEnv(os.Environ())` + `HOME=<Entry>/home`, `TMPDIR=<Entry>/tmp`, `GIT_SSH_COMMAND=worker.GitSSHCommand("", <Entry>/home/.ssh/known_hosts)` (`worker.RunGitKnownHosts(in.Entry)` creates the dir before `prepareInstall`), `GIT_TERMINAL_PROMPT=0`, `ANSIBLE_CONFIG=<Entry>/input/galaxy.cfg`, `ANSIBLE_COLLECTIONS_PATH=<Entry>/collections`, `ANSIBLE_COLLECTIONS_SCAN_SYS_PATH=false`, `GIT_ALLOW_PROTOCOL=https:http:ssh:git`. stdout and stderr lines go to `line` as they arrive, the last 16 KiB of stderr are kept, `cmd.WaitDelay = 5*time.Second`. A context that ends → `g.Runner.stopGroup(worker.RunEnv{Config: g.Config}, "ansible-galaxy", pid, exited)` (TERM → grace → KILL, through the helper under a play uid), then an error wrapping `ctx.Err()`. A start failure under a play uid names the helper and both variables like the runner's; a non-zero exit → `&requirements.ExitError{Code, Stderr}` (a helper or supervisor refusal is in that stderr).
- `prepareInstall(in)` (spec layout): `ShareRepoRead(in.RepoCacheDir, in.CloneDir)`; `shareAncestors(in.RepoCacheDir, in.Entry)` (every directory down to and including the temp entry: group r-x, never w); `chmodReadShared` on `input/` and its files; `ShareWorkDir` on `home`, `tmp`, `roles`, `collections` (owner bits mirrored into the group, setgid: 2770). Nothing else in the cache directory is touched.
- `RemoveAll(dir)`: `os.RemoveAll(dir)`; on an error with a play uid, `worker.RemoveAsPlayUID(ctx 10 s, g.Config, dir)` (its result only logged) and `os.RemoveAll(dir)` again, whose error is returned.

- [ ] **Step 1: Failing tests** (fake galaxy and fake helper scripts written per test; `r := &Runner{KillGrace: 200 * time.Millisecond, Log: slog.New(slog.NewTextHandler(&logs, nil))}`).
  - `TestGalaxyBin`: `/opt/x/bin/ansible-playbook` → `/opt/x/bin/ansible-galaxy`; `ansible-playbook` and `""` → `ansible-galaxy`.
  - `TestGalaxyCommandLine` (no play uid; the script writes `"$@"`, `pwd`, `env`): a role and a collection install → argv exactly `[role install -r <File> -p <Entry>/roles]` and `[collection install -r <File> -p <Entry>/collections]`; `pwd` is `in.Dir`.
  - `TestGalaxyEnvironment`: with `t.Setenv("ANSIBLE_COLLECTIONS_PATH", "/usr/share/x")`, `t.Setenv("ANSIBLE_STRAY", "1")`, `t.Setenv("PULSIX_STRAY", "1")` → `HOME=<Entry>/home`, `TMPDIR=<Entry>/tmp`, `ANSIBLE_CONFIG=<Entry>/input/galaxy.cfg`, `ANSIBLE_COLLECTIONS_PATH=<Entry>/collections`, `ANSIBLE_COLLECTIONS_SCAN_SYS_PATH=false`, `GIT_ALLOW_PROTOCOL=https:http:ssh:git`, `GIT_TERMINAL_PROMPT=0`, `GIT_SSH_COMMAND` containing `UserKnownHostsFile='<Entry>/home/.ssh/known_hosts'`; neither `ANSIBLE_STRAY` nor `PULSIX_STRAY`; `<Entry>/home/.ssh` exists.
  - `TestGalaxyWarnsWhenNotIsolated`: no play uid → the log has `ansible-galaxy is not isolated: PULSIX_WORKER_PLAY_UID is not set, so it runs as the worker's own uid` and the file; with a play uid (next test's setup) it does not.
  - `TestGalaxyCommandUnderThePlayUID`: `Config{PlayUID: 65533, SetuidHelper: "/helper", PlaySupervisor: "/sup", AnsibleBin: <dir>/ansible-playbook}` with an executable `<dir>/ansible-galaxy` → `command(in)` has `Path` `/helper` and `Args` exactly `[/helper 65533 /sup ` + `supervisor.Command(200ms, <dir>/ansible-galaxy, [role install -r <File> -p <Entry>/roles])` + `]`, `Dir` `in.Dir`, `SysProcAttr.Setpgid`.
  - `TestGalaxyRefusesThePlayUIDWithoutTheSupervisor`: `PlayUID` set, `PlaySupervisor ""`, a recording fake helper → the exact refusal above; the helper never ran; no line.
  - `TestGalaxyNamesAHelperThatDoesNotStart`: `SetuidHelper` a missing path → error contains that path, `PULSIX_WORKER_PLAY_UID=65533` and `PULSIX_WORKER_SETUID_HELPER`.
  - `TestGalaxyReportsAHelperRefusal`: helper `echo 'pulsix-setuid-exec: not setuid-root (euid 501); refusing to exec without dropping privilege' >&2; exit 1` → `errors.As` an `ExitError` with code 1 whose `Stderr` has that line.
  - `TestGalaxyStreamsItsOutput`: `echo a; echo b >&2; echo c` → lines contain `a`, `b`, `c`, `a` before `c`. `TestGalaxyExitError`: `echo boom >&2; exit 3` → `Code 3`, `Stderr` contains `boom`.
  - `TestGalaxyStopsItsProcessGroupOnCancel` (no play uid): `sleep 300 & echo $! > <pidfile>; wait`; cancel once the pid file exists → `Install` returns within 5 s with `errors.Is(err, context.Canceled)`; `syscall.Kill(pid, 0)` reports `ESRCH` within 5 s.
  - `TestGalaxyRemoveAllAsThePlayUID` (skip as root): `<dir>/sub` 0500 holding a file, so `os.RemoveAll` fails; `PlayUID 65533` and a fake helper `printf '%s\n' "$@" > <args>; shift 4; chmod -R u+w -- "$@"; rm -rf -- "$@"` → `RemoveAll(dir)` returns nil, `dir` is gone, the helper's args are `65533`, an absolute `rm`, `-rf`, `--`, `<dir>/sub`. Without a play uid the same setup returns an error naming `sub`, and the helper never ran.
  - `galaxy_share_test.go` `TestPrepareInstallOpensOnlyItsTargetsToThePlayGroup`: `RepoCacheDir/org/repo` (a clone with a file), `org/repo.requirements` 0700 with `.lock` 0600, another entry `<40 hex>-<16 hex>/roles/x` 0700, and `.tmp-x/{input/1.yml, input/galaxy.cfg, home/.ssh, tmp, roles, collections}` 0700/0600 → after `prepareInstall`: `RepoCacheDir`, `org`, `repo.requirements`, `.tmp-x`, `input`: group read and traverse, none group-writable; `input/*` group-readable, not writable; `home`, `home/.ssh`, `tmp`, `roles`, `collections`: `perm 0770` with `ModeSetgid`; `.lock` still 0600; the other entry still 0700 throughout; every clone path group-readable, none group-writable. A symlink at `org/repo.requirements` (the temp entry below a symlinked parent) → error containing `symlink on the path`, nothing chmodded behind the link.
  - The loop's existing work dir tests (`workdir_test.go`) pass unchanged through `RemoveAsPlayUID`.
- [ ] **Step 2:** `go test -race -count=1 -timeout 900s -run 'Galaxy|PrepareInstall' ./internal/worker/runner/` — FAIL.
- [ ] **Step 3:** implement.
- [ ] **Step 4:** `./internal/worker/runner/` and `./internal/worker/` pass.
- [ ] **Step 5: Commit** `feat(runner): run ansible-galaxy under the play uid, writable only into its temp entry`.

### Task 8: The run claim carries the repository subpath (goal 2, Ruling 1)

**Files:** Modify `internal/jobs/queue_sql.go` (`repoKindPayload`: `"repo_subpath": repo.Subpath`, comment). Test `internal/jobs/queue_payload_test.go`.

**Interfaces:** Produces the claim key `repo.repo_subpath` (string, always present) next to `subpath` (repo subpath joined with the playbook path, unchanged).

- [ ] **Step 1: Failing test.** `TestBuildPayloadRepoRunShipsTheRepoSubpath` (built like `TestBuildPayloadRepoPlaybookShipsTheGitServerPin`; kind payload decoded into `map[string]map[string]any`): repo subpath `""`, playbook `site.yml` → `repo_subpath` present and `""`, `subpath` `site.yml`; repo subpath `ansible` → `repo_subpath` `ansible`, `subpath` `ansible/site.yml`.
- [ ] **Step 2:** `go test -race -count=1 -timeout 900s -run TestBuildPayloadRepo ./internal/jobs/` — FAIL (key absent).
- [ ] **Step 3:** implement.
- [ ] **Step 4:** `./internal/jobs/` passes.
- [ ] **Step 5: Commit** `feat(jobs): ship the repository subpath in a repo run's claim`.

### Task 9: The runner reads the repo subpath and names both keys in `ansible.cfg` (goals 2, 8)

**Files:** Modify `internal/worker/runner/playbook.go` (`repoSpec` payload: `RepoSubpath *string \`json:"repo_subpath"\``; `gitRepoSpec.RepoSubpath string`), `internal/worker/runner/gitrepo.go` (`gitRepoSpec` field), `internal/worker/runner/layout.go` (`ansibleCfgText`), `internal/worker/runner/inventory.go` (`materializeRunnerConfig` parameter only), `internal/worker/runner/runner.go` (call site only). Create `internal/worker/runner/requirements.go` (`repoSubpath`), `internal/worker/runner/requirements_test.go`. Tests: rename `galaxy_roles_test.go`'s `TestAnsibleCfgRolesPath` to `TestAnsibleCfgNamesTheInstalledRequirements`; mechanical `""` → `requirements.Entry{}` in `edge_test.go`, `malformed_line_test.go`, `vault_test.go`, `unsafe_test.go`, and `rolesDir` → `requirements.Entry{RolesDir: rolesDir}` in `galaxy_roles_test.go`, `role_search_test.go`.

**Interfaces:**
- Produces:
  ```go
  func repoSubpath(claimed *string, subpath, playbookPath string) (string, error) // Ruling 1
  func ansibleCfgText(workDir string, vaultFiles []vaultFile, installed requirements.Entry) string
  func (r *Runner) materializeRunnerConfig(job *workerpb.Job, env worker.RunEnv, installed requirements.Entry) error
  const systemCollectionsPath = "/usr/share/ansible/collections"
  ```
  `roles_path = <RolesDir>:` + `systemRolesPath` when `RolesDir != ""`; `collections_path = <CollectionsDir>:` + `systemCollectionsPath` when `CollectionsDir != ""`; both in `[defaults]`. Until Task 10, `Run` passes `requirements.Entry{RolesDir: rolesDir}` (7.83 behaviour unchanged).

- [ ] **Step 1: Failing tests.**
  - `TestRepoSubpath` table: claimed `"ansible"` → `ansible`; claimed `""` → `""`; nil, `site.yml`, `site.yml` → `""`; nil, `ansible/site.yml`, `site.yml` → `ansible`; nil, `a/b/playbooks/site.yml`, `playbooks/site.yml` → `a/b`; nil, `other.yml`, `site.yml` → error `the claim's subpath "other.yml" does not end with the playbook path "site.yml"`.
  - `TestAnsibleCfgNamesTheInstalledRequirements`: zero entry → neither key; `{Dir: /c/o/r.requirements/k, RolesDir: …/k/roles, CollectionsDir: …/k/collections}` → exactly `roles_path = /c/o/r.requirements/k/roles:/usr/share/ansible/roles:/etc/ansible/roles` and `collections_path = /c/o/r.requirements/k/collections:/usr/share/ansible/collections`, both before `[ssh_connection]`.
- [ ] **Step 2:** `go test -race -count=1 -timeout 900s ./internal/worker/runner/` — FAIL.
- [ ] **Step 3:** implement.
- [ ] **Step 4:** `./internal/worker/runner/` passes.
- [ ] **Step 5: Commit** `feat(runner): read the claim's repository subpath and name installed collections in ansible.cfg`.

### Task 10: A repo run installs its requirements before the play (goals 2, 5, 6; play uid)

**Files:** Modify `internal/worker/runner/requirements.go` (`ensureRequirements`), `internal/worker/runner/playbook.go` (`materializePlaybook` returns `*repoCheckout`; `galaxyRolesDir` and its doc go; the comment above `materializePlaybook`), `internal/worker/runner/runner.go` (`Runner.Requirements` field; in `Run`: `red := newRedactor(job)` first and reused by `streamEvents`, the line func, the step after `materializePlaybook`), `internal/worker/runner/gitrepo.go` (header: the clone needs no lock in v0.2b; the requirements cache next to it has one), `internal/worker/runner/share.go` (`ShareRepoRead` doc names the requirements entry), `internal/worker/clonefs.go` (`OpenCloneFS`), `internal/worker/sync.go` (only the `Requirements` interface, beside `RepoFetcher`), `cmd/worker/main.go`, `internal/app/embedded_worker.go`, `internal/e2e/ansible_test.go`, `internal/e2e/shared_resources_test.go` (`runExec.Requirements = runner.NewRequirements(runExec, cfg)` — `wcfg` in the embedded worker, the harness `workerCfg` in e2e). Tests: create `internal/worker/runner/requirements_run_test.go`; `git mv internal/worker/runner/galaxy_roles_test.go internal/worker/runner/requirements_roles_test.go` and migrate it and `role_search_test.go`.

**Interfaces:**
- Consumes Tasks 6, 7, 9.
- Produces:
  ```go
  // internal/worker
  type Requirements interface { Ensure(ctx context.Context, req requirements.Request) (requirements.Entry, error) }
  type ReadRoot interface { fs.StatFS; Close() error }
  func OpenCloneFS(cloneDir string) (ReadRoot, error) // openCloneFS(cloneDir, ""), nil interface on error

  // internal/worker/runner
  type repoCheckout struct { Clone, RepoSubpath string }
  func (r *Runner) materializePlaybook(ctx context.Context, job *workerpb.Job, env worker.RunEnv) (string, *repoCheckout, error)
  func (r *Runner) ensureRequirements(ctx context.Context, env worker.RunEnv, c *repoCheckout, line func(string)) (requirements.Entry, error)
  ```
  `ensureRequirements`: nil checkout → zero; `OpenCloneFS`, `Find(clone, c.RepoSubpath)`; empty → zero; `r.Requirements == nil` → `no requirements installer wired`; `r.HeadCommit`; `Ensure` (with `RepoCacheDir: env.RepoCacheDir`); with `env.Config.PlayUID != 0`, `ShareRepoRead(env.RepoCacheDir, entry.Dir)` (`share the installed requirements with the play uid: %w`). `Run` wraps an error as `install requirements: %w`, sends `Requirements: <that text>` unless `errors.Is(err, context.Canceled)`, and returns `StatusError` before any run file is written; a failed line send ends the run with `send requirements log: %w`.

- [ ] **Step 1: Failing tests** (`requirements_run_test.go`; Runner with `writeFakeRunner`, `GitBin: gitOnPATH(t)`, `Requirements: requirements.NewService(&requirementstest.Galaxy{…}, &requirementstest.Clock{Step: time.Second}, 0)`; fixtures via `gitFixtureFiles` with `playbooks/site.yml` and `requirements.yml` = `"roles:\n  - name: acme.web\ncollections:\n  - name: acme.tools\n"`; `repoRunJob` (no `repo_subpath`: the derived path); `head` = `git rev-parse HEAD` of the source).
  - `TestRepoRunInstallsItsRequirementsBeforeTheRunStarts`: events before the first `run_start` are all `stdout`; their texts start with `Requirements: installing for commit <head[:7]> from requirements.yml` and end with a match of `^Requirements: installed in \d+\.\d s$`; the written `ansible.cfg` holds both lines of Task 9 for `CacheDir(<cache>/<org>/<repo>)/<head>-<16 hex>`.
  - `TestRepoRunReusesTheInstallOfItsCommit`: a second run → its only pre-`run_start` text is `Requirements: using the install for commit <head[:7]> on this worker`; `Calls()` still 2.
  - `TestRepoRunInstallsAgainForANewCommit`: `gitCommit` changes the playbook; a second run installs again; two entries exist, each named after its commit.
  - `TestRepoRunWithoutRequirementsHasNoRequirementsStep`: fixture without requirements, `Requirements: nil` → no `stdout` before `run_start`, `ansible.cfg` has neither key.
  - `TestRepoRunUsesTheClaimsRepoSubpath`: claim with `repo_subpath` `ansible`, playbook `ansible/playbooks/site.yml`, fixture with `ansible/requirements.yml` (roles only) and a different root `requirements.yml` → the one role call's copy has the bytes of `ansible/requirements.yml` and `Dir` `<clone>/ansible`.
  - `TestRepoRunFailsWhenItsRequirementsDoNotInstall`: `Err: &requirements.ExitError{Code: 1, Stderr: "ERROR! boom"}` → `StatusError`, error exactly `install requirements: ansible-galaxy role install -r requirements.yml: exit 1: ERROR! boom`; the last event is `stdout` `Requirements: ` + that text; no `run_start`; the fake runner's args file was never written.
  - `TestRepoRunFailsWhenTheRequirementsCacheIsRefused`: before the run, `<RepoCacheDir>/<org>/<repo>.requirements` is a symlink to a dir holding `keep`, a dangling symlink, or a file → error exactly `install requirements: open requirements cache <path>: a symlink, which is not followed` (`…: not a directory` for the file); the last event text is `Requirements: ` + it; `<WorkDir>/ansible.cfg` does not exist; no galaxy call; the link is still a link and `keep` is unchanged.
  - `TestRepoRunCancelledDuringTheInstall`: `Hold`, cancel from `OnInstall` → `errors.Is(err, context.Canceled)`; no `Requirements: install requirements` line; no `.tmp-*`.
  - `TestRepoRunWithRequirementsAndNoInstaller`: `Requirements: nil` → `install requirements: no requirements installer wired`.
  - `requirements_roles_test.go`: `TestRepoRunNamesTheGalaxyDirInItsConfig` is deleted (covered above). `TestRequirementsEntryIsReadableByThePlayUID` replaces `TestGalaxyRolesAreReadableByThePlayUID`: `PlayUID 12345`; a first `materializePlaybook` + `ensureRequirements` publishes the entry (the fake writes `roles/galaxy_only/tasks/main.yml` and `collections/ansible_collections/acme/tools/plugins/x.py`); the test then seeds what the share must clear — every directory from the role file up to and including `<repo>.requirements` 0770, both files 0660 — and adds `roles/link` → an outside file of mode 0600 (the take-over would refuse it, so it is planted after publishing); a second, reusing pass: every non-symlink path under the entry and every directory up to `RepoCacheDir` is group-readable (directories also group-traversable) and none group-writable; the outside file is still 0600. `TestPublishedEntryIsTheWorkersOwn`: after a first run the entry's files are owner-only (dirs 0700, files 0600), whatever modes the fake wrote. `TestGalaxyRoleResolvesInRealAnsible` and `TestRoleSearchFromASubdirectoryInRealAnsible` seed their installed roles through the fake (the fixture gets a `requirements.yml` naming them) instead of writing `galaxyPath`; the control "without the installed roles" uses a fixture without `requirements.yml`; `galaxyPath` goes.
- [ ] **Step 2:** `./internal/worker/runner/` — FAIL. Then prove the play-uid half can fail: temporarily drop `&^ 0o020` in `chmodReadShared`, run `-run TestRequirementsEntryIsReadableByThePlayUID` → FAIL with `group-writable`; restore. Record both.
- [ ] **Step 3:** implement; delete `galaxyRolesDir`.
- [ ] **Step 4:** `./internal/worker/runner/`, `./internal/worker/`, `./cmd/worker/`, `./internal/app/` pass (900 s); `./internal/e2e/` passes (1800 s, own invocation).
- [ ] **Step 5: Commit** `feat(runner): install a repo run's requirements for its commit before the play`.

### Task 11: The sync installs through the cache; its read opens the entry (goals 5, 7, 8)

**Files:** Modify `internal/worker/sync.go` (package comment; `SyncExecutor.Requirements` replaces `GalaxyBin`/`InstallTimeout`; `NewSyncExecutor(fetcher RepoFetcher, reqs Requirements)`; `GalaxyDir`, `galaxyDirSuffix`, `defaultInstallTimeout`, `findRequirements`, `installRequirements`, `truncateTail` go), `internal/worker/sync_sources.go` (package comment; `readVarSources`/`collectVarSources` take `installed requirements.Entry`), `internal/worker/clonefs.go` (`openInstalledRolesFS(installed requirements.Entry)`; `errNotGalaxyDir` goes; header comment), `cmd/worker/main.go`, `internal/app/embedded_worker.go`, `internal/e2e/ansible_test.go`, `internal/e2e/shared_resources_test.go` (pass the runner's `Requirements`), `docs/upgrades.md`. Create `internal/worker/sync_requirements.go`, `internal/worker/sync_requirements_test.go`. Tests: `internal/worker/sync_test.go` (the four galaxy tests and `fakeGalaxyOK` move or go), `internal/worker/sync_sources_galaxy_test.go` (rewritten), `internal/worker/system_e2e_test.go`.

**Interfaces:**
- Consumes Tasks 6, 7, 10 (`Requirements`, `OpenCloneFS`).
- Produces: `func (e *SyncExecutor) ensureRequirements(ctx context.Context, dir, subpath string, env RunEnv, sink func(*workerpb.RunEvent) error) (requirements.Entry, error)` — like the runner's (no share; `RepoCacheDir: env.RepoCacheDir`); lines as `stdout` events with payload `{"text": …}` (`json.Marshal`); the job error is `install galaxy requirements: %w` and the last line `Requirements: ` + that text. `requirements_installed` is `!entry.IsZero()`. `openInstalledRolesFS`: zero entry → `(nil, nil)`; else `requirements.OpenDir(installed.Dir)`, then `root.OpenRoot("roles")` as the read root, the outer root closed; every error wrapped `open installed roles: %w`, logged at warn `open the installed roles` with `err`, one `read_failed` note (message unchanged), the read goes on.
- `docs/upgrades.md`: under "Version skew", a 7.88 paragraph (`repo.repo_subpath` additive; an older worker ignores it and keeps 7.83's behaviour; a new worker under an older manager derives it; the fallback goes one minor later; a downgraded worker ignores `<repo>.requirements/`). Under "Behaviour changes", **7.88, requirements:** the first run (or sync) of each commit installs (time, network to the galaxy source); a repository's `ansible.cfg` no longer steers the install; a `requirements.yml` holding only comments no longer fails a sync; local sources outside the repository, `include:` and role names with a path are refused; with `PULSIX_WORKER_PLAY_UID` set, ansible-galaxy runs as the play uid and can write only into its temp entry (a role or collection that tries to write elsewhere, or whose files the worker cannot read, fails the install by name), and the sync opens the clone read-only to the play group as runs already do; without a play uid the worker log warns that the install is not isolated; `<repo>.galaxy` is removed by the first ensure that installs, and stays for a repository that no longer has requirements (remove by hand).

- [ ] **Step 1: Failing tests** (fake `Requirements` = `requirements.NewService(&requirementstest.Galaxy{…}, &requirementstest.Clock{Step: time.Second}, 0)`; clone at `<tmp>/repo`, `testCommit`).
  - `TestSyncExecutorInstallsRequirementsForItsCommit`: subpath `ansible` with `ansible/requirements.yml` → `succeeded`, `requirements_installed` true; events: `stdout` lines (first `Requirements: installing for commit <testCommit[:7]> from requirements.yml`), then the one `ok`; the entry exists under `CacheDir(clone)`.
  - `TestSyncExecutorReusesTheInstallOfItsCommit`: a second sync → one `stdout` `Requirements: using the install for commit … on this worker`, no new call, `requirements_installed` true.
  - `TestSyncExecutorRequirementsFailureFailsTheJob`: `ExitError{3, "cannot resolve"}` → `StatusError`, error exactly `install galaxy requirements: ansible-galaxy role install -r requirements.yml: exit 3: cannot resolve`, last event `stdout` `Requirements: ` + it, no `ok` event.
  - `TestSyncExecutorCommentOnlyRequirementsInstallNothing`: `"# none\n"` → `succeeded`, `requirements_installed` false, events `[stdout "Requirements: requirements.yml lists nothing to install", ok]`, no call.
  - `TestSyncExecutorRefusesARequirementsFileLinkedOutOfTheClone`: `requirements.yml` → symlink to an outside file → `StatusError`, error contains `requirements.yml` and `outside the repository's files`, no call.
  - `TestSyncExecutorRefusesALocalSourceOutsideTheClone`: `- src: ./link/r.tar.gz` with `link` → an outside dir holding `r.tar.gz` → error contains `requirements.yml: the source "./link/r.tar.gz" is outside the repository`, no call.
  - `sync_sources_galaxy_test.go`: `TestSyncExecutorReadsRolesInstalledFromRequirements` and `TestSyncExecutorDoesNotFollowSymlinksOutOfTheInstalledRoles` install their roles and links through the fake (`OnInstall` makes the links inside `in.Entry/roles`; `rel` climbs out of the entry into the clone's `secret.yml`, `git` into `.git/config`); expectations unchanged. `TestSyncReadFindsInstalledRolesInAnsiblePlaybookOrder`: `playbooks/site.yml` uses `inroles`, `beside`, `besideonly`, `rootonly`; repo has `playbooks/roles/inroles`, `playbooks/beside`, `playbooks/besideonly`, `roles/rootonly`; installed `inroles` and `beside`; each role declares one option named after where it lives → fields `[inroles_repo, beside_installed, besideonly_repo]`, `beside`'s source file `(requirements)/beside/meta/argument_specs.yml`, `rootonly` → `RoleNotFound`. `TestSyncReadOfAnEntryThatCannotBeOpened` replaces `TestSyncExecutorInstalledRolesThatCannotBeOpened`: `collectVarSources` called directly with an `Entry` whose `Dir` is a symlink to a dir of roles, a dangling symlink, a file, or missing → the log has `open the installed roles` and `a symlink, which is not followed` / `not a directory` / `no such file or directory`; one `read_failed` note with today's message; the playbook's role `RoleNotFound`; `r.Failed` false.
  - `system_e2e_test.go` `TestSyncExecutorFetchesThroughTheRealRunner`: `NewSyncExecutor(runExec, runner.NewRequirements(runExec, worker.Config{AnsibleBin: <dir>/ansible-playbook}))` with the recording fake at `<dir>/ansible-galaxy`; the comment-only `requirements.yml` → `requirements_installed` false, the fake galaxy's args file absent, events `[stdout …lists nothing to install, ok]`.
- [ ] **Step 2:** `./internal/worker/` — FAIL.
- [ ] **Step 3:** implement; remove the old symbols; `grep -rn "GalaxyDir\|galaxyRolesDir\|GalaxyBin" internal cmd` prints nothing.
- [ ] **Step 4:** `./internal/worker/`, `./internal/worker/runner/`, `./internal/worker/requirements/...`, `./cmd/worker/`, `./internal/app/` pass; `./internal/e2e/` (1800 s); `make test`.
- [ ] **Step 5: Commit** `feat(worker): install and read a sync's requirements through the per-commit cache`.

### Task 12: Requirements with real Ansible, offline (test strategy "Real Ansible")

**Files:** Create `internal/worker/runner/requirements_real_test.go` (`requireAnsibleGalaxy` gated like `requireAnsiblePlaybook`; `roleTarball(t, name string, files map[string]string) string` — `archive/tar` + `gzip`, always with `<name>/meta/main.yml` = `"galaxy_info: {author: pulsix}\ndependencies: []\n"`: galaxy refuses a role tarball without it, verified).

**Interfaces:** Consumes Tasks 7 and 10 (`runner.NewRequirements(r, worker.Config{AnsibleBin: <ansible-playbook>})`, no play uid, `materializePlaybook`, `ensureRequirements`, `materializeRunnerConfig`, `subprocessEnv`). Job: `repo:<id>:site.yml`, `subpath` `site.yml`, `repo_subpath` `""`.

- [ ] **Step 1: Tests** (fixture through `gitFixtureFiles`; the layout verified with ansible-core 2.21.5):
  - `TestRequirementsInstallAndResolveInRealAnsible`: `requirements.yml` `roles: [{name: vendored_role, src: ./vendor/vendored_role.tar.gz}]`; `roles/requirements.yml` (list form) `- {name: shadowed_role, src: ../vendor/shadowed_role.tar.gz}`; `collections/requirements.yml` `collections: [{name: ../vendor/demo, type: dir}, {name: ../vendor/shadow, type: dir}]`; `vendor/demo` and `vendor/shadow` hold `galaxy.yml` (`namespace: pulsix_test`, `name`, `version: 1.0.0`, `readme: README.md`, `authors: [pulsix]`), `README.md` and `roles/greet/tasks/main.yml` (`from-entry-demo` / `from-entry-shadow`); committed next to the root playbook: `roles/shadowed_role/tasks/main.yml` (`from-repo-role`) and `collections/ansible_collections/pulsix_test/shadow/roles/greet/tasks/main.yml` (`from-adjacent-collection`); `site.yml` runs `vendored_role`, `shadowed_role`, `pulsix_test.demo.greet`, `pulsix_test.shadow.greet`. Assert: the entry holds `roles/vendored_role`, `roles/shadowed_role`, `collections/ansible_collections/pulsix_test/{demo,shadow}`; `ansible-playbook -i localhost, -c local site.yml` with the run's env exits 0 with `"msg": "from-vendored-role"`, `from-repo-role`, `from-entry-demo`, `from-adjacent-collection`, and neither `from-entry-shadowed-role` nor `from-entry-shadow`.
  - `TestRequirementsCommentOnlyInstallsNothingInRealAnsible`: `requirements.yml` `"# none\n"` → zero entry, no `CacheDir`, the one line `Requirements: requirements.yml lists nothing to install`.
  - `TestTraversalFixturesEscapeWithoutIsolationInRealAnsible` — the control for Task 13, proving its fixtures attack (no play uid; everything inside `t.TempDir()`): (a) `collections/requirements.yml` `collections: [{name: ../vendor/evil.tar.gz, type: file}]` with `evilCollectionTarball(t, target)` (Task 13's builder, duplicated here) and `target = <tmp>/outside/ESCAPED_COLLECTION` → the run's `Ensure` succeeds and `target/MANIFEST.json` exists; (b) `requirements.yml` `- {name: outer, src: ./vendor/outer.tar.gz}` where `outer`'s `meta/main.yml` depends on `{src: ./vendor/inner.tar.gz, name: ../../../ESCAPED_DEP}` → `<RepoCacheDir>/<org>/ESCAPED_DEP/meta/main.yml` exists (beside the clone). Both are what ansible-core 2.21.5 did here; the log has the "not isolated" warning.
  - `TestRequirementsRefuseLocalGitInRealAnsible`: a second local repo holding a role, `- {name: gitrole, src: "git+file://<path>"}` → error contains `transport 'file' not allowed`; no entry, no `.tmp-*`.
- [ ] **Step 2 (verify):** `go test -race -count=1 -timeout 900s -run 'InRealAnsible' ./internal/worker/runner/` — passes here (ansible-core 2.21.5); then with `PULSIX_E2E_REAL_ANSIBLE=1`. If one fails, the run is wrong, not the test: fix and say so. Record the output.
- [ ] **Step 3:** `./internal/worker/runner/` passes.
- [ ] **Step 4:** lint, gofmt.
- [ ] **Step 5: Commit** `test(runner): install and use requirements with real Ansible`.

### Task 13: In the worker image, galaxy cannot write outside its entry (owner decision 2026-11-06)

**Files:** Create `internal/e2e/uidboundary/requirements_test.go` (with `tarGz(t, files map[string]string) []byte`, `roleTarball`, `evilCollectionTarball(t, target string) []byte`). Modify `internal/e2e/uidboundary/boundary_test.go`: `runOneJobErr` sets `executor.Requirements = runner.NewRequirements(executor, cfg)`, and `gitFixture` takes extra files (path → content; binary tarball content as a string is fine) next to its playbook.

**Interfaces:** Consumes the wired worker (`runOneJob`, `localhostJob`, `writeWorkerKey`, `inImage`) and Tasks 7, 10. `evilCollectionTarball` — the verified minimal shape (ansible-core 2.21.5 installed it at `target`): members `MANIFEST.json` = `{"collection_info": {"namespace": "pulsix_test", "name": strings.Repeat("../", 32) + strings.TrimPrefix(target, "/"), "version": "1.0.0", "dependencies": {}}, "file_manifest_file": {"name": "FILES.json", "ftype": "file", "chksum_type": "sha256", "chksum_sha256": <hex sha256 of the FILES.json bytes>, "format": 1}, "format": 1}` and `FILES.json` = `{"files": [{"name": ".", "ftype": "dir", "chksum_type": null, "chksum_sha256": null, "format": 1}], "format": 1}`.

- [ ] **Step 1: Tests** (each `inImage(t)`; repo jobs shaped like `TestRepoPlaybookRunsUnderThePlayUID`; `cache = /var/lib/pulsix-worker/repo-cache/<org>`).
  - `TestRequirementsInstallUnderThePlayUID`: `requirements.yml` with a vendored role tarball (`from-installed-role`) and `collections/requirements.yml` with a `type: dir` collection whose role prints `from-entry-collection`; `playbooks/site.yml` uses both → `succeeded`, events contain both texts; every path under `<cache>/<repo>.requirements/<entry>` has `Stat_t.Uid == 65532` and no group or world write bit; no `.tmp-*` is left; `<cache>/<repo>.requirements` is not group-writable.
  - `TestRequirementsCannotWriteOutsideTheirEntry` table: (a) the traversal collection tarball with `target = <cache>/ESCAPED_COLLECTION`; (b) the `outer`/`inner` dependency with `name: ../../../ESCAPED_DEP` → status `error`, the error starts with `install requirements: ansible-galaxy ` and contains `Permission denied`; the last `stdout` event text starts with `Requirements: install requirements:`; `os.Lstat(target)` is `ENOENT` (for (b) `<cache>/ESCAPED_DEP`); no entry and no `.tmp-*` under `<cache>/<repo>.requirements`; the clone's files are unchanged (`git status --porcelain` empty in the clone).
- [ ] **Step 2 (verify):** these run only inside the image: `docker build -f deploy/worker/Dockerfile -t noixdev/pulsix-worker:dev .` then `go test -race -count=1 -timeout 1800s -run TestPlaybookUIDBoundary ./internal/e2e/` (CI's docker job runs it; locally only with docker and the built image — record whether it ran or skipped). Run directly they skip (`PULSIX_UID_BOUNDARY_IN_IMAGE` unset). A failure of (a) or (b) with the target present is the isolation not holding: fix Task 7, never the test.
- [ ] **Step 3:** `go vet ./internal/e2e/...`; lint; gofmt.
- [ ] **Step 4:** `TestPlaybookUIDBoundary` reports no `--- SKIP` inside the image.
- [ ] **Step 5: Commit** `test(e2e): ansible-galaxy cannot write outside its cache entry under the play uid`.

### Task 14: One journey through ansible-runner (test strategy, e2e)

**Files:** Create `internal/e2e/requirements_test.go`. Modify the "galaxy needs network" comments in `internal/e2e/harness_test.go` (`materializeFixtureRepo`), `internal/e2e/steps_test.go` (`setupRepo`) and `internal/e2e/fixtures/git/README.md` (the base fixture still has no requirements; the requirements journey pushes vendored ones).

**Interfaces:** Consumes the wired worker (Tasks 10–11) and the e2e helpers `wireScenario`, `pinHostKey`, `pushFixtureChange`, `awaitJob`, `awaitEvents`, `createTemplate`, `runTemplateSucceeds`; `roleTarball` is duplicated locally (test packages do not share).

- [ ] **Step 1: Test** `TestRequirementsEndToEnd`: skip without docker; skip unless `PULSIX_E2E_REAL_ANSIBLE=1` (galaxy and ansible-runner are real only there). `s.pinHostKey()`; push `requirements.yml` (`roles: [{name: req_role, src: ./vendor/req_role.tar.gz}]`, `collections: [{name: ./vendor/demo, type: dir}]`), the tarball (`req_role` with `meta/argument_specs.yml` declaring `req_role_greeting` str default `hi`, `tasks/main.yml` debug `from-installed-role`), `vendor/demo/{galaxy.yml, README.md, plugins/action/hello.py}` (`namespace: pulsix_e2e`, `name: demo`; an action plugin returning `msg: from-collection-entry`, controller-side, so the exec-only host serves it — verified with ansible-core 2.21.5) and `requirements_demo.yml` (roles `req_role`; task `pulsix_e2e.demo.hello:` registered and printed). Sync → `succeeded`, `requirements_installed` true, a `stdout` event text starting `Requirements: installing for commit`; `GET …/repos/<id>/playbooks` lists `requirements_demo.yml` with `fields == 1`, not failed (the read saw the installed role). Pick it, `createTemplate`, `runTemplateSucceeds`; its events have the `stdout` text `Requirements: using the install for commit <sync commit[:7]> on this worker` and payloads containing `from-collection-entry` and `from-installed-role` — so ansible-runner did not override either key.
- [ ] **Step 2:** `go test -race -count=1 -timeout 1800s -run TestRequirementsEndToEnd ./internal/e2e/` — skips locally (no ansible-runner); record it. CI runs it with `PULSIX_E2E_REAL_ANSIBLE=1`.
- [ ] **Step 3:** `go test -race -count=1 -timeout 1800s ./internal/e2e/` passes.
- [ ] **Step 4:** `make test`; lint; gofmt.
- [ ] **Step 5: Commit** `test(e2e): a run uses a role and a collection installed for its commit`.

---

## Self-review

- **Coverage:** goal 1 layout/key/fill/lock/evict → Tasks 2, 4, 6; goal 1a → Task 1 (plus refusals through `Ensure` 6, the run 10, the read 11); goal 2 (step order, share, both keys, failing run, claim field) → 8, 9, 10; goal 3 → 2 (sync/real symlink cases 11); goal 4 (commands, env, binary resolution) → 7; goal 5 (failure, timeout, cancel, network, sync) → 6, 7, 10, 11; goal 6 (lines, redaction, cap) → 6, 10, 11; goal 7 → 11; goal 8 (legacy removal, skew, fallback, upgrades doc) → 4, 6, 9, 11; goal 9 → Global Constraints (noix-docs) and Tasks 10/11 (package comments, upgrades); Constraints (local sources, lock scope, include) → 2, 3, 4; owner decision 2026-11-06 (play uid, layout and modes, take-over, no fallback, warning, tests) → 5, 6, 7, 10, 12, 13; test strategy unit/real/manager/regression → 1–14.
- **Types:** `Files`, `Request` (with `RepoCacheDir`), `Entry`, `Install` (with `CloneDir`, `RepoCacheDir`), `Galaxy` (`Install`, `RemoveAll`), `ExitError`, `Clock`, `CacheDir`, `OpenDir`, `takeOver` (Tasks 1–6) are what Tasks 7, 10, 11, 12 use; `runner.Galaxy`, `runner.NewRequirements`, `worker.RemoveAsPlayUID` (7), `worker.Requirements`, `worker.OpenCloneFS` (10) are what 11–14 use; `repoCheckout` and `ensureRequirements` (10) are what 12 uses; `evilCollectionTarball` is defined in 13 and duplicated in 12 (test packages do not share).
- **Review Focus:** 1 → Tasks 2, 12, 13; 2 → Tasks 5, 10, 13; 3 → Task 7; 4 → Tasks 6, 10; 5 → Tasks 4, 6, 7, 10.
