# Custom script checks (backlog 3.6g) — design

Status: the owner accepted the proposal on 2026-11-14 ("passt weiter"): the
host defines the commands in `agent.yaml` (NRPE model), the manager only
picks them by name; no arguments from the manager; Nagios format (exit code
plus perfdata) and a JSON format; perfdata and JSON values become numeric
metrics a rule can judge; a default rule takes over the script's own
verdict; Linux, macOS and Windows; reload without a restart like the
secrets. Every other decision below is **(Claude's call)**.

Also found while planning: backlog 3.6a's subject (plugins declaring their
parameter schema and metrics in one place for the GUI and the validator)
was delivered by 3.54 (`internal/checkplugins`); 3.6a is marked done with a
pointer there.

## Why the host defines the commands

The agent runs as root (SYSTEM on Windows). A check whose command came from
the manager would let anyone who can edit checks — an org admin, or a
compromised manager — run arbitrary code as root on every host. Nagios
plugins make this worse than it looks (`negate`, `check_by_ssh` run any
command they are given). So the command line, program and arguments, is
written on the host in the agent's own root-only configuration file; the
manager's check names a command and nothing else.

## `agent.yaml`: `commands:`

```yaml
secrets:
  …
commands:
  disk_root: ["/usr/lib/nagios/plugins/check_disk", "-w", "20%", "-c", "10%", "-p", "/"]
  backup_age:
    - /usr/local/lib/pulsix/backup-age.sh
```

- A new optional top-level key next to `secrets:` in the file 3.5 reads
  (`internal/agent/secrets`): same path, same permission rule, same strict
  YAML subset (no tags but `!!str`, no anchors, no nulls, one document), the
  same one-minute re-read. A file that fails any of it drops secrets and
  commands alike, as today.
- A command is a name (the secret-name pattern `[A-Za-z0-9_.-]{1,64}`)
  mapping to a sequence of 1–64 strings, each at most 4096 bytes; the first
  is the program and must be an absolute path. At most 256 commands. Errors
  name a line and a fixed reason, never an argument (an argument may hold a
  password).
- `Set` gains `Command(name) ([]string, bool)` (a copy) and
  `CommandNames()`; its printed and logged forms keep showing names only.
  The package keeps its name (`secrets`); its doc comment says it now reads
  the whole file.
- **Version skew:** an agent older than this change refuses the whole file
  when it has a `commands:` key, and so loses its secrets too.
  `docs/upgrades.md` and the user docs say to update the agent before adding
  `commands:`.

## The `script` plugin

Descriptor (`internal/checkplugins/script.go`, Runs = Agent):

| Param | Type | Rule |
|---|---|---|
| `commands` | list | required, non-empty, at most 32 entries, each `FormatCommandName` (the secret-name pattern; a new format next to `FormatSecretName`) |
| `format` | enum | `nagios` (default), `json` |
| `timeout_seconds` | integer | 1–60, default 10 |

`ItemParam` is `commands`; `ItemEntry` cuts an item at its first `:` (a
perfdata item `disk_root:/` belongs to the entry `disk_root`).

Metrics:

| Metric | Kind | Item | Value |
|---|---|---|---|
| `script.status` | discrete | command | `ok`, `warn`, `crit`, `unknown` |
| `script.output` | discrete | command | the first line of output (below) |
| `script.exit_code` | numeric | command | the process's exit code; absent when it did not run to an exit |
| `script.duration_seconds` | numeric | command | wall time of the run |
| `script.value` | numeric | `<command>:<label>` | one perfdata or JSON metric value |

Several checks may use the plugin under their own keys (a slow command on a
long interval, `script-backup`), as with every plugin.

### Running a command (agent, `internal/agent/checks/plugins/script`)

- The scheduler hands the plugin a lookup for commands the way it hands a
  `SecretPlugin` its secret: a new `checks.CommandPlugin` interface
  (`CollectWithCommands(ctx, params, commands Commands)`) and a scheduler
  option `WithCommands`, fed in `internal/agent` from the same watcher as
  the secrets. The plugin never reads the file.
- A command name the file does not define → status `unknown`, output
  `command "<name>" is not defined on this host` (plus the file's problem
  when the file is missing or refused, as the secret lookup says it).
- **Program check, before every run:** the program, after resolving
  symlinks, must be a regular file; on Unix owned by root, not writable by
  group or others, executable; its directory owned by root and not writable
  by group or others. On Windows owner SYSTEM, Administrators or
  TrustedInstaller, and no allow ACE granting a write right (the installer's
  write mask) to any other SID, for the file and its directory. **Every
  argument that is an absolute path naming an existing file** (the script
  given to an interpreter: `powershell.exe -File C:\…\x.ps1`,
  `/usr/bin/python3 /opt/x.py`) passes the same check. A failure → status
  `unknown`, output naming the path and the reason (never another
  argument).
