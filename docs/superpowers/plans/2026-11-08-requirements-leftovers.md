# Requirements Leftovers (7.92, 7.97) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A requirements install under the play uid works under a worker umask of 077 (7.92), and every leftover of the 7.88 reviews (7.97) is fixed, tested or recorded as won't fix.

**Architecture:** The setuid helper floors the exec'd program's umask at `old & 027`, so plays and galaxy installs under the play uid stay group-readable for the worker (Task 8). The source check reports permission and glob refusals as what they are and reads directories in batches (Task 1); the file reader refuses Mercurial sources (Task 2). URL userinfo redaction moves into one leaf package used by the requirements log and the git diagnostics, and galaxy's line splitter never cuts a word (Task 3). Everything else is local fixes, tests and de-duplication behind the existing layers.

**Tech Stack:** Go 1.27, C (the static setuid helper, `cc -static` in `deploy/worker/Dockerfile`), ansible-core 2.21 / ansible-galaxy on the worker, Docker for the in-image boundary test.

**Spec:** Backlog rows 7.92 and 7.97 in `docs/BACKLOG.md`, and the owner-approved decisions of 2026-11-08 (package A). The decisions file was a scratch file; its full content is carried in **Rulings** below, which are binding.

## Global Constraints

- Work on the checked-out branch `fix/7.92-7.97-requirements-leftovers`; do not switch branches.
- Everything in the repository is English: code, comments, commits, docs.
- Conventional Commits; every commit ends with exactly `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. Commit only the task's own files.
- TDD: each fix comes with a test that fails first, watched failing, then the minimal change. A test that pins existing behaviour says so and is expected to pass at once.
- Tests per package with a timeout: `go test -race -count=1 -timeout 900s ./internal/<pkg>/`; `internal/api` needs `-timeout 1800s`; `internal/e2e` runs on its own invocation. Never one whole-module `go test ./...`.
- Before every commit: `golangci-lint run` (0 issues) and `gofmt -s -l .` (no output).
- A file over ~400 lines is a signal to split; a test file this plan would grow past that gets its new tests in the new file the task names.
- Docker: about 50 unrelated containers run on this machine. Touch only containers a test starts itself (`TestPlaybookUIDBoundary` uses `docker run --rm`). Never prune, stop or remove anything else.
- No migration, no API change, no SPA change.
- End-user docs (`NoiXdev/noix-docs`) are the controller's, not a task's.

## Review Focus

1. A worker with the ordinary umask 022 or 002: plays and installs keep exactly the modes they had; the floor only ever clears group read/execute bits of the mask, it never loosens one (Task 8, `TestSetuidHelperFloorsTheUmask` rows 022, 002, 007).
2. A token used as a bare URL username in galaxy's failure (the stderr tail inside the step's error) and in a galaxy error, not only in a streamed line (Task 3, `TestEnsureRemovesABareTokenUsername`).
3. A galaxy line over 64 KiB whose credential URL straddles the cut, and a 16 KiB stderr tail whose front cut lands inside a URL (Task 3, `TestGalaxyLineWriterNeverCutsAWord`, `TestGalaxyStderrTailStartsAtAWord`).
4. A source below a directory the worker cannot read: on the way to the source, the refusal says "permission denied", never "outside the repository" (Task 1, `TestCheckSourcesNamesWhatItCannotCheck`).
5. A run or sync cancelled while the worker is still fetching the repository (not only during `HeadCommit`): it ends cancelled, not error, because both go through `runGitOut` (Task 4, subtest `a fetch cancelled while git runs`).

## Rulings

The owner's decisions (binding, copied in full in substance), then the choices this plan makes where they left one open (marked **plan**).

**7.92, umask floor in the setuid helper.**
1. `deploy/worker/setuid-exec.c`, exec form only (not `--signal`): before `execv`, after the privilege drop, read the mask with `old = umask(0)` and set `umask(old & 027)`. The play uid's new files then never deny the worker's group read or traverse: 022 stays 022, 077 becomes 027, 002 stays 002; never more permissive than the worker asked for, only the group read/execute bits are cleared. It applies to plays and galaxy installs alike (both go through the helper). Documented in the C file's header style; the program stays short. Executed as `umask(old & ~(mode_t)050)` (clear exactly the group read and execute bits), per the Task 8 review: `old & 027` also cleared the owner bits.
2. `TestRequirementsUnderAWorkerUmaskOf077` flips: under umask 077 the install succeeds with the assertions of `TestRequirementsInstallUnderThePlayUID` (same file set).
3. Without a play uid galaxy runs as the worker itself and the umask question does not arise; said in a comment.
4. Verification rebuilds the worker image: `docker build -f deploy/worker/Dockerfile -t noixdev/pulsix-worker:dev .`, then `PULSIX_E2E_WORKER_IMAGE=noixdev/pulsix-worker:dev go test ./internal/e2e -run TestPlaybookUIDBoundary -timeout 1800s` (entry point confirmed: `internal/e2e/playbook_uid_test.go` builds `./uidboundary`'s test binary and runs it in the image with `docker run --rm`).

**7.97, fix — checks (`internal/worker/requirements`).**
5. A permission error or a glob character in a source path must not read as "outside the repository": report what happened (permission denied / glob characters not supported) and keep refusing.
6. Refuse every `hg+…` source and `scm: hg` with "Mercurial sources are not supported" (the image has no hg; `hg+<local path>` is accepted today).
7. `walkTree` reads a directory in batches and charges the existing entry budget per batch, so one directory with millions of entries fails by name instead of loading everything.
8. `collection.go`'s stale galaxy line numbers are replaced by function names (preferred: they do not go stale).
9. Untyped `ns.class`: investigate; refuse on a parser differential, else leave it and record the reasoning in a comment. The harmless git/subdirs collection names stay refused (conservative), only documented.

**7.97, fix — runtime.**
10. A token used as a bare URL username (`https://TOKEN@host/…`) is redacted in galaxy output and log lines: the userinfo of any URL with a scheme. scp-style users (`git@host:`) stay, matching `redactURLs`. Both redaction paths (`requirements/lines.go` `removeUserinfo`, `runner/gitrepo.go` `redactURLs`) become consistent.
11. The 64 KiB line split must not cut a URL's userinfo across lines: redact before splitting, or never cut inside userinfo; the simpler correct one.
12. A cancel during `HeadCommit`'s git ends as cancelled, not error.

