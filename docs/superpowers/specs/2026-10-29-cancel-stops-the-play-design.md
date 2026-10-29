# Making Cancel stop the play — design

**Backlog:** 2.107 (the defect), and the production half of 2.108.
**Date:** 2026-10-29
**Predecessor spec:** `docs/superpowers/specs/2026-10-28-cancel-across-the-uid-boundary-design.md`

## Intent, and why now

Cancel does not stop a playbook. It never has, on any worker, with or
without a play uid. The worker signals the process group of the
`ansible-runner` it started, and the play is not in that group:

- `ansible-runner` 2.4.3 starts `ansible-playbook` through `pexpect.spawn()`,
  which pty-forks it into a new session and process group. This is an
  unconditional property of attaching a pty and no `ansible-runner run`
  flag changes it.
- `ansible-core` 2.21.4's `WorkerProcess._detach()` forks one worker per
  host per task into yet another new process group.

The worker's `kill(-pgid, …)` reaches the leader, the leader dies, the job
reports `cancelled` within the 5 s grace, and the two groups below are
reparented and run to completion. Backlog 2.107 has the source locations
and the pgids observed in the failing run.

Backlog 2.108 adds the second half of the picture: the worker binary is PID 1
in its container (`deploy/compose/docker-compose.yml` ends the entrypoint in
`exec /pulsix-worker`, and the image's own `ENTRYPOINT` is the binary). Every
process a play orphans is reparented to it, and the worker waits only for the
`exec.Cmd` it started, so each one becomes a permanent zombie once it exits.

Why now: this is the last thing standing between the playbook uid separation
(2.42) and being able to say "Cancel works". The run view currently carries
an unconditional warning that the play may still be running against every
host, because it may.

## What an operator wants out of Cancel

Unchanged from the predecessor spec, which got it from the operator: people
cancel because something is visibly going wrong or because the run was
started by mistake, and both want it to stop promptly. Ansible has no
"finish the current task and start nothing new", and a command already
started on a target host keeps running when the SSH connection drops. So
Cancel means: **nothing of this run keeps running on the worker, and nothing
new is started on any host**. It cannot mean "the remote command was undone
or stopped", and the run view must keep saying so for the hosts where a
command was in flight.

## What the spike showed

A throwaway script inside the real worker image (`noixdev/pulsix-worker:dev`,
run with `--init` so the script was not PID 1) started
`ansible-runner run … --json` on a `command: sleep 300` play against
localhost.

- **With `prctl(PR_SET_CHILD_SUBREAPER)` set on the script:** after a `TERM`
  to the `ansible-runner` leader alone (what the worker does today), the
  leader and the pexpect child died, and the `WorkerProcess`
  `ansible-playbook` was reparented **to the script**, not to PID 1. The
  `sh → sh → python3 → sleep` chain stayed below it. One `TERM` to every
  process group among the script's descendants left no descendant 0.2 s
  later, and a reap left no zombie.
- **Without the subreaper, otherwise identical:** after the same leader-only
  `TERM`, the `sleep` was outside the script's process tree, reparented to
  init. A descendancy walk could not find it.

That is the whole design in one observation: a subreaper keeps the run's tree
closed, so "everything this run started" becomes a walk instead of a guess.

## Goals

1. A cancelled run leaves no process of its own running on the worker,
   whether or not `PULSIX_WORKER_PLAY_UID` is set.
2. It does so without being able to touch any process that is not a
   descendant of that run, including a concurrent job's play running under
   the same uid.
3. A run that ends on its own also leaves nothing behind. Anything a play
   started in the background on localhost is stopped when the run ends, the
   same way the run's work directory is removed.
4. Nothing a run started becomes a zombie of the worker (2.108, production
   half).
5. The job still reports `cancelled` promptly. Today that takes ~5 s; it may
   take longer only by the time the play's own processes need to die.
6. The run view tells the truth after the fix: the unconditional "it does
   not stop the play" warning goes, and the conditional warning about hosts
   with a command in flight returns.
7. `TestCancellingAPlayUnderThePlayUIDStopsIt` passes with its `t.Skip` line
   deleted and nothing else in it changed.

## Non-goals

- Stopping a command already running on a target host. The SSH connection
  drops and the remote process keeps going, as the predecessor spec
  established. The run view says so for the affected hosts.
- More than one concurrent job per worker. Goal 2 must hold if that ever
  changes, but this work does not add it.
- A longer or configurable grace. `defaultKillGrace` stays 5 s.
- Isolating the play with PID namespaces or cgroups. Both need privileges
  the worker container does not have and must not be given (see
  Constraints).

## Anti-goals

- **Finding the play by uid.** Every play on a worker is the same uid, so
  that would reach a concurrent job's play. The predecessor spec and 2.107
  both rule it out.
- **Growing the setuid helper.** `deploy/worker/setuid-exec.c` stays exactly
  what it is. Every line added to a setuid-root program has to be read as
  an attack surface, and nothing here needs root.
- **A cancel that reports success while something still runs.** If the
  supervisor below cannot account for the tree, the job must still report
  and the worker must log it. A silent "cancelled" is what this work
  removes.
- **Weakening the test that caught this.** Goal 7 means exactly one deleted
  line.

## Constraints

- No new privilege for the worker or the play: no `CAP_SYS_ADMIN`, no
  cgroup delegation, no user namespaces. Docker's default seccomp profile
  forbids most of that anyway. `PR_SET_CHILD_SUBREAPER` needs no privilege.
- Production is Linux (the image). Developers run `make test` on macOS. The
  logic must be unit-testable there, and the Linux mechanics must be
  proven inside the image, like the uid boundary.
- Linux ≥ 5.3 is assumed (pidfd). A host kernel older than that must make
  the supervisor refuse by name (see "Failures name themselves"), not fall
  back to signalling raw pids.
- The play-uid path keeps going through the setuid helper's exec form
  unchanged: `helper UID PROGRAM ARGS…`. The helper already execs whatever
  absolute `PROGRAM` the worker names, so nothing about it changes.
- `summarize`'s observable behaviour for runs that were not cancelled stays
  exactly as it is: exit codes 0/2/3/4 and the others map the same way, a
  run killed by a signal still reads as "terminated by signal", and the
  setuid helper's and the extra-vars guard's failures are still named.
- The event stream must still end when the run ends, and only then.
- Migrations: none. Proto: no change. API: no change.

## Considered and rejected

- **The worker walks the tree itself at cancel time** (snapshot the
  descendants of the leader, then signal every group found). The tree is
  intact before the first signal, so the snapshot would be complete. But
  anything forked during the grace in a new group, and everything once its
  parent dies, falls out of reach again. The pgids it remembered can be
  recycled. And it does nothing for 2.108.
- **The worker as subreaper** (`PR_SET_CHILD_SUBREAPER` on the worker, or
  simply being PID 1). Orphans of every job pool at one process, so which
  run they belong to is lost. Reaping them would also mean a `wait4(-1)`
  loop racing `exec.Cmd.Wait()` for the runner's own exit status.
- **`init: true` / tini alone.** That reaps zombies but keeps the play
  running and unfindable. It is still worth having as a backstop (below),
  not as the fix.
- **PID namespace or cgroup per run** (`cgroup.kill` would be ideal). It
  needs privileges that are not there (Constraints).
- **Configuring ansible-runner or ansible-core out of the nesting.** Neither
  split is configurable, as 2.107 records, and pinning internals of either
  would break on their next release.

## How it works

### A supervisor process per run

The worker no longer starts `ansible-runner` directly. It starts a small
**play supervisor**, and the supervisor starts `ansible-runner`:

```
worker ── [setuid helper, exec form] ── play supervisor ── ansible-runner ── …the play
```

With no play uid, the helper is not in the chain and the supervisor runs as
the worker's uid. With a play uid, the helper execs the supervisor in its
place, so the supervisor runs as the play uid, the same uid as everything
below it, and can signal its own descendants with no helper round trip.

**Where it lives:** the supervisor is a hidden mode of the worker binary
itself, re-executed via `os.Executable()`. The worker's `main` recognises a
reserved first argument (name left to the builder; it must not collide with
any existing flag or subcommand) **before** it reads any configuration or
environment, and hands off to the supervisor. My call, over a separate
`cmd/` binary: a separate binary needs its own Dockerfile line, its own
`PULSIX_WORKER_*` path variable and a defined behaviour for workers running
outside the image without it. The worker binary is always present wherever
a worker runs, and the image's `/pulsix-worker` is already executable by
the play uid (`-rwxr-xr-x root root`, checked in the `:dev` image). The supervisor takes nothing from its environment (the
worker's `subprocessEnv` is what reaches it, already stripped of
`PULSIX_*`). Its argv carries the grace and the runner command line, and the
runner path stays the absolute path `resolveRunnerBin` produces.

**What it does:**

1. Marks itself a child subreaper (`PR_SET_CHILD_SUBREAPER`). If that fails,
   it refuses to run the play and says why (see "Failures name
   themselves").
2. Starts `ansible-runner` with the inherited stdout and stderr, in its own
   process group (the one the worker created with `Setpgid`, so a worker
   `TERM` to that group still reaches the leader exactly as today). It then
   **drops its own copy of stdout** (points fd 1 at `/dev/null`), so the
   event stream's EOF depends only on the run's processes, not on the
   supervisor staying alive.
3. Reaps every child it is handed, its own and every orphan of the tree, so
   nothing of the run becomes a zombie.
4. **On `SIGTERM`** (a cancel, a broken event stream, a worker shutdown, the
   same three paths that call `stopGroup` today), it **stops the tree**:
   - `TERM` to every live descendant;
   - repeat for new descendants until none remain or the grace (passed by
     the worker, today 5 s) runs out;
   - `KILL` to every live descendant, repeated until none remain;
   - reap, then exit.
5. **When `ansible-runner` exits on its own**, it stops whatever descendants
   remain the same way (`TERM`, grace, `KILL`), reaps, then exits with
   `ansible-runner`'s own status (below). In the common case nothing
   remains and this costs one `/proc` walk.

"Descendant" means a process whose parent chain leads to the supervisor,
read from `/proc/<pid>/stat`. Because the supervisor is a subreaper, an
orphan stays in that chain, so the walk is complete by construction.

**Signalling by pidfd, not by pid or pgid.** Between reading a pid from
`/proc` and signalling it, the process can exit, be reaped by its own parent
(a non-supervisor parent deeper in the tree) and have its pid recycled by an
unrelated process of the same uid. Signalling a pgid has the same hole.
Each signal therefore goes through `pidfd_open` → re-check that the pid is
still a descendant → `pidfd_send_signal`. Once the pidfd is open, it names
that process and no other, so the re-check after opening is what closes the
race. Direct children of the supervisor cannot be recycled at all before the
supervisor reaps them, but the rule is the same for every pid.

**Exit status.** The supervisor's exit must read to `summarize` exactly as
`ansible-runner`'s would have:
- the runner's exit code is passed through unchanged;
- if the runner was killed by a signal, the supervisor resets that signal to
  its default disposition and raises it on itself, so `ProcessState` still
  reports a signal and `summarize` still says "terminated by signal".

On a cancel, the status comes from the worker's own `cancelledCh` as today,
not from the exit code.

**Failures name themselves.** A supervisor that cannot do its job (the prctl
refused, the runner could not be started, `/proc` unreadable) writes one line
to stderr with a fixed prefix of its own and exits non-zero without running
the play. `summarize` recognises that prefix the way `setuidHelperFailure`
recognises the helper's, so the job's error names the supervisor instead of
reading as "ansible-runner exited with code 1".

### The worker's side

- `newRunnerCmd` builds `supervisor --grace <killGrace> -- <abs runner> <args…>`
  in place of `<runner> <args…>`, with the helper prefix unchanged when a play
  uid is set. On a non-Linux worker it keeps today's direct spawn, since
  there is no subreaper there and production never runs there.
- `stopGroup` sends `TERM` as today, but **owns no escalation of its own
  any more**. It waits for `exited` up to the grace **plus a backstop
  margin** (my call: 10 s), and only then sends the unconditional `KILL` to
  the group. The supervisor does the play's `TERM → grace → KILL`. The
  worker's `KILL` exists only for a supervisor that is wedged, and when it
  fires it is logged at warn level naming the run, because a supervisor
  killed this way leaves its orphans to PID 1 again, which is the case
  `init: true` below catches.
- The supervisor's path is a `worker.Config` field, `PlaySupervisor`, that
  `worker.Load` fills with `os.Executable()` on Linux and leaves empty
  elsewhere. Empty means today's direct spawn. A `Config` built by hand, as
  most runner and e2e tests do, therefore keeps today's behaviour, and every
  process that goes through `Load` must recognise the reserved argument.
  That is `cmd/worker`, and also **`cmd/manager`**, because the embedded
  worker (`internal/app/embedded_worker.go`, `PULSIX_EMBEDDED_WORKER=1`)
  loads its config through `Load` inside the manager binary. It also
  includes the in-image test binary of `internal/e2e/uidboundary`, which
  gets a `TestMain` that dispatches.

### PID 1 (2.108)

`deploy/compose/docker-compose.yml`'s `worker` service gets `init: true`.
With the supervisor in place nothing of a play should reach PID 1 any more,
but other paths can still orphan into it (a repo sync's `git` killed by its
context leaves its `ssh` child, and a supervisor killed by the backstop
leaves its tree). Docker's init reaps those. It is one line, it changes
nothing for a deployment that does not use the compose file, and the admin
docs say to pass `--init` to a plain `docker run` (my call: documented, not
baked into the image's `ENTRYPOINT`, because the compose file overrides the
entrypoint and would silently bypass it).

### What the run view shows

Back to what the predecessor spec designed before 2.107 overturned it:

- The cancelled-run warning is shown **only when at least one host was
  interrupted** (`cancelRows.some(r => r.state === 'interrupted')`). Its copy
  names those hosts' situation: the play was stopped, but the command already
  started on an interrupted host may still be running **on that host**, so
  check it before re-running. It no longer says the play may still be
  running against every host.
- The "no task in flight" pill is no longer `var(--px-ok)`. A host with no
  task in flight may still have been only partly applied (task 3 of 10), and
  green reads as "fine". My call: the neutral `var(--px-unknown)` that
  "never started" already uses. The label stays.
- The component comment that explains the unconditional warning is replaced
  by one that explains the condition, and points here.

The exact copy is left to the builder within those three points.

### Records

In the same work, because each of these currently says Cancel does not stop
the play:

- `docs/BACKLOG.md`: close 2.107, close 2.108 (both halves), and update
  2.104's and 2.42's "what still gates it" text.
- `README.md` and `deploy/compose/.env.example`: the "known limitation
  (2.107)" lines go.
- The predecessor spec gets a one-line pointer from its "As built" note on
  the unconditional warning to this document.
- End-user docs (`NoiXdev/noix-docs`, branch `pulsix`):
  `user/playbooks-and-jobs.md`'s "Cancelling a run" says Cancel stops the
  run on the worker, says what it still cannot stop (a command already
  running on a host), and says what the run view shows. `admin/security.md`
  line ~41 is checked for the same claim. The admin page that documents
  running the worker container gains the `--init` note. No tech-stack names
  on those pages.

## Decisions I made

- The supervisor is a hidden mode of the worker binary, not a separate
  binary.
- A run that ends on its own also has its leftover descendants stopped
  (goal 3). This is a behaviour change for a play that deliberately leaves
  a background process running on localhost **inside the worker
  container**. That was never a supported use, and it was a way for one
  job to leave something running into the next.
- The worker's backstop `KILL` comes 10 s after the grace, and logs.
- Signals go through pidfds, not pids or pgids.
- `init: true` in compose, plus `--init` in the docs for plain `docker run`,
  rather than tini in the image.
- The "no task in flight" pill becomes neutral, not green.

## What is left to the builder

- The reserved argument's spelling and the supervisor's stderr prefix.
- The supervisor's internal structure. CLAUDE.md's rules apply: the policy
  (`TERM`, repeat, grace, `KILL`, repeat, reap, exit status) sits behind a
  small interface over the process table, signalling and reaping, with an
  injected clock. It is unit-tested with fakes on any OS, and the
  `/proc`/pidfd/prctl implementation is Linux-only behind build tags.
- How the reap loop wakes (`SIGCHLD` through `os/signal` is the obvious
  one). The supervisor must not use `exec.Cmd.Wait` for the runner, because
  its own `wait4(-1)` loop would race it. It starts the runner with
  `os.StartProcess`/`syscall.ForkExec` and learns the runner's status from
  that loop.
- Exactly how `runner` tests on Linux get a supervisor binary.
- The run-view copy, within the three points above.

## Risks and things to verify

- **The event stream.** The supervisor sits between the worker and the
  runner's stdout. Verify that a normal run's events arrive unchanged and in
  order, and that the stream ends promptly on a normal exit, a failure and a
  cancel. A supervisor that kept a stdout copy open would hang
  `streamEvents` until it exited.
- **ansible-runner's own SIGTERM handling** still runs, because the
  worker's `TERM` still reaches the leader's group. Check that nothing it does
  on `TERM` (artifact writes into the work dir) races the work-dir removal
  differently than today.
- **A fork storm during the stop.** The `KILL` loop has to converge. Every
  new process is a descendant and gets killed on the next pass, and nothing
  can leave the tree, but the loop needs an upper bound and must log if it
  hits it.
- **`removeAsPlayUID`** (`internal/worker/workdir.go`) assumed until now
  that a cancelled play might still be writing into the work dir.
  With the tree gone first, that assumption should be dead. Check that
  nothing in it now hides a real failure.
- **The in-image reaper in `cancel_test.go`** (`reapPlayUIDLeftovers`)
  becomes a no-op on a correct worker. It stays as a guard, since its
  failure now means a regression.

## Acceptance

1. In the image, with a play uid: cancelling a `sleep 300` play leaves no
   process of the play uid, **live or zombie**, within the grace plus one
   second, and the job reports `cancelled`.
   `TestCancellingAPlayUnderThePlayUIDStopsIt` passes with only its `t.Skip`
   line deleted.
2. In the image, **without** a play uid: the same, for the worker's own uid
   (a new in-image test; 2.107 is uid-independent and so is the proof).
3. In the image: a play whose localhost task starts a background process
   (`nohup sleep 300 &`) and then ends successfully leaves that process gone
   after the run, and the job reports `succeeded`.
4. In the image: a play that runs normally reports the same status, stats
   and events as before, including a failed run (exit 2), an unreachable
   host (exit 4) and the extra-vars guard's error.
5. Unit tests, on any OS, pin the supervisor's policy: `TERM` to all, repeat
   for new descendants, `KILL` after the grace, the exit-status mapping
   (code passthrough, signal re-raise) and the self-naming failure.
6. `summarize` recognises the supervisor's failure line, with a unit test.
7. The run view: the warning appears only with an interrupted host, and
   "no task in flight" is not green. Vitest covers both.
8. `deploy/compose/docker-compose.yml`'s worker has `init: true`.
9. `make test`, `golangci-lint run` (no new findings over the 26
   pre-existing errcheck findings golangci-lint 2.15.0 reports on `main`)
   and `gofmt -s -l .` pass. The SPA's ESLint and `tsc -b` pass.
10. The records listed under "Records" say what is now true.

## As built

Shipped on `fix/2.107-cancel-stops-the-play`. Everything the sections above
specify was built as written; what follows is every place the build chose or
differs, and how acceptance came out.

### What was left to the builder, and what was chosen

- **Reserved argument:** `__pulsix-play-supervisor`. **Stderr prefix of every
  supervisor line:** `pulsix-play-supervisor: `, and of a refusal
  `pulsix-play-supervisor: refused: ` (`supervisor.RefusedPrefix`). Argv is
  `<supervisor> __pulsix-play-supervisor --grace <duration> -- <absolute runner
  path> <runner args…>`; `parseArgs` is strict and refuses anything else.
- **Structure** (`internal/worker/supervisor`): `stop.go` holds the policy
  behind a `procTable`/`clock` pair (`stopTree`, tested with fakes on any OS);
  `run.go` the OS-neutral loop (`supervise`, `Main`); `system_linux.go` the
  `/proc`, `pidfd` and `prctl` implementation, `system_other.go` a stub that
  refuses. The loop does not use `exec.Cmd.Wait` for the runner: it starts it
  with `syscall.ForkExec` and learns its status from its own `wait4(-1)` loop,
  woken by `SIGCHLD` through `os/signal`.
- **Stop loop bounds:** poll 50 ms, at most 100 `KILL` passes, then
  `ErrTreeDidNotDrain` naming the survivors, reported on one prefixed stderr
  line while the runner's own status is still what the supervisor exits with.
- **Runner tests on Linux** build `./cmd/worker` (`supervised_linux_test.go`)
  and use it as the supervisor; hand-built `Config`s keep the direct spawn.

### Differences from the text above

- **A failing signal never ends the stop.** `stopTree` collects signal errors,
  carries on through the pass and into the `KILL` phase, and returns them
  joined with the outcome. A single unsignalable process must not spare its
  siblings (goal 1).
- **Unreadable `/proc` entries are skipped**, not fatal, and `Signal`'s
  descent re-check walks the target's ppid chain up to the supervisor's own
  pid instead of re-reading all of `/proc`.
- **The supervisor needs `pidfd_open`** (kernel 5.3 or newer) and probes it on
  startup; without it, it refuses and names why, like a failed `prctl`. The
  refusal names both causes: a kernel older than 5.3, or a seccomp profile
  that blocks `pidfd_open`/`pidfd_send_signal` (Docker's default profile
  before 20.10). The installation docs state the host requirement; probing
  at worker start, so an unsupported host shows at boot rather than at the
  first job, is backlog 2.112.
- **Re-raising a death signal** first sets `RLIMIT_CORE` to 0 so a runner that
  died of `SIGSEGV` or `SIGABRT` does not dump a second core, then resets the
  disposition and raises it through `rt_sigaction` + `tgkill`
  (`signal.Reset` would restore only the Go runtime's handler).
- **Two failure kinds, not one.** A refusal (nothing started) exits 1; a
  failure after the runner started (stdout could not be dropped, a stop pass
  did not drain) is reported and the runner's own status is still used. The
  two are told apart on the wire: a refusal's line starts with
  `pulsix-play-supervisor: refused: `, a report's only with
  `pulsix-play-supervisor: `. `supervisorFailure` matches only the refused
  form, so a real Ansible exit 1 beside a report keeps the generic
  "ansible-runner exited with code 1". The worker logs every prefixed line at
  warn level with `job_id` once the run has exited, on every path out of
  `Run` (a cancel, a broken event stream, a normal end), as
  `play supervisor reported a problem` (`logSupervisorReports`). This is what
  the anti-goal "the job must still report and the worker must log it" and
  the fork-storm risk's "must log if it hits it" ask for; before the final
  review the worker read these lines only for an exit it did not understand.
- **The descendant walk passes through zombies.** `descendantsFrom` puts `Z`
  and `X` entries into its parent → child map and leaves them out only of the
  result: a thread-group leader whose main thread exited while other threads
  run shows as `Z`, and its children are still its own, so stopping the walk
  there would hide them from the stop.
- **`worker.Load` fails** on Linux if `os.Executable()` fails, instead of
  quietly running unsupervised. A cancel that silently does not work is the
  failure this work exists to remove.
- **`stopGroup` takes the job id** (the spec's signature was open) so the
  backstop warning names the run: `play supervisor did not stop the run in
  time; killing its process group`, with `job_id`, `pgid`, `grace`, `backstop`.
- **The in-image test binary's `TestMain` dispatch** landed with the runner
  change rather than with the in-image tests: once the runner is supervised,
  the in-image test binary is the supervisor, so the e2e group could not pass
  without it.
- **`playUIDProcessExists`** (in-image helper) counts only live play-uid
  processes that are not the supervisor. With the supervisor running as the
  play uid, the old definition released the "play has started" wait on the
  supervisor itself. The post-cancel checks stay strict (`playUIDEntryExists`
  counts live and zombie entries).

- **The Add worker dialog's snippets carry the init too** (not in the spec):
  `workerSnippets` (`internal/api/worker_snippets.go`) now emits
  `docker run -d --init …` and `init: true` in the compose service, with a
  test beside it. Documenting `--init` as a manual step would have left every
  worker added through the GUI without one. This is a user-visible GUI change.
- **The embedded worker's container still has no init**: only the `worker`
  service got `init: true`, not `manager`. Recorded as backlog 2.111; the
  embedded worker is off by default and its play orphans are still reaped by
  the supervisor, so only the residual paths named under "PID 1" remain.

### Acceptance

1. **Met.** `TestCancellingAPlayUnderThePlayUIDStopsIt` passes with only its
   `t.Skip` line deleted (0.26 s). Its wait releases on `ansible-runner`, so it
   cancels before the `sleep` exists and is the weaker signal; the sleep-level
   proof is `TestACancelledPlayLeavesNoZombie` (waits for the `sleep`, then no
   play-uid `/proc` entry, live or zombie, within grace + 1 s; 0.52 s).
2. **Met.** `TestCancellingAPlayWithoutAPlayUIDStopsIt` (0.59 s). With the
   supervisor switched off, it and `TestACancelledPlayLeavesNoZombie` fail
   with the `sleep` still alive.
3. **Met.** `TestABackgroundProcessDoesNotOutliveItsRun` (0.70 s).
4. **Partly in the image.** `TestAFailingPlayStillReportsFailed` (0.41 s)
   covers a failed run in the image; exit codes 0, 2 and 4 are covered with
   the real supervisor by `TestSupervisedExitCodesPassThrough` (compared
   against the unsupervised run), and the extra-vars guard's error by
   `summarize`'s unit tests, which do not go through the supervisor. Neither
   has an in-image test.
5. **Met**, on any OS (`stop_test.go`, `run_test.go`, `main_test.go`), with
   the Linux-only real-process tests in `system_linux_test.go`.
6. **Met** (`supervisorFailure`, ahead of the extra-vars check in `summarize`;
   it names only a refusal, and reports from after the runner started are
   logged instead).
7. **Met** (`RunDetailPage.test.tsx`).
8. **Met.**
9. **Met.** `golangci-lint` reports only the 26 pre-existing findings; filed
   as backlog 2.110. The branch adds none, but one of them sits in a file it
   touches: `cmd/manager/main_test.go:80` (`<-returned` in a `select`, from
   2026-09), in the file the dispatch test was added to. A second, unrelated pre-existing
   failure surfaced when the runner tests ran as root in a container:
   backlog 2.109.
10. **Met**; see below.

### Risks, as checked

- **`removeAsPlayUID`** (`internal/worker/workdir.go`) hides nothing: each of
  its failures (listing the dir, finding `rm`, the helper's run, with its
  output) is returned and logged by `removeWorkDir` at warn level, and a work
  dir that still cannot be removed is logged at error level. Nothing in it
  depended on the play still running, so nothing changed.
- **`ansible-runner`'s `TERM`-time artifact writes vs the work-dir removal:**
  no race is left. The loop removes the work dir only after `Run` returns,
  `Run` returns only after `cmd.Wait` on the supervisor, and the supervisor
  exits only after its stop has drained and reaped the whole tree,
  `ansible-runner` included, so whatever `ansible-runner` writes on `TERM` is
  written before the removal starts. Observed in the image (final-review
  run, 11/11 pass): the two cancel tests that check the data dir
  (`TestCancellingAPlayUnderThePlayUIDStopsIt`,
  `TestCancellingAPlayWithoutAPlayUIDStopsIt`) find no surviving `job-*`
  dir, and none of the nine runs' worker logs carries a
  `could not remove the run's work dir` or
  `could not clear the play uid's leftovers` line.
- **The event stream**, **the fork storm** and **the in-image reaper**: as
  recorded under Acceptance (acceptance 3 and 4, `stopTree`'s bounds and
  `ErrTreeDidNotDrain`, `reapPlayUIDLeftovers` kept as a guard).

### Records, as done

- `docs/BACKLOG.md`: 2.104, 2.107 and 2.108 are `done` with what shipped and
  the measured numbers above; 2.42's "what still gates it" now says nothing
  does; 2.109 and 2.110 are new, and 2.111 records the embedded worker's
  missing init. The final review added 2.112 (probe pidfd support at worker
  start), 2.113 (a version-pinned worker keeps the old Cancel while the run
  view stops warning; a decision to make), 2.114 (the review's deferred
  minors) and 2.115 (the pre-existing malformed-event warning).
- `deploy/compose/docker-compose.yml`: `init: true` on `worker`, with a
  comment. `deploy/compose/.env.example` and `deploy/compose/README.md`: the
  limitation lines now say what is true (the top-level `README.md` never
  carried one, so it is unchanged).
- The predecessor spec carries a pointer from its unconditional-warning note.
- End-user documentation, `NoiXdev/noix-docs` branch `pulsix` (local commit):
  see the pulsix commit message for its hash. Pages changed are listed in
  the task report; none names the tech stack. The final review added
  noix-docs `93946df`: a "Worker host" section on the requirements page (a
  host kernel from late 2019 or newer, and a runtime that allows the system
  calls for it), a note in the cancel section that Cancel stops the play
  only on workers of this release or newer and that pinned worker images
  need updating after an upgrade, and a changelog line saying both.
