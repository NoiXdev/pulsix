# Custom Script Checks Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A `script` check plugin runs commands the host defines under `commands:` in `agent.yaml`, reads Nagios (exit code + perfdata) or JSON output, and reports status, message, exit code, duration and values; two seed rules take over the script's verdict.

**Architecture:** `internal/agent/secrets` parses `commands:` next to `secrets:`; the scheduler hands a `checks.CommandPlugin` a command lookup (like `SecretPlugin`'s secret); the plugin lives in `internal/agent/checks/plugins/script` (parsing, plugin, runner, program check split by file); the descriptor in `internal/checkplugins/script.go` drives manager validation, catalogue, API and SPA; seed rules plus a backfill migration.

**Tech Stack:** Go (agent and manager), goose SQL migration, sqlc untouched, React SPA (fixture, generated schema, i18n).

**Spec:** `docs/superpowers/specs/2026-11-14-script-checks-design.md`

## Global Constraints

- Command name pattern `^[A-Za-z0-9_.-]{1,64}$` (the secret-name pattern); argv 1–64 entries, each ≤ 4096 bytes, first entry an absolute path; ≤ 256 commands.
- Params: `commands` list (required, non-empty, ≤ 32 entries, `FormatCommandName`), `format` enum `nagios` (default) | `json`, `timeout_seconds` integer 1–60 default 10. `ItemParam` `commands`; `ItemEntry` cuts at the first `:`.
- Metrics: `script.status` (discrete: `ok` `warn` `crit` `unknown`), `script.output` (discrete), `script.exit_code`, `script.duration_seconds`, `script.value` (numeric). Value items are `<command>:<label>`.
- Output cap 64 KiB stdout, stderr discarded; ≤ 64 values per command; ≤ 4 commands of one check run at once; empty message → `(no output)`; label bound 512 bytes (`checks.MaxLabelLen`).
- Fixed texts: `command "<name>" is not defined on this host`; `timed out after <N>s`; `an earlier run of this command has not exited yet`.
- Errors, logs and observations never contain a command argument other than the program path or a checked file path, and never a secret value.
- Seed rules: "Script reports critical" (`script.status` in [`crit`], crit) and "Script reports warning" (`script.status` in [`warn`, `unknown`], warn), scope org, form mode.
- Repo rules: TDD; never `go test ./...` or `make test` — single packages with `-race -timeout 900s` (internal/api: `-timeout 1800s`); `golangci-lint run` clean on touched packages; Conventional Commits with footer `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`; no state-changing git besides your own commits (no stash, no checkout of other branches, no reset); temp files only in `/private/tmp/claude-501/-Users-noidee--dev-pulsix/76b69f36-5040-49f9-a80e-08a1ef63bcdb/scratchpad`. Never run the agent or an installer as a service on this Mac, never sudo. ~50 foreign Docker containers run here: touch none; the Postgres test container the test helpers start is fine.
- Everything in the repo English; public docs never name the tech stack.

## Review Focus

1. A manager-side value can never change what runs: only names cross the wire; argv comes from the file (Task 1, Task 3 tests).
2. A program or script file others can write is refused before it runs, including through a symlink and the interpreter-argument case (Task 4).
3. A timeout kills the whole tree; a child the script started does not keep the round or the pipe open (Task 4, real-process test).
4. The script's environment holds only the built variables — no token, no inherited secret (Task 4).
5. Hostile output (huge, binary, control characters, endless perfdata, deep JSON) yields bounded, reportable observations and never an item or label the manager's contract refuses (Task 3).

---

### Task 1: `commands:` in `agent.yaml`

**Files:** `internal/agent/secrets/parse.go` (+ a new `commands.go` if parse.go would pass ~400 lines), `set.go`, tests `parse_test.go` / new `commands_test.go`, `set_test.go`.

**Interfaces — Produces:** `func (s Set) Command(name string) (argv []string, ok bool)` (a copy); `func (s Set) CommandNames() []string` (sorted). `Set.String`/`LogValue` unchanged in spirit: names only (add command names, never argv).

- [ ] Tests first: top-level `commands:` accepted alone and next to `secrets:`; flow `[a, b]` and block sequences; refusals each with a line and fixed reason — relative program, empty sequence, 65 entries, 4097-byte entry, 257 commands, bad name, duplicate name, `commands` twice, mapping or nested sequence as entry, null entry, tag other than `!!str`, anchor. A refusal's error text never contains the offending argument (use a unique marker string in the argument and assert absence). The unknown-top-level-key reason now names both keys. `Command` returns a copy (mutating it does not change the set). RED → GREEN.
- [ ] Update the package doc comment (it reads the whole file: secrets and commands).
- [ ] `go test -race -timeout 900s ./internal/agent/secrets/...`, lint → commit `feat(agent): read host-defined commands from agent.yaml`.

### Task 2: Scheduler hands command plugins a lookup

**Files:** `internal/agent/checks/command.go` (new; mirrors `secret.go`), `scheduler.go`, `internal/agent/secrets_wiring.go` (+ where `WithSecrets` is passed to the scheduler), tests `internal/agent/checks/scheduler_command_test.go`, `internal/agent/secrets_wiring_test.go`.

**Interfaces — Produces:**
```go
// Commands is where a CommandPlugin looks a command up. ok is false when the
// file does not define name; problem then says why the file has no commands
// at all (missing, refused, broken), or is empty when only this one is missing.
type Commands interface {
	Command(name string) (argv []string, ok bool, problem string)
}
type CommandPlugin interface {
	Plugin
	CollectWithCommands(ctx context.Context, params map[string]any, commands Commands) ([]Observation, error)
}
func WithCommands(c Commands) SchedulerOption
```
- [ ] Tests first: the scheduler calls `CollectWithCommands` (never `Collect`) for a `CommandPlugin`, with the configured lookup; without `WithCommands` it passes a lookup that defines nothing and reports problem "" ; `internal/agent`'s source returns the watcher's argv and, when the file is absent or refused, the same problem texts `secretsSource` uses (share the helper). RED → GREEN.
- [ ] Wire `WithCommands(commandsSource{w})` wherever `WithSecrets` is wired, same watcher.
- [ ] `go test -race -timeout 900s ./internal/agent/...` (the agent packages only), lint → commit `feat(agent): give command plugins the host's commands`.

### Task 3: Script plugin — parsing and collection

**Files (new package `internal/agent/checks/plugins/script`):** `script.go` (Plugin, params, Collect logic), `nagios.go`, `jsonformat.go`, `label.go` (message cleaning, item building), `runner.go` (only the `runner`/`checker` interfaces and `runResult` here; the real implementations are Task 4), tests per file. Not registered yet.

**Interfaces — Produces (Task 4 implements, Task 5 registers):**
```go
type runResult struct {
	stdout    []byte        // at most maxOutput (64 KiB) bytes
	truncated bool          // the process wrote more than maxOutput
	exitCode  int           // valid when exited
	exited    bool          // ran to an exit; false when killed or timed out
	duration  time.Duration
}
// runner runs argv directly. Errors: errTimeout (ctx deadline; the tree was
// killed), errBusy (an earlier run of the same argv could not be stopped and
// has not exited), or a start failure.
type runner interface { Run(ctx context.Context, argv []string) (runResult, error) }
// checker refuses a file others could change; the error says why, without the path.
type checker interface { Check(path string) error }
func New() checks.Plugin  // the real runner and checker (Task 4 fills them; until then New may wire placeholders behind an unexported constructor used by tests)
func newPlugin(r runner, c checker) *Plugin
```
- [ ] Tests first, table-driven with a fake runner and checker:
  - exit code → status: 0 ok, 1 warn, 2 crit, 3 unknown, 4/255/-1 unknown, not exited unknown;
  - Nagios output: first line message; perfdata on line one and after `|` in later lines; quoted labels with spaces and `''`; units `s ms us % B KB MB GB TB c` normalised (1024ⁿ for bytes); garbage entries skipped; 65th value dropped; duplicate label keeps the first;
  - JSON: valid object; missing/unknown status, invalid JSON, non-number metric, non-object, `truncated` output → `unknown` with a fixed reason, never the script's text; extra keys ignored; deeply nested JSON does not panic;
  - message cleaning: invalid UTF-8 replaced, control chars and U+2028/2029 removed, trimmed, cut to 512 bytes on a rune boundary, empty → `(no output)`;
  - items `<command>:<label>`; one failing `checks.ReportableItem` or with an empty label skipped;
  - undefined command → `unknown` + `command "<name>" is not defined on this host` (+ the problem when present); checker refusal on the program and on an absolute-path argument that exists (fake checker answers per path) → `unknown`, output names the path and the reason; timeout → `timed out after <N>s`; busy text; start failure → `unknown`;
  - `exit_code` only when exited; `duration_seconds` always when a run happened;
  - at most 4 runs at once (fake runner counts concurrency), all commands reported;
  - params: absent `format` → nagios, absent `timeout_seconds` → 10 s deadline on the ctx given to the runner; defensive parsing of `[]any`.
  - Collected observations never contain an argv entry other than checked paths (marker test).
- [ ] Implement. `Name()` is `script`. The plugin implements `checks.CommandPlugin`; its plain `Collect` reports every listed command as `unknown` "commands are not available" (only reached if wired without the scheduler's lookup).
- [ ] `go test -race -timeout 900s ./internal/agent/checks/...`, lint → commit `feat(agent): parse Nagios and JSON output of host commands`.

### Task 4: Script plugin — real runner and program check

**Files:** `internal/agent/checks/plugins/script/exec.go`, `exec_unix.go`, `exec_windows.go` (process group / job object, environment), `check_unix.go`, `check_windows.go`, tests incl. `exec_test.go` (real processes; Unix scripts via `/bin/sh` in `t.TempDir()`, Windows via `cmd.exe /c` or PowerShell in CI), `check_unix_test.go`, `check_windows_test.go`.

**Interfaces — Consumes:** Task 3's `runner`, `checker`, `runResult`, `errTimeout`, `errBusy`.

- [ ] Tests first:
  - exit codes 0/2/7 read; stdout over 64 KiB → `truncated` and exactly 64 KiB kept; stderr not in stdout;
  - timeout: a script that starts a background child sleeping 60 s and itself sleeps → `errTimeout` within timeout + a few seconds, and the child is gone afterwards (Unix: write the child's pid to a file, then `kill -0` fails; Windows: the job killed it);
  - environment: a script printing its environment shows exactly the built variables (Unix `PATH`, `LANG`, `LC_ALL`; Windows the listed set) — set a marker variable in the test process first (`t.Setenv`) and assert it is absent;
  - working directory `/` (Windows: system drive root);
  - busy: a process the kill cannot stop (test hook like the smart runner's `noKill`) marks argv busy; the next Run returns `errBusy` until it exits;
  - checker Unix: group- or other-writable file refused; file owned by a non-root user refused (when the tests run as non-root, a temp file is exactly that: assert the refusal; the accept path for a root-owned file is tested against `/bin/sh` or `/usr/bin/env` resolved through `filepath.EvalSymlinks`, skipped if not root-owned); writable directory refused; not executable refused; directory/fifo refused; a symlink is resolved and its target checked (symlink in a temp dir to `/bin/sh` passes the target check — the link's own directory does not matter; spec: check the resolved file and its directory).
  - checker Windows (CI): `C:\Windows\System32\cmd.exe` passes; a file in a temp dir with the default user ACL is refused (owner is the test user / others can write).
- [ ] Implement per spec (Setpgid + kill of the group via `cmd.Cancel`; job object with `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`, assigned right after `Start`, closed on timeout; `WaitDelay` 2 s; the smart runner's `inflight` pattern for busy). Windows write mask: the same rights the installer's `Assert-RemovableBinDir` uses (FILE_WRITE_DATA, FILE_APPEND_DATA, FILE_WRITE_EA, FILE_DELETE_CHILD, FILE_WRITE_ATTRIBUTES, DELETE, WRITE_DAC, WRITE_OWNER, GENERIC_WRITE, GENERIC_ALL) on any allow ACE whose SID is not SYSTEM, Administrators or TrustedInstaller. `New()` wires the real runner and checker.
- [ ] `GOOS=windows go vet ./internal/agent/checks/plugins/script/` and `GOOS=windows go test -c -o /dev/null` (compile only; the Windows tests run in CI's agent-windows job); `GOOS=darwin go vet` likewise; Linux tests here; lint (incl. `GOOS=windows golangci-lint run ./internal/agent/checks/plugins/script/` if it works) → commit `feat(agent): run host commands without a shell, with a built environment and a tree kill`.

### Task 5: Register the plugin, descriptor, seed rules, SPA

**Files:** `internal/agent/checks/plugins/registry.go` (+ `registry_test.go`, the note's plugin list), `internal/checkplugins/script.go`, `formats.go` (`FormatCommandName`, + `formats_test.go`), `registry.go` (+ tests named in the "Adding a plugin" note), `internal/checks/plugins.go` (`PluginScript`) + tests, `internal/rules/catalog_test.go`, `internal/rules/seed.go` + `seed_test.go` (exact set now seven; the "five" in names/comments updated), new migration `internal/db/migrations/00094_seed_script_rules.sql` (+ integration test that an existing org gets exactly the two rows once, and an org already holding a rule of that name gets no duplicate — follow 00084's test if it has one), `web/` (fixture, `src/api/schema.d.ts`, en/de plugin names in `checks.dialog.plugins` and `instance.shared.checks.plugins`, `checks.params` texts for `commands`, `format`, `timeout_seconds`; the commands help says the names are defined in the host's agent.yaml).

- [ ] Follow the "Adding a plugin" note in `internal/agent/checks/plugins/registry.go` step by step; tests first where the note names golden tests (update them, watch them fail, then add the descriptor).
- [ ] Seed rules: `SeedRules()` gains the two rules (Global Constraints); the migration inserts the same rows for every existing organization lacking a rule of that name (origin `seed`, the same columns and ast/source shape `SeedForOrg` writes — derive the jsonb from what `Form.Compile`/`Format` produce, and add a test that compares the migrated rows' ast and source with `SeedForOrg`'s for a fresh org).
- [ ] SPA: `cd web && PATH=$PWD/node_modules/.bin:$PATH bash scripts/api-gen.sh`; fixture regen per the note; `npm run lint`, `npm run typecheck` (or the repo's equivalent), `npx vitest run src/features/checks`.
- [ ] Go: `-race -timeout 900s` for `./internal/checkplugins/... ./internal/checks/... ./internal/rules/... ./internal/agent/...`, the migration's package; then the WHOLE `./internal/api` once with `-timeout 1800s`; lint → commit `feat(checks): offer script checks with default rules for the script's verdict`.

### Task 6: Documentation

**Files:** `docs/BACKLOG.md` (3.6g done with a summary; 3.6a done: "delivered by 3.54, `internal/checkplugins`"; new planned row for manager-supplied arguments with per-command opt-in; Last updated 2026-11-14), `docs/upgrades.md` (version skew paragraph for the plugin, and `commands:` needing the new agent: an older agent refuses the whole file and loses its secrets); noix-docs scratch clone `/private/tmp/claude-501/-Users-noidee--dev-pulsix/76b69f36-5040-49f9-a80e-08a1ef63bcdb/scratchpad/noix-docs-merge`, new branch `docs/pulsix-script-checks` from `docs/pulsix-agent-uninstall`: Agent configuration page (`commands:`), a script checks section or page (formats, program check, environment, the service sandbox on Linux, default rules, item-scoped rules on values, examples: a Nagios plugin and a small JSON script, Windows `powershell.exe -File`), the checks list, changelog; `npm run build`. Push nothing.

- [ ] Edit, build, commit in both repos (`docs: record script checks (backlog 3.6g)`, `docs(pulsix): run your own scripts as checks`).
