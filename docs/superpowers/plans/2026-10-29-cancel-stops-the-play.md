# Making Cancel stop the play — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A cancelled (or finished) playbook run leaves no process of its own on the worker, live or zombie, with or without a play uid (backlog 2.107, production half of 2.108).

**Architecture:** A per-run *play supervisor* (a hidden mode of the worker binary, re-executed through `os.Executable()`) sits between the worker (or the setuid helper) and `ansible-runner`. It marks itself `PR_SET_CHILD_SUBREAPER`, so nothing of the run can leave its process tree. On `SIGTERM` or when the runner exits, it stops every descendant (`TERM`, then `KILL` after the grace, through pidfds) and reaps everything. The worker's own `KILL` becomes a backstop. The run view goes back to warning only about hosts with a command in flight.

**Tech Stack:** Go 1.27 (`golang.org/x/sys/unix` for prctl, pidfd, wait4), React/TypeScript + Vitest, Docker compose.

**Spec:** `docs/superpowers/specs/2026-10-29-cancel-stops-the-play-design.md`

## Global Constraints

- Reserved first argument: `__pulsix-play-supervisor`. Stderr prefix of every supervisor line: `pulsix-play-supervisor: `.
- Supervisor argv: `<supervisor> __pulsix-play-supervisor --grace <duration> -- <absolute runner path> <runner args…>`.
- Grace = the runner's `killGrace()` (`defaultKillGrace` 5 s). Worker backstop after the grace: **10 s** (`defaultSupervisorBackstop`).
- Linux only. On every other OS `worker.Load` leaves `Config.PlaySupervisor` empty and the runner spawns directly, exactly as today.
- The supervisor reads nothing from its environment. The environment it receives is the runner's (`subprocessEnv`, already stripped of `PULSIX_*`).
- Signals to descendants go only through `pidfd_open` → re-check descendancy → `pidfd_send_signal`. Never `kill(pid)`, never `kill(-pgid)` from the supervisor.
- `deploy/worker/setuid-exec.c` is not touched.
- `summarize`'s mapping for uncancelled runs is unchanged. A runner killed by a signal still yields `ProcessState.ExitCode() < 0` at the worker.
- No migrations, no proto change, no API change.
- Gates before every commit: `make test`, `golangci-lint run` (no findings beyond the 26 pre-existing errcheck findings on `main`; none in touched files), `gofmt -s -l .` empty. SPA: `npx eslint .` and `npx tsc -b` in `web/` (node from `$HOME/.nvm/versions/node/v24.14.0/bin`; never run prettier).
- Public docs (noix-docs) name no tech stack (no "ansible-runner", "pidfd", "Docker", "Linux" in user pages; "container" and "worker" are fine).
- Commits: Conventional Commits, footer `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. Commit only when the user asked for this plan to be executed.

## Review Focus

1. **A runner that ignores `TERM`** (the existing "sticky" fake does `trap '' TERM`). The supervisor must still `KILL` it after the grace, and the job must still report `cancelled`. Pinned in Task 4's Linux test.
2. **A descendant that starts its own session and double-forks** (`setsid sh -c 'sleep 300 &'`). This is the 2.107 shape without ansible. It must be gone after a cancel. Pinned in Task 4's Linux test.
3. **A cancel that arrives before the runner has started, or after it has already exited.** The supervisor must not hang and must not signal anything outside its tree. Pinned in Task 2's loop tests.
4. **The event stream on a normal run.** The supervisor must not keep stdout open, or `streamEvents` hangs until the supervisor exits. Pinned in Task 4's Linux test (normal run completes and its events arrive).
5. **Exit status passthrough** for codes 0/2/4 and for a signal death. Otherwise `summarize` misreads failed and unreachable runs. Pinned in Task 1 and Task 4.

---

### Task 1: Supervisor contract and stop policy (any OS)

**Files:**
- Create: `internal/worker/supervisor/supervisor.go`: `Arg`, `ErrPrefix`, `Command`, `parseArgs`, `exitFor`
- Create: `internal/worker/supervisor/stop.go`: `procTable`, `clock`, `stopTree`, `ErrTreeDidNotDrain`
- Test: `internal/worker/supervisor/supervisor_test.go`, `internal/worker/supervisor/stop_test.go`

**Interfaces:**
- Produces:
  - `const Arg = "__pulsix-play-supervisor"`
  - `const ErrPrefix = "pulsix-play-supervisor: "`
  - `func Command(grace time.Duration, bin string, args []string) []string` returns `[Arg, "--grace", grace.String(), "--", bin, args…]`
  - `func parseArgs(args []string) (grace time.Duration, argv []string, err error)`: `args` is everything after `Arg`. It requires `--grace <positive duration>`, `--`, and at least one argv element, which must be an absolute path.
  - `func exitFor(ws syscall.WaitStatus) (code int, sig syscall.Signal)` returns `(ws.ExitStatus(), 0)` if the process exited and `(0, ws.Signal())` if it was signalled.
  - `type procTable interface { Descendants() ([]int, error); Signal(pid int, sig syscall.Signal) error }`. `Descendants` returns only live (non-zombie) descendants. `Signal` returns nil for a pid that is gone or no longer a descendant.
  - `type clock interface { Now() time.Time; Sleep(time.Duration) }`
  - `var ErrTreeDidNotDrain = errors.New("descendants survived the kill passes")`
  - `func stopTree(pt procTable, clk clock, grace time.Duration, poll time.Duration, maxKillPasses int) error`

- [ ] **Step 1: Write the failing tests**

`supervisor_test.go`, table-driven:
- `TestCommandRoundTrips`: `parseArgs(Command(5*time.Second, "/usr/local/bin/ansible-runner", []string{"run", "/w", "--json"})[1:])` returns grace `5s` and argv `["/usr/local/bin/ansible-runner","run","/w","--json"]`.
- `TestParseArgsRefuses`: each of these returns an error: no `--grace`; `--grace 0s`; `--grace -1s`; `--grace x`; missing `--`; nothing after `--`; relative program `ansible-runner`; unknown flag `--foo`.
- `TestExitFor`: exit 0 → (0,0); exit 2 → (2,0); exit 4 → (4,0); signalled `SIGKILL` → (0,SIGKILL); signalled `SIGTERM` → (0,SIGTERM). Build `WaitStatus` values by hand (`syscall.WaitStatus(code<<8)` for an exit, `syscall.WaitStatus(sig)` for a signal; this encoding is the same on Linux and Darwin).

`stop_test.go`: a `fakeTable` holding a set of live pids. Each pid has a scripted reaction per signal: dies on TERM, ignores TERM, or spawns a new pid when it receives TERM. A `fakeClock` advances only through `Sleep`. Tests:
- `TestStopTreeEmptyReturnsAtOnce`: no descendants → nil, zero `Sleep` calls, zero signals.
- `TestStopTreeTermsEveryDescendantOnce`: pids 10,11,12 all die on TERM → each gets exactly one `SIGTERM`, no `SIGKILL`, returns nil.
- `TestStopTreeTermsLateForks`: pid 10, on TERM, spawns 20 and dies → 20 also gets one `SIGTERM`.
- `TestStopTreeKillsAfterGrace`: pid 10 ignores TERM → one `SIGTERM`, then once the fake clock has passed `grace` one `SIGKILL`, and nil. Assert that the `SIGKILL` was not sent before `grace` had elapsed.
- `TestStopTreeGivesUp`: pid 10 ignores everything → `errors.Is(err, ErrTreeDidNotDrain)` after exactly `maxKillPasses` `SIGKILL`s.
- `TestStopTreeDescendantsErrorIsReturned`: `Descendants` errors → the error is wrapped and returned.

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./internal/worker/supervisor/ -timeout 300s`
Expected: build failure, undefined `Command`, `parseArgs`, `exitFor`, `stopTree`.

