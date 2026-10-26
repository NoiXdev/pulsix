# Running playbooks under their own uid — design

Backlog 2.42. Status: **built** — see
`docs/superpowers/plans/2026-10-26-playbook-uid-separation.md` and the commits
it names. Two things below were superseded during execution and are kept as
written so the reasoning survives: the shared `pulsix-run` group (the play's
PRIMARY gid is the worker's own instead, which removed a variable), and
"execs one fixed program" (work-dir cleanup became a second caller of the
helper). Cancellation across the uid boundary is an open defect the
whole-feature review found; it is not solved here.

## Why

Anyone who may run jobs in an organization can run arbitrary code on that
organization's workers — a playbook task can target `localhost`. The
architecture spec §14 says so deliberately, and lists what such code
reaches today: every secret the claim revealed, the worker's own client
certificate and key, and the worker's network.

The certificate is the one that stings. With it, the code is no longer a
play running on a worker; it IS that worker, and can claim further jobs of
the same organization and zone — jobs it was never given, with their own
revealed credentials. Everything else on the list is bounded by the job
that was legitimately dispatched. This is the exposure to close.

The current answer in the spec — "give each trust domain its own
organization, or at least its own zone and worker" — stays correct and
stays recommended. This narrows what one trust domain's own playbook can
do to its own worker.

## Goals

- A playbook task running as `localhost` cannot read the worker's client
  certificate and key.
- The same for the enrollment token, where one is still on disk.
- Existing deployments keep working unchanged until an operator opts in.

## Non-goals

- Isolating a play from the secrets of **its own** job. Those are the
  credentials the job was dispatched with; the play is entitled to them.
  Separating them would mean a credential-broker process, which is a far
  larger change with its own failure modes.
- Isolating a play from the worker's network. A play that may SSH to the
  fleet can reach the manager's gRPC port either way; the certificate is
  what makes that reach meaningful, and it is the certificate this
  removes.
- Protecting against a malicious *worker operator*. Whoever runs the
  container can read anything in it.

## Anti-goal

**Granting the worker a privilege broader than the one it needs.**
`CAP_SETUID` on the worker binary would be the smallest diff and is the
wrong trade: a process holding it can `setuid(0)`. A compromised worker
would gain a path to root that it does not have today, so the change
would make the posture worse while appearing to improve it. Any design
that ends there is a failure even if playbooks do run under their own uid.

## What was settled

### File access needs no privilege

The first instinct is to `chown` the run's work dir to the play uid. That
is privileged — a non-root owner may not give a file away — and it would
have made the privilege question twice as large.

It is avoidable. A shared group (`pulsix-run`) with the setgid bit on the
work dir does the whole job:

- the worker's own uid and the play uid are both members;
- the work dir is `2770`, so everything created inside inherits the group;
- run files become `0660` and run directories `0770` instead of today's
  `0600`/`0700`;
- the play uid reads the inventory, the playbook, the keys and the env,
  and writes artifacts, the fact cache and connection sockets.

The worker's identity directory is **not** in that group and keeps mode
`0700` owned by the worker uid. That is the whole mechanism: one
directory the play uid is not in the group for.

Group-writable run files mean the play can alter its own run's files.
That is already true — it is the play's own run — and it buys the
privilege-free file story, which is worth it.

### The spawn is the only privileged step

`exec.Cmd.SysProcAttr.Credential` is how the worker starts ansible-runner
as another uid. Whether it may is the one open decision; it is not a Go
question. The options, in the order they should be weighed:

1. **A setuid-root helper in the image** that execs one fixed program as
   one fixed uid, with no arguments taken from the environment. Smallest
   privileged surface; costs a second binary written with the care a
   setuid program needs, and a reason for reviewers to look at it.
2. **The worker container runs as root and the worker drops to its own
   unprivileged uid for everything but the spawn.** No new binary, but it
   reverses the image's current `USER 65532` and makes the worker process
   root for its whole life — a bigger target than the helper.
3. **`CAP_SETUID` on the worker binary.** Rejected above; listed so the
   next reader sees it was considered and why it lost.

My recommendation is (1). It is the only one where the privileged thing
is small enough to read in one sitting.

### Opt-in, not a flip

`PULSIX_WORKER_PLAY_UID` unset keeps today's behaviour exactly. An
operator on a bind-mounted host directory, or a custom image, must be
able to upgrade without their runs breaking on a uid their filesystem
does not know. The image ships the second uid and the shared group so the
default compose stack can turn it on, but the code must never assume it.

## What is left to the builder

- Where the uid/gid pair is read and validated (config package, with the
  rest of `PULSIX_WORKER_*`), and what a half-set pair does.
- Whether the shared group is a build-time constant of the image or
  configuration. A constant is simpler; configuration helps someone
  running the worker outside the image.
- How the run's directories get their group and setgid bit — at creation
  in `layout.go`, or once for the work dir with inheritance doing the
  rest.
- What happens when the spawn is refused at runtime (no privilege, uid
  absent): fail the job with an actionable error, or log and fall back to
  the current behaviour. **Decide deliberately** — a silent fallback
  turns a security feature into one that cannot be relied on, and an
  operator who set the variable has said what they want.
- Tests: the mechanism is a uid boundary, so the honest test is an
  integration one that runs a play as the second uid and has it fail to
  read a file only the worker uid can see. A unit test can cover the
  config and the directory modes, but not the boundary itself.
- `admin/security.md` and the worker configuration page: what the
  variable does, what it does not protect against (the non-goals above),
  and that the per-trust-domain recommendation still stands.