**7.97, fix — tests and cleanup.**
13. Tests for the take-over's untested branches ("changed while it was copied", destination errors).
14. In-image: the warning filter catches the stop-path warnings; the entry walk checks owner bits.
15. `evilCollectionTarball` lives in one shared test helper (e.g. `internal/worker/requirements/requirementstest`).
16. `cleanSubpath` (`internal/worker/sync_requirements.go`) and `runner.cleanRepoPath` become one exported helper both import without a cycle.
17. `internal/e2e/uidboundary/requirements_test.go` (408 lines) is split by topic.

**7.97, won't fix** (recorded in the backlog row with the reason): YAML 1.1 implicit types differ from yaml.v3, but only for non-strings, with no escape route; a hard link to the worker key needs `protected_hardlinks=0`, the kernel default protects; `removeEntry`'s temp-name claim races with probability 2^-64.

**Docs.** `docs/upgrades.md` "Behaviour changes": hg sources refused; the umask floor. `docs/BACKLOG.md`: 7.92 and 7.97 done, won't-fix items with reasons.

**Plan rulings.**
- **P1 (item 9): untyped `ns.class` is harmless; it stays accepted.** In ansible-core 2.21.5 `AnsibleCollectionRef.is_valid_collection_name` refuses a Python keyword (`not iskeyword(...) and isidentifier()`), so `_ComputedReqKindsMixin.from_requirement_dict` does not keep `ns.class` as a name: it reads it as a local path through `_is_concrete_artifact_pointer` (a file, a collection dir or a namespace dir in galaxy's working directory) or fails. Verified with `ansible-galaxy` 2.21.5: `collections: [ns.class]` without such a path fails with "Neither the collection requirement entry key 'name', nor 'source' point to a concrete resolvable collection artifact"; with a directory `ns.class/` holding the `galaxy.yml` of `pulsix_test.other` it installed `pulsix_test.other`. Our check already treats the string both ways: it is in `p.sources` (resolved, and walked as a directory) and, as a kept name, `namespaceName` only lets two plain components through. So there is a differential, but no reading escapes the check. Task 2 records this in `checkUntypedName`'s comment and pins it with a test.
- **P2 (item 4):** all Python `file:line` citations in the package's non-test files (`collection.go`, `shape.go`, `source.go`, `source_tree.go`, 22 in all, urllib's included) become function names; `doc.go` names the ansible-core version they were read against (2.21.5) once.
- **P3 (item 10):** `ssh://git@host/…` now also loses its user (`ssh://[redacted]@host/…`): the rule is "any URL with a scheme". The `redact_test.go` row "user-only userinfo stays" changes to an scp-style `git@github.com:org/repo.git`, which stays. The shared code is a new leaf package `internal/worker/urlcred` (requirements may import nothing from `internal/worker` or `runner`, see its `doc.go`).
- **P4 (item 11):** never cut inside a word. A URL's userinfo never holds white space, so the splitter cuts at the last white-space byte of the 64 KiB window; a word longer than the window is left out whole, with a marker line. Redacting before the split cannot work alone: the window's end may cut a URL whose `@` has not arrived yet. The same bug class sits in galaxy's 16 KiB stderr tail (`lineWriter.kept`, cut at its front): the tail now starts after its first white-space byte when it was cut.
- **P5 (item 12):** the fix sits in `runGitOut`, which `HeadCommit` shares with the fetch; a fetch cancelled mid-git now also ends cancelled. Recorded in `docs/upgrades.md`.
- **P6 (item 15):** the shared helper also takes `tarGz` and `roleTarball` (the evil tarball is built with `tarGz`, both are "kept in sync" copies today), and `internal/e2e/requirements_test.go`'s third `roleTarball` copy goes too.
- **P7 (item 13):** two test seams join `takeOverHooks`: `beforeOpen` and `afterRead`.
- **P8 (item 14):** the entry walk checks exact modes: after `ShareRepoRead` a directory is `0750`, a regular file `0640` or `0750`, a symlink is not mode-checked. If the image shows other modes, report them; do not loosen the check.

