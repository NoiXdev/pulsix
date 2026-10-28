# Cancelling a run that is not the worker's own uid — design

**Backlog:** 2.104 (the defect), and the half of 2.42 it gates.
**Date:** 2026-10-28
**Predecessor spec:** `docs/superpowers/specs/2026-10-26-playbook-uid-separation-design.md`

## Intent, and why now

Backlog 2.42 made the worker able to run a playbook under a second, lower
uid, so that a play task targeting `localhost` cannot read the worker's
client certificate and key. Its whole-feature review then found that the
feature, with the switch on, breaks cancellation: the worker runs as uid
65532 with no effective capabilities and may not signal a process running
as 65533, so `kill` is refused. An operator presses *Cancel run*, the job
reports `cancelled`, the run view says cancelled — and the play runs to
completion against the fleet.

That is why `PULSIX_WORKER_PLAY_UID` is not set in the shipped image and
why 2.42, though done, is marked *not safe to enable*. This design is what
makes it safe to enable.

The same code path is how a broken event stream stops a run, so today an
unobservable run also keeps going, holding the worker's concurrency slot
until it finishes by itself.

### The second defect, which bites regardless of the uid

`terminateGroup` (`internal/worker/runner/runner.go`) sends `SIGTERM` to
the process group, then polls `syscall.Kill(-pid, 0)` and treats **any**
error as "the group is gone", returning early. With a play uid set that
error is `EPERM` — permission denied, i.e. the group is very much alive —
so the function returns and the `SIGKILL` after the grace is never even
attempted. Both halves of the TERM→KILL policy fail, and the second one
fails for a reason that has nothing to do with the uid: a liveness probe
that cannot distinguish "gone" from "not allowed to ask" is wrong on its
own terms.

## What an operator wants out of Cancel

From the conversation that produced this document. People cancel for two
reasons: something is visibly going wrong, or the run was started by
mistake. Both want it to stop promptly. Neither wants Pulsix to wait
politely first.

An earlier idea in the backlog — a long, operator-configurable grace so
the current task could "finish cleanly" — was dropped once it became clear
it buys nothing:

- Ansible has no "finish the current task, start nothing new, then stop".
  `SIGTERM` aborts the play; there is no graceful drain to wait for.
- During any such grace the command already started on the target host
  keeps running anyway, because dropping the SSH connection does not kill
  the remote process.

So the grace would not protect the host; it would only delay the operator.
What replaces it is honesty after the fact (below). The existing policy —
`SIGTERM`, a short grace, then `SIGKILL` — is kept unchanged, including
the 5-second `defaultKillGrace`.

## Goals

1. **Cancel actually stops the play**, with `PULSIX_WORKER_PLAY_UID` set or
   unset, by the same TERM→KILL policy in both cases.
2. **`terminateGroup` stops reading `EPERM` as "already gone"**, so the
   `SIGKILL` after the grace is reached whenever the group is still alive.
3. **A cancelled run says what it left unfinished.** The run view shows,
   per host: finished, interrupted mid-task (naming the task that was
   running), or never started. When at least one host was interrupted, an
   explicit warning says Pulsix only dropped the connection and the command
   on that host may still be running, so its state should be checked before
   a re-run.
4. **`PULSIX_WORKER_PLAY_UID` becomes safe to enable** — the remaining
   blocker on 2.42 after 2.105 is this row.

## Non-goals

- **No intervention on the target host.** Pulsix does not try to kill the
  remote command, does not open a second connection, does not run a
  cleanup play. The operator's decision, made explicitly: name the
  situation, do not act on it.
- **No long or configurable grace period.** See above. `defaultKillGrace`
  stays 5 seconds and stays a constant; no settings table, no new
  protocol field.
- **The per-host breakdown is for cancellation only.** A run that stops
  because the worker restarted or the event stream broke does not get it.
  The operator's call: the breakdown answers "I pressed the button, what
  did I interrupt", and that question only arises when somebody pressed
  the button.
- **No change to what `cancelled` means** to the manager, the job state
  machine, or the API. The existing `cancelling` → `cancelled` transition
  is untouched.

## Anti-goals

Things that would make this a failure even if cancellation worked:

- **Any new privilege.** If the fix leaves a root process alive for the
  duration of a play, or gives the worker a capability it does not have
  today, it has traded a correctness bug for a weaker trust boundary.
  2.42's own anti-goal (no `CAP_SETUID` on the worker, because a process
  holding it can `setuid(0)`) applies unchanged here.
