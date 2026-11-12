# Agents on Windows and macOS, with service checks — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The Pulsix agent installs, enrolls and reports on Windows (as a
Windows service) and macOS (as a launchd daemon). The `service` check
watches Windows services and launchd jobs.

**Architecture:**
- **Decisions in portable files:** every decision lives in files that
  compile on every system and are tested with fakes there (backlog
  3.2/3.3/3.8). This covers env-file parsing, hostname normalization,
  the protection rules, log rotation, the service-handler state sequence,
  the service state tables and the `launchctl` parser.
- **Thin system adapters:** only the calls into the system live in
  `_windows.go` and `_darwin.go` files: the service manager, access lists
  and `exec launchctl`.
- **Installers:** `install.sh` learns macOS. A new `install.ps1` serves
  Windows. Both are rendered by `internal/agentdist`, so the manager and
  the CI install stub share one renderer.

**Tech Stack:**
- Go 1.27.3 with `golang.org/x/sys/windows` (incl. `svc`, `svc/mgr`),
  already a dependency.
- gopsutil v4.
- POSIX sh and Windows PowerShell 5.1.
- React/Vitest for the dialog.
- GitHub Actions `windows-latest` and `macos-latest`.

**Spec:** `docs/superpowers/specs/2026-11-12-windows-macos-agents-design.md`

## Global Constraints

- Everything in the repo is English; public docs (noix-docs) never name the tech stack.
- Conventional Commits, footer `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. No push.
- Never one whole-module `go test ./...`. Run single packages with `-race -timeout 900s`, or `make test`. `golangci-lint run ./...` must pass before every commit.
- Subagents run no `git stash` or other state-changing git beyond their own commits.
- Only touch Docker containers you start yourself; this plan needs none.
- Never install the agent as a service on the developer's Mac or edit `/Library` there. Real installs happen only in the CI runner jobs.
- Names, verbatim:
  - Windows service `PulsixAgent`, display name `Pulsix agent`.
  - launchd label `dev.noix.pulsix-agent`, plist `/Library/LaunchDaemons/dev.noix.pulsix-agent.plist`.
  - Binaries:
    - Windows: `C:\Program Files\Pulsix\pulsix-agent.exe`.
    - Linux and macOS: `/usr/local/bin/pulsix-agent`.
  - Data directories:
    - Linux: `/etc/pulsix`.
    - macOS: `/Library/Application Support/Pulsix`.
    - Windows: `%ProgramData%\Pulsix`.
  - Logs:
    - macOS: `/Library/Logs/Pulsix/agent.log`.
    - Windows: `%ProgramData%\Pulsix\logs\agent.log`.
- Log rotation at 10 MiB (10485760 bytes) to `<file>.1`. Windows service stop wait: 20 s. Recovery: restart after 5 s.
- Served binary names: `pulsix-agent-<goos>-<arch>`, no `.exe`. Targets: `linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64`.
- Linux behaviour does not change (unit file, data dir, journald), except that the default agent name is now normalized.
- API changes are additive only.

## Review Focus

1. **An env file written by PowerShell 5.1.** It may start with a UTF-8 BOM and end its lines with CRLF. The agent must read `PULSIX_MANAGER_ADDR` from it unchanged (Task 1 test), and `install.ps1` writes without a BOM (Task 8).
2. **A hostname with no usable characters, or with non-ASCII ones.**
   - `MÜLLER-PC` becomes `m-ller-pc`.
   - `---.local` leaves nothing, and the agent gives the explicit-name error rather than enrolling as an empty name (Task 1 test).
3. **Re-running an installer over a running agent (an upgrade).** Windows locks a running exe, so the script stops the service and waits for *Stopped* before copying. macOS boots the daemon out first. The CI smoke runs each installer twice (Task 10).
4. **A service stop while the agent's loop ignores cancellation** (stuck in a dial). The handler reports *stopped* after 20 s anyway (Task 4 test with an injected wait).
5. **Rotating a log file that is open on Windows.** Windows cannot rename an open file, so the writer closes before it renames and reopens after. The Task 3 test runs on the Windows CI job.

---

### Task 1: Portable agent configuration: env file, default data dir, hostname normalization

**Files:**
- Create: `internal/agent/envfile.go`, `internal/agent/envfile_test.go`
- Create: `internal/agent/agentname.go`, `internal/agent/agentname_test.go`
- Modify: `internal/agent/config.go` (default data dir and default name)
- Modify: `internal/agent/config_test.go`

**Interfaces:**
- Produces:
  - `func ParseEnvFile(r io.Reader) (map[string]string, error)`
  - `func MergeEnv(env []string, file map[string]string) []string`. The process environment wins and file keys are appended.
  - `func DefaultDataDir(goos, programData string) string`
  - `func normalizeAgentName(hostname string) (string, bool)`
  - `Config` gains `DataDirIsDefault bool`, true when `PULSIX_DATA_DIR` was unset.

- [ ] **Step 1: Write failing tests.**
  - `TestParseEnvFile` (table):
    - `"A=1\nB=2\n"` gives `{A:1,B:2}`.
    - `"\uFEFFPULSIX_MANAGER_ADDR=m:8444\r\nX=y\r\n"` gives `{PULSIX_MANAGER_ADDR:"m:8444", X:"y"}`.
    - `"# c\n\n  \nK=a=b\n"` gives `{K:"a=b"}`.
    - `"K=\"q\"\n"` gives `{K:"\"q\""}` (no quote stripping).
    - `"novalue\n"` is an error naming line 1.
    - `"=v\n"` is an error.
  - `TestMergeEnvProcessWins`: `MergeEnv([]string{"A=env"}, {"A":"file","B":"file"})` contains `A=env` and `B=file`, and `A` appears once.
  - `TestDefaultDataDir` covers:
    - `linux` gives `/etc/pulsix`;
    - `darwin` gives `/Library/Application Support/Pulsix`;
    - `windows` with `C:\ProgramData` gives `C:\ProgramData\Pulsix`;
    - `windows` with `""` gives `C:\ProgramData\Pulsix`;
    - `freebsd` gives `/etc/pulsix`.

    Join with `\` for Windows explicitly; do not use `filepath`, which is host-dependent.
  - `TestNormalizeAgentName` (table, output then ok):

    | Input | Output | ok |
    |---|---|---|
    | `WIN-SRV01` | `win-srv01` | true |
    | `Johns-MacBook-Pro.local` | `johns-macbook-pro` | true |
    | `host.example.com.` | `host.example.com` | true |
    | `MÜLLER-PC` | `m-ller-pc` | true |
    | `my_box` | `my-box` | true |
    | `---.local` | `""` | false |
    | `web-01` | `web-01` | true (unchanged) |

    Check every ok output against the hosts pattern `^[a-z0-9]([a-z0-9.-]{0,252}[a-z0-9])?$`.

    Treat a multi-byte rune as a single `-`.
  - In `config_test.go`:
    - `TestLoadDefaultNameIsNormalizedHostname`: inject the hostname through a package-level-free seam, an unexported `hostnameFunc` field on a loader or a `loadWith(env, hostname func() (string, error), goos, programData string)`. Hostname `WIN-SRV01` gives `AgentName == "win-srv01"`.
    - An explicit `PULSIX_AGENT_NAME=Mixed_Case` is kept verbatim.
    - Hostname `---` gives the existing "set the agent name explicitly" error.
    - `DataDirIsDefault` is true without `PULSIX_DATA_DIR` and false with it.
- [ ] **Step 2: Run** `go test -race -timeout 900s ./internal/agent/ -run 'EnvFile|MergeEnv|DefaultDataDir|NormalizeAgentName|LoadDefault'`. Expected: FAIL (undefined).
- [ ] **Step 3: Implement.**
  - `Load(env)` keeps its signature and calls `loadWith(env, os.Hostname, runtime.GOOS, os.Getenv("ProgramData"))`.
  - The 64-rune certificate check applies to the normalized name.
  - Keep `config.go` under ~400 lines by putting the new helpers in the new files.
- [ ] **Step 4: Run** the same command, then `go test -race -timeout 900s ./internal/agent/...`. Expected: PASS.
- [ ] **Step 5: Run** `golangci-lint run ./internal/agent/...`, then commit with `feat(agent): env file, per-system data directory and a hostname-safe default name`.

### Task 2: Protection of the env file and the Windows data directory

**Files:**
- Create: `internal/agent/protect.go`, the portable rules and `ensureDataDir`.
- Create: `internal/agent/protect_unix.go` (`//go:build !windows`).
- Create: `internal/agent/protect_windows.go`.
- Create: `internal/agent/protect_test.go` (portable, with fakes).
- Create: `internal/agent/protect_windows_test.go`, which runs only on the Windows CI job against a real temp dir.
- Modify: `internal/agent/enroll.go:54-58`, which calls `ensureDataDir` instead of the bare `MkdirAll`.