- [ ] **Step 3: Implement**

`stopTree` algorithm:
- TERM phase: repeat `Descendants`, then `SIGTERM` each pid not yet TERMed, until `Descendants` is empty (return nil) or `clk.Now()` passes `start+grace`. Sleep `poll` between passes.
- KILL phase: up to `maxKillPasses` times, `Descendants`, return nil if empty, else `SIGKILL` all and sleep `poll`.
- If the KILL phase runs out, return `ErrTreeDidNotDrain`, wrapped with the pids that are left.

Package doc comment in `supervisor.go`: what the supervisor is, why it exists (2.107, 2.108), and a pointer to the spec.

- [ ] **Step 4: Run them to see them pass**

Run: `go test ./internal/worker/supervisor/ -race -timeout 300s`
Expected: `ok`.

- [ ] **Step 5: Commit**

`feat(worker): add the play supervisor's argv contract and stop policy`

---

### Task 2: The supervise loop and its Linux system (prctl, /proc, pidfd, wait4)

**Files:**
- Create: `internal/worker/supervisor/run.go`: `system`, `supervise`, `Main` (OS-neutral)
- Create: `internal/worker/supervisor/system_linux.go`: `linuxSystem` (`//go:build linux`)
- Create: `internal/worker/supervisor/system_other.go`: `newSystem` returning an error on non-Linux (`//go:build !linux`)
- Test: `internal/worker/supervisor/run_test.go` (fakes, any OS), `internal/worker/supervisor/system_linux_test.go` (`//go:build linux`)

