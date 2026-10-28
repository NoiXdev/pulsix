# Cancel Across the UID Boundary — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make *Cancel run* actually stop a playbook that runs under `PULSIX_WORKER_PLAY_UID`, and make a cancelled run say per host what it left unfinished.

**Architecture:** The worker cannot signal another uid's processes, so the signal goes through the existing setuid helper, which drops to the play uid *first* and signals *after* — holding exactly the play's own authority. The liveness probe that read `EPERM` as "already gone" is removed rather than patched: the worker learns the play ended from its own child's exit, and the group KILL is then unconditional. The per-host breakdown is derived in the SPA from events already stored plus the job's target list; no endpoint, no query, no migration.

**Tech Stack:** Go 1.27, C (static, `-Wall -Wextra -Werror`), React 19 + TypeScript + Vitest, Docker (`python:3.13-slim`).

**Spec:** `docs/superpowers/specs/2026-10-28-cancel-across-the-uid-boundary-design.md`

## Global Constraints

- Everything in the repository is English: code, comments, commit messages, identifiers. Chat is German.
- Conventional Commits, footer `Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>`.
- TDD: failing test first, then the minimal implementation. No feature without tests.
- Run the suite as `make test`, never a whole-module `go test ./...`. `golangci-lint run` and `gofmt -s -l .` must pass before every commit.
- **No new privilege.** The helper must never signal before the `setgroups` → `setgid` → `setuid` drop and its read-back. `CAP_SETUID` on the worker stays forbidden.
- `defaultKillGrace` stays `5 * time.Second` and stays a constant. No settings table, no new protocol field, no migration.
- The helper is installed `root:pulsix` mode **4750**; `PLAY_UID` and `WORKER_GID` are compiled in.
- The SPA has **no Prettier config** — ESLint and `tsc -b` are the only formatting gate. Never run `prettier --write`.
- Node lives at `$HOME/.nvm/versions/node/v24.14.0/bin`; add it to `PATH` before any `npm`/`npx`.

## Review Focus

1. **A cancel that lands after the play already exited.** `ESRCH` from the signal is the normal race, not a failure — it must not log as one or fail the run. (Task 2)
2. **`PULSIX_WORKER_PLAY_UID` unset.** The entire sequence must behave exactly as today: direct signals, same grace, same outcome. (Task 2)
3. **A cancelled run with no events at all** — cancelled before any task started. Every target reads "never started" and no warning appears. (Task 4)
4. **An event whose host is not in the job's target list** (the ansible host name differs from the host id). It is shown, never silently dropped. (Task 4)
5. **The helper asked to signal with euid ≠ 0.** It must refuse before sending anything — a signal that escapes without the drop is the anti-goal. (Task 1)

---

### Task 1: The setuid helper's signal mode

**Files:**
- Modify: `deploy/worker/setuid-exec.c`
- Create: `internal/worker/runner/setuidhelper_c_test.go`

**Interfaces:**
- Produces: the CLI contract `pulsix-setuid-exec UID --signal SIGNAL PGID`, where `SIGNAL` is exactly `TERM` or `KILL` and `PGID` is a positive integer greater than 1. Exit 0 on success **and on `ESRCH`**; exit 1 with a `pulsix-setuid-exec: ` line on stderr otherwise. The existing form `pulsix-setuid-exec UID PROGRAM [ARG ...]` is unchanged.

Order of operations in `main`, and it is load-bearing: argc → uid drift check → mode detection → (signal mode) signal-name and pgid validation → `geteuid() != 0` refusal → `setgroups`/`setgid`/`setuid` with read-back → `kill(-pgid, sig)`. Validation before the privilege check is what makes the contract testable without root; the drop before the `kill` is what keeps the authority at the play's level.

- [ ] **Step 1: Write the failing test**

`internal/worker/runner/setuidhelper_c_test.go`, package `runner_test`. One helper that compiles the C file once per test binary with `cc -Wall -Wextra -Werror -O2 -DPLAY_UID=65533 -DWORKER_GID=65532` — **deliberately without `-static`**, which is a property of the shipped image, not of the argument contract under test, and which the developer machine (darwin) cannot link. Skip the whole file when `exec.LookPath("cc")` fails or the compile fails on this platform, naming the reason in the skip message; CI is Linux and will not skip. Then `TestSetuidHelperSignalModeContract`, table-driven over argv and the stderr fragment each must produce:

