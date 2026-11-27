# Arguments from the manager for script checks (backlog 3.6j) — implementation plan

> **For agentic workers:** steps use checkbox (`- [ ]`) syntax.

**Goal:** backlog 3.6j: "Arguments from the manager for script checks: per-command opt-in in `agent.yaml` with validated placeholders — Today argv comes only from `agent.yaml`, so a check cannot pass a value such as a path or a port. A command would opt in to named placeholders in `agent.yaml`, each with a validation rule (pattern, range), and the manager would supply only values that pass; nothing else crosses the wire."

**Architecture:** a leaf package `internal/scriptargs` holds what both sides share: the placeholder name and value shape, the `args` entry syntax, the three rule kinds and a `Template` (argv with placeholders plus their rules) whose `Bind(values)` is the only way to get an argv out. `internal/agent/secrets` parses a new mapping form of a command (`run:` plus `args:`) into a `Template`; the list form becomes a fixed `Template`. `checks.Commands` hands the script plugin a `Template` instead of an argv; the plugin binds the check's values for that command before the file checks and the run, which stay as they are. The manager's `script` descriptor gains a list parameter `args` (`command:placeholder=value`, format `scriptArg`, cross-checked against `commands`); the SPA gets the format twin, the texts and the regenerated fixture.

**Spec:** the backlog row above and the binding security model of the dispatch; 3.6g's design (`docs/superpowers/specs/2026-11-14-script-checks-design.md`) for everything that does not change.

## Global Constraints
- Layers per `CLAUDE.md`; package tests only (`go test -race -timeout 900s ./internal/<pkg>/...`, `internal/api` with `-timeout 1800s -run …`); never `go test ./...`, `make test` or `internal/e2e`.
- `golangci-lint run` on touched packages and `gofmt -s -l` clean before each commit; SPA `npx vitest run <files>`, `npm run lint`, `npx tsc -b`.
- Generated files only through their generators: `web/src/test/checkPlugins.json` (`go test ./internal/checkplugins -run SPAFixture -update-spa-fixture`), `web/src/api/schema.d.ts` (`npm run api:gen`).
- No migration, no proto change, no heartbeat change.
- Conventional Commits, footer `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.

## Rulings
- **R1 Syntax in `agent.yaml`: a mapping form next to 3.6g's list form.** `name: {run: [program, arg, …], args: {placeholder: rule, …}}`. The list form keeps its exact 3.6g meaning (every entry literal, `{x}` included). — An existing file means what it meant; only a command written in the new form takes values. — Cost if wrong: a list-form command cannot take values; rewrite it in the mapping form.
- **R2 Placeholders are whole arguments only.** In `run`, an entry that is exactly `{name}` is a placeholder; an entry holding `{name}` among other text (`--port={port}`) is refused at load ("a placeholder must be a whole argument"), the program can never be one. Every placeholder in `run` must have a rule under `args` and every rule must be used (both config errors). A placeholder may appear more than once. — 3.6g passes argv elements unchanged and nothing in its design builds arguments from parts; whole arguments cannot change how a neighbouring option is parsed. `--port 8080` and `-p 8080` cover the plugins we know. — Cost if wrong: a program that only accepts `--opt=value` needs a wrapper script.
- **R3 Placeholder names** `[a-z][a-z0-9_]{0,31}`, at most 16 per command. — Short, unambiguous in `command:placeholder=value`.
- **R4 Rules: exactly one kind per placeholder.** `pattern: <RE2>` (anchored by the agent as `^(?:…)$`, at most 512 bytes, compiled at load), `min:` + `max:` (both, whole numbers, min ≤ max), or `values: [a, b, …]` (1–64 distinct entries that pass the value shape). Optional `default:` (must pass the rule at load) is bound when the check supplies no value; without one a missing value refuses the run. A rule of none or two kinds, an unknown key or a bad pattern refuses the whole file at load, like every other `agent.yaml` fault. — The three kinds the dispatch names; a default lets a host add a placeholder to a command existing checks already use.
- **R5 Value shape on both sides:** 1–512 bytes, valid UTF-8, no control character (C0, DEL, C1). The manager checks only this shape (it does not know the host's rules); the agent checks it again, then the rule. — Bounded, no line breaks, nothing a terminal or log line misreads. Empty values are not sent: leaving a value out means "use the default".
- **R6 Option injection:** a value starting with `-` is refused for a `pattern` rule unless the rule sets `allow_leading_dash: true`; a `values` entry may start with `-` (the administrator listed it); a range admits a negative number only when `min` is negative (the administrator wrote it). A range value is passed in canonical decimal (`+5`, `05` → `5`; `-0` → `0`), so the program never sees a form the rule did not mean.
- **R7 Batch programs on Windows:** when the program's base name ends in `.bat` or `.cmd` or is `cmd.exe` (case ignored, trailing dots and spaces ignored), values must not contain any of `" % ^ & | < > ! ( )` — cmd.exe re-parses its command line, and Go's quoting does not escape for it. Applied on every OS (a Unix file of that name is rare and the check is cheap). — Cost if wrong: a value like `Program Files (x86)` cannot reach a batch file; pass it via a PowerShell script instead.
- **R8 No shell, nothing else from the manager:** values become separate argv elements exactly as bound; no environment variables, no working directory, no program from the manager. A value with `;rm -rf /` or `$(…)` reaches the program as that literal text (pinned by a real-process test).
- **R9 The file checks apply to the bound argv.** A bound value that is an absolute path naming a file is checked like a configured argument (3.6g). — The checker does not know where an argument came from, and a manager-chosen path is less trusted than the administrator's. — Cost if wrong: a value naming a file others may write is refused, as the same path written in `agent.yaml` is today; a per-placeholder opt-out would be a follow-up.
- **R10 Manager parameter: a list `args` of `command:placeholder=value` entries** (format `scriptArg`, at most 64 entries of at most 640 bytes; the value is everything after the first `=`). The descriptor's `Check` refuses an entry whose command `commands` does not list and a placeholder set twice for one command. Not an item parameter. — A list of strings fits the descriptor types, the SPA's one-per-line list editor and the local-check params unchanged; command names never contain `:`.
- **R11 Values are never echoed.** A refusal names the command (the item), the placeholder and a fixed reason (`argument "port" must be a whole number from 1 to 65535`), never the value, on both sides. A value the agent does not recognise by name is named only when its name passes the name shape. A value that passed and then failed the file check is named with the path as 3.6g names a refused argument (it passed the host's rule and is the manager's own text).
- **R12 A command that takes no values refuses any value** ("this command takes no arguments on this host"); values for a placeholder the command does not declare are refused too. — The manager's check says what it means to send; silently dropping it would report a reading for something else.
- **R13 Version skew needs no protocol change.** (a) An older agent cannot hold a command that needs a value: it refuses the mapping form, so the whole `agent.yaml` (secrets and commands with it) until it is updated — the 3.6g and 3.5a precedent; `docs/upgrades.md`: update the agent before writing the mapping form. (b) An older agent receiving `args` for a list-form command ignores them and runs the command exactly as its administrator wrote it, which a new agent refuses (R12); documented, and visible because the values have no effect on output. No capability bit exists for plugin parameters; adding one needs a proto field, a column and per-host config filtering, out of proportion for (b). (c) An older manager refuses `args` when a check is saved (unknown parameter) and passes stored ones through unchanged. — Cost if wrong: a heartbeat capability and a host-page hint as a follow-up.
- **R14 No heartbeat report of commands or rules.** The heartbeat carries no command names today (3.6g non-goal); the SPA validates the shape only, and the agent's refusal arrives as the command's `script.output`. — Cost if wrong: a later item adds names and rules to the heartbeat for early validation.