---

### Task 1: The source check names what it cannot check and reads directories in batches (items 5, 7)

**Files:** Modify `internal/worker/requirements/source.go`, `source_tree.go`. Tests: create `internal/worker/requirements/source_refusal_test.go`; modify `source_test.go` (glob rows), `source_tree_test.go` (`treeFS`).

**Interfaces:**
- Produces (package-internal): `var errUnreadable = errors.New("cannot be read: permission denied")`, `var errGlob = errors.New("holds a glob character (*, ? or [), which is not supported")`, `func (c *sourceCheck) readDir(name string, entries *int) ([]fs.DirEntry, error)`.

- [ ] **Step 1: Failing tests** in `source_refusal_test.go`.
  - `TestCheckSourcesNamesWhatItCannotCheck`: a `permFS` (wraps `refusingFS`; `Lstat` and `Stat` of `vendor/closed` and below return `&fs.PathError{Op: "lstat", Path: name, Err: fs.ErrPermission}`), `checkSources(t.Context(), clone, testCloneDir, "", plan{name: "requirements.yml", sources: []string{src}})`, rows:
    - `vendor/closed/r.tar.gz` → exactly `requirements.yml: the source "vendor/closed/r.tar.gz" cannot be read: permission denied`, `errors.Is(err, errUnreadable)`, `!errors.Is(err, errOutside)`.
    - `vendor/*` and `vendor/[ab]` → `requirements.yml: the source "vendor/*" holds a glob character (*, ? or [), which is not supported` (resp. `[ab]`), `errors.Is(err, errGlob)`, not `errOutside`.
    - `vendor/link/x` (refused by the clone's Stat, not a permission) still → `errOutside`.
  - `TestCheckSourceReadsADirectoryInBatches`: `hugeDirFS` (wraps `refusingFS` with `"big/.keep"`; `Open("big")` returns a generator dir whose `ReadDir(n)` hands out up to `n` entries named `f%07d` (regular files) of `total`, records every `n`, and `io.EOF` at the end). With `total = 10*listChunk` and `treeLimits{entries: 2*listChunk + 1, depth: 64, lookups: maxLookups}`: `source(testCloneDir, "", "big")` → `errors.Is(err, errTooManyEntries)`; every recorded `n == listChunk`; entries handed out ≤ `3*listChunk`. With `total = listChunk + 5` and `defaultLimits()`: nil.
- [ ] **Step 2:** `go test -race -count=1 -timeout 900s -run 'TestCheckSourcesNamesWhatItCannotCheck|TestCheckSourceReadsADirectoryInBatches' ./internal/worker/requirements/` — FAIL (errOutside instead of the new errors; the walk lists `big` through `fs.ReadDir` and gets `.keep`).
- [ ] **Step 3: Implement.**
  - `resolve`: the glob case returns `errGlob`; an `Lstat`/`Stat` error that `errors.Is(err, fs.ErrPermission)` returns `errUnreadable`, any other stays `errOutside`. `confirm` likewise. `source`'s doc comment names both.
  - `sourceError`: `errGlob` and `errUnreadable` join the `"%s: the source %q %w"` branch.
  - `walkTree` lists through `readDir`: `c.clone.Open(orDot(name))`, the file must be an `fs.ReadDirFile` (else `read <name>: not a directory`); `ReadDir(listChunk)` until `io.EOF`, adding each batch's length to `*entries` and returning `errTooManyEntries` as soon as it exceeds `c.lim.entries`; `context.Cause(c.ctx)` between batches; the file closed; entries sorted by name at the end (the walk's order stays). Read errors keep the `read <name>: %w` wrapping. The per-entry `entries++` check goes; the per-entry `c.spend()` stays (lookup counts unchanged). `listChunk` is `takeover_walk.go`'s.
  - Tests: the glob rows of `TestCheckSource` (`vendor/*`) and `TestCheckSourceFollowsLinksLikeTheKernel` (`vendor/in/*`) expect `errGlob` (their tables gain a `want error` field, `errOutside` by default for refused rows). `treeFS` overrides `Open` instead of `ReadDir`: for `col/unreadable` it returns a dir whose `ReadDir` fails with the same `&fs.PathError{Op: "readdir", Path: name, Err: errors.New("boom")}`, so `TestCheckSourcesMessages` keeps its text.
- [ ] **Step 4:** `go test -race -count=1 -timeout 900s ./internal/worker/requirements/` — PASS.
- [ ] **Step 5: Commit** `fix(requirements): name a permission error or a glob, read a large directory in batches`.

### Task 2: Mercurial refused, the `ns.class` ruling, galaxy cited by function name (items 6, 8, 9)