**Interfaces:**
- Consumes: Task 1's `parseArgs`, `exitFor`, `stopTree`, `procTable`, `clock`, `ErrPrefix`.
- Produces:
  - `func Main(args []string) int`: `args` is everything after `Arg`. It returns the code to exit with. If the runner died of a signal, it resets that signal's disposition and raises it on itself first, and returns `128+sig` only if it is still alive afterwards.
  - `type reaped struct { Pid int; Status syscall.WaitStatus }`
  - `type system interface { procTable; SetSubreaper() error; Start(argv []string) (pid int, err error); DropStdout() error; Reap() ([]reaped, error) }`. `Reap` is non-blocking: it calls `wait4(-1, WNOHANG)` until nothing more is reapable.
  - `func supervise(sys system, clk clock, events <-chan os.Signal, grace time.Duration, argv []string) (syscall.WaitStatus, error)`
  - `func newSystem() (system, error)`

- [ ] **Step 1: Write the failing loop tests (`run_test.go`, any OS)**

A `fakeSystem` scripts `Start`, `Reap` and `Descendants`. `events` is a channel the test feeds with `syscall.SIGCHLD`/`syscall.SIGTERM`. Tests:
- `TestSuperviseSetsSubreaperBeforeStart`: a call-order log shows `SetSubreaper`, then `Start`, then `DropStdout`.
- `TestSuperviseRefusesWithoutSubreaper`: `SetSubreaper` errors → `Start` is never called and the error is returned.
- `TestSuperviseReturnsRunnerStatus`: the runner pid is reaped with exit 2 and no descendants are left → the returned status has `ExitStatus()==2` and no signal was sent.
- `TestSuperviseSweepsAfterNormalExit`: the runner is reaped with exit 0 but a descendant 30 is still live → 30 gets `SIGTERM`, then the status (exit 0) is returned after 30 is gone.
- `TestSuperviseStopsTreeOnTerm`: a `SIGTERM` event arrives while the runner and two descendants are live → all of them get `SIGTERM`, and the runner's reaped status is what is returned.
- `TestSuperviseTermBeforeAnythingElse`: `SIGTERM` is already queued when the loop starts → it returns promptly with the runner's status and makes no `Sleep` beyond one grace.
- `TestSuperviseReapsOrphans`: `Reap` returns pids other than the runner's → they are consumed and do not end the loop.