### Fix round 1 (independent security review `3.6j-review.md`, controller rulings)
- **R15 Printable ASCII by default (review 1, 6; amends R5).** A value is printable ASCII (0x20–0x7E) on every OS unless its rule sets `unicode: true` (pattern or values; a listed value or a default beyond ASCII needs it too). With or without it, the shared shape now also refuses format characters (Cf: soft hyphen, bidi overrides and isolates, zero width, BOM), line and paragraph separators and non-characters, on the manager (and its SPA twin) and the agent. — A Windows program reading its command line in the ANSI code page best-fits U+FF02 to `"` and U+00AD/U+FF0D to `-`, splitting one value into several arguments or an option. — Cost if wrong: a host wanting non-ASCII values sets `unicode: true`; the docs say plainly that on Windows this can let ANSI programs see other characters.
- **R16 Batch delimiters (review 2; amends R7).** `,` `;` `=` join `" % ^ & | < > ! ( )` in the set refused for a `.bat`/`.cmd`/`cmd.exe` program (CR and LF fail the shape); pinned per character. The batch name is judged with a stream suffix cut off (`check.bat::$DATA`).
- **R17 Leading `/` for Windows programs (review 4; amends R6).** For a program written as a Windows path (drive letter or `\\`), a pattern value starting with `/` needs the same `allow_leading_dash: true` as one starting with `-`. A Unix program takes `/…` as a path.
- **R18 `..` segments (review 5a).** A pattern value holding a `..` segment (split at `/` and `\` on every OS: `..`, `../x`, `/a/../b`, `/a/..`, `a\..\b`) is refused unless the rule sets `allow_dotdot: true`. Listed values are the administrator's own.
- **R19 Generic file-check refusal for values (review 5b; amends R11).** A bound value the file check refuses or cannot check is reported as `argument "<name>" was refused by the file check` — neither the path nor whether it exists or who may write it; literal arguments keep 3.6g's wording. `Template.BindNamed` tells the plugin which argument came from which placeholder.
- **R20 Streams in the program (review 7).** On Windows a program path with a `:` after the drive (`check.bat::$DATA`, device forms) is refused at `agent.yaml` load.
- **R21 Docs (review 3, 5c).** The user docs warn that a placeholder handed to a shell or interpreter that runs its arguments (`sh -c`, `bash -c`, PowerShell `-Command`, `cmd /c`, `ssh`, eval-style tools) turns the value into code — only with tight patterns or `values`, and PowerShell with `-File`; and that a pattern admitting arbitrary absolute paths makes the file check look at them, which can mount automounted network paths. No load-time refusal of such commands (the administrator's choice). A Windows real-process test (review 8) is left to a Windows run.

### Fix round 2 (re-review, controller rulings)
- **R22 Stricter `..` (N1; amends R18).** Segments are split at `/`, `\` and `:` on every OS, and a segment of dots and spaces only with at least two dots counts as `..` (`.. `, `...`, `C:..\x`, `..:stream`). — Windows trims trailing dots and spaces from a name.
- **R23 Option check past blanks (N2; amends R6, R17).** The leading `-` / `/` check looks at the value with leading spaces and tabs trimmed. — A program's own parsing may skip them.
- The Windows real-process test (review 8) is backlog 3.135.

## Review Focus
- A value that is not allowed never runs: `TestBind` (scriptargs) table — option injection, shape, each rule, missing value, unknown placeholder, no-argument command, batch metacharacters.
- Errors never quote a value: `TestBindErrorsNeverQuoteValues`, `TestScriptArgsRefusalNeverQuotesValue` (plugin).
- A bad `args:` section refuses the file at load, with a line and without quoting a pattern or default: `TestParseRefusesCommandArgs` (secrets).
- Shell text is data: `TestRealRunPassesValuesLiterally` runs a real script with `;rm -rf /`, `$(id)` and a space as separate arguments and reads them back.
- The manager refuses a malformed entry and one whose command is not listed: `TestScriptArgsParam`, `formats.json` vectors (Go and TS).
- Fix round 1: `TestValueProblemRefusesInvisibleCharacters`, `TestBindPrintableASCIIByDefault`, `TestBindBatchRefusesEveryMetacharacter`, `TestBindLeadingSlashOnWindows`, `TestBindDotDotSegments`, `TestBindNamed` (scriptargs); `TestScriptFileCheckOnValuesIsGeneric` (plugin); `TestProgramNamingAStreamIsRefused` and the new refusal rows (secrets).

## Task 1: Shared rules (`internal/scriptargs`)
- Files: `scriptargs.go` (names, shape, entry syntax), `rule.go`, `template.go` + tests.
- Interfaces: `ValidName`, `ValueProblem(v) string`, `ParseEntry(s) (command, name, value string, ok bool)`, `EntryProblem(s) string`, `Placeholder(arg) (string, bool)`, `EmbedsPlaceholder(arg) bool`; `PatternRule(expr, allowDash)`, `RangeRule(min, max)`, `EnumRule(values)`; `Param{Rule, Default, HasDefault}`; `Fixed(argv)`, `NewTemplate(argv, params) (Template, *TemplateError)`, `Template.Bind(values) ([]string, error)`, `Template.Argv()`, `Template.Placeholders()`.
- Tests: `TestValueProblem`, `TestParseEntry`, `TestPatternRule`, `TestRangeRule`, `TestEnumRule`, `TestNewTemplate`, `TestBind`, `TestBindErrorsNeverQuoteValues`, `TestBindBatchPrograms`.
- [ ] Commit `feat(agent): rules for values a script check passes to a command`.

## Task 2: `agent.yaml` mapping form (`internal/agent/secrets`)
- Files: `commands.go` (mapping form), new `commandargs.go` (the `args:` rules), `set.go` (`Command` returns a `scriptargs.Template`), tests.
- Tests: `TestParseAcceptsCommandArgs`, `TestParseRefusesCommandArgs` (table: every refusal, none quotes a pattern, default or argument), existing command tests adapted.
- [ ] Commit `feat(agent): commands in agent.yaml may declare placeholders with rules`.

## Task 3: The plugin binds values (`internal/agent/checks`, `…/plugins/script`, `internal/agent`)
- Files: `checks/command.go` (`Commands.Command` returns a `scriptargs.Template`), `script/params.go` (parse `args`), `script/script.go` (bind before checks), `agent/secrets_wiring.go`; fakes in tests.
- Tests: `TestScriptBindsArgs`, `TestScriptArgsRefusals` (no-argument command, unknown placeholder, rule failure, option injection, malformed param), `TestScriptArgsRefusalNeverQuotesValue`, `TestRealRunPassesValuesLiterally` (Unix real process).
- [ ] Commit `feat(agent): script checks pass the manager's values to placeholders`.

## Task 4: Manager descriptor, API doc and SPA
- Files: `internal/checkplugins/script.go`, `formats.go` (`FormatScriptArg`), `testdata/formats.json`, `registry_test.go`, `script_test.go`; `internal/api/check_plugin_schema.go` + tests; fixture and `schema.d.ts` regenerated; `web/src/features/checks/paramFormats.ts`, i18n en/de.
- Tests: `TestScriptArgsParam`, format vectors, `paramFormats.test.ts`, API param doc test.
- [ ] Commit `feat(checks): script checks pass values to host commands`.

## Task 5: Docs
- Backlog 3.6j → done; `docs/upgrades.md` (R13).
- [ ] Commit `docs: backlog 3.6j done`.