- **A warning nobody reads.** The interrupted-host warning must not appear
  on runs that interrupted nothing. A banner that is almost always
  unnecessary gets skipped, and then it is missing on the one run where it
  matters. Hence goal 3's condition.
- **A fix that only the test can see.** The existing boundary test's
  stand-in helper execs *without* dropping uid, so it proves a signal
  crosses an `execv` and is structurally blind to the half that fails. A
  test of the new behaviour that shares that blindness has not tested
  anything.

## Constraints

- Everything in the repository is English; the chat is German. Conventional
  Commits, footer `Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>`.
- Tests first. Go ≥ 80 % coverage, SPA ≥ 70 %. Run the suite as
  `make test`, never a whole-module `go test ./...`.
- Migrations are additive only. **This design needs none** — see "How", the
  breakdown is derived from events that are already stored.
- The worker has no database connection. Anything it needs arrives over
  gRPC in the claim, or from its environment.
- `deploy/worker/setuid-exec.c` is a setuid-root program. It is statically
  linked, built with `-Wall -Wextra -Werror`, installed `root:pulsix` mode
  **4750**, and its two uids and the gid are compiled in. Every change to
  it is a change to the only privileged surface in the worker.

## How it works

### The signal has to come from the play's own uid

A process may signal another only when its real or saved uid matches the
target's. The play's processes are uid 65533 through and through; the
worker is 65532. No ordering or flag on the worker's side changes that.

What already exists is a program that can become 65533: the setuid helper.
It drops privilege and `execv`s whatever the worker names. So the
authority to signal the play is reachable through the helper, and the
question is only what it runs on the other side of the drop.