**Files:** Modify `internal/worker/requirements/shape.go`, `collection.go`, `source.go`, `source_tree.go` (comments only), `doc.go`. Tests: create `internal/worker/requirements/shape_scm_test.go`; modify `shape_collections_test.go`.

**Interfaces:**
- Produces (package-internal): `func (p *plan) refuseMercurial(src string) error` → `fmt.Errorf("%s: the source %q is a Mercurial source; Mercurial sources are not supported", p.name, src)`.

- [ ] **Step 1: Failing tests.**
  - `shape_scm_test.go` `TestParseFileRefusesMercurialSources`: `parseFile("requirements.yml", …)` returns exactly the message above for: `- hg+https://h/r` (src `hg+https://h/r`); `- "hg+./vendor/r,1.0,r"` (src `hg+./vendor/r`); `- {src: "hg+https://h/r", name: r}`; `- {src: "https://h/r", scm: hg, name: r}` (src `https://h/r`); `collections: ["hg+./vendor/c"]`; `collections: [{name: "hg+https://h/c", type: git}]`; `collections: [{name: ns.c, type: git, source: "hg+https://h/c"}]`. Accepted (nil): `- git+https://h/r`; `- {src: "git+https://h/r", scm: hg, name: r}` (galaxy takes the prefix, so git); `- {src: https://h/r, scm: git, name: r}`; `- acme.hg`.
  - `shape_collections_test.go` `TestAnUntypedKeywordNameIsCheckedAsAPath` (pins P1, expected to pass at once): `parseFile` of `collections: [ns.class]` is nil; `checkSources` on a `refusingFS` with `ns.class/galaxy.yml` and `ns.class/out` = `link("../../outside")` → `requirements.yml: the source "ns.class" holds the link "ns.class/out", which leads outside the repository`.
- [ ] **Step 2:** `go test -race -count=1 -timeout 900s -run 'Mercurial|UntypedKeyword' ./internal/worker/requirements/` — the Mercurial rows FAIL (accepted today), the pin PASSes.
- [ ] **Step 3: Implement.** The scm is computed as galaxy's `RoleRequirement.role_yaml_parse` does: a role string's src (after the comma split) up to its first `+`; a role mapping's `src` up to its first `+`, and only when `src` holds no `+`, its `scm:` value. `addRoleString` and `addRoleMapping` call `refuseMercurial` when that is exactly `hg`; `addCollections` when a collection string, `name:` or `source:` starts with `hg+`. Comments:
  - `checkUntypedName` gets P1's reasoning (keyword → path reading, both readings checked, function names), and `checkCollection` one sentence: git and subdirs names are refused although galaxy installs no collection under them (conservative, kept).
  - Every Python `file:line` citation in the package's non-test files becomes the enclosing function, looked up in the installed ansible-core 2.21.5 (`python -c 'import ansible, os; print(os.path.dirname(ansible.__file__))'` with galaxy's interpreter) and found by content where the line moved. For `collection.go`: `__init__.py:1500,1507-1508` → `ansible.galaxy.collection.install`; `_collection_finder.py:1058-1074` → `AnsibleCollectionRef.is_valid_collection_name`; `cli/galaxy.py:838-841` → `GalaxyCLI._init_coll_req_dict`; `dataclasses.py:330-483` and its `:338`…`:474` → `_ComputedReqKindsMixin.from_requirement_dict` (naming the step: `dict.get`, the valid-name branch, the pointer branch, the source swap); `:473-474` → `ConcreteArtifactsManager.get_direct_collection_fqcn`; `dataclasses.py:560-561` → `_ComputedReqKindsMixin.is_virtual`; `collection/__init__.py:738` → `install_collections`. `doc.go` gains: "Galaxy's behaviour is cited by function name, read against ansible-core 2.21.5."
- [ ] **Step 4:** `go test -race -count=1 -timeout 900s ./internal/worker/requirements/` — PASS. `grep -nE '\.py:[0-9]|[ (]:[0-9]{2,4}' internal/worker/requirements/*.go | grep -v _test.go` prints nothing.
- [ ] **Step 5: Commit** `fix(requirements): refuse Mercurial sources, cite galaxy by function name`.

### Task 3: One URL-userinfo redaction, and no galaxy line cut inside a word (items 10, 11)

**Files:** Create `internal/worker/urlcred/urlcred.go`, `internal/worker/urlcred/urlcred_test.go`. Modify `internal/worker/requirements/lines.go`, `ensure_install.go` (callers), `ensure.go` (`Galaxy.Install` doc), `doc.go`; `internal/worker/runner/gitrepo.go`, `galaxy.go`; `internal/worker/sync_requirements.go` (comment). Tests: `internal/worker/requirements/lines_test.go`, `internal/worker/runner/redact_test.go`, `galaxy_helper_test.go`.

