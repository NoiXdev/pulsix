# Agent Uninstall and Upgrade Robustness Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** One command removes the agent on Linux, macOS and Windows; the Windows upgrade checks before it stops, suspends recovery and waits for the process; a Linux re-run restarts the agent; CI smokes cover install, upgrade and uninstall on all three systems.

**Architecture:** Both installers live twice, byte-identical: `internal/agentdist/files/install.sh` = `deploy/install.sh`, `internal/agentdist/files/install.ps1` = `deploy/agent/windows/install.ps1` (`TestEmbeddedFilesMatchDeploySources`). The manager renders them (`render.go`, `powershell.go`). Go tests check shape and syntax; the CI jobs `agent-macos`/`agent-windows` run `scripts/ci/install-smoke-*.{sh,ps1}` against `internal/agentdist/cmd/installstub`.

**Tech Stack:** POSIX sh (shellcheck), PowerShell 5.1 + 7 (ASCII only), Go tests, GitHub Actions.

**Spec:** `docs/superpowers/specs/2026-11-13-agent-uninstall-upgrade-design.md`

## Global Constraints

- Flags: `install.sh --uninstall [--keep-data]`; `install.ps1 -Uninstall [-KeepData]` (parameter sets `Install` default, `Uninstall`). Closing hint: the agent stays registered in Pulsix, revoke or delete it on the Agents page. Nothing installed → "pulsix-agent: nothing to remove", exit 0.
- Windows waits: 30 s for `Stopped`, 30 s for the process to exit, copy retried 5 × 1 s. Recovery actions after install: exactly today's `reset= 86400 actions= restart/5000/restart/5000/restart/5000` and `failureflag 1`, restored in a `finally`.
- Edit the `internal/agentdist/files/` copy and copy it byte-for-byte to the `deploy/` path in the same commit.
- install.ps1 stays ASCII and runs under Windows PowerShell 5.1 (no `??`, no `-AsPlainText`, no ternary, no PS7-only cmdlets/params).
- **Never run either installer, or any smoke script, on this Mac**, never sudo, never install a service here. Local runtime checks only inside a throwaway container you start with `docker run --rm` and a unique `--name pulsix-sdd-…`; ~50 foreign containers run on this machine — never touch, stop or remove any other container or image.
- Repo rules: TDD; never `go test ./...` or `make test` (single packages `-race -timeout 900s`); `golangci-lint run` clean; commit footer `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`; no state-changing git besides your own commits; temp files only in the scratchpad `/private/tmp/claude-501/-Users-noidee--dev-pulsix/76b69f36-5040-49f9-a80e-08a1ef63bcdb/scratchpad`.

## Review Focus

1. Uninstall can never delete outside its fixed paths: no empty-variable `rm -rf`, no following a symlink/junction into its target.
2. A failed Windows upgrade leaves the old agent running (checks before stop) or, after the stop, a service whose recovery actions are restored.
3. The install path for a fresh install is unchanged in effect on every system.
4. Re-running uninstall, and uninstalling something never installed, succeed quietly.
5. `--keep-data`/`-KeepData` keeps identity and `agent.yaml`, and a later install reuses them.

---

### Task 1: `install.sh` — uninstall and the Linux restart

**Files:** `internal/agentdist/files/install.sh` (+ `deploy/install.sh` copy), `internal/agentdist/render_test.go` / `embed_test.go` (shape tests).

- [ ] Tests first (Go, on the rendered or raw script): parser accepts `--uninstall` and `--keep-data`; `--keep-data` without `--uninstall` and `--uninstall` with `--manager`/`--token`/`--token-file`/`--name`/`--server-name` are usage errors (assert the script text, plus a Go test that runs the rendered script with `sh` for the usage/argument cases only — they exit before any root check or file access; skip the root-required paths); usage line names `--uninstall [--keep-data]`; Linux install uses `systemctl restart`; uninstall removes the unit, binary, `/etc/pulsix` (Linux) and plist, binary, logs, data dir (macOS); the closing hint and "nothing to remove" strings; no `rm -rf "$VAR/"`-style trailing slash on the data dir. RED → GREEN.
- [ ] Shape: an `uninstall` function per system, run before the token/manager checks (it needs neither); every removal tolerant of absence; macOS bootout + the same 20 s wait loop; Linux `systemctl` steps only when `/run/systemd/system` exists.
- [ ] Runtime check in a throwaway container (e.g. `docker run --rm --name pulsix-sdd-uninstall -v <scratch>:/w:ro debian:stable-slim sh /w/install.sh --uninstall`): render the script to the scratchpad first (a tiny Go test helper or `go run` is fine), create fake `/usr/local/bin/pulsix-agent`, unit and `/etc/pulsix/agent.env` inside the container, run `--uninstall` (removed, hint printed), run again ("nothing to remove", exit 0), `--keep-data` keeps `/etc/pulsix`; usage errors exit 2. Put the commands and output in the report.
- [ ] `go test -race -timeout 900s ./internal/agentdist/...`, `golangci-lint run ./internal/agentdist/...`, shellcheck clean → commit `feat(agent): remove the agent with install.sh --uninstall and restart it on a Linux upgrade`.