**Decision (mine, not the operator's): the helper gains a second mode that
signals a process group, and nothing else.**

```
pulsix-setuid-exec UID PROGRAM [ARG ...]        # unchanged
pulsix-setuid-exec UID --signal SIGNAL PGID     # new
```

The ordering is the whole safety argument: the helper performs the
existing `setgroups` → `setgid` → `setuid` drop, with the same read-back
of the kernel's own answer, and signals **after** it. At the moment it
calls `kill(2)` it holds exactly the authority a play holds and nothing
more; root was given up several lines earlier. It can therefore reach the
play's processes and, by the kernel's own permission check, nothing that
is not uid 65533. The compiled-in drift check on `UID` stays in force in
both modes.

Three limits on the new mode, none of them negotiable:

- **Only `TERM` and `KILL`.** Those are the two signals the policy sends.
  A general signal relay is a larger surface for no gain.
- **`PGID` must be greater than 1**, parsed with the same whole-string
  strictness `UID` already gets. `kill(-1, …)` means *every process this
  uid may signal*; a parse that let `1`, `0` or a negative through would
  turn a cancel into a fleet-wide kill of the play uid's processes.
  The worker always passes a positive pgid and the helper negates it
  itself, so the caller never writes the minus sign.
- **Silence on success, a named failure otherwise**, exactly like the exec
  path: a refusal has to reach the operator rather than disappear into
  "exited with code 1". `setuidHelperFailure` (`runner.go`) is the
  existing mechanism.

With `PULSIX_WORKER_PLAY_UID` unset the helper is not involved at all: the
worker owns the processes and signals them directly, as it does today.
Both paths must run the same TERM→grace→KILL sequence, so the difference
is confined to how the signal is delivered.

Two alternatives were considered and rejected:

- **Run `kill(1)` through the existing helper**, changing no privileged
  code at all. This was the preferred answer and was checked rather than
  assumed: the worker image is `python:3.13-slim`, which ships **no `kill`
  binary** — neither `/bin/kill` nor `/usr/bin/kill` — only the shell
  builtin. Reaching it would mean either adding `procps` to a deliberately
  small image or exec'ing `/bin/sh -c`, and this design line has avoided
  shells on purpose (`removeAsPlayUID`'s own comment: "no glob, so nothing
  depends on a shell"). A separate non-setuid `pulsix-signal` binary would
  work and adds no privilege, but it is a second file to build and reason
  about for authority the helper can express in a dozen lines.
- **A resident supervisor** that keeps the privilege for the life of the
  play and relays signals. It needs no new verb, but it costs a process
  per job and takes from the helper its best property: that it starts one
  program and is gone.

Both modes must stay readable in one sitting. If the signal mode cannot be
written that way, that is a signal the approach is wrong, not that the
program should grow.

### The liveness probe goes away

`terminateGroup`'s poll is replaced rather than taught about `EPERM`. The
worker is the parent of the process it started, so the authoritative
answer to "did it end" is its own `Wait`, not a permission-gated probe of
somebody else's process group. The grace becomes: send TERM, wait for the
child's exit or the grace to expire, whichever comes first; on expiry,
send KILL to the group.

This removes the `EPERM`-as-gone bug by construction rather than by adding
a case to it, and it is correct with the uid feature off as well.

Orphaned grandchildren can outlive the group leader. The KILL after the
grace still goes to the whole group, as today.

### What the run view shows

No new storage. `job_events` already holds one row per Ansible event with
`host`, `task`, `state` and `seq`, and `useRunLog` seeds the SPA with the
complete history from `seq > 0` before subscribing to the live stream. A
host whose last event is a task that never reached a terminating state is
exactly "interrupted mid-task"; a host with no events at all is "never
started".

The derivation is therefore a **pure function in the SPA**, beside
`hostResultRows` in `web/src/features/runs/status.ts`, unit-tested with
Vitest. No endpoint, no query, no migration.

It needs two inputs, not one. The events alone cannot produce "never
started" — a host with no events contributes no row to read. The job's own
target list does: `jobDTO.Hosts` (`internal/api/job_handlers.go`) already
travels to the run view as `hosts`, and "never started" is that list minus
every host the events mention. The same wrinkle `hostResultRows` already
lives with applies here: the events' `host` field is the ansible host
name, which is the host id for targets the claim names and something else
for those it does not, so the subtraction is on whatever key appears and
an unmatched name is shown rather than dropped.

The event vocabulary is `internal/worker/runner/events.go`'s mapping:
`task` opens a host's step, and `ok`, `changed`, `failed`, `skipped` and
`unreachable` close it. A host whose last event is `task` is the
interrupted one.

This matters because a cancelled run has no recap: `stats` is built from
ansible-runner's final stats event, which never arrives on an abort, so
the job row's stats are nil and the existing per-host table renders
nothing at all. The breakdown is new information on that screen, not a
re-styling of something already there.

The warning appears only when the breakdown found at least one interrupted
host.

## What is left to the builder

- The exact spelling of the signal mode's arguments, and whether the signal
  is named (`TERM`) or numeric. Keep the drift check, the read-back, and
  the three limits above.
- How the worker's cancel goroutine and `cmd.Wait()` hand the exit to each
  other. Any shape is fine as long as no code path reads a permission error
  as a terminated process.
- Where the breakdown function lives in the SPA module and what it is
  called.
- The copy of the warning and of the three per-host states, in the SPA's
  existing voice.
- Whether `removeAsPlayUID`'s existing `exec.LookPath("rm")` pattern should
  be reused for anything here. It should not be needed.

## Risks and things to verify

- **The uncapped event seed.** The breakdown depends on the SPA holding the
  whole history; `Service.Events` applies no limit today. If a cap is ever
  added, the derivation moves server-side. Worth a line in the code that
  depends on it.
- **The test must cross a real uid boundary.** The existing
  `internal/e2e/uidboundary` test is the place; a unit test with a
  stand-in helper that does not drop uid cannot see this defect, which is
  precisely how it shipped.
- **The C file's own header comment says mode 4755**; the Dockerfile
  installs 4750. Stale comment in a setuid program — correct it in passing.
- **Docs.** `admin/security.md` and the environment-variable page in
  `noix-docs` describe the uid separation; whether the off-switch may now
  be recommended changes with this row. 2.106 already notes that
  `.env.example` and the README never mention `PULSIX_WORKER_PLAY_UID`.

## Acceptance

1. With `PULSIX_WORKER_PLAY_UID` set, cancelling a running play stops it —
   proven across a real uid drop, not against a stand-in that keeps its uid.
2. With the variable unset, cancellation behaves exactly as before.
3. A group that survives the grace receives `SIGKILL`; a permission error
   from any liveness question never ends the sequence early.
4. The run view of a cancelled run lists every host as finished,
   interrupted (with the task named), or never started, and shows the
   warning only when at least one host was interrupted.
5. `make test` green, `golangci-lint run` and `gofmt -s -l .` clean.
6. Backlog 2.104 closed; 2.42's "not safe to enable" note updated to say
   what is left, which after this is nothing.