**Interfaces:**
- Consumes: `Config.DataDirIsDefault` (Task 1).
- Produces:
  - `type protector interface`:
    ```go
    type protector interface {
        // Loose reports who besides the allowed principals can access path; "" means none.
        Loose(path string) (string, error)
        // Protect makes path accessible to the allowed principals only.
        Protect(path string) error
    }
    ```
  - `func newProtector() protector`:
    - On Unix, `Loose` on a **file** reports `owner uid N` when the owner is not `os.Geteuid()`, and `mode 0xxx` when `mode&0o077 != 0`. On a **directory** it reports `""`, so the Unix data dir behaviour is unchanged. `Protect` is `chmod 0700`.
    - On Windows, `Loose` lists the trustees of allow ACEs other than SYSTEM (`S-1-5-18`) and Administrators (`S-1-5-32-544`) that grant any read or write right, plus a missing protected flag. `Protect` sets a protected DACL with full control for those two SIDs only (`SetNamedSecurityInfo` with `PROTECTED_DACL_SECURITY_INFORMATION`).
  - `func CheckEnvFile(p protector, path string) error`:
    - not a regular file, or a symlink: error;
    - `Loose` non-empty: error naming the path and what was found.
  - `func ensureDataDir(p protector, dir string, isDefault bool, log *slog.Logger) error`:
    1. `MkdirAll(dir, 0o700)`.
    2. Call `Loose(dir)`. If it reports loose:
       - default dir: `Protect`, and log `"tightened the data directory's access"` with dir and found;
       - custom dir: return `ErrSetupFailed` wrapping `"data directory %s is accessible to %s; restrict it or unset PULSIX_DATA_DIR"`.
    3. Call `Loose` again after `Protect`. Still loose is `ErrSetupFailed`.
  - Ruling carried from the spec: on macOS, "root" means the agent's effective user, which is root under launchd.