- [ ] **Step 2: Run to see them fail**

Run: `go test ./internal/worker/supervisor/ -timeout 300s`
Expected: undefined `supervise`, `system`, `reaped`.

- [ ] **Step 3: Implement `supervise` and `Main`**

`supervise`: `SetSubreaper` → `Start(argv)` → `DropStdout` → loop on `events`:
- On `SIGCHLD`, call `Reap` and record the runner's status if it is among the results.
- On `SIGTERM`, or once the runner's status is known, call `stopTree(sys, clk, grace, 50*time.Millisecond, 100)` and then `Reap` until the runner's status is known.

A `stopTree` error is written to stderr with `ErrPrefix` and does not change the returned status. A started runner always yields a status, never only an error.

`Main`:
- `parseArgs`, `newSystem`, `signal.Notify(events, SIGCHLD, SIGTERM)`, `supervise`, `exitFor`.
- Every failure writes one line `ErrPrefix + reason` to stderr and returns 1.

- [ ] **Step 4: Write the failing Linux tests (`system_linux_test.go`)**

These run against real processes and need no image.
- `TestLinuxSystemFindsOrphanedGrandchild`: `SetSubreaper`, then `Start([]string{"/bin/sh","-c","setsid sh -c 'sleep 300 & ' ; sleep 300"})`. After 500 ms, `Descendants` contains a pid whose `/proc/<pid>/cmdline` is `sleep 300` and whose session differs from the supervisor's.
- `TestLinuxSystemSignalRefusesForeignPid`: `Signal(os.Getppid(), 0)` returns nil and does not signal (the parent is not a descendant). Then `Signal` on a started child with signal 0 returns nil.
- `TestLinuxSystemReapLeavesNoZombie`: start `/bin/true`, wait, `Reap` → its pid is reported with exit 0, and `/proc/<pid>` no longer exists.
- `TestLinuxSystemDescendantsSkipsZombies`: a child that has exited but not been reaped is not in `Descendants`.

Note that these tests make the *test process* a subreaper. That is harmless for the test binary and is the point of the test.

- [ ] **Step 5: Implement `linuxSystem`**

- `SetSubreaper`: `unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0)`.
- `Start`: `syscall.ForkExec` with the inherited fds 0/1/2 and the same env; no `Setpgid`, so the runner stays in the supervisor's group, which the worker's `TERM` reaches.
- `DropStdout`: open `/dev/null` and `unix.Dup3` it onto fd 1.
- `Descendants`: one pass over `/proc/*/stat` building ppid→children, then a walk from `os.Getpid()`, skipping state `Z` and `X`. Parse the stat fields after the last `)`.
- `Signal`: `unix.PidfdOpen(pid, 0)` (ESRCH → nil), re-check that pid is still a descendant (fresh walk), `unix.PidfdSendSignal`, close the fd.
- `Reap`: loop `unix.Wait4(-1, &ws, unix.WNOHANG, nil)` until it returns 0 or ECHILD.
- `newSystem` (Linux) probes `unix.PidfdOpen(os.Getpid(), 0)` once. `ENOSYS` (a kernel older than 5.3) returns an error naming the missing pidfd support, so `Main` refuses by name and never falls back to raw pids. Pin this with `TestNewSystemNamesMissingPidfd` against an injected open func.
- `system_other.go`: `newSystem` returns `errors.New("the play supervisor needs Linux")`.

- [ ] **Step 6: Run everything**

Run: `go test ./internal/worker/supervisor/ -race -timeout 300s`, then on Linux:
`docker run --rm -v "$PWD":/src -w /src golang:1.27.1-alpine go test ./internal/worker/supervisor/ -count=1 -timeout 300s`
Expected: `ok` both times (the second one runs the `_linux` tests).

- [ ] **Step 7: Commit**

`feat(worker): supervise a run as a child subreaper and stop its whole tree`

---