### Task 2: `install.ps1` — uninstall and the Windows upgrade

**Files:** `internal/agentdist/files/install.ps1` (+ `deploy/agent/windows/install.ps1`), `internal/agentdist/powershell_test.go`.

- [ ] Tests first: `TestInstallPs1Shape` gains the parameter sets (`ParameterSetName = 'Install'`, `'Uninstall'`, `[switch]$Uninstall`, `[switch]$KeepData`), `Win32_Service`/`ProcessId`, the process wait, the copy retry, the recovery clear (`actions= ""` or the equivalent) and its restore inside a `finally`, the reparse-point refusal before any recursive removal, `sc.exe delete` via `Invoke-Sc "delete …"`, the closing hint, "nothing to remove"; an order test (by string index): the data-directory checks of an existing directory come before the service stop, the recovery clear before the stop, the process wait before `Copy-Item`. Keep every existing assertion (move `[Parameter(Mandatory)][string]$Manager` to its parameter-set form and update that one string). RED → GREEN.
- [ ] Implement per spec; one helper `Stop-AgentService` (clear recovery, stop, wait status, wait process) used by upgrade and uninstall; `Set-AgentRecovery` used at the end and in the `finally`. An uninstall leaves no `finally` that would re-add recovery to a deleted service.
- [ ] Parse check: `TestInstallPs1Parses` skips without `pwsh`; additionally parse the rendered script in a throwaway container if `mcr.microsoft.com/powershell` (any tag) can be pulled — `docker run --rm --name pulsix-sdd-pwsh -v <scratch>:/w:ro <image> pwsh -NoProfile -Command '<the parser call from the test>'`. If the image is unavailable, say so in the report.
- [ ] `go test -race -timeout 900s ./internal/agentdist/...`, lint → commit `feat(agent): remove the agent with install.ps1 -Uninstall and make the Windows upgrade wait and keep recovery`.

### Task 3: CI smokes

**Files:** `scripts/ci/install-smoke-macos.sh`, `scripts/ci/install-smoke-windows.ps1`, new `scripts/ci/install-smoke-linux.sh`, `.github/workflows/ci.yml` (new `agent-linux` job modelled on `agent-macos`: checkout, setup-go with the same pins and version, build `pulsix-agent-linux-$(go env GOARCH)` + `.sha256` + installstub, run the smoke; no unit tests or lint — the go job has them).

- [ ] macOS and Windows: after the upgrade, uninstall with the real command (fetched installer, `--uninstall` / `-Uninstall`), assert service/daemon, binary, plist/unit and data dir gone, run it again (exit 0, "nothing to remove"). Windows additionally: after each install `sc.exe qfailure PulsixAgent` shows the three restart actions; a `-KeepData` uninstall between upgrade and the final uninstall keeps `ProgramData\Pulsix` with `agent.env`, then a reinstall and the full uninstall (keep the job's runtime reasonable). The existing `cleanup`/`Remove-Agent` stays as the fallback.
- [ ] Linux: same structure as the macOS script (systemd: `systemctl is-active`, start count from `journalctl -u pulsix-agent`), fresh install, upgrade asserting the start count rose (restart fix), uninstall, uninstall again.
- [ ] Local: `shellcheck` on the shell scripts; parse the ps1 as in Task 2 if the image is available; `actionlint` if installed, else a YAML parse. These scripts are never run here. Commit `ci(agent): smoke-test uninstall and upgrades on Linux, macOS and Windows`.

### Task 4: Documentation

**Files:** `docs/BACKLOG.md` (3.49 and 3.51 done with a summary; 3.51 names the Linux restart fix; "Last updated"); noix-docs scratch clone `/private/tmp/claude-501/-Users-noidee--dev-pulsix/76b69f36-5040-49f9-a80e-08a1ef63bcdb/scratchpad/noix-docs-merge`, new branch `docs/pulsix-agent-uninstall` from `docs/pulsix-retention`: agents page "Removing the agent from a host" (one command per system, keep-data, revoke in Pulsix; keep the manual commands as a short fallback), wherever the installer options are listed, the upgrade wording (Linux re-run restarts), changelog; `npm run build`. Push nothing.

- [ ] Edit, build, commit in both repos (`docs: record the agent uninstall and upgrade fixes (backlog 3.49, 3.51)`, `docs(pulsix): remove the agent with one command`).