- [ ] **Step 1: Write failing portable tests with a fake protector** (a map of path to loose string, and recorded `Protect` calls):
  - `TestEnsureDataDirTightensTheDefault`: the fake reports `"BUILTIN\\Users"` once and `""` after Protect. The result is nil, Protect was called once, and the log line is present.
  - `TestEnsureDataDirRefusesALooseCustomDir`: the error `errors.Is(err, ErrSetupFailed)`, its text contains the dir and `BUILTIN\Users`, and Protect was never called.
  - `TestEnsureDataDirFailsWhenProtectDidNotStick`: Protect succeeds but the dir stays loose, giving `ErrSetupFailed`.
  - `TestCheckEnvFile`: a dir path is an error, a symlink is an error, loose is an error with the found text, tight is nil.
  - Unix-only `TestUnixProtectorFileModes` (`//go:build !windows`):
    - a temp file with 0600 gives `""`;
    - 0640 gives `"mode 0640"`;
    - a directory with 0755 gives `""`.
- [ ] **Step 2: Run** `go test -race -timeout 900s ./internal/agent/ -run 'EnsureDataDir|CheckEnvFile|UnixProtector'`. Expected: FAIL.
- [ ] **Step 3: Implement** the portable file, the Unix and Windows adapters, and the enroll.go call (`ensureDataDir(newProtector(), l.cfg.DataDir, l.cfg.DataDirIsDefault, l.log)`). Inject the protector through `Loop` only if the existing fakes need it; otherwise call `newProtector()`.

  Write `protect_windows_test.go` (`//go:build windows`):
  1. Create a temp dir. It inherits a loose list, and `Loose` reports non-empty.
  2. `Protect` it; `Loose` then reports `""`.
  3. Write a file inside; `Loose` on the file reports `""`, because it inherits the protected list.
- [ ] **Step 4: Run** the step 2 command, `go test -race -timeout 900s ./internal/agent/...`, and `GOOS=windows go vet ./internal/agent/...`. Expected: PASS and vet clean.
- [ ] **Step 5: Run** lint, then commit with `feat(agent): protect the env file and the Windows data directory`.

### Task 3: Rotating log file

**Files:**
- Create: `internal/agent/logfile/logfile.go`, `internal/agent/logfile/logfile_test.go`

**Interfaces:**
- Produces:
  - `func Open(path string, maxBytes int64) (*Writer, error)`. It creates the parent dir with 0o700, opens in append mode and counts the existing size.
  - `(*Writer).Write(p []byte) (int, error)`, safe for concurrent use. When `size+len(p) > maxBytes` and `size > 0`, it first:
    1. closes the file;
    2. removes `path+".1"`;
    3. renames `path` to `path+".1"`;
    4. reopens.
  - `(*Writer).Close() error`.
  - `const DefaultMaxBytes = 10 << 20`.

- [ ] **Step 1: Write failing tests:**
  - `TestWriterRotatesAtTheLimit`: with max 100, write 60 then 60 bytes. `path.1` holds the first 60 and `path` the second 60.
  - `TestWriterReplacesAnOlderRotation`: rotating twice leaves exactly `path` and `path.1`.
  - `TestWriterCountsAnExistingFile`: pre-create 90 bytes, Open with max 100, write 20. That rotates.
  - `TestWriterCreatesTheParentDirectory`.
  - `TestWriterConcurrentWrites`: 8 goroutines × 100 writes under `-race`, with no lost bytes across both files.
  - `TestALineLargerThanTheLimitIsStillWritten`: a 150-byte write at max 100 lands whole in a fresh file.
- [ ] **Step 2: Run** `go test -race -timeout 900s ./internal/agent/logfile/`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** it again. Expected: PASS.
- [ ] **Step 5: Run** lint, then commit with `feat(agent): a size-rotated log file`.

### Task 4: `cmd/agent`: flags, env file, log file and the Windows service handler

**Files:**
- Modify: `cmd/agent/main.go`
- Create: `cmd/agent/service.go` (portable state sequence), `cmd/agent/service_test.go`
- Create: `cmd/agent/service_windows.go` (`svc.Run` adapter), `cmd/agent/platform_other.go` (`//go:build !windows`)
- Create: `cmd/agent/main_test.go`, or extend it if it exists

**Interfaces:**
- Consumes:
  - `agent.ParseEnvFile`, `agent.MergeEnv`, `agent.CheckEnvFile` and the protector (Task 1/2). Export `agent.NewProtector()` if `cmd` needs it, or wrap the check as `agent.LoadEnvFile(path string) (map[string]string, error)`, which does the check and the parse; prefer the latter.
  - `logfile.Open` (Task 3).
- Produces:
  - `func run(args, env []string) int`. It parses `--env-file` and `--log-file` with a fresh `flag.FlagSet` (ContinueOnError; an unknown flag gives exit 2). With `--env-file` it calls `LoadEnvFile`, then `MergeEnv`. With `--log-file` the slog text handler writes to the logfile writer; otherwise it writes to stderr as today. It then runs the existing body.
  - `func runAgent(ctx context.Context, cfg agent.Config) int`: the existing loop part, exit codes 0/1/2.
  - Portable `serve`:
    ```go
    type svcState int
    const (svcStartPending svcState = iota; svcRunning; svcStopPending; svcStopped)
    // serve runs body under a service manager: it reports svcRunning once body has
    // started, and on a stop request cancels body's ctx, reports svcStopPending and
    // returns body's exit code once it returns, or 1 after stopWait if it does not.
    func serve(stop <-chan struct{}, report func(svcState), body func(context.Context) int, stopWait time.Duration) uint32
    ```
  - `service_windows.go`:
    - `func runPlatform(args, env []string) int` checks `svc.IsWindowsService()`.
    - When true, it calls `svc.Run("PulsixAgent", handler)`. The handler's `Execute` translates `svc.Stop`/`svc.Shutdown` into `stop` and `svcState` into `svc.Status`, and returns the exit code from `serve` as `(false, code)`.
    - Otherwise it calls `run`.
  - `platform_other.go`: `runPlatform` simply calls `run`.
  - `main` calls `os.Exit(runPlatform(os.Args[1:], os.Environ()))`.