### Task 3: Entry points and `Config.PlaySupervisor`

**Files:**
- Modify: `cmd/worker/main.go` (`main`, new `entry`)
- Modify: `cmd/manager/main.go:66` (`dispatch`)
- Modify: `internal/worker/config.go` (`Config.PlaySupervisor`, `Load`)
- Test: `cmd/worker/main_test.go`, `cmd/manager/main_test.go`, `internal/worker/config_test.go`

**Interfaces:**
- Consumes: `supervisor.Arg`, `supervisor.Main`.
- Produces:
  - `worker.Config.PlaySupervisor string`. `Load` sets it to `os.Executable()` when `runtime.GOOS == "linux"` and leaves it empty otherwise. There is no environment variable for it.
  - `cmd/worker`: `func entry(args, env []string) int` routes `args[1] == supervisor.Arg` to `supervisor.Main(args[2:])` and everything else to `run(env)`; `main` calls `os.Exit(entry(os.Args, os.Environ()))`. The check happens before anything reads config or env.
  - `cmd/manager`: `dispatch` gains `case args[0] == supervisor.Arg: return supervisor.Main(args[1:])`.

- [ ] **Step 1: Write the failing tests**
- `cmd/worker` `TestEntryRoutesSupervisorArg`: `entry([]string{"/pulsix-worker", supervisor.Arg}, nil)` returns 1. That is `Main`'s refusal of the missing `--grace`; it never reaches `run`, which with a nil env would fail config loading with exit 2.
- `cmd/manager`: the same through `dispatch([]string{supervisor.Arg}, …)`. It must return 1, not the unknown-command exit 2.
- `internal/worker` `TestLoadPlaySupervisor`: on Linux `cfg.PlaySupervisor` equals `os.Executable()` and is absolute; on other OSes it is empty. Branch on `runtime.GOOS` in the test.

- [ ] **Step 2: Run to see them fail.** `go test ./cmd/worker/ ./cmd/manager/ ./internal/worker/ -run 'Entry|Dispatch|PlaySupervisor' -timeout 900s`, expected FAIL.
- [ ] **Step 3: Implement.** Doc comments: `PlaySupervisor`'s says why `Load` and not a hand-built `Config` sets it (spec, "The worker's side"), and that the manager has to dispatch because of the embedded worker.
- [ ] **Step 4: Run to see them pass.** The same command, expected `ok`.
- [ ] **Step 5: Commit.** `feat(worker,manager): recognise the play supervisor's reserved argument`

---

### Task 4: Run ansible-runner under the supervisor

**Files:**
- Modify: `internal/worker/runner/runner.go`: `newRunnerCmd` (~line 344), `stopGroup` (~line 561), `summarize`, `Runner` (new `SupervisorBackstop`), new `defaultSupervisorBackstop`, new `supervisorFailure`
- Test: `internal/worker/runner/runner_test.go` (argv, backstop, summarize), new `internal/worker/runner/supervised_linux_test.go` (`//go:build linux`)

**Interfaces:**
- Consumes: `supervisor.Command`, `supervisor.ErrPrefix`, `worker.Config.PlaySupervisor`.
- Produces:
  - `func newRunnerCmd(bin string, args []string, env worker.RunEnv, subprocessEnv []string, grace time.Duration) (*exec.Cmd, error)`.
    - With `PlaySupervisor` set: the runner path goes through `resolveRunnerBin`, and the command is `PlaySupervisor supervisor.Command(grace, abs, args)…`.
    - With `PlayUID` set as well: `helper uid PlaySupervisor <that argv>`.
    - Without `PlaySupervisor`: exactly today's command.
  - `const defaultSupervisorBackstop = 10 * time.Second`; `Runner.SupervisorBackstop time.Duration` (zero means the default).
  - `stopGroup` keeps its signature. When `env.Config.PlaySupervisor != ""` it waits `killGrace()+backstop` before the `KILL`. If that wait timed out rather than seeing `exited`, it logs at warn level `"play supervisor did not stop the run in time; killing its process group"` with `pgid`, `grace` and `backstop`.
  - `func supervisorFailure(stderr string) string`. If the first stderr line starts with `supervisor.ErrPrefix`, it returns that line trimmed. `summarize` checks it after `setuidHelperFailure` and before `extraVarsLoadFailure`.

