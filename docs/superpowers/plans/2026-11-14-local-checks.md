# Local checks in `agent.yaml` (backlog 3.5a) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Operators define checks in the host's `agent.yaml` (`checks:`); the agent runs them next to the manager's checks, reports them in its heartbeat, and the manager shows and judges them like any other check, read-only.

**Architecture:** `internal/agent/secrets` parses and validates `checks:` against `internal/checkplugins`; the agent scheduler keeps a second desired set (`ApplyLocal`) and runs the union, the manager's key winning. The heartbeat carries the running local checks and the problems; the manager re-validates, stores them on `host_agents` (migration 00095) and resolves them into `ResolvedSet.Local` (like `Synthetic`: outside `Version`, inside `All()`), which the rule evaluation and the host page already consume.

**Tech Stack:** Go, gopkg.in/yaml.v3, protobuf (buf), sqlc + goose on Postgres, huma, React/TypeScript SPA (Vitest).

**Spec:** `docs/superpowers/specs/2026-11-14-local-checks-design.md`

## Global Constraints

- Everything in the repository is English; public docs never name the tech stack.
- Tests: never one module-wide `go test ./...`; run single packages as `go test -race -timeout 900s ./internal/<pkg>/...` (`internal/api` with `-timeout 1800s`); `golangci-lint run` and `gofmt -s -l .` clean before every commit.
- Conventional Commits; footer `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. Commit on the feature branch only; no push, no tag, no `git stash`, no other state-changing git.
- Migrations are additive (`00095_host_agent_local_checks.sql`); Down for development only.
- Integration tests use `internal/db/testdb`; never dial a default URL or port; touch only Docker containers the test itself starts.
- Error and log texts never carry a secret value, a command argument or a check's params.
- Local check key: `local-` prefix and the whole key matches `checkplugins.CheckKeyPattern` (`^[a-z0-9][a-z0-9-]{0,62}$`).
- Bounds: at most 32 local checks; params canonical JSON at most 2048 bytes; interval 10 s – 86400 s, default 60 s; at most 32 problems, each at most 256 bytes on the manager.
- Exact texts: definition key refusal `keys starting with local- are reserved for checks defined on the host`; shadowed problem `check "<key>": a check from the manager has the same key`; cap refusal `more than 32 local checks`.

## Review Focus

1. A local check and a manager check with the same key (a definition created before this change): exactly one runs on the agent (the manager's), exactly one is resolved on the manager, and removing the manager's lets the local one start — Task 2 and Task 5 tests.
2. The manager is unreachable at agent start: local checks must run and follow file edits within a minute — Task 3 test.
3. A malicious or broken agent reports a huge, duplicated or invalid local check list: the manager stores only what passes its own validation, bounded — Task 4 tests.
4. A scalar like `port: 5432` must reach the plugin as a number and `"5432"` as a string, the same types a manager push gives — Task 1 tests.
5. The push version must not change when only local checks change (no re-push storm) — Task 5 test.

---

### Task 1: Parse and validate `checks:` in `agent.yaml`

**Files:**
- Modify: `internal/checkplugins/` (new `interval.go`: `MinInterval = 10 * time.Second`, `MaxInterval = 24 * time.Hour`); `internal/checks/definitions.go` (`MinInterval`/`MaxInterval` become aliases of the checkplugins constants)
- Create: `internal/agent/secrets/localchecks.go`, `internal/agent/secrets/localchecks_test.go`
- Modify: `internal/agent/secrets/parse.go` (accept the `checks` key, doc comment), `set.go` (`LocalChecks`, `LocalCheckProblems`, printed/logged form), `watcher.go` (log problems when the file changes)

**Interfaces:**
- Produces:
  ```go
  // package secrets
  type LocalCheck struct {
      Key, Plugin string
      Params      map[string]any // nil when none
      Interval    time.Duration
      ParamsJSON  string         // canonical JSON ("{}" when none)
  }
  func (s Set) LocalChecks() []LocalCheck      // key order, deep copies
  func (s Set) LocalCheckProblems() []string   // "line N: <reason>", line order
  ```
  `checkplugins.MinInterval`, `checkplugins.MaxInterval`; `checkplugins.LocalCheckPrefix = "local-"` and `checkplugins.ValidLocalCheckKey(key string) bool` (prefix and pattern) — used by Tasks 4 and 5 too.
- Canonical JSON: `encoding/json` of the params map (Go sorts map keys); the manager re-encodes the same way (`internal/checks` `canonicalParams` — reuse it via a small exported helper in `checkplugins` if `internal/checks` cannot be imported by the agent; it cannot, so add `checkplugins.CanonicalParams(map[string]any) ([]byte, error)` and make `internal/checks.canonicalParams` call it).

- [ ] **Step 1: Write the failing tests** (`localchecks_test.go`, table-driven over whole files):
  - the spec's example: two checks in key order, `local-backup` interval 300 s with params `{"commands": []any{"backup_age"}, "format": "json"}`, `local-pg` interval 60 s (default), ParamsJSON `{"commands":["backup_age"],"format":"json"}`.
  - scalar typing in params: `port: 5432` → `float64(5432)`, `ratio: 1.5` → `1.5`, `tls: true` → `true`, `port: "5432"` → `"5432"`, `name: !!str 12` → `"12"`, list `[a, 2]` → `[]any{"a", float64(2)}`.
  - file-level refusals (Load/parse returns an error, no secrets, no commands, no checks): `checks` a sequence; an entry a scalar; `params` a sequence; a params value that is a mapping; a nested sequence in params; a null entry; a tag other than `!!str`.
  - entry-level refusals (file accepted, entry absent, problem text starts with `line <N>: `): key `backup` (no prefix), key `local-Bad`, key `local-` + 60 chars (over pattern), unknown entry field `intervall`, plugin missing, plugin `nope`, a server-run plugin (pick one with `Runs == Server` from `checkplugins.All()` in the test), `interval_seconds: 5`, `interval_seconds: 90000`, `interval_seconds: "60"`, params refused by `checkplugins.Validate` (problem contains the parameter name), params JSON over 2048 bytes, 33 valid entries → 32 kept (first 32 in key order) and one problem `more than 32 local checks` on the 33rd's line.
  - `Set.String()`/`LogValue` show local check keys and the problem count, never a param value (assert a distinctive param string is absent).
  - `LocalChecks()` returns copies (mutating a returned Params map does not change the next call).
- [ ] **Step 2:** `go test -race -timeout 900s ./internal/agent/secrets/ ./internal/checkplugins/` — FAIL.
- [ ] **Step 3:** Implement. Scalar resolution with `yaml.Node.ShortTag()` after the existing tag refusal (`!!int`, `!!float` → `strconv.ParseFloat`, `!!bool` → bool; quoted style or `!!str` → string). Entry validation order: key, fields, plugin, interval, `checkplugins.Validate`, JSON size; then the cap. Problem texts never include params values beyond what `checkplugins.Validate` itself says.
- [ ] **Step 4:** Tests pass; also `go test -race -timeout 900s ./internal/checks/` (the alias) — PASS.
- [ ] **Step 5:** Commit `feat(agent): read local checks from agent.yaml`.

### Task 2: Scheduler runs the local set next to the manager's

**Files:**
- Modify: `internal/agent/checks/scheduler.go` (or a new `local.go` beside it)
- Test: `internal/agent/checks/scheduler_local_test.go`

**Interfaces:**
- Consumes: `checks.Spec`.
- Produces: `func (s *Scheduler) ApplyLocal(specs []Spec) (shadowed []string)` — shadowed keys sorted. `func (s *Scheduler) Shadowed() []string` (current shadowed local keys, sorted; Apply can change them) for the heartbeat.

- [ ] **Step 1: Failing tests** with the existing fake ticker/clock helpers:
  - manager set `{a}` and local `{local-x}` both run; results carry their keys.
  - `ApplyLocal` with the same spec again keeps the local check's ticker (no restart: same ticker instance / no stop).
  - local `{a}` then `Apply({a})`: the manager's `a` runs (its plugin/params), the local `a` is stopped, `Shadowed()` = `[a]`; then `Apply({})`: the local `a` starts again, `Shadowed()` empty.
  - manager `{a}` first, then `ApplyLocal({a})` returns `[a]` and does not touch the running manager check.
  - `Version()` after `ApplyLocal` is the manager's version unchanged; `Apply`'s unrunnable/version behaviour unchanged when a local set is present.
  - `ApplyLocal(nil)` stops every local check and no manager check.
  - `ApplyLocal` before `Run` and after `Run` returned behave like `Apply` (tracked then started; nothing leaked after).
- [ ] **Step 2:** `go test -race -timeout 900s ./internal/agent/checks/` — FAIL.
- [ ] **Step 3:** Implement: keep `managerSpecs` and `localSpecs`; one reconcile computes the desired map (manager specs, then local specs whose key is not a manager key) and diffs it against `s.checks` with the existing `specsEqual`/`startLocked`/`stopLocked`. `Apply` stores the manager set and reconciles; the unrunnable list and version stay computed from the manager specs only.
- [ ] **Step 4:** PASS.
- [ ] **Step 5:** Commit `feat(agent): run local checks next to the manager's`.