```go
{name: "pgid 1 is refused", argv: []string{"65533", "--signal", "TERM", "1"}, want: "not a process group"},
{name: "pgid 0 is refused", argv: []string{"65533", "--signal", "TERM", "0"}, want: "not a process group"},
{name: "a negative pgid is refused", argv: []string{"65533", "--signal", "TERM", "-5"}, want: "not a process group"},
{name: "trailing junk is refused", argv: []string{"65533", "--signal", "TERM", "42x"}, want: "not a process group"},
{name: "an unsupported signal is refused", argv: []string{"65533", "--signal", "HUP", "4242"}, want: "unsupported signal"},
{name: "the uid drift check comes first", argv: []string{"65534", "--signal", "TERM", "4242"}, want: "this helper is built for uid 65533"},
{name: "no signal without the drop", argv: []string{"65533", "--signal", "TERM", "4242"}, want: "not setuid-root"},
```

Every case asserts exit status 1 and that stderr starts with `pulsix-setuid-exec: `. The last case is Review Focus 5: run as an ordinary user, the helper must reach the `geteuid` refusal — proving no `kill` is attempted before the drop.

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/worker/runner/ -run TestSetuidHelperSignalModeContract -count=1`
Expected: FAIL — the helper prints the usage line for an unknown argument instead of the per-case messages.

- [ ] **Step 3: Implement the signal mode in `deploy/worker/setuid-exec.c`**

Add `static int parse_pgid(const char *s, pid_t *out)` using the same whole-string strictness `argv[1]` already gets (`strtoul`, reject `errno`, empty parse, trailing characters and a leading `-`), additionally rejecting any value `<= 1`. `kill(-1, …)` means every process this uid may signal, so a lax parse turns one cancel into a fleet-wide kill of the play uid's processes — say that in the comment. Map `TERM`→`SIGTERM` and `KILL`→`SIGKILL` and nothing else. After the existing drop and read-back, call `kill(-pgid, sig)` and return 0 when it succeeds **or** fails with `ESRCH` (the group finished on its own between the worker's decision and this call — the common race, not an error).

Update the header comment: the usage line, the argument that the signal mode adds no authority because the drop precedes it, and the stale `mode 4755` (the Dockerfile installs **4750**).

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/worker/runner/ -run TestSetuidHelperSignalModeContract -count=1 -v`
Expected: PASS, every case.

- [ ] **Step 5: Verify the image still builds the helper**

Run: `docker build -f deploy/worker/Dockerfile -t noixdev/pulsix-worker:dev .`
Expected: the `cc -static … -Werror` stage succeeds. A warning is a finding here, not a nuisance.

- [ ] **Step 6: Commit**

```bash
git add deploy/worker/setuid-exec.c internal/worker/runner/setuidhelper_c_test.go
git commit -m "feat(worker): give the setuid helper a signal mode that drops privilege first"
```

---

### Task 2: The worker signals through the helper, and the liveness probe goes away

**Files:**
- Modify: `internal/worker/runner/runner.go` (`terminateGroup` and its two call sites, around lines 214-240 and 433-443)
- Test: `internal/worker/runner/runner_test.go`

**Interfaces:**
- Consumes: Task 1's CLI contract.
- Produces:
  - `func (r *Runner) signalGroup(env worker.RunEnv, pgid int, sig syscall.Signal) error` — direct `syscall.Kill(-pgid, sig)` when `env.Config.PlayUID == 0`, otherwise one run of `<helper> <uid> --signal <TERM|KILL> <pgid>`.
  - `func (r *Runner) stopGroup(env worker.RunEnv, pgid int, exited <-chan struct{})` — the TERM → grace → KILL policy.

`stopGroup` replaces `terminateGroup` at both call sites. Its sequence: send TERM; wait for `exited` or `r.killGrace()`, whichever comes first; then send KILL **unconditionally**. There is no liveness probe. The unconditional KILL is what today's probe was reaching for — the group can outlive its leader through orphaned grandchildren — and an extra KILL to an empty group is a harmless `ESRCH`.

`exited` is closed by a goroutine that owns `cmd.Wait()`:

```go
var waitErr error
exited := make(chan struct{})
go func() { waitErr = cmd.Wait(); close(exited) }()
```

The main path then blocks on `<-exited` where it used to call `cmd.Wait()`. The write-before-close / read-after-receive ordering is what makes `waitErr` race-free; `-race` proves it.