- [ ] **Step 1: Write the failing tests (`runner_test.go`, any OS)**
- `TestNewRunnerCmdSupervised` (table): {no supervisor, no uid} → `[bin args…]` unchanged; {supervisor `/sup`, no uid} → `/sup __pulsix-play-supervisor --grace 5s -- <abs bin> args…`; {supervisor, uid 65533} → `<helper> 65533 /sup __pulsix-play-supervisor --grace 5s -- <abs bin> args…`. `Setpgid` stays true in all three. Update the existing call at `runner_test.go:764` to pass a grace.
- `TestStopGroupWaitsForTheSupervisorBackstop`: `Config.PlaySupervisor` is set, `KillGrace` 20 ms, `SupervisorBackstop` 80 ms, and `exited` never closes. It returns after at least 100 ms, and the log contains `play supervisor did not stop the run in time`.
- `TestStopGroupSupervisedReturnsOnExit`: the same setup, with `exited` closed after 10 ms. It returns in well under 100 ms and logs no warning.
- `TestSummarizeNamesTheSupervisor`: rc 1 with stderr `pulsix-play-supervisor: set child subreaper: operation not permitted\n` → `StatusError` and that line as the reason. rc 1 with stderr from ansible itself keeps the generic `ansible-runner exited with code 1`.

- [ ] **Step 2: Run to see them fail.** `go test ./internal/worker/runner/ -run 'Supervis|NewRunnerCmd' -timeout 900s`, expected FAIL.
- [ ] **Step 3: Implement.** Update `newRunnerCmd`'s and `stopGroup`'s doc comments: the supervisor owns `TERM → grace → KILL` of the play, and the worker's `KILL` is a backstop for a wedged supervisor (spec, "The worker's side").
- [ ] **Step 4: Run to see them pass.** The same command, expected `ok`.
- [ ] **Step 5: Write the failing Linux end-to-end runner tests (`supervised_linux_test.go`)**

`TestMain` is not needed. The test builds `./cmd/worker` with `go build -o <tmp>/pulsix-worker` once (via `sync.Once`) and sets `Config.PlaySupervisor` to it. It uses the package's existing `writeFakeRunner` plus two new modes on the fake:
- `FAKE_RUNNER_ESCAPE` set → `setsid sh -c 'exec sleep 300' &`, then emit events and stick.
- `FAKE_RUNNER_DAEMON` set → `setsid sh -c 'exec sleep 301' &`, then emit the fixture and exit 0.

Tests:
- `TestSupervisedCancelStopsAnEscapedGrandchild`: start a run with `FAKE_RUNNER_ESCAPE` and cancel it after its first event. Status is `cancelled`. Within `KillGrace+1s` no process whose cmdline is `sleep 300` exists, live or zombie.
- `TestSupervisedCancelKillsAStickyRunner`: `FAKE_RUNNER_STICKY` with `KillGrace` 200 ms → `cancelled` within 2 s, and the fake's pid is gone.
- `TestSupervisedNormalRunStopsItsDaemon`: `FAKE_RUNNER_DAEMON` with the success fixture → `succeeded` with the same stats as the unsupervised run of that fixture, and no `sleep 301` is left afterwards.
- `TestSupervisedExitCodesPassThrough`: `FAKE_RUNNER_EXIT` set to 2 and to 4, each with a failed or unreachable fixture → `failed`, as unsupervised.

