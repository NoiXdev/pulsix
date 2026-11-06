# Galaxy requirements installed per commit, by the run itself — design

Date: 2026-11-05. Backlog 7.88 (found by 7.83). Owner decision: 2026-11-05 (chat, recorded below). Code facts behind this document: `internal/worker/sync.go` (`findRequirements`, `installRequirements`, `GalaxyDir`), `internal/worker/sync_sources.go` and `clonefs.go` (the 7.75 read of installed roles), `internal/worker/runner/{playbook.go,layout.go,runner.go,share.go,gitrepo.go}`, `internal/jobs/queue_sql.go` (`repoKindPayload`), `internal/varsource/{discover.go,roles.go}`, the 7.75 spec `2026-11-05-variable-editor-design.md` (goal 4, role lookup), and the plan on branch `fix/7.89-7.90` (`docs/superpowers/plans/2026-11-06-7x-review-leftovers.md`: its dropped Task 3 moves here per the 2026-11-05 controller ruling, see goal 1a; its corrected role order is followed in goals 2 and 7). Ansible behaviour verified locally against ansible-core 2.21.5 (`/opt/homebrew/bin/ansible-galaxy`), with local sources only; see "Verified Ansible behaviour".

## Intent

A repository playbook that needs roles or collections from Ansible Galaxy (or a git host) names them in `requirements.yml`. Today only the repo sync installs them, with `ansible-galaxy install -r requirements.yml -p <clone>.galaxy`. That has two defects (backlog 7.88):

1. With `-p`, ansible-galaxy installs **roles only**: the collections are skipped with a warning and the command exits 0. The worker image carries ansible-core only, so a playbook that uses a collection (`community.general.*`, `ansible.posix.*`, …) fails at run time with "couldn't resolve module/action".
2. The installed roles exist only on the worker that synced, from that sync's commit. A run on another worker, after a push that was not synced yet, or after `requirements.yml` dropped a role, sees other roles or none; the galaxy directory is never cleaned.

Success: a run of a repository playbook always has exactly the roles and collections its commit's requirements name, on whichever worker it lands, and the run log says what was installed or reused. The sync and the run share one install per commit, so a synced commit's first run does not install again on the same worker.

## Owner decision

From 2026-11-05 (chat, binding):
- **Every run installs its repository's requirements itself — roles and collections — cached per commit**, so every worker at the right commit sees the same roles and collections.
- Cost accepted: the first run (or sync) of a commit on a worker pays the install time, and a worker needs network access to the galaxy source.
- The play uid stays (`PULSIX_WORKER_PLAY_UID`, default 65533).

## Verified Ansible behaviour (ansible-core 2.21.5, 2026-11-05)

Probed in a scratch directory with local fixtures (a role as a git repo and as a tarball, a collection as a tarball, a git repo and a directory); nothing was installed outside the scratch directory.