**Interfaces:**
- Produces: `package urlcred`; `func Redact(s string) string`. Two rules, in order: `([A-Za-z][A-Za-z0-9+.-]*://)[^@/\s?#]+@` → `${1}[redacted]@` (the userinfo of any URL with a scheme), then the existing `[^@/\s:]+:[^@/\s]+@` → `[redacted]@` (user:password anywhere, scp-style included). A match never contains white space (RE2 `\s`: space, `\t`, `\n`, `\f`, `\r`), which the line splitter relies on.
- Produces: `const wordLeftOut = "… a word longer than 64 KiB left out"` and `func (w *lineWriter) tail() string` in `runner/galaxy.go`.

- [ ] **Step 1: Failing tests.**
  - `urlcred_test.go` `TestRedact` table: `https://ghp_abc123@github.com/o/r.git` → `https://[redacted]@github.com/o/r.git`; `git+https://tok@h/r` → `git+https://[redacted]@h/r`; `HTTPS://TOK@h/r` → `HTTPS://[redacted]@h/r`; `https://u:p@h/x` → `https://[redacted]@h/x`; `ssh://git@h/x` → `ssh://[redacted]@h/x`; `deploy:secret@host.example.org:repo` → `[redacted]@host.example.org:repo`; unchanged: `git@github.com:o/r.git`, `https://h/x?to=a@b`, `mailto:a@b.c`, `no url here`; `https://user@dom:pass@h/` → `https://[redacted]@[redacted]@h/`.
  - `lines_test.go` `TestEnsureRemovesABareTokenUsername`: galaxy line `cloning https://ghp_s3cret@git.example.org/r.git` reaches the log as `cloning https://[redacted]@git.example.org/r.git`; with `g.Err = &requirements.ExitError{Code: 1, Stderr: "fatal: https://ghp_s3cret@git.example.org/r.git not found"}` and with `g.Err = errors.New("clone https://ghp_s3cret@git.example.org/r.git: refused")` the error contains `https://[redacted]@git.example.org/r.git` and never `ghp_s3cret`.
  - `redact_test.go`: `TestRedactURLs` gains `"a token as a bare username"` (`fatal: unable to access 'https://ghp_tok@example.org/r.git/'` → `… 'https://[redacted]@example.org/r.git/'`); row "user-only userinfo stays" becomes `"an scp-style user stays"` with `fatal: could not read from git@github.com:org/repo.git` unchanged (P3).
  - `galaxy_helper_test.go`: `TestGalaxyLineWriterNeverCutsAWord`: `pre := strings.Repeat("y", maxGalaxyLine-8)`, `url := "https://user:tok3n@host/r.git"`, write `pre + " " + url + "\n"` (in one Write, and again byte by byte) → lines exactly `[pre, url]`. `TestGalaxyLineWriterLeavesOutALongWord`: write `strings.Repeat("z", maxGalaxyLine+5) + " end\n"`, in one Write and split across two → lines `[wordLeftOut, "end"]`. `TestGalaxyStderrTailStartsAtAWord`: `&lineWriter{send: …, keep: 8}` fed `"abc https://tok@h x\n"` keeps `"tok@h x\n"` and `tail()` is `"x\n"`; with `keep: 64` (never overflowed) `tail()` is the whole input. `TestGalaxyLineWriterBoundsWhatItHolds` now expects `[wordLeftOut, "abcd"]` (the over-long `x` word is left out).
- [ ] **Step 2:** run `go test -race -count=1 -timeout 900s` on `./internal/worker/urlcred/` (does not compile), and with `-run` on the new tests in `./internal/worker/requirements/` and `./internal/worker/runner/` — FAIL.
- [ ] **Step 3: Implement.**
  - `urlcred.Redact` per the Interfaces; its doc says why: a token can be a URL's bare username, and an scp-style `git@` user names no secret.
  - `requirements`: `urlUserinfo` and `removeUserinfo` go; `commandLines.line`, `stderrTail`, `withoutUserinfo` and `warn` call `urlcred.Redact`. `doc.go`'s import sentence adds "except the leaf package urlcred". `Galaxy.Install`'s doc adds: a line never splits a word (a run of non-white-space bytes), so the service can remove URL userinfo line by line.
  - `runner/gitrepo.go`: `urlCredPattern` goes; `redactURLs` replaces the repo URL first, then `urlcred.Redact`. Its comment states the scp-style rule.
  - `runner/galaxy.go` `lineWriter`: when `buf` reaches `maxGalaxyLine` without a newline, cut at the last white-space byte (` `, `\t`, `\f`, `\r`) in `buf[:maxGalaxyLine]`: send `buf[:i]`, drop the byte, keep the rest. No white space there: send `wordLeftOut` and drop bytes up to and including the next white-space byte or newline, across Writes (a `skipping` field). `kept` stays raw; a `cut` field is set when it overflowed; `tail()` returns `kept`, or after a cut its bytes after the first white-space byte (`""` without one). `Install` builds `ExitError{Stderr: stderr.tail()}`.
- [ ] **Step 4:** `go test -race -count=1 -timeout 900s ./internal/worker/urlcred/ ./internal/worker/requirements/ ./internal/worker/runner/` and `./internal/worker/` — PASS.
- [ ] **Step 5: Commit** `fix(worker): redact a token used as a bare URL username, never cut a galaxy line inside a word`.