- [ ] **Step 6: Run them in a Linux container.**
`docker run --rm -v "$PWD":/src -w /src golang:1.27.1-alpine go test ./internal/worker/runner/ -run Supervised -count=1 -timeout 900s`
Expected: `ok`. (If alpine's busybox lacks `setsid`, the fake falls back to `nohup`. Check before deciding the test is wrong.)
- [ ] **Step 7: Commit.** `feat(worker): run ansible-runner under the play supervisor`

---

### Task 5: Prove it in the image, un-skip 2.107's test

**Files:**
- Create: `internal/e2e/uidboundary/main_test.go`: `TestMain` that dispatches `supervisor.Arg`
- Create: `internal/e2e/uidboundary/cancel_nouid_test.go`
- Modify: `internal/e2e/uidboundary/cancel_test.go`: delete **only** the `t.Skip(...)` line of `TestCancellingAPlayUnderThePlayUIDStopsIt`, and update the file and test doc comments that describe it as skipped
- Modify: `internal/e2e/uidboundary/proc_test.go`: add `playUIDEntryExists(t) bool` (any `/proc` entry of the play uid, zombies included)
- Modify: `internal/e2e/playbook_uid_test.go:72-84`: the comment about the differently worded skip goes

**Interfaces:**
- Consumes: `supervisor.Arg`, `supervisor.Main`; the existing helpers `inImage`, `writeWorkerKey`, `workerConfig`, `localhostJob`, `fakeManager`, `waitUntil`, `playUIDProcessExists`, `debugPlayUIDProcesses`.
- Produces: `func processWithCmdline(t *testing.T, want string) (pid int, state string, ok bool)` in `proc_test.go`. It matches the `/proc/<pid>/cmdline` with NULs joined by spaces and trimmed.

- [ ] **Step 1: Write the failing tests**

`TestMain`: if `len(os.Args) > 1 && os.Args[1] == supervisor.Arg`, then `os.Exit(supervisor.Main(os.Args[2:]))`; otherwise `os.Exit(m.Run())`.

`cancel_nouid_test.go`. Its config is `workerConfig(t)` with `PlayUID` set to 0, so the play runs as the worker's own uid 65532:
- `TestCancellingAPlayWithoutAPlayUIDStopsIt`: playbook `command: sleep 303`. Wait until `processWithCmdline(t, "sleep 303")` is live, then cancel. Status is `cancelled`, and within `5s+1s` `processWithCmdline` reports nothing, live or zombie.
- `TestABackgroundProcessDoesNotOutliveItsRun`: playbook `shell: nohup sleep 304 >/dev/null 2>&1 &` with a play uid configured (default `workerConfig`). Status is `succeeded`, and afterwards no `sleep 304` exists.
- `TestAFailingPlayStillReportsFailed`: playbook `ansible.builtin.fail: msg=x` → `failed`.

In `cancel_test.go`, after `StopsIt`'s existing closing assertion, nothing is added. The zombie-free check lives in a new test so that `StopsIt` stays unmodified. Add `TestACancelledPlayLeavesNoZombie`, which repeats the play-uid cancel and asserts `!playUIDEntryExists(t)` within the grace plus 1 s.

- [ ] **Step 2: Rebuild the image and run, expecting failures only from missing code**
```
docker build -f deploy/worker/Dockerfile -t noixdev/pulsix-worker:dev .
go test ./internal/e2e -run TestPlaybookUIDBoundary -v -count=1 -timeout 1800s
```
Expected: if Tasks 1–4 are in, everything passes, `StopsIt` included. Look at the output for `--- PASS: TestCancellingAPlayUnderThePlayUIDStopsIt` and for no `--- SKIP` line at all. If anything fails, fix the code, not the test.

- [ ] **Step 3: Run `make test`** (the e2e group includes this). Expected exit 0.
- [ ] **Step 4: Commit.** `test(e2e): prove a cancel stops the play, with and without a play uid; un-skip 2.107`

---

### Task 6: The run view tells the truth again

**Files:**
- Modify: `web/src/features/runs/RunDetailPage.tsx:24-33,242-266`
- Test: `web/src/features/runs/RunDetailPage.test.tsx:245-340`

**Interfaces:**
- Consumes: `cancelHostRows`, `CancelHostState` (unchanged).

- [ ] **Step 1: Change the tests first**
- `renders the warning and all three host states for a cancelled run with an interrupted host`: still renders the warning. It now matches `/may still be running on that host/` (the new copy) instead of `/it does not stop the play/`.
- `warns on a cancelled run even when every target never started` becomes `does not warn on a cancelled run with no interrupted host`: `queryByRole('alert')` is null and the host list still renders.
- New: `does not colour "no task in flight" as ok`: the pill's `style.color` is `var(--px-unknown)`.

- [ ] **Step 2: Run to see them fail.** In `web/`: `npx vitest run src/features/runs/RunDetailPage.test.tsx`, expected FAIL.
- [ ] **Step 3: Implement**
- Wrap the alert in `cancelRows.some((r) => r.state === 'interrupted')`.
- Set `CANCEL_STATE_COLOR.finished` to `'var(--px-unknown)'`.
- New copy, exact: `The run was stopped. On a host marked interrupted, a command had already started there and may still be running on that host — stopping the run does not stop a command already running on a host. Check those hosts before you re-run.`
- Replace the block comment above the alert with the reason for the condition, pointing to this spec and 2.107.
- [ ] **Step 4: Run** the vitest file plus `npx eslint .` and `npx tsc -b`. Expected: all pass.
- [ ] **Step 5: Commit.** `fix(web): warn about a cancelled run only where a command was in flight`

---

### Task 7: PID 1, records and user docs

**Files:**
- Modify: `deploy/compose/docker-compose.yml` (`worker:` service: `init: true`, with a comment naming 2.108)
- Modify: `deploy/compose/.env.example:135-136`, `README.md` (the 2.107 limitation lines)
- Modify: `docs/BACKLOG.md`: rows 2.107 and 2.108 become `done` with what shipped, and the "what still gates it" text in 2.104 and 2.42 is updated
- Modify: `docs/superpowers/specs/2026-10-28-cancel-across-the-uid-boundary-design.md` (one-line pointer from the unconditional-warning note to the new spec)
- Modify: `docs/superpowers/specs/2026-10-29-cancel-stops-the-play-design.md` (an "As built" section with every deviation from the spec)
- Modify (noix-docs, branch `pulsix`, in a **separate worktree** so the user's current checkout `docs/nfc-advanced-server-settings` stays untouched: `git -C ~/_dev/noix-docs worktree add <scratch>/noix-docs-pulsix pulsix`): `src/content/docs/pulsix/user/playbooks-and-jobs.md` ("Cancelling a run"), `src/content/docs/pulsix/admin/security.md` (~line 41, check), and the admin page that documents running the worker container (add `--init` for a plain container run)

- [ ] **Step 1: Compose.** Add `init: true` to the `worker` service. Verify with `docker compose -f deploy/compose/docker-compose.yml config --quiet` (exit 0).
- [ ] **Step 2: Records in this repo** as listed. The BACKLOG rows say what shipped, with file paths, and repeat the acceptance numbers measured in Task 5.
- [ ] **Step 3: User docs.**
  - "Cancelling a run" says Cancel stops the run on the worker and starts nothing new on any host.
  - It says that a command already running on a host may continue there.
  - It says that the run view marks those hosts as interrupted, and to check them before re-running.
  - No tech-stack names. Commit on `pulsix` in noix-docs with `docs(pulsix): …`, and mention the docs commit in the pulsix commit message.
- [ ] **Step 4: Gates.** `make test`, `golangci-lint run`, `gofmt -s -l .` as in Global Constraints.
- [ ] **Step 5: Commit.** `docs: close 2.107 and 2.108; cancel stops the play` (the compose change goes in the same commit with a `build:` note in the body, or as a separate `build(compose): run the worker under an init` commit).