- [ ] **Step 1: Write failing tests:**
  - `TestServeReportsRunningThenStopped`: body blocks on ctx. After a stop request, reports are `[svcRunning, svcStopPending, svcStopped]` and the code is 0.
  - `TestServeGivesUpAfterStopWait`: body ignores ctx (blocks on a channel never closed during the test, closed in cleanup); stopWait is 50ms. `serve` returns 1 within 1s and the last report is `svcStopped`.
  - `TestServePassesTheExitCode`: body returns 2 without a stop. The code is 2 and the last report is `svcStopped`.
  - `TestRunReadsTheEnvFile`: write a temp env file (0600) with `PULSIX_MANAGER_ADDR=` an invalid value `http://x`. `run([]string{"--env-file", f}, nil)` returns 2. This proves the file was read; without it the error is "is required".

    Assert on the log through `--log-file` pointed at a temp path, whose content contains `must be host:port`.
  - `TestRunRefusesALooseEnvFile` (`//go:build !windows`): mode 0644 returns 2, and the log names the file.
  - `TestRunUnknownFlag`: returns 2.
- [ ] **Step 2: Run** `go test -race -timeout 900s ./cmd/agent/`. Expected: FAIL.
- [ ] **Step 3: Implement.** Then check `GOOS=windows go build ./cmd/agent` and `GOOS=darwin go build ./cmd/agent`.
- [ ] **Step 4: Run** the tests and both cross builds. Expected: PASS.
- [ ] **Step 5: Run** lint, then commit with `feat(agent): env and log file flags, and running as a Windows service`.

### Task 5: `service` plugin on Windows and macOS

**Files:**
- Modify: `internal/agent/checks/service.go`. The lister seam changes to per-unit results.
- Create: `internal/agent/checks/service_states.go`, the portable tables and the launchctl parser, with `service_states_test.go`.
- Modify: `internal/agent/checks/service_linux.go` (adapter onto the new seam).
- Create: `internal/agent/checks/service_windows.go`, `service_darwin.go`, `service_windows_test.go`, `service_darwin_test.go`.
- Modify: `internal/agent/checks/service_stub.go`. Its build tag becomes `!linux && !windows && !darwin`.
- Modify: `service_stub_test.go` and `service_test.go` to match the tag and the seam.

**Interfaces:**
- Produces:
  - The new seam:
    ```go
    type unitResult struct { Label string; Err error }
    type unitLister interface {
        // States looks every unit up; a whole-lookup failure is the error,
        // a per-unit failure is that unit's Err. Keys are the names as requested.
        States(ctx context.Context, units []string) (map[string]unitResult, error)
    }
    ```
    `Collect` reports for each requested unit in request order:
    - a label when `Err` is nil;
    - an error observation for that unit when `Err` is set;
    - `unknown` when the unit has no key.

    A whole-lookup error keeps today's behaviour: every unit gets an error observation and the error is returned.
  - `func systemdLabel(loadState, activeState string) string`, renamed from `serviceStateLabel` with its body unchanged.
  - `func windowsLabel(state, win32ExitCode uint32) string`. The local constants are stopped=1, startPending=2, stopPending=3, running=4, continuePending=5, pausePending=6, paused=7.

    | Input | Label |
    |---|---|
    | running | `active` |
    | startPending, continuePending | `activating` |
    | stopped with exit 0 or 1077 | `inactive` |
    | stopped with any other exit (incl. 1066) | `failed` |
    | everything else | `unknown` |
  - `type launchdJob struct { PID string; Status string }`.
  - `func parseLaunchctlList(out []byte) (map[string]launchdJob, error)`:
    - The first non-empty line must be the header `PID\tStatus\tLabel`; otherwise it is an error.
    - Each further line splits on tabs into exactly 3 fields; malformed lines are skipped.
  - `func launchdLabel(job launchdJob, found bool) string`:

    | Job | Label |
    |---|---|
    | not found | `unknown` |
    | PID not `-` | `active` |
    | PID `-` and Status `0` | `inactive` |
    | anything else | `failed` |
  - The Windows adapter connects with `windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)` once per call, then opens each unit with `SERVICE_QUERY_STATUS` and `QueryServiceStatus`:
    - `ERROR_SERVICE_DOES_NOT_EXIST` gives `Label "unknown"`;
    - any other error is that unit's `Err`.
  - The Darwin adapter runs `exec.CommandContext(ctx, "/bin/launchctl", "list")` and feeds the output through `parseLaunchctlList`. Labels are compared exactly.