### Task 3: Agent loop applies local checks and reports them

**Files:**
- Modify: `proto/pulsix/agent/v1/agent.proto` (spec's `LocalCheck`, `Heartbeat.local_checks = 5`, `local_check_problems = 6`), regenerate `internal/pb/agentpb` with `make buf-generate` (and `make buf-lint`)
- Modify: `internal/agent/loop.go`, `internal/agent/session.go`, `internal/agent/secrets_wiring.go` (or a new `localchecks.go` in `internal/agent`)
- Test: `internal/agent/localchecks_test.go`

**Interfaces:**
- Consumes: `secrets.Set.LocalChecks/LocalCheckProblems` (Task 1), `Scheduler.ApplyLocal/Shadowed` (Task 2).
- Produces: wire fields `Heartbeat.LocalChecks []*agentpb.LocalCheck`, `Heartbeat.LocalCheckProblems []string`.

- [ ] **Step 1: Failing tests** (existing loop harness and fakes):
  - an `agent.yaml` with one local check and no reachable manager: after the startup look the scheduler runs `local-…` (its result reaches the disconnect buffer).
  - editing the file (fake clock past the watcher interval, the minute ticker fires) without any stream: the new set is applied; a refused file (bad permissions or a syntax error) stops the local checks.
  - a heartbeat carries `local_checks` (key, plugin, params_json, interval_seconds) and `local_check_problems` = the file's problems followed by `check "<key>": a check from the manager has the same key` for each shadowed key.
- [ ] **Step 2:** `go test -race -timeout 900s ./internal/agent/` — FAIL.
- [ ] **Step 3:** Implement `applyLocal(set secrets.Set)` (convert to `checks.Spec`, `ApplyLocal`, warn-log shadowed keys when the shadowed list changes) and call it after every `l.secrets.Current()`: the startup look in `Run`, `sendHeartbeat`, and a new once-a-minute ticker in `Run` (through the loop's injected ticker factory) that lives for the process lifetime like the scheduler goroutine. A refused file yields an empty set.
- [ ] **Step 4:** PASS; `make buf-lint` clean.
- [ ] **Step 5:** Commit `feat(agent): apply local checks without the manager and report them in the heartbeat`.

### Task 4: Manager stores the reported local checks

**Files:**
- Create: `internal/db/migrations/00095_host_agent_local_checks.sql` (spec's columns, comments in the style of 00086)
- Modify: `internal/db/queries/agents.sql` (`RecordAgentHeartbeat` writes the two columns; the query behind `SecretsByHost` — or a new `LocalChecksByHost(s)` — reads them); regenerate with `make sqlc`
- Modify: `internal/agents/secrets.go` (or new `localchecks.go`): validation and bounds; `service.go` `Heartbeat`; `store_sql.go`
- Modify: `internal/agentapi/stream.go` (`processHeartbeat` passes the new fields)
- Modify: `internal/api/agent_handlers.go` (agent DTO `local_check_problems`, nullable, null when unknown)
- Tests: `internal/agents/localchecks_test.go` (unit), `internal/agents/store_localchecks_test.go` (integration), `internal/db/…schema_test.go` (column shape like `agent_secrets_schema_test.go`), `internal/agentapi/stream_test.go`, `internal/api/agent_secrets_test.go` (or a sibling)

**Interfaces:**
- Produces:
  ```go
  // package agents
  type LocalCheck struct {
      Key, Plugin string
      Params      map[string]any
      Interval    time.Duration
  }
  type LocalChecksReport struct {
      Known    bool          // false: older agent or no heartbeat yet
      Checks   []LocalCheck  // validated, key order
      Problems []string
  }
  // Heartbeat gains the report: change the signature to take one struct
  // HeartbeatReport{Version string; SecretNames []string; SecretsState string;
  //   LocalChecks []LocalCheckInput; LocalCheckProblems []string}
  // where LocalCheckInput{Key, Plugin, ParamsJSON string; IntervalSeconds int32}.
  func (s *Service) LocalChecksByHost(ctx context.Context, orgID, hostID uuid.UUID) (LocalChecksReport, error)
  func (s *Service) LocalChecksByHosts(ctx context.Context, orgID uuid.UUID, hostIDs []uuid.UUID) (map[uuid.UUID]LocalChecksReport, error) // for ResolveOrg/ResolveHosts
  ```
- Consumes: `checkplugins.ValidLocalCheckKey`, `MinInterval`, `MaxInterval`, `Validate`, `CanonicalParams` (Task 1).

- [ ] **Step 1: Failing tests:**
  - unit: each manager refusal drops only that check and logs key + reason (capture the logger; assert no params text): bad key, duplicate key (second dropped), unknown plugin, server-run plugin, interval 5 s / 90000 s, params_json not an object, over 2048 bytes, `Validate` refusal; the 33rd valid check dropped; problems capped at 32 and each cut to 256 bytes on a rune boundary (`boundLine`); `secrets_state == ""` → both columns NULL (unknown).
  - integration: heartbeat round-trip — checks and problems read back equal; NULL → `Known == false`; zero checks → `Known`, empty; `LocalChecksByHosts` for two hosts.
  - schema test: both columns nullable, no default.
  - agentapi: `processHeartbeat` hands the proto fields through.
  - API: agent DTO `local_check_problems` null when unknown, `[]` when none, the list otherwise.
- [ ] **Step 2:** Run the four packages (`internal/agents`, `internal/db`, `internal/agentapi` at 900 s, `internal/api` at 1800 s) — FAIL.
- [ ] **Step 3:** Implement. Store `local_checks` as a JSON array of `{key, plugin, params, interval_seconds}`.
- [ ] **Step 4:** PASS.
- [ ] **Step 5:** Commit `feat(agents): store the local checks an agent reports`.

### Task 5: Resolve local checks and show them on the host page

**Files:**
- Modify: `internal/checks/resolver.go` (`ResolvedSet.Local`, `All()`), `internal/checks/definitions.go` (`BindingLocal`, the `local-` key refusal in `ValidateDefinitionShape`), the resolve bundle's loading (a port for local checks, wired in `internal/app`)
- Modify: `internal/api/host_check_handlers.go` (local rows after the effective definitions; enums `source`/`origin` gain `local`; `definition_id` omitted for local)
- Tests: `internal/checks/resolver_test.go` (or `resolver_local_test.go`), `internal/checks/definitions_test.go`, `internal/api/host_checks_local_test.go`, `internal/rules` or `internal/app` test that a local tuple is evaluated

**Interfaces:**
- Consumes: `agents.LocalChecksByHost(s)` (Task 4) through a small interface declared in `internal/checks` (`LocalCheckSource`), set with an option like `WithLocalChecks(src)`; without it `Local` is empty.
- Produces: `checks.BindingLocal BindingKind = "local"`; `ResolvedSet.Local []Resolved`.

- [ ] **Step 1: Failing tests:**
  - `ResolveHost` with a stored `local-x`: `Local == [local-x]` with `Source == BindingLocal`, `Version` equal to the same host's version without the local check, `All()` includes it after `Checks` and `Synthetic`.
  - a local key equal to a definition key or a synthetic key is not in `Local`.
  - an unmonitored host: `Local` empty.
  - `ResolveHosts`/`ResolveOrg` fill `Local` too (one query for all hosts).
  - `CreateDefinition`/`UpdateDefinition` with key `local-x` → `ErrInvalidInput` with the exact text; a definition can never carry `BindingLocal`.
  - host checks route: a local check row with `source: "local"`, `origin: "local"`, no `definition_id`, `enabled: true`, its values, `missing_secret` when it names a secret the agent did not report.
  - rule evaluation: an org rule on a metric fires for a local check's tuple (through `ContextForHost`/`app/rules_ports.go`).
- [ ] **Step 2:** Run `internal/checks`, `internal/app`, `internal/rules` (900 s) and `internal/api` (1800 s) — FAIL.
- [ ] **Step 3:** Implement. `MissingSecret` takes a `Definition`; build the local row's warning through the same helper (adapt its parameter to what both need rather than duplicating it).
- [ ] **Step 4:** PASS; regenerate `web/src/api/schema.d.ts` with `cd web && npm run api:gen`.
- [ ] **Step 5:** Commit `feat(checks): resolve local checks and list them on the host page`.

### Task 6: SPA

**Files:**
- Modify: `web/src/features/hosts/hostChecks.ts` (`sourceLabel` for `local`), `HostChecksCard.tsx` / `HostCheckValueRow.tsx` (no definition actions for a local check), `HostAgentCard.tsx` (problems list), i18n en/de, fixtures (`web/src/test/hostDetail.tsx` and whichever fixture the host checks tests use)
- Tests: `hostChecks.test.ts`, `HostChecksCard.test.tsx`, `HostAgentCard.test.tsx`

- [ ] **Step 1: Failing tests:** `sourceLabel` of a local check is "Defined on the host" (en) / "Auf dem Host definiert" (de); the card renders a local check without any edit/disable/override control while Forget, graph and rules still render for its values; the agent card lists the problems when present and nothing when the list is empty or null.
- [ ] **Step 2:** `cd web && npx vitest run src/features/hosts` — FAIL.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** PASS; `npm run lint` and `npx tsc --noEmit` (or the repo's scripts) clean.
- [ ] **Step 5:** Commit `feat(web): show checks defined on the host`.

### Task 7: Documentation

**Files:**
- Modify: `docs/BACKLOG.md` (3.5a done, pointer to the spec), `docs/upgrades.md` (version skew both ways, migration 00095)
- noix-docs scratch clone `/private/tmp/claude-501/-Users-noidee--dev-pulsix/76b69f36-5040-49f9-a80e-08a1ef63bcdb/scratchpad/noix-docs-merge`, new branch `docs/pulsix-local-checks` from `docs/pulsix-script-checks`: `reference/agent-configuration.md` (`checks:` section with the spec's example, the two refusal levels, the `local-` prefix, types, bounds), `user/monitoring.md` (local checks on the host page, read-only, problems on the agent card), the changelog. No tech stack named. Build the site the way earlier docs tasks did and report the page count.

- [ ] **Step 1:** Write the docs.
- [ ] **Step 2:** Build the docs site — PASS.
- [ ] **Step 3:** Commit in both repos (`docs: …`).
