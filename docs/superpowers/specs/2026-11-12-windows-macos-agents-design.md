# Agents on Windows and macOS, with service checks (backlog 3.2, 3.3, 3.8) — design

Status: the owner asked on 2026-11-11 for 3.8 (service checks for Windows
and launchd). It turned out that no agent runs on Windows or macOS yet, and
on 2026-11-12 the owner chose to build 3.2 (Windows agent), 3.3 (macOS
agent) and 3.8 together. The owner handed the remaining decisions to Claude
("leg los" for 3.13, the same standing choice). Every decision below that
the owner did not make is marked **(Claude's call)**.

## Intent

Pulsix monitors Linux hosts through its agent. Many fleets also have Windows
servers and Macs (build machines, office servers), and today those can only
be watched from the outside (3.13). After this work an operator enrolls a
Windows or macOS machine the same way as a Linux one: the Add-agent dialog
shows a command, the operator runs it on the machine, the agent installs
itself as a system service, enrolls, and reports. The system checks (cpu,
memory, disk, network, uptime) report on all three systems, and the
`service` check watches Windows services and launchd jobs as it already
watches systemd units.

## Goals

- Agent binaries for `windows/amd64`, `windows/arm64`, `darwin/amd64` and
  `darwin/arm64`, built and served by the manager next to the two Linux
  ones.
- **Windows:** the agent runs as a Windows service, installed by a
  PowerShell script the manager serves.
- **macOS:** the agent runs as a launchd daemon, installed by the existing
  `install.sh`, which learns macOS.
- The `service` plugin reports `service.state` for Windows services and
  launchd jobs.
- The system plugins and the facts report correctly on Windows and macOS:
  no metric that the platform cannot measure, no flood of pseudo devices.
- The Add-agent dialog shows the command for each system.
- Continuous integration builds, tests and lints the Windows and macOS code
  on real Windows and macOS runners, and installs the agent there once.

## Non-goals

- Code signing and notarization of the binaries. A darwin/arm64 binary is
  ad-hoc signed by the Go linker and runs; a binary downloaded with `curl`
  or PowerShell carries no quarantine mark, so Gatekeeper and SmartScreen
  do not stop it. **(Claude's call:** signing needs certificates and an
  Apple developer account that the project does not have. It is written
  down as a new backlog row.)
- An uninstaller. The end-user docs give the three commands per system.
- Agent self-update (backlog 3.18).
- Per-user launchd agents (the `gui/<uid>` domain). The agent runs as root
  and sees the system domain only: system daemons and Apple's own services.
- Windows service details beyond the state: start type, the account it runs
  as, dependencies.
- 32-bit Windows, and Windows releases older than the oldest the Go
  toolchain supports (Windows 10 and Server 2016).
- An MSI or pkg installer.

## Agent on all platforms

### Locations

| | Linux (unchanged) | macOS | Windows |
|---|---|---|---|
| Binary | `/usr/local/bin/pulsix-agent` | `/usr/local/bin/pulsix-agent` | `C:\Program Files\Pulsix\pulsix-agent.exe` |
| Data directory (identity, env file) | `/etc/pulsix` | `/Library/Application Support/Pulsix` | `%ProgramData%\Pulsix` |
| Service | systemd unit `pulsix-agent` | launchd daemon `dev.noix.pulsix-agent`, plist in `/Library/LaunchDaemons` | Windows service `PulsixAgent`, display name "Pulsix agent" |
| Log | journald | `/Library/Logs/Pulsix/agent.log` | `%ProgramData%\Pulsix\logs\agent.log` |

**(Claude's call:** each system's usual places. The agent's default data
directory, used when `PULSIX_DATA_DIR` is unset, becomes the platform's
entry in this table.)

### The agent's default name

When `PULSIX_AGENT_NAME` is unset, the agent names itself after the
system's hostname, and enrollment creates the host under that name unless
the token carries a name hint. A host name must match
`^[a-z0-9]([a-z0-9.-]{0,252}[a-z0-9])?$` (`internal/hosts`). Windows
hostnames are usually upper case (`WIN-SRV01`). Mac hostnames are mixed
case and often end in `.local` (`Johns-MacBook-Pro.local`). Today such a
name is refused at enrollment, and the service would restart into the same
refusal forever.

So the agent normalizes the hostname before using it as its default
name:
1. Lower-case it.
2. Strip a trailing `.local` and a trailing dot.
3. Replace every character outside `[a-z0-9.-]` with `-`.
4. Trim leading and trailing `-` and `.`.

If nothing usable is left, the agent stops with the existing "set the agent
name explicitly" error. An explicit `PULSIX_AGENT_NAME` is used exactly as
given, as today.

This applies on every system. A Linux agent whose hostname is already a
valid name gets the same name as before. An agent that has already
enrolled keeps its identity, because the name only matters at enrollment.
The manager does not change. **(Claude's call:** the agent is the one
place that knows the name came from a hostname rather than from the
operator.)

### Configuration file

systemd reads `agent.env` itself (`EnvironmentFile=`). launchd and the
Windows service manager have no such thing, and the alternatives would put
the enrollment token where other users can read it: a launchd plist, or the
service's registry key. So the agent learns one flag, `--env-file <path>`:
it reads `KEY=VALUE` lines (blank lines and lines starting with `#`
ignored, no quoting, no expansion) and uses them for every variable the
process environment does not already set. The launchd plist and the
Windows service pass it; the Linux unit does not need it and stays as it
is. **(Claude's call.)**

The agent refuses an env file that is not a regular file. On macOS it
refuses one that is owned by someone other than root or that the group or
others can read or write. On Windows it refuses one whose access list
grants read or write to anyone except SYSTEM and the Administrators group.
The token is in it.

### The data directory on Windows

`os.MkdirAll` ignores permission bits on Windows: a new directory under
`%ProgramData%` inherits an access list that lets every local user read it,
and the agent's private key is in it. On Windows the agent sets a protected
access list on its data directory (inheritance off; full control for
SYSTEM and Administrators, nothing for anyone else) whenever it creates the
directory, and it checks that list before it writes the key.

If the directory already exists with a looser list, what happens depends
on where it is:
- **The default `%ProgramData%\Pulsix`:** the agent replaces the list and
  logs that it did.
- **A directory the operator chose with `PULSIX_DATA_DIR`:** the agent
  does not touch the list. A rewritten access list cannot be undone, and
  the directory may be shared. The agent stops with exit code 2 and names
  the directory and the access it found.

The install script sets the protected list on the default directory before
it writes `agent.env`. **(Claude's call.)**

macOS needs nothing new: the Unix modes the agent already uses (0700
directory, 0600 files) apply.

### Running as a Windows service

When the service manager starts the binary, the agent runs under the
service control handler:
- it reports *running* once its loop has started;
- on Stop or Shutdown it cancels the loop and reports *stop pending*, then
  *stopped* once the loop has returned or after 20 seconds;
- it reports exit code 2 to the service manager when the loop stops for a
  reason that needs the operator, and 1 for any other failure, the same
  codes the Linux process exits with.

Started from a console, the binary runs in the foreground as on Linux.

The service is installed with start type *automatic (delayed start)*, so
it starts after the network is up. Its recovery actions restart it after
5 seconds on the first, second and later failures. That matches the Linux
unit's `Restart=on-failure` and `RestartSec=5s`. **(Claude's call.)**

### Running as a launchd daemon

The plist runs `/usr/local/bin/pulsix-agent --env-file
"/Library/Application Support/Pulsix/agent.env" --log-file
/Library/Logs/Pulsix/agent.log`. Its settings:
- `RunAtLoad` true;
- `KeepAlive` set to `SuccessfulExit` false, so launchd restarts the agent
  after a failure but not after a clean stop;
- `ThrottleInterval` 5;
- `ProcessType` `Background`.

launchd stops it with SIGTERM, which the agent already handles.

### Log file

On Windows and macOS stderr goes nowhere useful: the service manager drops
it, and launchd can write it to a file but never rotates it. So the agent
learns `--log-file <path>`. The agent then writes its log to that file
instead of stderr, creating the parent directory with the data directory's
protection. When the file reaches 10 MiB the agent renames it to
`agent.log.1`, replacing any older one, and starts a new file. On Linux
the flag is not used and journald keeps the log. **(Claude's call.)**

## Install

### macOS: `install.sh`

The existing script learns macOS. It tells the two apart with `uname -s`;
any other system stops with an error naming the three supported ones. On
macOS it:
- maps `uname -m` `x86_64` and `arm64` to `amd64` and `arm64`;
- downloads `/agents/bin/darwin/<arch>` and verifies its checksum with
  `shasum -a 256`, because macOS has no `sha256sum`;
- checks a `--token-file` with BSD `stat -f`;
- installs the binary to `/usr/local/bin`, creating the directory if it is
  missing (a new Apple silicon Mac has none);
- writes `agent.env` (mode 0600) into the data directory (mode 0700, owned
  by root);
- writes the plist with mode 0644, owned by `root:wheel`;
- if the daemon is already loaded, unloads it with `launchctl bootout`,
  then loads it with `launchctl bootstrap system <plist>`.

Running the script again upgrades the binary and keeps the identity, as on
Linux.

The command does not change: `curl -fsSL <manager>/install.sh | sudo sh -s
-- --manager <addr> …` works on both systems. The plist is embedded into
the script the same way as the systemd unit, from
`internal/agentdist/files/`, with the same drift guard and heredoc check.

### Windows: `install.ps1`

The manager serves a new script, `GET /install.ps1`, templated with its
public URL like `install.sh` and rate-limited the same way. It runs in
Windows PowerShell 5.1 (every supported Windows has it) and in PowerShell
7. The dialog's command is

```
[Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor 3072; & ([scriptblock]::Create((irm <public-url>/install.ps1))) -Manager <addr> [-ServerName <name>] [-Name <hint>]
```

to be run in a PowerShell window opened as administrator. **(Claude's
call:**
- The leading statement turns on TLS 1.2 (3072) before the first
  download. Windows PowerShell 5.1 on Server 2016 and older Windows 10
  builds otherwise offers only TLS 1.0 and 1.1, which a current reverse
  proxy refuses. It adds 1.2 to what is already enabled rather than
  replacing it.
- The `[scriptblock]::Create` form passes parameters to a downloaded
  script without saving it first.)

The script:
1. Stops unless it runs as administrator.
2. Turns on TLS 1.2 for its own downloads, in case it was started some
   other way.
3. Reads the token:
   - with `-TokenFile <path>`, from that file. The file must not be
     readable by anyone other than SYSTEM, Administrators and the file's
     owner; otherwise the script stops.
   - with `-Token`, as given. Like `--token`, this exists for automation,
     and the help says so.
   - with neither, it asks with `Read-Host -AsSecureString`, which hides
     the input.
4. Maps `$env:PROCESSOR_ARCHITECTURE` (`AMD64`, `ARM64`) to the binary.
   Any other architecture stops the script.
5. Downloads the binary and its checksum. It compares the checksum with
   `Get-FileHash` and stops on a mismatch.
6. If the `PulsixAgent` service exists, stops it.
7. Copies the binary to `C:\Program Files\Pulsix\pulsix-agent.exe`.
8. Creates `%ProgramData%\Pulsix` with the protected access list described
   above, then writes `agent.env` into it.
9. Creates the service, or updates its binary path if it already exists.
   The binary path carries `--env-file` and `--log-file`.
10. Sets delayed automatic start and the recovery actions, then starts the
    service.

Running it again upgrades the agent and keeps the identity.

### API and dialog

- The token-creation response keeps `install_command`, which now serves
  both Linux and macOS.
- It gains `install_command_windows`, built from the same parts. The change
  is additive.
- The Add-agent dialog shows both commands, one labelled "Linux and macOS",
  the other "Windows (PowerShell as administrator)". Each has its own copy
  button.

## Service checks (3.8)

The `service` plugin keeps its parameter (`units`, a list of names) and
its single metric `service.state`, with the label set `active`, `inactive`,
`failed`, `activating` and `unknown`. Each name is looked up on the
platform's own service manager. All names are looked up together; if that
lookup fails as a whole, every name reports a collection error, as on
Linux today.

### Windows

The names are service names, the registry key names (`wuauserv`, `W3SVC`),
not display names. Windows compares them without regard to case. The agent
reports each item under the name the operator wrote. The plugin opens the
service manager once per collection, with connect rights only, and opens
each service with query-status rights only.

| Service state | `service.state` |
|---|---|
| Running | `active` |
| Start pending, continue pending | `activating` |
| Stopped, exit code 0 or 1077 (never started since boot) | `inactive` |
| Stopped, any other exit code (also a service-specific one) | `failed` |
| Stop pending, pause pending, paused | `unknown` |
| No such service | `unknown` |

**(Claude's call:** Windows has no failed state. A stopped service whose
last exit code is an error is what an operator means by failed, and it
gives a Windows service the same rule shape as a systemd unit.) Any other
error opening one service (access denied, for example) is that one item's
collection error; the other names still report.

### macOS

The names are launchd labels (`com.apple.sshd`, `org.nginx.nginx`). The
plugin runs `/bin/launchctl list` once per collection, as root, so the
system domain, and reads its tab-separated `PID Status Label` lines. The
format has been stable for well over a decade; `launchctl print` warns that
its own output is not an interface. **(Claude's call.)**

| `launchctl list` line | `service.state` |
|---|---|
| a PID | `active` |
| `-` and status 0 | `inactive` |
| `-` and any other status (an exit code, or a negative signal number) | `failed` |
| no line for the label (not loaded) | `unknown` |

macOS has nothing that maps to `activating`. Labels are compared exactly.
If the command fails or its output has no header line, the whole lookup
fails.

### Linux

Unchanged.

### GUI

The service check's field hint names all three: "systemd units on Linux,
service names on Windows, launchd labels on macOS".

## System plugins and facts on Windows and macOS

Every system plugin compiles and runs on all three systems. Where a
platform cannot measure a metric, the plugin leaves it out; it never
reports zero or an error for it. **(Claude's call:** an absent metric reads
as "not measured", which is the truth, while a zero would satisfy or trip
rules wrongly.)

- **cpu:** Windows has no load average. On Windows, cpu reports only
  `cpu.used_pct`; `cpu.load1`, `cpu.load5` and `cpu.load15` are absent.
  macOS has a real load average and reports all four.
- **memory:** unchanged. On Windows, swap is the page file.
- **disk:**
  - Windows reports each fixed drive by its letter (`C:`). It skips
    removable, network and optical drives unless the check names them in
    `mounts`. It has no inodes, so `disk.inodes_used_pct` is absent.
  - macOS reports `/`, `/System/Volumes/Data` and any mounted volume under
    `/Volumes`. It skips the other APFS system volumes under
    `/System/Volumes` (`VM`, `Preboot`, `Update`, `xarts`, `iSCPreboot`,
    `Hardware`): they share one container and are no fuller than `/`.
    Pseudo filesystems such as `devfs` and `autofs` are skipped as on
    Linux.
- **network:** the default exclusion list gains macOS's virtual interfaces
  (`lo`, `utun`, `awdl`, `llw`, `gif`, `stf`, `bridge`, `anpi`, `ap`) and
  Windows's (loopback pseudo-interfaces, `isatap`, Teredo, the hidden Wi-Fi
  Direct adapters). As today, an explicit `interfaces` list bypasses it.
  Windows interface names contain spaces ("Ethernet 2") and are reported
  as they are.
- **uptime:** unchanged.
- **facts:** `os` is `windows` or `darwin`, so the automatic tags
  `os:windows` and `os:darwin` exist. An operator can therefore bind checks
  to Windows or Mac hosts through a monitoring group. `os_version` is the
  platform and its version as gopsutil reports them. Nothing else changes.

The seeded organization checks apply to every host, whatever its system.
cpu, memory, disk, network and uptime discover what each system has, so
they need no change. **(Claude's call:** the seed and its migration stay
as they are.) Two things follow from that, and the end-user docs say both:
- **The seeded `service` check watches `sshd`.**
  - On Windows that is the OpenSSH server's real service name. It reads
    `unknown` where OpenSSH server is not installed.
  - On macOS the job's label is `com.openssh.sshd`, so `sshd` reads
    `unknown` there.
  - No seeded rule judges `service.state`, so this raises no alert. It only
    shows as an unknown item.
  - An operator who wants it right binds a `service` check under the same
    key to a monitoring group for `os:darwin` or `os:windows`. That
    overrides the organization-wide one for those hosts.
- **A disk check that names mounts explicitly** behaves as today. A path
  that cannot be read on the host's system reports collection errors for
  that mount. On macOS or Windows, a Linux path such as `/var` may
  resolve to a real volume and report its numbers. Such a check belongs
  bound to an `os:linux` group.

## Build, release and CI

- `scripts/agent-binaries.sh` builds the six targets. Windows binaries are
  served as `pulsix-agent-windows-<arch>` (no `.exe` in the served name; the
  install script saves them with the extension). The manager image grows by
  the four binaries, roughly 60 MB. **(Claude's call.)**
- CI gains two jobs, on `windows-latest` and `macos-latest`. Each:
  - runs the unit tests of `internal/agent/...` and `cmd/agent` on that
    system;
  - runs `golangci-lint` for that system;
  - installs the agent with its install script against a stub that serves
    the script, the binary and the checksum. It then checks that the
    service is registered and running and that the env file's protection
    is right, and removes the agent again. The agent cannot enroll against
    a stub; it keeps retrying, and that is enough to show it runs as a
    service.
- The two jobs are ordinary jobs of `ci.yml`: a failure turns CI red, and
  so blocks the release workflow, whose guard requires green CI on
  `main`.
- They enforce no coverage floor of their own. Instead, every decision
  the platform code makes lives in files that compile on every system and
  run against fakes, so the Linux coverage gate counts it. That covers:
  - the state mapping tables;
  - the `launchctl list` parser;
  - the env-file parser and its permission rules;
  - the hostname normalization;
  - the log rotation;
  - the service handler's state sequence, behind a small interface.

  Only the thin system calls stay in `_windows.go` and `_darwin.go` files:
  the service manager API, the access-list calls and running `launchctl`.
  The runner jobs test those. **(Claude's call.)**
- The macOS code is also tested locally by `make test` on the developer's
  Mac.
- Real enrollment against a real manager on Windows and macOS is not
  automated: the Linux e2e gate covers the manager side, which does not
  depend on the agent's system. Before release, a person runs the steps by
  hand on one Windows and one macOS machine; the end-user docs carry the
  steps.

## Documentation

- `docs/BACKLOG.md`: 3.2, 3.3 and 3.8 are done. New rows: signing and
  notarization of the agent binaries, and an uninstall command.
- `docs/upgrades.md`: agents for Windows and macOS, the new install script,
  and the larger manager image. The agent's new flags are additive. Linux
  agents need no update.
- End-user docs (noix-docs, as always without naming the tech stack):
  - the agent install page gets Windows and macOS sections, with the
    removal steps;
  - `reference/check-plugins.md` gets the service names per system and the
    metrics absent on Windows;
  - the changelog.

## Left to the builder

- How the Windows service handler and the foreground mode share `run`.
- The stub server used by the CI install jobs.
- Exact Windows interface names in the exclusion list, checked against what
  gopsutil reports on the CI runner.
- How the Windows access list is built and checked (`golang.org/x/sys/windows`
  is already a dependency).