- [ ] **Step 1: Write failing tests:**
  - `TestWindowsLabel` (table over every row above, incl. stopped+1066 giving `failed` and paused giving `unknown`).
  - `TestParseLaunchctlList` uses the fixture

    ```
    "PID\tStatus\tLabel\n123\t0\tcom.a\n-\t0\tcom.b\n-\t78\tcom.c\n-\t-9\tcom.d\nbroken line\n"
    ```

    It gives 4 jobs. A missing header is an error, and empty output is an error.
  - `TestLaunchdLabel` gives:
    - `com.a` → `active`;
    - `com.b` → `inactive`;
    - `com.c` → `failed`;
    - `com.d` → `failed`;
    - absent → `unknown`.
  - `TestServiceCollectPerUnitError`: the fake lister returns `{"a": {Label: "active"}, "b": {Err: errors.New("access denied")}}` for units `[a b c]`. That gives a label for `a`, an error observation for `b` and `unknown` for `c`, and Collect returns a nil error.
  - `TestServiceCollectKeepsTheRequestedName`: units `["W3svc"]` with the fake keyed `"W3svc"` give the item `"W3svc"`.
  - Keep today's whole-failure test, adapted to the seam.
  - `service_darwin_test.go` (`//go:build darwin`) runs the real lister against `com.apple.launchd.peruser.0`-independent names. It asserts that a made-up label `dev.noix.does-not-exist` gives `unknown` and that the call itself succeeds. It runs as an ordinary user, so it only sees that user's domain, which is enough for this assertion.
  - `service_windows_test.go` (`//go:build windows`):
    - `EventLog` gives `active`;
    - `NoSuchSvc12345` gives `unknown`;
    - `eventlog` in lower case gives `active` under the item `eventlog`.
- [ ] **Step 2: Run** `go test -race -timeout 900s ./internal/agent/checks/ -run 'WindowsLabel|Launchctl|LaunchdLabel|ServiceCollect'`. Expected: FAIL.
- [ ] **Step 3: Implement.** Check that the build works for each of `linux`, `windows`, `darwin` and `freebsd` with `GOOS=<os> go vet ./internal/agent/checks/`.
- [ ] **Step 4: Run** `go test -race -timeout 900s ./internal/agent/checks/` (on this Mac that includes the darwin adapter test) and the vets. Expected: PASS.
- [ ] **Step 5: Run** lint, then commit with `feat(agent): service checks for Windows services and launchd jobs`.

### Task 6: System plugins on Windows and macOS

**Files:**
- Modify: `internal/agent/checks/cpu.go`, `disk.go`, `network.go`, and their `_test.go` files.
- Create: `internal/agent/checks/platform.go`, the portable per-system rules.
- Create: `internal/agent/checks/drive_windows.go` and `drive_other.go`, the drive-type seam.

**Interfaces:**
- Produces:
  - `func newCPUPluginFor(goos string) cpuPlugin`. On `windows` it has `Load: nil`. `newCPUPlugin()` calls it with `runtime.GOOS`.
  - In `diskPlugin`:
    - a new field `GOOS string` (empty means `runtime.GOOS`);
    - a field `DriveKind func(mount string) string`, returning `"fixed"`, `"removable"`, `"remote"`, `"cdrom"` or `"other"`. On Windows it is `windows.GetDriveType(mount+`\`)`; elsewhere it is nil.
  - `func discoveredMount(goos string, p disk.PartitionStat, driveKind func(string) string) bool`:
    - **linux:** unchanged (the pseudo-filesystem filter).
    - **darwin:**
      - Skip `devfs`, `autofs` and `nullfs` as well as the Linux pseudo types.
      - Skip any mountpoint under `/System/Volumes/` except exactly `/System/Volumes/Data`.
      - Keep `/` and anything under `/Volumes/`.
      - Skip every other mountpoint (e.g. `/private/var/vm`).
    - **windows:** keep only when `driveKind(mount) == "fixed"`.
  - `func reportsInodes(goos string) bool`, false on windows. When false, `disk.inodes_used_pct` is neither a value nor an error.
  - `func excludedInterfacePrefixesFor(goos string) []string`:
    - linux: today's list, unchanged.
    - darwin: `lo utun awdl llw gif stf bridge anpi ap`.
    - windows: `Loopback isatap Teredo 6TO4 Local Area Connection*`.

    `discoveredInterface` takes the list as an argument. The plugin holds the list, built from `runtime.GOOS`.
  - Explicit `mounts` and `interfaces` params bypass all of this, as today.

- [ ] **Step 1: Write failing tests:**
  - `TestCPUOnWindowsReportsNoLoad`: `newCPUPluginFor("windows").Load == nil`, and Collect with a fake Percent yields only `cpu.used_pct`.
  - `TestCPUOnDarwinKeepsLoad`.
  - `TestDiscoveredMountDarwin` (table) keeps `/`, `/System/Volumes/Data` and `/Volumes/Backup`, and skips:
    - `/System/Volumes/VM`, `/System/Volumes/Preboot` and `/System/Volumes/Update/mnt1`;
    - `/dev`, which has fstype `devfs`;
    - `/private/var/vm`.
  - `TestDiscoveredMountWindows` with a fake driveKind: `C:` fixed is kept, `D:` cdrom is skipped, `Z:` remote is skipped.
  - `TestDiskOnWindowsOmitsInodes`: with GOOS `windows`, the fake partitions `C:` and the fake usage give exactly the `disk.used_pct` and `disk.free_bytes` observations.
  - `TestDiskExplicitMountsBypassTheWindowsFilter`.
  - `TestExcludedInterfacesPerSystem`:
    - darwin skips `utun3`, `awdl0` and `bridge100` but keeps `en0`;
    - windows skips `isatap.{GUID}` and `Local Area Connection* 2` but keeps `Ethernet 2`;
    - linux keeps `ap0`, because that prefix is darwin-only.
  - `TestDiskLiveDiscoveryOnThisMac` (`//go:build darwin`) runs the real plugin without params. Each item is `/`, `/System/Volumes/Data` or under `/Volumes/`.
