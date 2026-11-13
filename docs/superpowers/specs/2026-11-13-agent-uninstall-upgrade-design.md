# Agent uninstall and upgrade robustness (backlog 3.49, 3.51) — design

Status: the owner accepted the proposal on 2026-11-13 ("passt"): one
command removes the agent (`install.sh --uninstall`, `install.ps1
-Uninstall`) with an option to keep the data; the Windows upgrade checks
the data directory before it stops the service, suspends the recovery
actions while it replaces the binary, and waits for the process to end;
the CI install smokes use the new uninstall and test an upgrade. Every
other decision below is **(Claude's call)**.

## Today

- Removal is three sets of manual commands in the user docs (agents page,
  "Removing the agent from a host").
- `install.ps1` stops the service (30 s wait for `Stopped`), copies the
  binary, then checks and tightens the data directory, then sets the
  recovery actions (`sc failure … restart/5000 ×3`, `failureflag 1`) and
  starts the service. A data-directory problem therefore stops the install
  after the old agent has already been stopped, and the service manager may
  restart the old agent (recovery actions) while the binary is replaced;
  `Stopped` can be reported before the process has fully exited, so the
  copy can hit a locked file.
- **Found while planning (Claude's call to fix here):** on Linux a re-run of
  `install.sh` replaces `/usr/local/bin/pulsix-agent` but `systemctl enable
  --now` leaves an already running service alone, so the old binary keeps
  running until the next reboot or crash. The docs say a re-run upgrades.

## Uninstall

### `install.sh --uninstall [--keep-data]` (Linux, macOS)

- `--uninstall` needs no `--manager` and no token; combined with any install
  flag it is a usage error. `--keep-data` without `--uninstall` is a usage
  error. Root is required (as for install).
- Linux: `systemctl disable --now pulsix-agent` when systemd and the unit
  exist (errors from a unit that is not loaded are ignored), remove
  `/etc/systemd/system/pulsix-agent.service`, `systemctl daemon-reload`
  (when systemd is present), remove `/usr/local/bin/pulsix-agent`, remove
  `/etc/pulsix` unless `--keep-data`. Logs are in the journal; nothing to
  remove.
- macOS: `launchctl bootout system/dev.noix.pulsix-agent` and wait for the
  job to disappear (the same 20 s loop as the upgrade), remove the plist and
  the binary, remove `/Library/Logs/Pulsix`, remove
  `/Library/Application Support/Pulsix` unless `--keep-data`.
- **Idempotent:** every step tolerates its target being absent; when
  nothing was installed it prints "pulsix-agent: nothing to remove" and
  exits 0. Otherwise it prints what it removed and, last, a line that the
  agent is still registered in Pulsix and should be revoked or deleted on
  the Agents page (the script has no credentials for that, by design).
- Paths are the script's fixed constants; `rm -rf` is never given a
  variable that could be empty. A symlinked data directory is removed as a
  link (no trailing slash), never followed.
- `--keep-data` keeps the data directory as it is: identity, certificate,
  `agent.env` and an operator's `agent.yaml`. A later install over it
  reuses the identity (the existing behaviour of an install over existing
  data).

### `install.ps1 -Uninstall [-KeepData]` (Windows)

- Parameter sets: `Install` (default; `-Manager` mandatory as today) and
  `Uninstall` (`-Uninstall` switch mandatory, optional `-KeepData`); mixing
  them is PowerShell's own parameter-set error. Administrator required.
- Steps: when the service exists, clear its recovery actions, stop it and
  wait for its process to exit (shared helper, below), `sc.exe delete
  PulsixAgent`; remove `pulsix-agent.exe` and then `Program Files\Pulsix`
  if empty; remove `ProgramData\Pulsix` (including `logs\`) unless
  `-KeepData`.
- A data directory that is a reparse point (symlink/junction) is **not**
  removed recursively — Windows PowerShell 5.1's `Remove-Item -Recurse`
  can follow a junction into its target. The script stops with a message
  naming the path. Same rule for `Program Files\Pulsix`.
- Idempotent and the same closing lines as the shell script.

## Windows upgrade (3.51)

New order of the install path:

1. Admin, TLS, input checks, token, arch, download and verify (unchanged).
2. **Data-directory checks before anything is stopped:** if the data
   directory exists, the existing checks (plain folder, not a reparse
   point, owner SYSTEM/Administrators, `Assert-OnlyAdmins` on it and on
   each child except `agent.yaml` and `agent.env`, which is rewritten
   anyway) run here; a failure stops the install while the old agent keeps
   running. Creating and tightening the directory (icacls) stays where it
   is, after the binary.
3. **Suspend recovery:** when the service exists, its recovery actions are
   cleared (`sc failure PulsixAgent reset= 0 actions= ""`) before it is
   stopped. They are set again (the existing `reset= 86400 actions=
   restart/5000 ×3` and `failureflag 1`) in a `finally` that also runs
   when the install fails after this point, so an aborted upgrade never
   leaves a service without recovery.
4. **Stop and wait for the process:** after `WaitForStatus('Stopped', 30
   s)`, the script reads the service's process id beforehand (`Get-CimInstance
   Win32_Service` → `ProcessId`; 0 = none) and waits up to 30 s for that
   process to exit (`Wait-Process` / `Get-Process -Id` polling). If it does
   not, the install stops with a message (old binary untouched).
5. **Copy with retry:** `Copy-Item` is retried a few times (e.g. 5 × 1 s)
   on an IO error (a scanner holding the file briefly), then stops with a
   message.
6. Data directory creation/ACLs, env file, `sc config|create`, recovery
   actions, start (unchanged).

The helper "stop the service and wait for its process" is shared by
upgrade and uninstall.

## Linux upgrade (found while planning)

After `systemctl daemon-reload`, `systemctl enable pulsix-agent` and
`systemctl restart pulsix-agent`, so a re-run runs the new binary. A fresh
install behaves as before (restart of a stopped unit starts it).

## Tests

- Go (`internal/agentdist`): shape tests for both scripts — the uninstall
  flags and parameter sets, the usage errors, the closing hint, the
  reparse-point refusal, the recovery suspension in a `finally`, the
  process wait, the order (data-directory checks before the stop) asserted
  by index; `sh -n` and shellcheck where present (existing
  `TestInstallScriptSyntax`); the PowerShell parse test where `pwsh` exists.
- Local, by hand (no CI here): the rendered `install.sh --uninstall` in a
  throwaway Linux container the implementer starts and removes itself
  (files created by hand, uninstall run twice, `--keep-data`, usage
  errors); `install.ps1` parsed with `pwsh` in a throwaway PowerShell
  container if the image is available. Never on the owner's Mac.
- CI (runs after the owner pushes): `scripts/ci/install-smoke-macos.sh`
  and `install-smoke-windows.ps1` remove the agent with the new uninstall
  and assert that service, binary and data are gone, then run the
  uninstall again (idempotent); Windows also asserts that the recovery
  actions are set again after the upgrade (`sc qfailure`), and runs one
  `-KeepData` uninstall that leaves the data directory. A new
  `scripts/ci/install-smoke-linux.sh` in a new `agent-linux` job on
  `ubuntu-latest` (systemd and sudo are there): fresh install, upgrade
  (asserts a new start: the restart fix), uninstall, uninstall again.
  The smokes' own `cleanup` keeps its manual removal as a fallback.

## Documentation

`docs/BACKLOG.md` (3.49, 3.51 done; the Linux restart noted in 3.51),
noix-docs agents page ("Removing the agent from a host" → the one command
per system, `--keep-data`, revoke in Pulsix), installation/reference pages
where the installer flags are listed, changelog.

## Non-goals

- Revoking the agent in Pulsix from the script (would need credentials on
  the host).
- An uninstall endpoint or button in the GUI.
- Code signing (3.48).