### Task 4: A git step cancelled by its context ends cancelled (item 12)

**Files:** Modify `internal/worker/runner/gitrepo.go` (`runGitOut`). Test: create `internal/worker/runner/gitrepo_cancel_test.go`.

**Interfaces:** none new. `runGitOut`'s failure while `ctx.Err() != nil` is `fmt.Errorf("git %s stopped: %w", args[0], ctx.Err())`, checked before the `ExitError` branch (a git killed by the context is an `ExitError` with code -1 today; a context done before the start is a plain error formatted with `%v`).

- [ ] **Step 1: Failing test** `TestGitEndsWithItsContext`, subtests: `HeadCommit cancelled before git starts` (real git, cancelled context) → `errors.Is(err, context.Canceled)`, message starts `git rev-parse stopped: `; `HeadCommit cancelled while git runs` (`GitBin` = a script in `t.TempDir()` that touches a marker file, then `exec sleep 30`; cancel once the marker exists) → `errors.Is(err, context.Canceled)` within 10 s; `a fetch cancelled while git runs` (same script, `FetchRepoDir` on a fresh cache) → `errors.Is(err, context.Canceled)`.
- [ ] **Step 2:** `go test -race -count=1 -timeout 900s -run TestGitEndsWithItsContext ./internal/worker/runner/` — FAIL.
- [ ] **Step 3:** implement as in Interfaces; `HeadCommit`'s doc says a cancel ends it with the context's error, which `installRequirements` and the loop map to cancelled.
- [ ] **Step 4:** `go test -race -count=1 -timeout 900s ./internal/worker/runner/ ./internal/worker/` — PASS.
- [ ] **Step 5: Commit** `fix(runner): end a git step cancelled by its context as cancelled`.

### Task 5: The take-over's changed and destination branches (item 13)

**Files:** Modify `internal/worker/requirements/takeover.go` (seams). Test: create `internal/worker/requirements/takeover_changed_test.go` (`takeover_test.go` is at 395 lines).

**Interfaces:**
- `takeOverHooks` gains `beforeOpen func(p string)` (in `copyOne`, after a successful `Lstat` of `p`, before any open or readlink) and `afterRead func(p string)` (in `copyFile`, after the copy is closed, before the second `Stat`).

- [ ] **Step 1: Tests** (with `takeOverSource`, `takeOverWith(t.Context(), root, src, defaultTakeOverLimits(), hooks)`), each ending with `onlyNames(t, dir, src)`:
  - `TestTakeOverRefusesAnEntryThatChangesWhileItIsCopied`, rows on `roles/r/f` (and the directory `roles/r`): a file replaced after it was listed (`beforeOpen`: remove, write anew); a file hard-linked after it was listed (`beforeOpen`: `os.Link` it beside); a directory replaced after it was listed (`beforeOpen` on `roles/r`: rename it away, `Mkdir` anew); a file that grows while copied (`afterRead`: append); a file whose mtime changes while copied (`afterRead`: `os.Chtimes` +1h). Each → `errors.Is(err, errTakeOverChanged)` and the message `take over the install: <path>: changed while it was copied`.
  - `TestTakeOverNamesADestinationError`, rows for a file, a directory and a symlink: `beforeOpen(p)` creates `p` in the destination temp entry (the cache's `.tmp-*` other than `src`) → message starts `take over the install: <p>: `, `errors.Is(err, fs.ErrExist)`.
- [ ] **Step 2:** `go test -race -count=1 -timeout 900s -run 'ChangesWhileItIsCopied|DestinationError' ./internal/worker/requirements/` — does not compile (no seams).
- [ ] **Step 3:** add the two seams. Then mutate once to watch the tests bite: drop the `!os.SameFile(fi, st)` check in `copyFile` → the "replaced" row FAILs; restore.
- [ ] **Step 4:** `go test -race -count=1 -timeout 900s ./internal/worker/requirements/` — PASS.
- [ ] **Step 5: Commit** `test(requirements): cover the take-over's changed-while-copied and destination branches`.

### Task 6: One repository path cleaner and one set of tarball fixtures (items 15, 16)

**Files:** Create `internal/worker/repopath.go`, `internal/worker/repopath_test.go`, `internal/worker/requirements/requirementstest/tarball.go`. Modify `internal/worker/sync_requirements.go` (drop `cleanSubpath`), `internal/worker/runner/requirements.go` (drop `cleanRepoPath`), `internal/worker/requirements/requirementstest/fake.go` (package doc), `internal/worker/runner/requirements_real_test.go`, `internal/e2e/requirements_test.go`, `internal/e2e/uidboundary/requirements_test.go`.

**Interfaces:**
- Produces: `func CleanRepoPath(p string) string` in package `worker`: no leading `/`, `path.Clean`, `"."` as `""`; a path leading out stays so (callers refuse it).
- Produces (package `requirementstest`): `func TarGz(t testing.TB, files map[string]string) string`, `func RoleTarball(t testing.TB, name string, files map[string]string) string`, `func EvilCollectionTarball(t testing.TB, target string) string`, moved verbatim from `runner/requirements_real_test.go` (string results), their comments kept; the evil one's comment names the two tests that use it (the runner's control and the in-image boundary).