- [ ] **Step 2: Run** `go test -race -timeout 900s ./internal/agent/checks/`. Expected: FAIL.
- [ ] **Step 3: Implement.** Check the vets on `GOOS=windows` and `GOOS=darwin`.
- [ ] **Step 4: Run** the step 2 command and the vets. Expected: PASS.
- [ ] **Step 5: Run** lint, then commit with `feat(agent): system checks report what Windows and macOS can measure`.

### Task 7: macOS install: plist, `install.sh`, six binary targets, shared renderer

**Files:**
- Create: `deploy/agent/launchd/dev.noix.pulsix-agent.plist`, `internal/agentdist/files/dev.noix.pulsix-agent.plist` (copy).
- Create: `internal/agentdist/render.go`, `render_test.go`. Move the rendering out of `internal/api/agent_install.go`.
- Modify: `internal/agentdist/embed.go` and `embed_test.go` (drift test covers the plist).
- Modify: `internal/agentdist/files/install.sh`.
- Modify: `scripts/agent-binaries.sh` (the six targets).
- Modify: `internal/api/agent_install.go` and its tests, so they use the renderer.

**Interfaces:**
- Produces:
  - `func Plist() []byte`.
  - `func RenderShellInstaller(publicURL string) ([]byte, error)`. It embeds both the unit and the plist; each is checked against its own heredoc delimiter, `PULSIX_UNIT_EOF` and `PULSIX_PLIST_EOF`. The template fields are `BaseURL`, `Unit` and `Plist`.
  - `internal/api` calls `agentdist.RenderShellInstaller(deps.PublicURL)`. The api's existing tests keep passing, adapted to the move.
- The plist content:
  - Label `dev.noix.pulsix-agent`.
  - ProgramArguments `/usr/local/bin/pulsix-agent`, `--env-file`, `/Library/Application Support/Pulsix/agent.env`, `--log-file`, `/Library/Logs/Pulsix/agent.log`.
  - `RunAtLoad` true, `KeepAlive` dict `SuccessfulExit` false, `ThrottleInterval` 5, `ProcessType` Background.
- `install.sh` changes:
  - It branches on `uname -s`:
    - `Linux`: today's path, unchanged.
    - `Darwin`: the path in the spec's "macOS: install.sh" section.
    - Anything else: `die "unsupported system <x> (Linux, macOS and Windows have agents; on Windows use install.ps1)"`.
  - The `stat` call differs by system: `stat -c '%u %a'` on Linux, `stat -f '%u %Lp'` on Darwin.
  - The checksum command differs: `sha256sum`, or `shasum -a 256` when that is missing.
  - On Darwin it does `launchctl bootout system/dev.noix.pulsix-agent 2>/dev/null || true`, then `launchctl bootstrap system <plist>`.
  - It writes the plist with `chown root:wheel` and `chmod 644`.

- [ ] **Step 1: Write failing tests:**
  - `TestEmbeddedFilesMatchDeploySources` now covers the plist.
  - `TestPlistContent` parses the plist with `encoding/xml` token reading, or with string checks for each key and value above, and checks `plutil -lint`-valid shape: XML header and DOCTYPE.
  - `TestRenderShellInstallerEmbedsUnitAndPlist`: the output contains the unit's `ExecStart=` line and the plist's `<string>dev.noix.pulsix-agent</string>`, and no `{{`.
  - `TestRenderShellInstallerRefusesADelimiterInThePlist`: drive this through an unexported `renderShellInstaller(script, unit, plist []byte, url string)`.
  - `TestInstallScriptKnowsDarwin`: the rendered script contains `Darwin)`, `shasum -a 256`, `stat -f` and `launchctl bootstrap system`.
  - `TestAgentBinariesScriptFillsThisDist`: extend it to expect the six names.
  - A new `TestInstallScriptSyntax` runs `sh -n` on the rendered script; skip it when `sh` is missing.
- [ ] **Step 2: Run** `go test -race -timeout 900s ./internal/agentdist/` and `go test -race -timeout 1800s ./internal/api/ -run Install`. Expected: FAIL.
- [ ] **Step 3: Implement.**
  - Run `plutil -lint` on the deploy plist locally (macOS), and `shellcheck` on `install.sh` if it is installed.
  - Run `scripts/agent-binaries.sh`, check the six files and their checksums, then restore the dist to its committed placeholder state (`git status` clean apart from your changes).
- [ ] **Step 4: Run** both commands from step 2. Expected: PASS.
- [ ] **Step 5: Run** lint, then commit with `feat(agent): install on macOS as a launchd daemon, and build the Windows and macOS binaries`.

### Task 8: Windows install: `install.ps1`, route and API field

**Files:**
- Create: `internal/agentdist/files/install.ps1` and `deploy/agent/windows/install.ps1`. The manager serves the copy in `internal/agentdist/files/`; the copy under `deploy/agent/windows/` is the source, and the drift test keeps them equal.
- Modify: `internal/agentdist/embed.go`, `render.go` and the tests.
- Modify:
  - `internal/api/agent_install.go`: the new `GET /install.ps1` route, with the same limiter and the same 503 when the public URL is missing.
  - `internal/api/agent_handlers.go`: `InstallCommandWindows`.
  - The api tests.