- Executed directly, never through a shell; working directory `/` (Windows:
  the system drive's root). The environment is built, not inherited, so the
  agent's token and secrets never reach a script: Unix `PATH=/usr/local/sbin:
  /usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin`, `LANG=C`, `LC_ALL=C`;
  Windows `SystemRoot`, `windir`, `SystemDrive`, `ComSpec`, `PATHEXT`,
  `TEMP`, `TMP` copied from the agent's own environment when set, and
  `PATH=%SystemRoot%\System32;%SystemRoot%;%SystemRoot%\System32\WindowsPowerShell\v1.0`.
- Standard output is read up to 64 KiB (more is cut off and noted for the
  JSON format, below); standard error is discarded.
- `timeout_seconds` bounds a run. On expiry the whole process tree is
  killed: Unix a new process group (`Setpgid`) killed as a group; Windows a
  job object with kill-on-close the process is assigned to right after it
  starts. `WaitDelay` bounds the wait for the output pipe. A process that
  does not go away after the kill marks its command busy until it exits;
  later rounds report `unknown` "an earlier run of this command has not
  exited yet" instead of starting another (the smart runner's pattern).
- One check's commands run at most 4 at a time; each yields its own items.
- The scripts run inside the agent's own service sandbox. On Linux that is
  the unit's hardening: read-only system (`ProtectSystem=strict`), no home
  directories, a private `/tmp`, `NoNewPrivileges`. The user docs say so.

### Nagios format

- Exit code 0 → `ok`, 1 → `warn`, 2 → `crit`, 3 and anything else (and no
  exit code: killed, timed out, failed to start) → `unknown`.
- Output: the first line up to `|` is the message; text after `|` on that
  line, and after a `|` in any later line (the long-output form), is
  perfdata.
- Perfdata `'label'=value[UOM];warn;crit;min;max`, space-separated, labels
  optionally single-quoted (`''` is an escaped quote). Only label and value
  are used. Units are normalised: `s` → seconds, `ms` ÷ 1000, `us` ÷ 10⁶,
  `%` as is, `B` / `KB` / `MB` / `GB` / `TB` → bytes (×1024ⁿ, the Nagios
  plugins' convention), `c` and none as is. An unparseable entry is
  skipped. At most 64 values per command; the rest are skipped.

### JSON format

Standard output is one JSON object:

```json
{"status": "ok", "message": "3 backups, newest 2 h old", "metrics": {"age_hours": 2, "count": 3}}
```

- `status` is required and exactly one of `ok`, `warn`, `crit`, `unknown`;
  `message` is an optional string; `metrics` an optional object of numbers
  (at most 64). Other keys are ignored. The exit code is reported but does
  not decide the status.
- Invalid JSON, a missing or unknown status, a non-number metric, or output
  over 64 KiB → status `unknown` with a fixed reason as output (never the
  script's text).

### Output label and items

- `script.output` is the message (Nagios first line, JSON `message`):
  invalid UTF-8 replaced, control characters removed, trimmed, cut to the
  label bound (512 bytes) on a rune boundary; an empty message is
  `(no output)`.
- A `script.value` item is `<command>:<label>`; one that is not a
  reportable item (`checks.ReportableItem`) or has an empty label is
  skipped. A label repeated in one run keeps its first value.

## Manager

- Validation, items, the rules catalogue and the API enums derive from the
  descriptor (3.54). Nothing else in the manager changes.
- **Default rules:** two seed rules, scope org, form mode:
  "Script reports critical" (`script.status` in `crit`, severity crit) and
  "Script reports warning" (`script.status` in `warn`, `unknown`, severity
  warn). `SeedRules` gains them; an additive migration inserts the same two
  rows (origin `seed`) for every existing organization that has no rule of
  that name, the way migration 00045 backfilled the first set. They match
  nothing on hosts without a script check. **(Claude's call:** `unknown`
  counts as a warning — a check that cannot run should be seen, and the
  operator can edit the rule.)
- `script.value` can be judged with an item-scoped rule (item
  `disk_root:/`, check key of the check) or any rule on the metric.

## SPA

The checks dialog offers `script` with its three parameters from the
descriptor (fixture, schema, en/de plugin name and parameter texts, as the
"Adding a plugin" note lists). The help text of `commands` says the names
are defined in the host's `agent.yaml`. No list of the host's commands in
the GUI.

## Tests

- `internal/agent/secrets`: `commands:` parsing — valid forms (flow and
  block sequences), every refusal (relative program, empty list, too many
  entries/commands, long argument, bad name, nested value, null, tag,
  duplicate), errors never quote an argument; a file with both keys.
- Script plugin, table-driven with a fake runner and fake program checker:
  exit-code mapping, perfdata parsing (quotes, units, long output, garbage,
  the 64 cap), JSON (valid, each refusal, size), label cleaning, item
  building and skipping, undefined command, program refused, timeout, busy,
  the concurrency bound.
- The real runner with real processes in temp dirs (Unix in the go job and
  macOS job, Windows in the agent-windows job): exit codes, output cap, a
  timeout that kills a child the script started (the tree kill), the
  environment holding only the built variables. The program check against
  real files: a group-writable file, a file owned by the test user (tests
  that need root ownership skip when not root, and assert the refusal when
  not root).
- Scheduler: a `CommandPlugin` gets the lookup; without `WithCommands`
  every name is undefined.
- `internal/checkplugins` golden tests, the rules catalogue table, the seed
  rules and the migration's rows (integration test against the Postgres
  container), the SPA fixture and texts — per the "Adding a plugin" note.

## Documentation

`docs/BACKLOG.md` (3.6g done; 3.6a done with the pointer to 3.54),
`docs/upgrades.md` (version skew: the plugin, and `commands:` needing the
new agent), noix-docs (Agent configuration: `commands:`; a new section on
script checks: both formats, the program check, the environment and
sandbox, the default rules, examples with a Nagios plugin and a JSON
script; changelog). No tech stack named.

## Non-goals

- Arguments from the manager (a later row: per-command opt-in with
  validated placeholders).
- Running as another user.
- A list of a host's commands in the GUI (would need the heartbeat to carry
  the names).
- Using a perfdata entry's own warn/crit thresholds.