| Command / setting | Observed |
|---|---|
| `ansible-galaxy install -r f -p d` (today's sync) | roles installed into `d`; `[WARNING] … contains collections which will be ignored`; exit 0 |
| `ansible-galaxy role install -r f -p d/roles` | roles only, collections section ignored silently; a second run prints "already installed, skipping", exit 0 |
| `ansible-galaxy collection install -r f -p d/collections` | collections only, into `d/collections/ansible_collections/<ns>/<name>`; roles section ignored |
| a list-form file (`- name: …`, the legacy roles format) | `role install` accepts it; `collection install` fails, exit 1 ("Expecting requirements file to be a dict with the key 'collections'") |
| a file with only `roles:` / only `collections:` / both lists empty | the other subcommand prints "Skipping install, no requirements found", exit 0 |
| a file holding only a comment (empty document) | **both subcommands fail**, exit 1, "No requirements found in file" |
| collection install, target not in the configured collections path | `[WARNING] … will not be picked up in an Ansible run`; silenced by setting `ANSIBLE_COLLECTIONS_PATH` to the target for the install |
| collection install of a collection already present on the Python path or a configured path | "Nothing to do. All requested collections are already installed", exit 0, **nothing installed into the target**; with `ANSIBLE_COLLECTIONS_SCAN_SYS_PATH=false` and `ANSIBLE_COLLECTIONS_PATH=<target>` it installs into the target |
| unreachable galaxy server | role install: exit 1, "was NOT installed successfully: … Connection refused"; collection install: exit 1, "Unknown error when attempting to call Galaxy" |
| `ansible.cfg` in the working directory | read by ansible-galaxy (a `[galaxy] server =` there redirected the install); `ANSIBLE_CONFIG=/dev/null` is refused ("Unsupported configuration file extension"), a generated `*.cfg` works |
| `GIT_ALLOW_PROTOCOL=https:http:ssh:git` | a `git+file://` role or collection source fails: "fatal: transport 'file' not allowed", exit 1 |
| relative sources (`src: ./vendor/r.tar.gz`, collection `name: ./vendor/c.tar.gz` `type: file`, `name: ../vendor/c` `type: dir`) | resolved against the **working directory**; offline, no network needed |
| a role `src` that is a directory | not supported (galaxy tries to download it); roles come from tarballs, SCM URLs or galaxy names |
| ansible-playbook with `roles_path = <cache>/roles:…` and `collections_path = <cache>/collections:…` | the playbook's own `roles/` wins over `roles_path`; the playbook's own `collections/` wins over `collections_path`; a collection in `collections_path` wins over the same collection on the Python path |
| the `ansible.cfg` key | `collections_path` (`[defaults]`, env `ANSIBLE_COLLECTIONS_PATH`); default `{{ ANSIBLE_HOME }}/collections:/usr/share/ansible/collections` |

## Goals

### 1. One per-commit requirements cache, shared by sync and run

Layout, next to the clone (outside its working tree, like today's `<clone>.galaxy`, so `reset --hard` and the playbook discovery never see it):

```
<RepoCacheDir>/<org-id>/<repo-id>                    the clone (unchanged)
<RepoCacheDir>/<org-id>/<repo-id>.requirements/      0700, created by the worker
    .lock                                            0600, flock target
    .tmp-<random>/                                   an install in progress (0700, MkdirTemp)
    <commit>-<hash16>/                               a published entry, never modified again
        roles/                                       always present (may be empty)
        collections/ansible_collections/<ns>/<name>  `collections/` always present
```

- **Key:** `<commit>-<hash16>`. `commit` is the clone's `HEAD` after the fetch (`HeadCommit`, full SHA-1 or SHA-256). `hash16` is the first 16 hex digits of a SHA-256 over the requirements root (relative to the clone) and, per counted file in the fixed order of goal 3, its location and its exact bytes. The commit alone is not enough: the repo's subpath is a Pulsix setting and can change without a commit, which changes which files count. The hash makes the key say exactly what was installed.
- **One entry per commit, not per requirements content.** Two commits with identical requirements each get their own install. That is the owner's "cached per commit": a floating requirement (an unpinned collection, a role at a branch) is re-resolved on the next commit, and one commit always sees the install it got first on that worker.
- **Fill atomically:** under the lock, an entry that opens as a real directory (goal 1a) is used as is. If it does not exist, the worker creates `.tmp-<random>/{roles,collections}`, runs the installs into it, and renames it to the entry name (`os.Root.Rename` on the cache directory's root: same file system, atomic). A failed or cancelled install removes its temp directory; a killed worker's leftover `.tmp-*` is removed by the next ensure of that repo (`os.Root.RemoveAll`, under the lock, so no install is in progress). Anything else at the entry name (a symlink, a file) is refused, not removed (goal 1a).
- **Lock:** an exclusive `flock` on `<repo-id>.requirements/.lock`, held for check → install → publish → evict. Readers of a published entry take no lock: it is immutable once renamed. Today a worker runs one job at a time (`loop.go`), so the lock never waits; it costs a few lines and keeps a second job or a second worker process on the same data dir from installing twice or evicting under an install. Its limit is stated under Constraints.
- **Evict:** at the end of every ensure, under the lock, the used entry's mtime is set to now and the repo's entries beyond the **3** most recently used are removed. Three covers the commit being run, the previous one (a run claimed just before a push) and one rollback; each entry is self-contained, so nothing else depends on a removed one. Eviction is per repository and runs only when that repository is synced or run.
- **One function, two callers:** a new package `internal/worker/requirements` exposes `Ensure(ctx, req) (Entry, error)` with `Entry{Dir, RolesDir, CollectionsDir}` (zero `Entry`, nil error when the commit has nothing to install). The sync executor and the runner call it with the same inputs; the galaxy command is behind an interface (`Galaxy`) declared next to the service, and time is injected (`Clock`) for the eviction order.

### 1a. Opening the cache: one function, open then verify, named refusals

This takes over the galaxy-directory items dropped from the 7.90 package (2026-11-05 controller ruling): one place that checks the directory, open-then-verify instead of check-then-open, a named refusal for a symlink, a logged or failing outcome when the cache is left out, tests for the open-error branches, and a play-uid test that can fail.

- **One function** in `internal/worker/requirements`, `openDir(path string) (*os.Root, error)`, is the only way any caller opens the repo's cache directory (`<repo-id>.requirements`) or an entry (`<commit>-<hash16>`). The installer, the eviction, the runner's check before writing `ansible.cfg` and the sync's variable read all use it. Nothing else `Lstat`s or `Stat`s these paths to decide whether to use them.
- **Open, then verify.** `os.OpenRoot(path)` first, then `os.Lstat(path)` and `os.SameFile(lstatInfo, root.Stat("."))`. The path must be a real directory (not a symlink), and it must be the same directory the root holds. Whatever is read or written afterwards goes through that `*os.Root` (`Mkdir`, `Rename`, `RemoveAll`, `Chtimes`, `OpenRoot` for the entry, `FS()` for the read). So what is used is the directory that was at the path once the open had happened, never one swapped in between a check and an open. Only ansible-galaxy (the temp dir it is given) and the generated `ansible.cfg` use plain paths, after this verification.
- **Named outcomes**, each wrapped with the path (`open requirements cache <path>: %w`):
  - does not exist → `fs.ErrNotExist` passes through. The caller creates the cache directory (`Mkdir` 0700, then opens it through `openDir` again) or installs the entry;
  - a symlink, also a dangling one → `errCacheSymlink` = "a symlink, which is not followed";
  - exists but is not a directory → `errCacheNotDirectory` = "not a directory" (only for a non-directory, never for a symlink);
  - verification found a different directory than the one opened → `errCacheReplaced` = "replaced while it was opened";
  - any other `OpenRoot` or `Lstat` error (EACCES, EIO, …) → that error, wrapped. Never treated as "missing".
  On an `OpenRoot` error the `Lstat` result decides which name is reported, so a dangling symlink reads as `errCacheSymlink` and not as "does not exist".
- **No repair of what the worker did not put there.** Only the worker's uid can write in its 0700 repo cache. A symlink or a file at the cache or entry path is therefore something nobody placed on purpose, as with a symlinked ancestor in `ShareRepoRead` (backlog 2.106). Removing it would hide that; following it would lead the roles path out of the cache. It is refused and named, and the operator removes it. The only things the worker removes are its own: `.tmp-*` leftovers, evicted entries and the legacy `<clone>.galaxy` (goal 8). `RemoveAll` through the root removes a link, never its target.



For a repo playbook (`repo:<id>:<path>`), after the fetch and before the run files are written:

1. resolve the requirements root and the counted files (goal 3) — nothing counted: no entry, no keys, no log line, as today without `requirements.yml`;
2. `Ensure` the entry for `HEAD`; the run log shows the step (goal 6);
3. with a play uid, `ShareRepoRead(RepoCacheDir, entry.Dir)` next to the clone's share (read-only for the play's group, symlinks skipped, ancestors refused if they are symlinks, as for the clone);
4. the generated `ansible.cfg` gets, when there is an entry:

```
roles_path = <entry>/roles:/usr/share/ansible/roles:/etc/ansible/roles
collections_path = <entry>/collections:/usr/share/ansible/collections
```

For a playbook at `<dir>/site.yml`, Ansible searches roles in `<dir>/roles`, then `roles_path` (the entry, then the system defaults), then `<dir>` itself. It never searches the repository root's `roles/` for a playbook in a subdirectory (ansible-core 2.21.5, checked on the 7.90 branch). It searches `<dir>/collections` before `collections_path` (verified here). So a role or collection committed next to the playbook still wins, as 7.83 established for roles. The two system defaults stay on each path; the home entries are left out because the run's HOME is an empty private directory. Inline, ad-hoc and system playbooks get neither key (unchanged).

**An unusable cache fails the run when the commit has requirements.** If `openDir` refuses the repo's cache directory or the entry (goal 1a), or anything else keeps `Ensure` from returning a verified entry, the run ends `error` before ansible-runner starts. The error text names the path and the reason, e.g. `install requirements: open requirements cache …/<repo-id>.requirements/<key>: a symlink, which is not followed`, and the same text is the last `Requirements:` log line. The run is not continued without the cache. 7.83 logged and continued because its galaxy directory was an optional by-product of a sync on that worker. Here the requirements are the run's own declared inputs, and a play started without them fails later with Ansible's "role not found" or "couldn't resolve module", which names neither the cache nor the cause. That is the anti-goal "a play starts although its requirements did not install". A commit without counted requirement files never opens the cache, so there is nothing to leave out or log, and no case remains where a run silently leaves an existing cache out.

The run's claim carries the repository's own subpath as an additive field `repo.repo_subpath` (`internal/jobs` `repoKindPayload`), next to today's `subpath` (repo subpath joined with the playbook path). The worker needs it to find `<repo subpath>/requirements.yml` exactly as the sync does.

### 3. Which files count

The **requirements root** is the repo's subpath when any of the files below exists there, the clone root otherwise (today's fallback, extended to all three locations). At the root, in this order:

1. `requirements.yml` — roles and collections (today's file; the format ansible-galaxy and ansible-builder read);
2. `roles/requirements.yml` — roles (AWX's location; the legacy list form is roles);
3. `collections/requirements.yml` — collections (AWX's location).

At each location `.yml` is taken when present, else `.yaml`. Every file found counts. Each file is installed on its own, from the directory that holds it (relative `src` and collection paths resolve against the working directory, verified), so a file means what it means when its author runs `ansible-galaxy` next to it. A role listed in two files is installed from the first (galaxy skips an installed role); a collection listed in two files is resolved by galaxy against what is already in the entry. The docs say to list each requirement once.

The worker reads each file itself (yaml.v3) to decide what to run, because galaxy fails on an empty document and on a list given to `collection install`:

| File content | Roles install | Collections install |
|---|---|---|
| empty, only comments, `null` | — | — |
| a list (legacy roles format) | yes | — |
| a mapping with a non-empty `roles` list | yes | — |
| a mapping with a non-empty `collections` list | — | yes |
| anything else (a scalar, a mapping whose `roles`/`collections` is not a list, invalid YAML) | the run (or sync) fails: "`<file>` is not a requirements file: …" | |

A commit whose counted files list nothing gets no entry and one log line ("requirements.yml lists nothing to install"). Each file is read through the clone's confined root (`openCloneFS` rules: no link leaving the clone, nothing under `.git`), and galaxy is handed a copy (`-r <tmp>/input/<n>.yml` inside the temp entry since the owner decision of 2026-11-06, below; working directory = the file's own directory in the clone). The hash covers exactly the bytes galaxy saw. Today `findRequirements` uses `os.Stat`, which follows a `requirements.yml` symlink out of the clone.

### 4. The commands

(The identity galaxy runs as, its `HOME`, `TMPDIR` and `ANSIBLE_CONFIG`, and how the worker takes the result over are set by "Owner decision 2026-11-06: galaxy runs under the play uid" at the end of this document; it supersedes the paths below where they differ.)

Per counted file, in the order of goal 3, roles before collections within a file:

```
ansible-galaxy role install       -r <copy> -p <tmp>/roles
ansible-galaxy collection install -r <copy> -p <tmp>/collections
```

Environment of both: `stripPrivateEnv(os.Environ())` (no `ANSIBLE_*` or `PULSIX_*` from the worker), plus
- `HOME=<workdir>/home` (the run's; galaxy's temp clones and response cache land there and are wiped with the work dir), `GIT_SSH_COMMAND` with the run's known_hosts, `GIT_TERMINAL_PROMPT=0` — as today;
- `ANSIBLE_CONFIG=<workdir>/requirements/galaxy.cfg`, a generated `[defaults]`-only file: the repository's own `ansible.cfg` is not read by the install, as it is not read by the run (owner answer 1);
- `ANSIBLE_COLLECTIONS_PATH=<tmp>/collections` and `ANSIBLE_COLLECTIONS_SCAN_SYS_PATH=false`: the "already installed" check sees only the empty target, so every listed collection lands in the entry, whatever else is installed on the worker (otherwise a worker with a system-wide collection would publish an entry without it, and workers would differ);
- `GIT_ALLOW_PROTOCOL=https:http:ssh:git`: no local git source, also not in a role's dependencies (see Constraints).

Default options only (dependencies installed; no `--force`, no `--ignore-errors`): a requirement that cannot be installed fails the step. The binary is `ansible-galaxy` next to the configured `ansible-playbook` (`PULSIX_ANSIBLE_BIN`) when that is a path, else from `PATH`; the sync uses the same resolution (today it always uses `PATH`), so sync and run install with the same Ansible.

### 5. Failure handling

- **Any install failure fails the run** before ansible-runner starts: status `error` (like a failed fetch), error text `install requirements: ansible-galaxy collection install -r collections/requirements.yml: exit 1: <redacted stderr tail, 4 KiB>`. Never a warning that lets the play start. The temp directory is removed; nothing is published.
- **Timeout:** one bound for the whole step, 10 minutes (today's `defaultInstallTimeout`), shared by sync and run. On expiry the galaxy process group is killed (galaxy spawns git; `cmd.WaitDelay` bounds the wait for the pipes) and the error says "timed out after 10m0s" plus the command.
- **Cancel:** a cancelled run kills the install the same way and ends `cancelled`; the next ensure removes the temp directory.
- **Network off:** galaxy's own error ends up in the run log and the error text. A commit with a published entry needs no network: runs of a known commit keep working while the galaxy source is down.
- **Private galaxy servers** (Automation Hub, a token): not supported (non-goal). `ANSIBLE_*` is stripped and the repo's `ansible.cfg` is not read, so the default server, a per-entry `source:` URL and public git or http sources are what works. A git source over ssh uses no key: the repo's git credential still does not ride along (v0.2b contract).
- **The sync** fails on an install failure as today (`install galaxy requirements: …`): a failing install at sync time predicts every run of that commit failing, and the repo page is where that is seen first. Its variable read does not run without the entry.

### 6. What the run log shows

The run streams the step as `stdout` events with `{"text": …}` (the shape `EventLog` renders as plain lines), before the play's output:

- `Requirements: installing for commit 1a2b3c4 from requirements.yml, collections/requirements.yml` … galaxy's stdout and stderr lines, live, each through the run's redactor and with URL userinfo removed (a token in a `src` URL), at most 500 lines per command, then `… N more lines` … `Requirements: installed in 12.4 s`;
- or `Requirements: using the install for commit 1a2b3c4 on this worker`;
- or `Requirements: requirements.yml lists nothing to install`;
- on failure the last galaxy lines are already in the log; the run's error carries the summary.

The lines are English (as all of Ansible's output in the log); no SPA change is needed. The sync job's log gets the same lines. Its ok event keeps `requirements_installed` (true when an entry exists after the sync, installed or reused).

### 7. The 7.75 variable read uses the same entry

The sync calls `Ensure` for its `HEAD` before the read, and the read gets the entry's roles from the same opened root: `openInstalledRolesFS` takes the verified entry (`openDir`, goal 1a) and opens `roles` inside it as its own confined root, instead of opening `GalaxyDir(clone)` by path. If that open fails, the 7.75 rule stands (nothing in the read fails the sync): the error is logged at warn with the path and the named reason (`open the installed roles`, `err` = e.g. "a symlink, which is not followed", every error and not only non-ENOENT ones), one `read_failed` note is added, and the read goes on without them. A sync whose `Ensure` failed never gets that far (goal 5).

The role order is the one the 7.90 branch corrects the parser to, matching ansible-playbook. For a playbook at `<dir>`: `<dir>/roles`, then the installed roles (the entry's `roles/`, i.e. `roles_path`), then `<dir>` itself. The repository root's `roles/` is never searched for a playbook in a subdirectory. This item changes only where the installed roles come from, not the order. The `(requirements)/<role>/…` names and the notes do not change. Roles inside installed collections stay `role_collection` (not read): resolving `ns.col.role` also needs the play's `collections:` keyword for short names and the playbook-adjacent `collections/`, which is beyond this item (follow-up, see "Found while specifying").

### 8. Migration and version skew

- `<clone>.galaxy` is no longer read or written. The first ensure of a repo removes it (under the lock, through the org directory's root: a directory is removed with its contents, a symlink there only as the link, never its target — the path is no longer read, so removing a stray link there hides nothing a run would use). `GalaxyDir` and `galaxyRolesDir` go away.
- No database migration. Wire changes are additive: `repo.repo_subpath` in the run claim. A worker one minor older ignores it and keeps 7.83's behaviour (roles from its own sync only). A new worker under an older manager (the wrong roll order) finds no `repo_subpath` and derives it: the claim's `subpath` with the suffix `/<playbook path>` of the `repo:` reference removed (exact for every manager released so far); that fallback is dropped one minor later.
- A downgraded worker ignores `<repo-id>.requirements/` (disk until removed by hand) and installs `.galaxy` again on its next sync.
- `docs/upgrades.md`, "Behaviour changes to check after an upgrade": the first run of each commit installs (time, network); a repo's `ansible.cfg` no longer steers the install; a `requirements.yml` holding only comments no longer fails a sync; local sources outside the repository are refused.

### 9. Documentation

- noix-docs (branch `pulsix`): `user/playbooks-and-jobs.md` "Sources" (runs install roles and collections from the three files per commit, the first run of a commit takes longer, pin versions for identical installs across workers, the worker needs to reach the galaxy source, private servers unsupported, what the log shows); `reference/variable-sources.md` ("installed roles are the ones the last sync on that worker installed" and "collections are not installed yet" are replaced: the roles installed for the synced commit; collection roles still not read); `admin/installation/requirements.md` "Worker host" (outbound HTTPS to galaxy.ansible.com or the git hosts the requirements name; disk for three installs per repository); `reference/changelog.md`.
- This repo: backlog row 7.88; `docs/upgrades.md` (goal 8); the package comments in `sync.go` (it claims to install collections), `gitrepo.go` ("the cache needs no lock") and `share.go`, which name the galaxy directory.

## Non-goals

- Private galaxy servers, Automation Hub tokens, or a galaxy credential type.
- Reading a repo's `ansible.cfg` (`[galaxy]` or anything else) for the install.
- Identical installs across workers for floating requirements: each worker resolves an unpinned requirement when it first installs that commit. Pinning versions is the author's tool, and the docs say so.
- Sharing installs between commits with identical requirements, or between workers.
- Reading variables of roles inside collections (stays `role_collection`).
- A global disk bound, or cleaning up clones and installs of deleted repositories.
- Pinning a run to the commit a sync read (7.75 non-goal; runs use the branch tip).
- Running more than one job per worker.

## Anti-goals

- A play starts although its requirements did not install.
- A run sees roles or collections of another commit, another repository or another organization from the cache.
- A half-written install is ever read, by a run or by the variable read.
- The play's uid can change an install a later job will use.
- A worker's own environment or a system-wide collection silently changes what an entry holds.
- A symlink, a file or a swapped directory at a cache path is followed, silently repaired, or silently left off the roles path; it is refused by name.
- A cache path is checked by one name and then opened by another (check-then-open).

## Constraints

- Layering per `CLAUDE.md`: the `requirements` package holds the rules (files, shapes, key, lock, publish, evict) behind a `Galaxy` interface and a `Clock`; the real `Galaxy` is a thin exec wrapper. No HTTP or SQL there. Files stay under ~400 lines.
- **Local sources.** The install runs as the worker's uid, which can read every organization's clone in the repo cache. A top-level entry whose source is a local path (`file://`, an absolute or relative path, collection `type: file|dir|subdirs`) must resolve inside the clone under the confined-root rules; anything else fails the step before galaxy runs ("`<file>`: the source `<x>` is outside the repository"). Local git sources are refused by git itself (`GIT_ALLOW_PROTOCOL`), also in role dependencies. A role dependency naming a local tarball on the worker is not inspected: that stays within the worker trust model of backlog 2.106 (work that must not share a worker gets a zone and a worker of its own). Owner answer 2.
- **Lock scope.** The lock serialises installs and evictions of one repository; it does not make the clone safe for concurrent jobs (fetch + reset are unlocked today). When `PULSIX_MAX_CONCURRENT > 1` is built, the clone needs the same lock, and eviction must skip entries a running job uses.
- `:` or a newline in `RepoCacheDir` would split `collections_path` as it splits `roles_path`; the 7.90 branch makes a worker refuse to start with `:` or a line break in its data dir, which covers both keys.
- English in code and log lines; no new UI strings; user docs updated in the same work item.

## Left to the builder

- Whether the play-uid share of an entry happens once at publish (before the rename) or on every run (as for the clone), as long as a published entry is group-readable before the play starts and never group-writable.
- The exact classification of a requirement's source as local or remote (table-tested; galaxy names never contain `/`).
- How `Ensure` reports its lines (callback or channel) and where in `Runner.Run` the step sits relative to the `run_start` bookend (the manager treats `run_start` like any event).
- The type and file names inside `internal/worker/requirements`, and how the sync and the runner share one `Galaxy` wiring (`cmd/worker`, `internal/app/embedded_worker.go`, `internal/e2e`).

## Test strategy

- **Unit (no Ansible):** table tests for the file shapes (goal 3 table) and the root fallback; the key (commit, subpath change, byte change → different key); `Ensure` with a fake `Galaxy` that writes marker files: hit, miss, publish by rename, a failing command leaves no entry and no temp dir, cancel mid-install, a stale `.tmp-*` is removed, a symlink or a file at the entry name or at `<repo-id>.requirements` is refused and left in place (goal 1a), eviction keeps the 3 most recent by the fake clock, legacy `.galaxy` removed, two concurrent `Ensure` calls install once (lock); the local-source check (inside, `../` outside, absolute, a symlink leading out, `type: dir`); the generated `ansible.cfg` with and without an entry (both keys in `[defaults]`); the galaxy command line and environment (`ANSIBLE_CONFIG`, `ANSIBLE_COLLECTIONS_PATH`, `ANSIBLE_COLLECTIONS_SCAN_SYS_PATH=false`, `GIT_ALLOW_PROTOCOL`, no inherited `ANSIBLE_*`); log line redaction and the 500-line cap; the derived `repo_subpath` fallback; the sync's variable read opening the entry's `roles` through the verified root, with the corrected role order of goal 7 (an installed role is found after `<dir>/roles` and before `<dir>`; the repository root's `roles/` is not searched for a playbook in a subdirectory).
- **Opening the cache (goal 1a), table over `openDir`** for both the cache directory and an entry: does not exist → `errors.Is(err, fs.ErrNotExist)`; real directory → a root whose `Stat(".")` is that directory; symlink to a directory and dangling symlink → `errCacheSymlink`, message contains "symlink"; regular file → `errCacheNotDirectory`, message contains "not a directory" and never "symlink"; a permission error (parent 0000; skipped as root) → neither ErrNotExist nor a sentinel, path in the message; replaced between open and verify → `errCacheReplaced`, driven by an unexported hook that runs after `OpenRoot` and swaps the path for another directory. Each refusal also through `Ensure` (the run's error names path and reason, nothing installed, the symlink and the file are still there, the link's target untouched) and through the sync's read (a warn log line with the reason plus one `read_failed` note, the read goes on). The runner's test of a repo run asserts that the run fails with that text and that no `ansible.cfg` naming the entry is written.
- **Play uid, a test that can fail:** before `ShareRepoRead`, seed the entry with what the share must clear: every directory from a role file up to and including `<repo-id>.requirements` at 0770, the role file at 0660, a collection file at 0660. Then assert that every entry is group-readable, that none is group-writable, and that a symlink inside the entry (and its target outside) keeps its mode. The plan step proves the "none group-writable" half by temporarily dropping `&^ 0o020` in `chmodReadShared` and watching it fail.
- **Manager:** `repoKindPayload` carries `repo_subpath` (with and without a repo subpath).
- **Real Ansible**, gated like today (`PULSIX_E2E_REAL_ANSIBLE=1` fails when the binaries are missing, otherwise skip): a fixture repository with vendored sources only, so no internet is needed — a role tarball built by the test (`archive/tar`), a collection as `type: dir` (or a tarball from `ansible-galaxy collection build`, offline), all referenced by relative paths from `requirements.yml`, `roles/requirements.yml` and `collections/requirements.yml`. Checks: real `role install` and `collection install` fill an entry; `ansible-playbook` with the generated `ansible.cfg` runs a role and a collection role from it while the playbook's own `roles/`/`collections/` still win; an empty-comment file installs nothing; a `git+file://` source fails with "transport 'file' not allowed". One `internal/e2e` journey through ansible-runner (the run uses a collection module from the entry, the sync's read sees an installed role) proves ansible-runner does not override the two keys.
- **Regression:** the 7.83 tests (`galaxy_roles_test.go`, `sync_sources_galaxy_test.go`, the system e2e fake galaxy) move to the entry layout.

## Found while specifying

- A deleted repository's clone (and its installs) stay on every worker that had it; nothing removes them. File as its own backlog item if wanted.
- Roles inside collections (`ns.col.role`) could be read by the 7.75 parser from the entry's `collections/`; that needs the play's `collections:` search list and the playbook-adjacent `collections/` too. Follow-up item.
- The e2e fixture comment "galaxy needs network" (`internal/e2e/harness_test.go`, `steps_test.go`) stops being true with vendored sources.

## Owner answers (2026-11-05)

1. **The repository's own `ansible.cfg` and the install:** the install stops reading it, as the run never does (confirmed).
2. **Local sources:** confirmed as written in Constraints — inside the repository only, local git refused everywhere, a role dependency naming a local tarball on the worker accepted under the 2.106 worker trust model.

## Calls made after the builder check (controller, 2026-11-05)

- **Base:** 7.88 is built on `main` after `fix/7.89-7.90` has merged. That branch's Task 3 is dropped (not implemented); its items are goal 1a here. The corrected role order and the data-dir refusal come from that branch and are in place when 7.88 starts.
- **`include:` in the legacy list-form roles file:** refused by the shape check, like any other top-level shape the worker does not accept: the step fails before galaxy runs with "`<file>`: `include:` is not supported; list the roles in this file". Following it inside the clone would need its own hashing and confinement rules for a rarely used legacy form; accepting it would let a requirements file read any file on the worker, against owner answer 2. Other local-file references inside a requirements file are covered by the local-source rule (Constraints).

## Owner decision 2026-11-06: galaxy runs under the play uid

### Threat (found while planning, verified offline with ansible-core 2.21.5)

ansible-galaxy writes where the requirements' metadata tells it to, not only below `-p`, and it runs as the worker's uid, which owns every organization's clones and caches:

- a top-level role `name: ../../escaped` was extracted two levels above `-p`;
- a **role dependency** (`dependencies:` in a role tarball's `meta/main.yml`) named `../../../ESCAPED_DEP` was extracted into `<RepoCacheDir>/<org>/` — beside the clones — from `-p <cache>/.tmp-x/roles`; dependencies are inside the tarball, so no check of the requirements file sees them;
- a **local collection tarball** (`type: file`) whose `MANIFEST.json` has `name: "../../../../<absolute path without its leading slash>"` was installed at that absolute path (a scratch directory far outside `-p`); a `type: dir` collection whose `galaxy.yml` name held `../` made galaxy try `/esc2`.

Today's sync has the same exposure. Any repository author (or the author of a role or collection it depends on) can therefore write into another repository's clone or cache, or another organization's, as the worker.

### Decision (owner, 2026-11-06, binding)

ansible-galaxy — role and collection install, for runs and for the sync — runs under the restricted play uid (`PULSIX_WORKER_PLAY_UID`, default 65533) and can write only into its temporary cache entry. The worker takes the result over afterwards and publishes it by rename as planned. Without a configured play uid (local development, non-Linux) the install runs as the worker, as before, and logs a warning that it is not isolated.

### Design

- **Same mechanism as the play.** Galaxy starts exactly like ansible-runner (`runner.newRunnerCmd`, generalised): through the setuid helper (`PULSIX_WORKER_SETUID_HELPER`, uid 65533, primary gid = the worker's 65532) and, on Linux, under the play supervisor (backlog 2.107), which ends the whole process tree when galaxy exits or is stopped. Cancel and timeout use the runner's `stopGroup` (TERM → grace → KILL, across the uid boundary through the helper's `--signal` mode, backlog 2.104). Leftover files the play uid owns are removed as the play uid through the helper, like the loop's work dir cleanup (`removeAsPlayUID`, generalised). The loop's play-uid sweep between jobs is unchanged.
- **Where the play uid may write.** The temp entry is laid out as
  ```
  <repo>.requirements/            0750  worker  (group may traverse, not write)
      .lock                       0600
      .tmp-<x>/                   0750  worker  (not group-writable: `../` out of roles/ or collections/ is EACCES)
          input/                  0750  worker; <n>.yml and galaxy.cfg 0640 (read-only to the play uid)
          home/ tmp/              2770  worker group, setgid (galaxy's HOME and TMPDIR; home/.ssh/known_hosts)
          roles/ collections/     2770  worker group, setgid (the two -p targets)
  ```
  Every directory from `RepoCacheDir` down to `.tmp-<x>` and down to the clone gets group read+traverse and never group write (the ancestor walk of `ShareRepoRead`, which refuses a symlink on the path). Only the worker uid and the play uid hold gid 65532, so the four 2770 directories are writable by the play uid and nothing else; everything above them is not, so a `../` name fails with "Permission denied". The run's work dir is not used by the install at all (it stays owner-only until `ShareWorkDir`, backlog 7.21).
- **Environment.** As goal 4, except `HOME=<tmp>/home`, `TMPDIR=<tmp>/tmp`, `ANSIBLE_CONFIG=<tmp>/input/galaxy.cfg`, `-r <tmp>/input/<n>.yml`, known_hosts `<tmp>/home/.ssh/known_hosts`. Galaxy's scratch (temp clones, response cache) is inside the temp entry and goes with it.
- **Take-over before publishing.** After the last command, with no process of the install left (the supervisor guarantees it), the worker copies `roles/` and `collections/` of `.tmp-<x>` into a fresh worker-owned `.tmp-<y>` and checks every entry while it copies: directories and regular files are copied (directories 0700, files 0600, or 0700 when any execute bit was set: setuid/setgid/sticky and group/world bits are dropped); a regular file must have one link (a hard link is refused), is opened without following links and must be the file that was listed; a symlink is recreated only when its target is relative and stays inside the entry; anything else (a device, a FIFO, a socket, a link leaving the entry, a file the worker cannot read) refuses the install, naming the path. `.tmp-<y>` is renamed to the entry name; `.tmp-<x>` is removed (as the play uid where needed). The published entry is therefore wholly worker-owned and owner-only, so no later play can change it, and nothing the play uid created survives in it.
- **How a run reads it.** Unchanged: `ShareRepoRead(RepoCacheDir, entry)` per run opens it read-only to the play's group; `roles_path` and `collections_path` name it.
- **No silent fallback.** With a play uid set, a helper that is missing or refuses, a supervisor that is not available (`PlaySupervisor` empty), or a path that cannot be opened to the play group fails the install (run `error`, sync failed) with the reason; galaxy never runs as the worker then. Only an unset play uid runs galaxy as the worker, with a warn line in the worker log per galaxy command: `ansible-galaxy is not isolated: PULSIX_WORKER_PLAY_UID is not set, so it runs as the worker's own uid`.
- **Interfaces.** `requirements.Galaxy` gains `RemoveAll(dir string) error` (removes what the install wrote, also what the play uid owns); `requirements.Install` carries `CloneDir` and `RepoCacheDir` (what to open to the play group) instead of the work dir; the exec implementation moves from `internal/worker` to `internal/worker/runner` (`runner.Galaxy`, built from the `Runner` and the worker `Config`), because the helper, the supervisor and `stopGroup` live there.
- **Cost.** One extra copy of each install (time and, briefly, twice the disk) on a cache miss; a role or collection whose files the worker cannot read (mode 0700 directories inside a tarball) fails the install by name.

### Tests

- Unit (no root): the modes the preparation sets (every 2770 directory, nothing above them group-writable, a symlinked ancestor refused); the command shape under the helper and the supervisor; the refusals (no supervisor, missing helper, helper refusal); the warning without a play uid; the take-over table (symlink out, absolute symlink, hard link, FIFO, setuid file normalised, unreadable file, legitimate tree copied byte for byte).
- Real Ansible as the worker (control, local and CI): the traversal collection tarball and the role dependency really do land outside the entry when nothing isolates galaxy — so the in-image test's fixtures are proven to attack.
- In the worker image (`internal/e2e/uidboundary`, driven by `TestPlaybookUIDBoundary`, CI's docker job; locally only after building the image): a legitimate install works under uid 65533 and is published worker-owned; the traversal tarball and the dependency fail the run with "Permission denied", and nothing exists at their target paths.