- Modify: `web/src/api/schema.d.ts`, regenerated the way the repo does it. See `package.json`/Makefile; CI checks it for drift.

**Interfaces:**
- Produces:
  - `func RenderPowerShellInstaller(publicURL string) ([]byte, error)`, with the template field `BaseURL`.
  - The DTO field `InstallCommandWindows string` with JSON `install_command_windows` and doc `"Run this in PowerShell as administrator on a Windows host"`.
  - `func agentInstallCommandWindows(deps Deps, nameHint string) string` produces exactly:

    ```
    [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor 3072; & ([scriptblock]::Create((irm <base>/install.ps1))) -Manager <addr>
    ```

    It appends ` -ServerName <name>` under the same condition as the shell command, then ` -Name <hint>`. It uses the same base and addr fallbacks.
  - The route serves `Content-Type: text/plain; charset=utf-8` and `X-Content-Type-Options: nosniff`.
- `install.ps1` behaviour:
  - It follows spec steps 1–10, with `param([Parameter(Mandatory)][string]$Manager, [string]$ServerName, [string]$Name, [string]$Token, [string]$TokenFile)`.
  - It runs under `Set-StrictMode -Version 3` and `$ErrorActionPreference = 'Stop'`.
  - It writes `agent.env` with `[IO.File]::WriteAllText($path, $text, (New-Object Text.UTF8Encoding $false))`: LF line endings, no BOM.
  - It sets the ACL with `icacls "<dir>" /inheritance:r /grant:r "*S-1-5-18:(OI)(CI)F" "*S-1-5-32-544:(OI)(CI)F"`.
  - It creates the service with `sc.exe create PulsixAgent binPath= "\"C:\Program Files\Pulsix\pulsix-agent.exe\" --env-file \"<data>\agent.env\" --log-file \"<data>\logs\agent.log\"" start= delayed-auto DisplayName= "Pulsix agent"`. If the service exists, it uses `sc.exe config` with the same arguments instead.
  - It sets recovery with `sc.exe failure PulsixAgent reset= 86400 actions= restart/5000/restart/5000/restart/5000`.
  - When stopping an existing service, it waits up to 30 s for `Stopped`, then fails.
  - A `-TokenFile` is refused when its ACL grants read to `S-1-1-0` (Everyone), `S-1-5-11` (Authenticated Users) or `S-1-5-32-545` (Users).
  - It checks every `sc.exe` exit code.

- [ ] **Step 1: Write failing tests:**
  - `TestRenderPowerShellInstaller`: the output contains the base URL and `PulsixAgent`, and no `{{`.
  - `TestEmbeddedFilesMatchDeploySources` covers the ps1.
  - `TestInstallPs1Shape` checks string presence of: `Mandatory`, `-AsSecureString`, `UTF8Encoding $false`, `/inheritance:r`, `S-1-5-18`, `S-1-5-32-544`, `delayed-auto`, `restart/5000`, `Get-FileHash` and `ARM64`.
  - In the api tests:
    - `TestAgentInstallCommandWindows`, a table with the exact string, the server-name case and the hint case;
    - the token-create response carries `install_command_windows`;
    - `GET /install.ps1` answers 200 with the content type, and 503 without a public URL;
    - the shared rate limiter applies.
- [ ] **Step 2: Run** `go test -race -timeout 900s ./internal/agentdist/` and `go test -race -timeout 1800s ./internal/api/ -run 'Install|AgentToken'`. Expected: FAIL.
- [ ] **Step 3: Implement.** Regenerate `schema.d.ts`, then run `cd web && npx tsc -b`.
- [ ] **Step 4: Run** the step 2 commands. Expected: PASS.
- [ ] **Step 5: Run** lint, then commit with `feat(agent): install on Windows as a service with install.ps1`.

### Task 9: Add-agent dialog and service field hint

**Files:**
- Modify: `web/src/features/agents/AddAgentDialog.tsx` and its tests (`AgentsPage.test.tsx`, `AgentsPage.de.test.tsx`).
- Modify: `web/src/features/checks/CheckDialog.tsx`, which shows the units hint.
- Modify: the i18n `en`/`de` files (find them with `grep -rn "installCommand" web/src/i18n`).

**Interfaces:**
- Consumes: `res.install_command_windows` (Task 8).
- Produces:
  - Two labelled command blocks in the dialog:
    - en `Linux and macOS` / de `Linux und macOS`;
    - en `Windows (PowerShell as administrator)` / de `Windows (PowerShell als Administrator)`.
  - Each block has its own copy button, with aria-labels `Copy the Linux and macOS command` and `Copy the Windows command` (de: `Befehl für Linux und macOS kopieren`, `Befehl für Windows kopieren`). The existing `copyCommand` key is replaced by those two.
  - The units hint, under the field:
    - en `systemd units on Linux, service names on Windows, launchd labels on macOS — one per line`;
    - de `systemd-Units unter Linux, Dienstnamen unter Windows, launchd-Labels unter macOS – einer pro Zeile`.

- [ ] **Step 1: Write failing Vitest tests:**
  - After creating a token, both commands are shown under their labels.
  - Each copy button copies its own command: assert the copy mock is called with `install_command_windows` for the Windows button.
  - The German test shows the German labels.
  - The check dialog with the `service` plugin shows the hint.