- [ ] **Step 1: Failing test** `TestCleanRepoPath` table: `""`→`""`, `"."`→`""`, `"/"`→`""`, `"ansible/"`→`"ansible"`, `"/ansible"`→`"ansible"`, `"a//b"`→`"a/b"`, `"./a/./b"`→`"a/b"`, `"../x"`→`"../x"`, `"a/../.."`→`".."`.
- [ ] **Step 2:** `go test -race -count=1 -timeout 900s -run TestCleanRepoPath ./internal/worker/` — does not compile.
- [ ] **Step 3:** add `CleanRepoPath`; both callers use it; the two private copies and their now-unused imports go. Move the three tarball builders; the runner, e2e and uidboundary tests import `requirementstest` and drop their copies (uidboundary's call sites lose their `string(...)` conversions). The e2e copy's "test packages do not share helpers" comment goes with it.
- [ ] **Step 4:** `go test -race -count=1 -timeout 900s ./internal/worker/ ./internal/worker/runner/ ./internal/worker/requirements/...` — PASS; `go vet ./internal/e2e/...` compiles both e2e packages; `grep -rn 'func tarGz\|func roleTarball\|func evilCollectionTarball\|func cleanSubpath\|func cleanRepoPath' internal` prints nothing.
- [ ] **Step 5: Commit** `refactor(worker): one repository path cleaner and one set of galaxy tarball fixtures`.

### Task 7: The in-image requirements tests, split and tightened (items 14, 17)

**Files:** In `internal/e2e/uidboundary/`: create `requirements_fixtures_test.go`, `requirements_escape_test.go`; modify `requirements_test.go`, `requirements_stop_test.go` (header comment).

**Interfaces:**
- Produces: `func assertPublishedInstall(t *testing.T, repoID string, result *workerpb.ReportResultRequest, events []*workerpb.RunEvent, logs string)`: today's assertions of `TestRequirementsInstallUnderThePlayUID` from the status check to the entry walk, with P8's exact modes in the walk. Task 8 consumes it.
- `requirementsWarnings(logs string) []string` also matches the stop path: a `level=WARN` line containing `requirements`, `galaxy`, `take-over`, `setuid helper` or `play supervisor` (the warnings of `signalGroup` and `stopGroup` in `runner/runner.go`).

- [ ] **Step 1:** pure moves first: `requirements_fixtures_test.go` takes `debugTask`, `vendoredCollection`, `installFixture`, `repoJob`, `orgCache`, `requirementsDir`, `cacheNames`, `requirementsWarnings`, `lastText`; `requirements_escape_test.go` takes `TestRequirementsCannotWriteOutsideTheirEntry` and the traversal half of the file comment; `requirements_test.go` keeps the install test and its comment. `go vet ./internal/e2e/...` clean.
- [ ] **Step 2:** extract `assertPublishedInstall`; `TestRequirementsInstallUnderThePlayUID` becomes `runOneJob` + that call. Tighten: in the walk, a directory must be exactly `0750`, a regular file `0640` or `0750`, any other type fails naming it, a symlink is only owner-checked; widen `requirementsWarnings` as above. Each file stays under ~300 lines.
- [ ] **Step 3:** `go vet ./internal/e2e/...` and `go test -count=1 -timeout 900s ./internal/e2e/uidboundary/` (every test skips outside the image) — clean. The in-image run of these assertions is Task 8's Step 2.
- [ ] **Step 4: Commit** `test(e2e): split the in-image requirements tests, check exact modes and stop-path warnings`.

### Task 8: The setuid helper floors the umask at 027 (7.92)

**Files:** Modify `deploy/worker/setuid-exec.c`, `internal/worker/runner/galaxy.go` (the `Install` doc gains Ruling 3: under a play uid the helper floors galaxy's umask so the take-over can read what galaxy wrote; without one galaxy runs as the worker and the question does not arise). In `internal/e2e/uidboundary/`: create `requirements_umask_test.go` (takes `TestRequirementsUnderAWorkerUmaskOf077` from `requirements_stop_test.go`, which keeps the cancel test).

**Interfaces:** Consumes `assertPublishedInstall` (Task 7) and `workerConfig(t).SetuidHelper`.

- [ ] **Step 1: Failing tests.**
  - `TestRequirementsUnderAWorkerUmaskOf077` flipped: umask 077 set and restored as today, `runOneJob` on the same job and repo ids, then `assertPublishedInstall(t, repoID, result, events, logs)`. Its comment states the evidence (galaxy inherits the worker's umask across helper and supervisor) and the fix (the helper's floor).
  - `TestSetuidHelperFloorsTheUmask`: for worker umasks `077`, `027`, `022`, `007`, `002` (set with `syscall.Umask`, restored), run `<helper> 65533 /bin/sh -c umask` → stdout `0027`, `0027`, `0022`, `0007`, `0002`.
- [ ] **Step 2: Watch them fail in the image, and Task 7's tightened checks pass.** `docker build -f deploy/worker/Dockerfile -t noixdev/pulsix-worker:dev .` on the unchanged helper, then `PULSIX_E2E_WORKER_IMAGE=noixdev/pulsix-worker:dev go test -count=1 -v ./internal/e2e -run TestPlaybookUIDBoundary -timeout 1800s` → FAIL with exactly: the flipped test (status error, `openat: permission denied`) and the `077` row of `TestSetuidHelperFloorsTheUmask` (prints `0077`); every other row and every other in-image test PASSes. A failure elsewhere (a mode, a stop-path warning) is a finding: report it, do not loosen.
- [ ] **Step 3: Implement** in the exec form, after the `signal_mode` branch and right before `execv` (`#include <sys/stat.h>`):
  ```c
  mode_t old = umask(0);
  umask(old & 027);
  ```
  The header gains a paragraph in its style: the exec form clears the group read and execute bits from the inherited mask (backlog 7.92); the worker reads what plays and galaxy write through the shared group (artifacts, the requirements take-over), and a worker started with umask 077 otherwise gets files it cannot open; `old & 027` never loosens what the worker asked for (022 and 002 stay); the signal form creates no files. A one-line comment at the call points to it. `umask` cannot fail, so there is nothing to check.
- [ ] **Step 4:** `cc -Wall -Wextra -Werror -O2 -DPLAY_UID=65533 -DWORKER_GID=65532 -o "$(mktemp -d)/h" deploy/worker/setuid-exec.c` clean; `go test -race -count=1 -timeout 900s -run TestSetuidHelper ./internal/worker/runner/` PASS; rebuild the image as in Step 2 and rerun `TestPlaybookUIDBoundary` → PASS, no `--- SKIP`. Record both runs' summary lines in the report.
- [ ] **Step 5: Commit** `fix(deploy): floor the play uid's umask at 027 in the setuid helper`.

### Task 9: Upgrade notes and backlog (docs)

**Files:** Modify `docs/upgrades.md`, `docs/BACKLOG.md`.

- [ ] **Step 1:** `docs/upgrades.md`, top of "Behaviour changes to check after an upgrade", three bullets:
  - **7.92, umask under the play uid:** plays and requirements installs under `PULSIX_WORKER_PLAY_UID` create files the worker's group can read even when the worker runs with umask 077: the setuid helper clears only the group read and execute bits of the worker's umask (077 becomes 027; 022 and 002 stay). Requirements installs that failed under umask 077 with `openat: permission denied` work after updating the worker image.
  - **7.97, Mercurial requirements sources:** a role whose source starts with `hg+` or that sets `scm: hg`, and a collection named or sourced `hg+…`, is refused before galaxy runs ("Mercurial sources are not supported"). The worker image never had `hg`; a worker installed elsewhere now refuses them too.
  - **7.97, cancelled during git:** a run or sync cancelled while the worker fetches the repository or reads its commit ends as cancelled instead of error.
- [ ] **Step 2:** `docs/BACKLOG.md`: row 7.92 → `done`, text: package `docs/superpowers/plans/2026-11-08-requirements-leftovers.md`, branch `fix/7.92-7.97-requirements-leftovers` (2026-11-08); the setuid helper sets `umask(old & 027)` before `execv`, the pinned test flipped to the install's assertions. Row 7.97 → `done`, text: one sentence per fixed group (checks, runtime, tests) as in Tasks 1–7, including P1's ruling (untyped `ns.class` read as a path by galaxy, both readings checked, kept) and P3–P5's consequences; then **Won't fix:** YAML 1.1 implicit types (differ from yaml.v3 only for non-strings, no escape route); hard link to the worker key (needs `protected_hardlinks=0`, the kernel default protects); `removeEntry`'s temp-name claim (race probability 2^-64). Anything the in-image run surfaced becomes a new `planned` row.
- [ ] **Step 3:** `git diff --check` clean; the two rows render as one table line each (`grep -cE '^\| 7\.(92|97) ' docs/BACKLOG.md` prints 2).
- [ ] **Step 4: Commit** `docs: record 7.92 and 7.97`.

---

## Self-review

- **Coverage:** Rulings 1–4 → Task 8 (helper, flipped test, the `Install` doc comment, image runs). 5, 7 → Task 1. 6, 8, 9 → Task 2. 10, 11 → Task 3. 12 → Task 4. 13 → Task 5. 15, 16 → Task 6. 14, 17 → Task 7. Won't fix and docs → Task 9.
- **Types:** `urlcred.Redact` (Task 3) is used by `requirements` and `runner`; `worker.CleanRepoPath` and the `requirementstest` builders (Task 6) by Task 7's files; `assertPublishedInstall` (Task 7) by Task 8; `listChunk` exists in `takeover_walk.go`.
- **Review Focus:** 1 in Task 8, 2 and 3 in Task 3, 4 in Task 1, 5 in Task 4.
