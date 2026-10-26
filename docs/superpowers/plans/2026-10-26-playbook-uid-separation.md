# Playbook uid separation — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A playbook task targeting `localhost` can no longer read the worker's client certificate and key.

**Architecture:** The worker keeps its own uid. ansible-runner is spawned under a second uid whose **primary group is the worker's own gid**, so the run's work dir is shared by group while the worker's identity directory — mode `0700`, owner-only — is not. One variable turns it on; unset keeps today's behaviour byte for byte.

**Tech Stack:** Go (`internal/worker`), `syscall.SysProcAttr.Credential`, Debian image with a setuid helper.

**Spec:** `docs/superpowers/specs/2026-10-26-playbook-uid-separation-design.md`

## Global Constraints

- `PULSIX_WORKER_PLAY_UID` unset must leave behaviour **identical** to today — no mode changes, no credential, no new failure modes.
- The worker binary must not gain `CAP_SETUID` (the spec's anti-goal: it is a path to `setuid(0)`).
- Migrations additive only; none are needed here.
- `make test` and `golangci-lint run` pass before every commit. Chat German, repository English.

### Three decisions the spec left open, settled here

1. **No shared group is created.** The play process's *primary* gid is the worker's own (`os.Getgid()`), so the work dir needs no new group and no extra variable, and everything the play creates is group-owned by the worker and readable back. This reduces the spec's sketch from three variables to one.
2. **The data dir becomes `0710` when a play uid is set** — owner `rwx`, group `--x`. The play can *traverse* to its own work dir but cannot list the data dir, so it cannot enumerate other jobs or see `identity` by name. The identity dir itself stays `0700`.
3. **A refused spawn fails the job**, with an error naming the uid and the likely cause. No silent fallback: an operator who set the variable has said what they want, and a feature that quietly turns itself off cannot be relied on.

## Review Focus

- `PULSIX_WORKER_PLAY_UID` set to the worker's own uid — the separation is a no-op and must be refused at load, not discovered at run time.
- `PULSIX_WORKER_PLAY_UID` set but the process may not setuid — every job fails; the error must name the variable, not surface as a bare `operation not permitted`.
- A non-numeric or negative value — refused at load with the variable named.
- A run whose play writes artifacts the worker must read back (the event stream) — group ownership has to survive files the *play* creates, not only ones the worker creates.
- The repo cache (`<data>/repos`, shared across jobs, written by the worker) — the play must not need it writable, and must not be able to poison it for the next job.
- **The certificate bundle at `/etc/pulsix-worker`.** The Dockerfile `chown`s it and sets no mode, so it is `0755` — world-readable. Separating uids changes nothing while the key sits there for anyone to read. Task 2 closes it; this is the one place the spec's goal would have been quietly missed.

**Already handled, do not redo:** `runner.stripPrivateEnv` drops every `PULSIX_*` and `ANSIBLE_*` variable from the subprocess environment, so an enrollment token *in the environment* is already out of the play's reach. Only the on-disk copies need work.

---

### Task 1: Config — `PULSIX_WORKER_PLAY_UID`

**Files:**
- Modify: `internal/worker/config.go` (env name block ~line 30, `Config` struct, `Load`'s tail ~line 180)
- Test: `internal/worker/config_test.go`

**Interfaces:**
- Produces: `Config.PlayUID uint32` — 0 means "run as the worker's own uid", the default.

- [ ] **Step 1: Write the failing tests**

```go
func TestLoadPlayUID(t *testing.T) {
	for name, tc := range map[string]struct {
		raw  string
		want uint32
		err  string
	}{
		"unset":        {"", 0, ""},
		"set":          {"65533", 65533, ""},
		"not a number": {"nobody", 0, "PULSIX_WORKER_PLAY_UID"},
		"negative":     {"-1", 0, "PULSIX_WORKER_PLAY_UID"},
		"zero is root": {"0", 0, "must not be 0"},
		"own uid":      {strconv.Itoa(os.Getuid()), 0, "the worker's own uid"},
	} { /* Load with the minimal valid env plus tc.raw; assert PlayUID and the error substring */ }
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/worker/ -run TestLoadPlayUID -count=1`
Expected: FAIL — `cfg.PlayUID` undefined.

- [ ] **Step 3: Add `envPlayUID = "PULSIX_WORKER_PLAY_UID"`, the `PlayUID uint32` field, and its parse in `Load`**

Parse with `strconv.ParseUint(raw, 10, 32)`, following `envMaxConcurrent`'s existing shape. Refuse 0 explicitly (it is root, never a de-escalation) and refuse `os.Getuid()` (a no-op separation the operator would believe in). Document on the field that 0 means off.

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/worker/ -run TestLoadPlayUID -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/worker/config.go internal/worker/config_test.go
git commit -m "feat(worker): read PULSIX_WORKER_PLAY_UID"
```

---

### Task 2: Directory modes the second uid can reach

**Files:**
- Modify: `internal/worker/loop.go` (`newWorkDir` ~line 417, repo-cache mkdir ~line 165)
- Create: `internal/worker/runner/share.go`
- Test: `internal/worker/runner/share_test.go`

**Interfaces:**
- Consumes: `Config.PlayUID` from Task 1.
- Produces: `runner.ShareWorkDir(dir string) error` — makes `dir` and everything under it group-accessible (dirs `0770` + setgid, files `0660`), for a caller that has already written the run's files.
- Produces: `Loop.newWorkDir` creates the work dir `0770|os.ModeSetgid` when `cfg.PlayUID != 0`, `0700` otherwise.

- [ ] **Step 1: Write the failing test**

```go
func TestShareWorkDirAddsGroupAccessMirroringOwner(t *testing.T) {
	// A dir with a 0600 file and a 0700 subdir containing a 0700 executable.
	// After ShareWorkDir: dirs 0770 with ModeSetgid, 0600 -> 0660, 0700 exec -> 0770.
	// Nothing gains world bits.
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/worker/runner/ -run TestShareWorkDir -count=1`
Expected: FAIL — undefined.

- [ ] **Step 3: Implement `ShareWorkDir` in `internal/worker/runner/share.go`**

`filepath.WalkDir`; for each entry take its current mode's owner triplet and copy it into the group triplet (`m | (m&0o700)>>3`), adding `os.ModeSetgid` on directories so entries the *play* creates inherit the worker's group. Never touch world bits.

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/worker/runner/ -run TestShareWorkDir -count=1`
Expected: PASS

- [ ] **Step 5: Make the data dir traversable and the work dir setgid**

In `loop.go`: when `cfg.PlayUID != 0`, `os.Chmod(cfg.DataDir, 0o710)` once at start (next to the existing repo-cache `MkdirAll`), and have `newWorkDir` chmod its fresh temp dir to `0770|os.ModeSetgid`. The identity dir is untouched — that it stays `0700` is the whole mechanism.

- [ ] **Step 6: Harden the identity directory wherever it lives**

The data dir's own `identity/` is created by the worker; `/etc/pulsix-worker` (the bundle mount, `PULSIX_WORKER_IDENTITY_DIR` or `PULSIX_WORKER_CERT`'s parent) is created by the Dockerfile with **no mode**, so it is `0755` and the play could read the key straight out of it. When `cfg.PlayUID != 0`, chmod the resolved identity directory to `0700` at start, next to the data dir's `0710`. Log at INFO when it changes something — an operator who bind-mounted that directory should see it happen.

- [ ] **Step 7: Write the test that the secrets are NOT reachable**

```go
func TestSharingLeavesTheWorkerSecretsOwnerOnly(t *testing.T) {
	// data dir with identity/ (0755 to start, the Dockerfile's shape),
	// repos/ (0700) and a work dir. After the loop's sharing step:
	// data dir 0710, work dir 0770|setgid, identity 0700, repos still 0700.
	// The repo cache matters as much as identity: group-writable, the play
	// could poison the next job's checkout.
}
```

- [ ] **Step 8: Run the package**

Run: `go test ./internal/worker/... -count=1`
Expected: PASS

- [ ] **Step 9: Commit**

```bash
git add internal/worker/loop.go internal/worker/runner/share.go internal/worker/runner/share_test.go
git commit -m "feat(worker): share the run's work dir by group, keep identity owner-only"
```

---

### Task 3: Spawn ansible-runner under the play uid

**Files:**
- Modify: `internal/worker/runner/runner.go:149` (the `exec.Command` site and its `SysProcAttr`)
- Test: `internal/worker/runner/runner_test.go`

**Interfaces:**
- Consumes: `Config.PlayUID`, `runner.ShareWorkDir` from Tasks 1 and 2.

- [ ] **Step 1: Write the failing test**

```go
func TestRunnerSpawnsUnderThePlayUID(t *testing.T) {
	// With PlayUID set, cmd.SysProcAttr.Credential is non-nil, Uid is the
	// configured one, Gid is os.Getgid(), and Setpgid is still true.
	// With PlayUID 0, Credential is nil and Setpgid is still true.
}
```
Assert against the built `*exec.Cmd` rather than a real run: the uid boundary itself is proven by the integration test in Task 4, and a unit test cannot setuid.

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/worker/runner/ -run TestRunnerSpawnsUnderThePlayUID -count=1`
Expected: FAIL

- [ ] **Step 3: Set the credential and call `ShareWorkDir` before `cmd.Start()`**

`cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}` keeps its existing field and gains `Credential: &syscall.Credential{Uid: playUID, Gid: uint32(os.Getgid())}` when the uid is non-zero. The primary gid is the WORKER's, so artifacts the play writes stay readable by the worker — this is what makes the event stream keep working.

- [ ] **Step 4: Write the failing test for a refused spawn**

```go
func TestRunnerRefusedSpawnNamesTheVariable(t *testing.T) {
	// PlayUID set to a uid this test process may not become (any uid but
	// its own, when not root). Run must fail, and the error must contain
	// "PULSIX_WORKER_PLAY_UID" and the uid — not a bare "operation not
	// permitted", which sends an operator to the wrong place.
	// Skip when running as root: there, the spawn succeeds.
}
```

- [ ] **Step 5: Make a refused spawn legible**

Wrap the `cmd.Start()` error when a credential was set: name `PULSIX_WORKER_PLAY_UID`, the uid, and that the worker may not change uid in this deployment. Per Global Constraints there is no fallback — the job fails.

- [ ] **Step 6: Run the package**

Run: `go test ./internal/worker/runner/ -count=1`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add internal/worker/runner/runner.go internal/worker/runner/runner_test.go
git commit -m "feat(worker): run the playbook under its own uid"
```

---

### Task 4: The image — a second uid and the privilege to reach it

**Files:**
- Modify: `deploy/worker/Dockerfile`
- Create: `deploy/worker/setuid-exec.c`
- Test: `internal/e2e/playbook_uid_test.go`

**Interfaces:**
- Consumes: everything above.

- [ ] **Step 1: Write the helper**

`deploy/worker/setuid-exec.c`: a setuid-root program that takes **no** arguments from the environment — it `setgid()`s to the worker's gid, `setuid()`s to a uid compiled in as `PLAY_UID`, verifies both took (`getuid() == PLAY_UID`), and `execv`s `argv[1:]`. It refuses outright if `geteuid() != 0`. Keep it under 60 lines; a reviewer must be able to read the whole thing.

- [ ] **Step 2: Build it into the image, owned by root, mode 4755**

Add `gcc` to the build stage only; the run stage copies the compiled binary. Add `useradd --system --uid 65533 --gid 65532 --no-create-home --shell /usr/sbin/nologin pulsix-play` — gid 65532 is the worker's group, which is what Task 3's `Credential` relies on. Give `/etc/pulsix-worker` mode `0700` in the Dockerfile too, so a fresh image is correct before the worker's own start-up chmod ever runs. Set `ENV PULSIX_WORKER_PLAY_UID=65533` so the shipped image is separated by default.

- [ ] **Step 3: Write the failing integration test**

```go
func TestPlaybookCannotReadTheWorkerKey(t *testing.T) {
	// A play with one localhost task that cats the worker's key path.
	// The task must fail with permission denied, and the run's own
	// events must still arrive (the worker can read artifacts the play wrote).
}
```
Guard it with the same self-skip `ansible.spec.ts` uses — it needs the worker image, which backlog 2.96 is waiting on. The skip must say so.

- [ ] **Step 4: Run it**

Run: `go test ./internal/e2e/ -run TestPlaybookCannotReadTheWorkerKey -count=1 -timeout 1800s`
Expected: PASS, or SKIP with the 2.96 reason.

- [ ] **Step 5: Commit**

```bash
git add deploy/worker/ internal/e2e/playbook_uid_test.go
git commit -m "feat(worker): ship a second uid for playbooks, with a setuid helper"
```

---

### Task 5: Documentation and the backlog

**Files:**
- Modify: `~/_dev/noix-docs/src/content/docs/pulsix/admin/security.md` (the worker trust model)
- Modify: `~/_dev/noix-docs/src/content/docs/pulsix/admin/configuration/environment-variables.md`
- Modify: `docs/superpowers/specs/2026-09-10-pulsix-architecture-design.md` §14
- Modify: `docs/BACKLOG.md` row 2.42

- [ ] **Step 1: Update the architecture spec's §14**

It currently says running playbooks under a separate uid "is a hardening step we may take". Replace with what shipped, and keep the per-trust-domain recommendation standing — the spec's own non-goals say why it is still the stronger answer.

- [ ] **Step 2: Update the end-user docs**

`security.md`: what the separation stops (the worker's certificate and key) and what it does not (the job's own revealed credentials, the worker's network). `environment-variables.md`: `PULSIX_WORKER_PLAY_UID`, that the shipped image sets it, and that a custom image or a bind-mounted data directory must provide the uid before setting it.

- [ ] **Step 3: Close row 2.42**

Record the three decisions from this plan's Global Constraints, and strike `CAP_SETUID` from the row's own suggestion with the reason.

- [ ] **Step 4: Commit both repositories separately**

Two commits, two repositories. Name the docs commit's hash in the server_manager one.