- [ ] **Step 2: Run** `cd web && npx vitest run src/features/agents src/features/checks`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** the same command, then `npm run lint` and `npx tsc -b`. Expected: PASS.
- [ ] **Step 5: Commit** with `feat(web): the Add-agent dialog shows the Windows command`.

### Task 10: CI on Windows and macOS runners, with an install smoke

**Files:**
- Create: `internal/agentdist/cmd/installstub/main.go`, a test-only HTTP server (package main).
- Create: `scripts/ci/install-smoke-macos.sh` and `scripts/ci/install-smoke-windows.ps1`.
- Modify: `.github/workflows/ci.yml`. It gains the jobs `agent-windows` and `agent-macos`.

**Interfaces:**
- Consumes: `agentdist.RenderShellInstaller` and `RenderPowerShellInstaller` (Tasks 7, 8).
- `installstub`:
  - It takes `-addr` (default `127.0.0.1:18080`) and `-dist <dir>`.
  - It serves `/install.sh` and `/install.ps1`, both rendered with `http://<addr>`, and `/agents/bin/{os}/{arch}[.sha256]` from `-dist`. It prints `ready` once listening.
- Each job:
  1. Checks out the code, then sets up Go 1.27.3, pinning the actions with the same SHAs as the `go` job.
  2. Runs `go test -race -timeout 900s ./internal/agent/... ./cmd/agent/...`. If any package there needs Postgres, exclude only that package and say why in a comment.
  3. Runs `golangci-lint-action`, pinned with the same SHA and version as the `go` job.
  4. Builds the agent for the runner's own arch into `dist/` with its `.sha256` (shell: `shasum -a 256`; PowerShell: `Get-FileHash`, lower-cased, in the `"<hash>  <name>"` format).
  5. Starts `installstub` in the background, waits for `ready`, and runs the smoke script.
- The smoke scripts:
  - Write a root/admin-only token file holding a well-formed but unknown token, of the form `pxa1.<id>.<secret>.<pin>` (check its shape in `internal/agent`'s token parsing).
  - Install against manager `127.0.0.1:1`, then install a second time.
  - Assert:
    - **macOS:** `sudo launchctl print system/dev.noix.pulsix-agent` shows `state = running`. The data dir is mode 700 and owned by root, and `agent.env` is mode 600.
    - **Windows:** `(Get-Service PulsixAgent).Status -eq 'Running'`. `(Get-Acl $env:ProgramData\Pulsix).AreAccessRulesProtected` is true, and its rules hold only SYSTEM and Administrators.
    - **Both:** the log file exists and contains `pulsix-agent starting`.
  - Then remove everything:
    - **macOS:** `launchctl bootout`, and delete the plist, binary, data dir and logs.
    - **Windows:** `sc.exe stop`, `sc.exe delete`, and delete the folders.
  - Print the agent log on failure.

- [ ] **Step 1: Write** `installstub` with a Go test, `TestStubServesRenderedScriptsAndBinaries`, which uses `httptest` on the handler built by an exported-in-package `newHandler(base, dist string) http.Handler`.
- [ ] **Step 2: Run** `go test -race -timeout 900s ./internal/agentdist/...`. Expected: PASS.
- [ ] **Step 3: Write** the two smoke scripts and the CI jobs. Locally:
  - run `sh -n scripts/ci/install-smoke-macos.sh`;
  - run `actionlint` if it is installed;
  - check the YAML with `python3 -c 'import yaml,sys; yaml.safe_load(open(".github/workflows/ci.yml"))'`.

  Do **not** run the macOS smoke locally: it installs a system daemon.
- [ ] **Step 4: Run** lint, then commit with `ci: test and install the agent on Windows and macOS runners`.
  - Note in the report that the jobs are first exercised when the branch is pushed. That is the owner's call, so the controller asks before pushing.

### Task 11: Documentation

**Files:**
- Modify: `docs/BACKLOG.md`:
  - 3.2, 3.3 and 3.8 become done, with short notes and the spec path;
  - new rows for signing and notarization of the agent binaries, and for an uninstall command, both planned.
- Modify: `docs/upgrades.md`:
  - agents for Windows and macOS, the new `GET /install.ps1` and `install_command_windows` (additive);
  - the manager image grows by about 60 MB;
  - Linux agents need no update. The default agent name is now normalized, which affects only new enrollments.
- Modify the noix-docs scratch clone at `/private/tmp/claude-501/-Users-noidee--dev-pulsix/76b69f36-5040-49f9-a80e-08a1ef63bcdb/scratchpad/noix-docs-merge`. Work on a new branch `docs/pulsix-windows-macos-agents`, created from `docs/pulsix-synthetic-checks`. Edit:
  - the agent install page: Windows and macOS sections, the commands, where things live, and the removal steps;
  - `reference/check-plugins.md`: the service names per system, the metrics absent on Windows, the macOS disk volumes, and the `sshd` seed note;
  - `reference/changelog.md`: an Unreleased entry.

  The docs never name the tech stack: no Go, gopsutil, PowerShell-internals jargon beyond the commands themselves, systemd/launchd only where the user must type them.

- [ ] **Step 1: Write** the docs, then run `npm run build` in the docs clone if its dependencies are installed; otherwise check the links by hand.
- [ ] **Step 2: Commit** in both repositories: `docs: agents on Windows and macOS (backlog 3.2, 3.3, 3.8)` and `docs(pulsix): install the agent on Windows and macOS`.
