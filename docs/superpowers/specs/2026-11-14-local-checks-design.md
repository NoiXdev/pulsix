# Local checks in `agent.yaml` (backlog 3.5a) — design

Status: the owner accepted the proposal on 2026-11-14 ("passt"): a
`checks:` section in the same file as `secrets:` and `commands:`, with the
same permission rule and the same one-minute re-read; each entry has a key,
a plugin, an interval and params; local keys start with `local-` so they
never collide with the manager's; the agent validates them against the same
plugin descriptors as the manager, refuses an invalid entry with its line
number and keeps the others running; local checks run in the same scheduler
next to the manager's and report under their own key, so rules on a metric
apply to them; the heartbeat reports them and the host page shows them as
"defined on the host", read-only, which needs a small migration like the
secret names did. Not part of it: editing local checks from the GUI, and a
local check overriding a manager check. Every other decision below is
**(Claude's call)**.

## Why

Some checks belong to the host, not to the fleet's configuration: a script
check whose command only one host has, a database check an operator wants
running before the manager knows the host, a check written by whoever
provisions the machine (config management drops `agent.yaml`). Today the
agent takes its whole check set from the manager's stream; 3.5 already gave
it a root-only file it re-reads, so local checks live there.

## `agent.yaml`: `checks:`

```yaml
secrets:
  …
commands:
  backup_age: ["/usr/local/lib/pulsix/backup-age.sh"]
checks:
  local-backup:
    plugin: script
    interval_seconds: 300
    params:
      commands: [backup_age]
      format: json
  local-pg:
    plugin: postgres
    params:
      secret: pg_main
```

- A new optional top-level key, read by `internal/agent/secrets` with the
  rest of the file: same path, permission rule, strict YAML subset and
  one-minute re-read. The package doc says it now reads checks too.
- `checks` maps a check key to an entry. An entry is a mapping with
  `plugin` (required, a string), `interval_seconds` (optional, an integer,
  default 60) and `params` (optional, a mapping; absent means none).
- **Two levels of refusal.**
  - *The file:* anything the strict subset already refuses — a syntax
    error, a tag, an anchor, a null, a duplicate key, a second document —
    and the wrong node kind where the format needs a mapping (`checks`
    itself, an entry, `params`), or a params value that is neither a
    scalar nor a sequence of scalars. As today the whole file is refused:
    no secrets, no commands, no local checks.
  - *One entry:* everything about what an entry says. The others keep
    running. The reasons, each with the entry's line:
    - the key is not `local-` followed by what `checkplugins.CheckKeyPattern`
      allows (the whole key must match the pattern);
    - an entry field other than the three;
    - `plugin` unknown to this build, or a plugin whose `Runs` is `Server`
      (the manager's probes: the agent cannot run them);
    - `interval_seconds` not an integer between 10 and 86400 (the
      manager's `MinInterval`/`MaxInterval`, moved into
      `internal/checkplugins` so both sides read one definition; the
      `internal/checks` names stay as aliases);
    - params refused by `checkplugins.Validate` (its own text, which names
      the parameter);
    - params whose canonical JSON exceeds 2048 bytes;
    - more than 32 valid entries (the entries after the 32nd in key order
      are refused, "more than 32 local checks").
- **Params types.** A plain scalar resolves the way YAML's core schema
  does: an integer or float becomes a number (`float64`, as a JSON-decoded
  manager push gives the plugin), `true`/`false` a bool, anything else a
  string; a quoted scalar is always a string. A sequence becomes a list of
  those. So `port: 5432` is a number and `commands: [backup_age]` a list,
  matching what the manager's JSON would carry. `!!str` stays the one
  accepted tag (it forces a string).
- `Set` gains `LocalChecks() []LocalCheck` (key order; each: `Key`,
  `Plugin`, `Params map[string]any` (a copy), `Interval time.Duration`,
  `ParamsJSON string` (canonical, what is reported)) and
  `LocalCheckProblems() []string` (`line N: <reason>`, in line order). Its
  printed and logged forms add the local check keys and the problem count,
  never params.
- Problems are logged when the file changes, like the secrets' state.

## Agent

- **Scheduler:** `ApplyLocal(specs []Spec) (shadowed []string)` installs
  the local set next to the manager's. The scheduler keeps both desired
  sets and runs their union; the same unchanged-keeps-its-phase rule as
  `Apply` holds for both. A local key that is also in the manager's set
  is not run (the manager's check runs) and is returned as shadowed; with
  the `local-` prefix that only happens for a manager definition created
  before this change. `Apply` re-checks the local set against the new
  manager set (a local check shadowed until now starts when the manager's
  key goes, and stops when one appears). `Version()` and `Apply`'s
  unrunnable/version rule stay about the manager's set only.
- **When the set is applied:** after every look at the file — the startup
  look in `Run` before the scheduler starts, every heartbeat, and a new
  once-a-minute look in `Run` that does not depend on the stream, so local
  checks start and follow file edits while the manager is unreachable. A
  refused file applies an empty local set (its local checks stop, as its
  secrets go).
- Local checks run like any other: secrets and commands from the same
  file, results through the same buffer and stream, keyed by their key.
- Shadowed keys are logged (warn, at most once per change of the set) and
  reported as problems (`check "<key>": a check from the manager has the
  same key`).
- **Heartbeat** gains:

  ```proto
  message LocalCheck {
    string key = 1;
    string plugin = 2;
    string params_json = 3; // canonical JSON
    int32 interval_seconds = 4;
  }
  repeated LocalCheck local_checks = 5;      // running ones, key order
  repeated string local_check_problems = 6;  // refused entries and shadowed keys
  ```

  Sent on every heartbeat (at most 32 × 2 KiB of params). An agent older
  than this sends neither; the manager reads `secrets_state == ""` as
  "unknown", as for the secret names.

## Manager

- **Store:** migration 00095 adds `host_agents.local_checks jsonb` and
  `host_agents.local_check_problems text[]`, nullable, no default (NULL is
  "unknown", written together with the secrets columns by the heartbeat;
  zero local checks is `[]` / `{}`). Down drops both (development only).
- **Heartbeat:** the manager validates what it is told again — it trusts
  the agent no more than any other input: at most 32 checks, each key
  `local-` + `CheckKeyPattern`, unique, a known agent-run plugin, interval
  in bounds, `params_json` at most 2048 bytes, parsing to an object that
  `checkplugins.Validate` accepts. A check failing that is dropped and the
  heartbeat logs it (key and reason, never params). Problems are kept as
  sent, at most 32, each cut to 256 bytes on a rune boundary (the existing
  `boundLine`).
- **Resolution:** `checks.ResolvedSet` gains `Local []Resolved`, sorted by
  key, built from the host's stored local checks, `Source` a new
  `BindingLocal` ("local") that no definition can be bound at (definition
  validation keeps refusing it). A local key equal to a key in `Checks` or
  `Synthetic` is left out (the manager's check wins, as on the agent).
  `Version` does not cover `Local` (the push never carries them), and
  `All()` includes them, so the rule evaluation, the host page's tuple
  states and the effective-rules view judge local tuples like any other.
  An unmonitored host resolves to an empty set, local checks included —
  the agent still runs them; their values are stored, not judged.
- **Definition keys:** creating or renaming a check definition to a key
  starting with `local-` is refused (`ErrInvalidInput`, "keys starting
  with local- are reserved for checks defined on the host"). Existing
  definitions keep their key and win over a local check of the same key.
- **Host page API** (`GET /hosts/{id}/checks`): local checks follow the
  effective definitions, each with `source: "local"`, `origin: "local"`,
  no `definition_id`, `enabled: true`, its values (`stillAsked` applies as
  for a definition), and the `missing_secret` warning like any check. The
  enums gain `local`. The host's agent DTO (where the secret names are
  reported) gains `local_check_problems` (absent when unknown).
- Rules: rules on a metric apply automatically. An item-scoped rule may
  name a local check's key (the rule's key validation is the pattern
  only).

## SPA

- The host checks card shows local checks with the source "Defined on the
  host" / "Auf dem Host definiert" and no action that edits, disables or
  overrides a definition (there is none behind it); forgetting an item,
  graphs and rules work as for any check.
- The host's agent card lists the local check problems when there are
  any, under a heading like the secrets' state.
- Schema, fixtures, en/de texts.

## Version skew

- An agent older than this change refuses an `agent.yaml` with `checks:`
  whole — and so loses its secrets and commands. Update the agent before
  adding `checks:` (`docs/upgrades.md`, user docs).
- A new agent with an older manager runs its local checks and the manager
  stores their values (ingest never looks a key up), but does not show or
  judge them until the manager is updated.

## Tests

- `internal/agent/secrets`: the format — a full example, defaults, each
  file-level refusal, each entry-level refusal with its line, scalar
  typing (number, bool, quoted, `!!str`), the 32 cap, the 2048-byte cap,
  key order, `Set`'s printed form showing no params.
- Scheduler: `ApplyLocal` runs the union; unchanged local keeps its phase;
  shadowing in both directions (local first then manager, and the reverse,
  and the manager key going away); `Version` unaffected; an empty local set
  stops local checks.
- Loop: local checks start before any stream; a file edit applies within
  the minute without a stream; a refused file stops them; heartbeat
  carries checks and problems.
- Manager: heartbeat validation (each refusal, bounds), store round-trip
  and NULL for an older agent (integration against the Postgres
  container), `ResolvedSet.Local` (sorting, shadowing by `Checks` and by
  `Synthetic`, `Version` unchanged, unmonitored host empty), the
  `local-` key refusal for definitions, the host checks route with a local
  check, the rule evaluation judging a local tuple.
- SPA: the card's local row (label, no edit actions), the agent card's
  problems.

## Documentation

`docs/BACKLOG.md` (3.5a done), `docs/upgrades.md` (version skew, migration
00095), noix-docs (Agent configuration: `checks:` with examples; the
monitoring page: local checks on the host page; changelog). No tech stack
named.

## Non-goals

- Editing or disabling local checks from the GUI or API.
- A local check overriding or disabling a manager check.
- Server-run plugins (probes) as local checks.
- Rules defined in `agent.yaml`.