Two things `signalGroup` must do, both learned the hard way: give the helper child `stripPrivateEnv(os.Environ())` (backlog 2.105 — this child's `/proc/<pid>/environ` belongs to the play uid), and bound it with a context timeout so a wedged helper cannot hold the cancel. A non-zero exit is logged as a warning naming the helper's stderr line, never swallowed and never turned into the run's status.

- [ ] **Step 1: Write the failing tests**

In `internal/worker/runner/runner_test.go`:

`TestSignalGroupGoesThroughTheHelperWhenAPlayUIDIsSet` — a stand-in helper script records its argv; assert it was called as `<uid> --signal TERM <pgid>` with the pgid the caller passed and no minus sign, and that the recorded environment contains no `PULSIX_` or `ANSIBLE_` variable.

`TestSignalGroupSignalsDirectlyWithoutAPlayUID` — `PlayUID == 0`: the stand-in helper file must not exist afterwards, i.e. it was never invoked. (Review Focus 2.)

`TestStopGroupKillsTheGroupAfterTheGrace` — an `exited` channel that is never closed and a `KillGrace` of 50 ms; assert both a TERM and a KILL reached the stand-in, in that order.

`TestStopGroupStillKillsTheGroupAfterTheLeaderExited` — `exited` already closed; assert the KILL is still sent, and that `stopGroup` returned in well under the grace.

`TestStopGroupDoesNotFailOnAProcessThatIsAlreadyGone` — the stand-in exits 0 (Task 1's `ESRCH` contract); assert nothing is logged at error level. (Review Focus 1.)

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/worker/runner/ -run 'TestSignalGroup|TestStopGroup' -count=1`
Expected: FAIL — `signalGroup` and `stopGroup` are undefined.

- [ ] **Step 3: Implement `signalGroup` and `stopGroup`, and delete `terminateGroup`**

Rewrite the cancel goroutine and the `scanErr` path to call `stopGroup(env, cmd.Process.Pid, exited)`; `Setpgid` already makes the child its own group leader, so its pid is the pgid. Keep the comment explaining that the second signal is best-effort.

- [ ] **Step 4: Run the package under race**

Run: `go test ./internal/worker/runner/ -count=1 -race -timeout 900s`
Expected: PASS, no race report.

- [ ] **Step 5: Negative verification**

Restore the old `EPERM`-blind probe in place of the wait on `exited`, confirm `TestStopGroupKillsTheGroupAfterTheGrace` fails, then put the new code back. A test that passes both ways has not tested anything.

- [ ] **Step 6: Commit**

```bash
git add internal/worker/runner/runner.go internal/worker/runner/runner_test.go
git commit -m "fix(worker): cancel a play that runs under the play uid, and stop reading EPERM as gone"
```

---

### Task 3: The proof inside the image

**Files:**
- Create: `internal/e2e/uidboundary/cancel_test.go`
- Modify: `internal/e2e/playbook_uid_test.go` only if the new test needs a fixture the driver must mount

**Interfaces:**
- Consumes: Tasks 1 and 2.

This is the only test that can see the defect. Unit tests use a stand-in helper that execs *without* dropping uid, which is structurally blind to exactly the half that fails — that blindness is why 2.42 shipped broken. Follow the package's existing shape (`inImage(t)`, the real `worker.Loop` with the real `runner.Runner` against the fake manager, the image's own constants).

- [ ] **Step 1: Write the failing test**

`TestCancellingAPlayUnderThePlayUIDStopsIt`: enqueue a job whose playbook runs a long `sleep` against `localhost`; poll `/proc/*/status` until at least one process with real uid 65533 exists — **assert that it did**, so the test cannot pass by the play never starting; cancel the run; then poll for up to 30 s and assert no process with real uid 65533 remains, and that the job's final status is `cancelled`.

- [ ] **Step 2: Run it to verify it fails against a tree without the fix**

A red run here is the whole point of the task, and Tasks 1-2 are already in the tree, so take them out of the image for one run:

```bash
git stash push deploy/worker/setuid-exec.c internal/worker/runner/runner.go
docker build -f deploy/worker/Dockerfile -t noixdev/pulsix-worker:dev .
go test ./internal/e2e/ -run TestPlaybookUIDBoundary -count=1 -timeout 1800s   # expect FAIL
git stash pop
```

Expected: FAIL — the sleep survives the cancel. If it passes here, the test is not seeing the defect and nothing below means anything.

- [ ] **Step 3: Rebuild with Tasks 1-2 in place and run again**

Run: `docker build -f deploy/worker/Dockerfile -t noixdev/pulsix-worker:dev . && go test ./internal/e2e/ -run TestPlaybookUIDBoundary -count=1 -timeout 1800s`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/e2e/uidboundary/cancel_test.go
git commit -m "test(e2e): prove a cancel crosses the play uid boundary inside the image"
```

---

### Task 4: What a cancelled run says it left unfinished

**Files:**
- Modify: `web/src/features/runs/status.ts`
- Modify: `web/src/features/runs/RunDetailPage.tsx` (beside the existing `rows`/per-host table, around lines 48 and 190)
- Test: `web/src/features/runs/status.test.ts`, `web/src/features/runs/RunDetailPage.test.tsx`

**Interfaces:**
- Produces:

```ts
export type CancelHostState = 'finished' | 'interrupted' | 'never-started';
export interface CancelHostRow { host: string; state: CancelHostState; task: string }
export function cancelHostRows(
  targets: string[],
  events: { host: string; task: string; state: string }[],
): CancelHostRow[];
```

Rules, from the spec: a host's step opens on state `task` and closes on `ok`, `changed`, `failed`, `skipped` or `unreachable` (`internal/worker/runner/events.go`'s mapping). A host whose last event is `task` is `interrupted` and carries that event's task name; a host with a closing last event is `finished`; a target that no event mentions is `never-started` with an empty task. Events with an empty host are ignored. A host the events mention that is not in `targets` is included rather than dropped — the events carry the ansible host name, which is the host id only for targets the claim names (the wrinkle `hostResultRows` already lives with). Sorted by host, `localeCompare`, like `hostResultRows`.

- [ ] **Step 1: Write the failing tests**

In `status.test.ts`, covering: a host that finished; a host whose last event is `task` (interrupted, task named); a target with no events (`never-started`, Review Focus 3 in its empty-events form); an event host absent from `targets` (included, Review Focus 4); an empty-host event ignored; `changed`/`skipped`/`unreachable` all counting as closing; ordering.

In `RunDetailPage.test.tsx`: a cancelled job with one interrupted host renders the warning and the three states; a cancelled job whose events are empty renders every target as never started and **no** warning; a succeeded job renders neither.

- [ ] **Step 2: Run them to verify they fail**

Run: `export PATH="$HOME/.nvm/versions/node/v24.14.0/bin:$PATH" && cd web && npx vitest run src/features/runs`
Expected: FAIL — `cancelHostRows` is not exported.

- [ ] **Step 3: Implement `cancelHostRows` in `status.ts`**

- [ ] **Step 4: Render it in `RunDetailPage.tsx`**

Only when the run's effective status is `cancelled`: a `Card` listing each host with its state, and the task name on the interrupted ones. Above the list, only when at least one row is `interrupted`, a warning in the page's existing voice saying Pulsix ended the connection, the command on that host may still be running, and its state should be checked before a re-run. Give the list an `aria-label` the way the per-host table does. The warning's condition is the anti-goal made concrete: a banner on every cancelled run is a banner nobody reads.

- [ ] **Step 5: Run the SPA gates**

Run: `npx vitest run src/features/runs && npx tsc -b && npx eslint src`
Expected: all three clean.

- [ ] **Step 6: Commit**

```bash
git add web/src/features/runs
git commit -m "feat(web): say per host what a cancelled run left unfinished"
```

---

### Task 5: Close the row, and the note that gates 2.42

**Files:**
- Modify: `docs/BACKLOG.md` (rows 2.104 and 2.42)
- Modify: `docs/superpowers/specs/2026-10-28-cancel-across-the-uid-boundary-design.md` (an "As built" section)
- Check, and modify only if it claims something now false: `~/_dev/noix-docs/src/content/docs/pulsix/admin/security.md` on branch `pulsix`

- [ ] **Step 1: Run the full gate**

Run: `make test` and `golangci-lint run` and `gofmt -s -l .`
Expected: green, 0 issues, no output.

- [ ] **Step 2: Write the spec's "As built" section**

What shipped, what the measurements were, and anything decided during execution that the spec did not anticipate — in the style of this repository's other specs.

- [ ] **Step 3: Update the backlog**

2.104 → `done`, naming the two defects it fixed (the uid boundary and the `EPERM`-blind probe) and the test that proves it. 2.42's "Done does not mean safe to enable" note: 2.105 and 2.104 are both closed now, so say what remains, which is nothing — while leaving the image's deliberate decision not to set `PULSIX_WORKER_PLAY_UID` exactly as it is, since that is 2.42's own reasoning about existing deployments and not this row's to overturn.

- [ ] **Step 4: Commit**

```bash
git add docs/
git commit -m "docs(backlog): close 2.104 and lift the gate on enabling playbook uid separation"
```

---

## Self-review notes

- **Spec coverage.** Goal 1 → Tasks 1-3. Goal 2 → Task 2. Goal 3 → Task 4. Goal 4 → Task 5. Every non-goal is a thing no task does; the "no configurable grace" one is enforced by `defaultKillGrace` appearing in no task's files.
- **Not covered by any task, on purpose:** the uncapped event seed the spec names as a risk. It is a property of `Service.Events` today, not something this change introduces, and Task 4's code carries a comment pointing at it.
- **Type consistency.** `cancelHostRows(targets, events)` is the only new SPA export and is used once. `signalGroup`/`stopGroup` are named identically in Tasks 2 and 3.
