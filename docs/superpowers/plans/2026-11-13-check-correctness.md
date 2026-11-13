# Check Correctness Bundle Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close backlog 3.10c, 3.12c, 3.55 and 3.13c: advisory errors visible, no wrong item expiry after an agent outage, unasked tuples out of evaluation, vanished hardware metrics reported, agent config version shown.

**Architecture:** Agent-side changes in the scheduler (advisory log) and plugins (count metrics, a vanished-metric helper for temperature/smart); manager-side additive columns on `host_agents` (`connected_since`; `config_version`, `config_expected`, `config_checked_at`) with sqlc queries; the rules loader gets a per-key "asks for item" predicate through `rules.CheckContext`; API DTOs and SPA badges for the config version.

**Tech Stack:** Go, PostgreSQL migrations + sqlc (v1.31.1 binary at `/private/tmp/claude-501/-Users-noidee--dev-pulsix/76b69f36-5040-49f9-a80e-08a1ef63bcdb/scratchpad/bin/sqlc`, run `<path> generate` from the repo root), huma OpenAPI + `cd web && PATH=$PWD/node_modules/.bin:$PATH bash scripts/api-gen.sh`, React/TypeScript.

**Spec:** `docs/superpowers/specs/2026-11-13-check-correctness-design.md`

## Global Constraints

- Migrations additive, next free numbers after `00087` (`00088_agent_connected_since.sql`, `00089_agent_config_version.sql`); follow `docs/upgrades.md`'s migration rules.
- `internal/rules` never imports `internal/checks`; it may import the leaf `internal/checkplugins`.
- Error texts never carry driver or server messages or secret values; advisory errors of a `SecretPlugin` are never logged.
- Exact values: reconnect gap 30 min; agent counts after `checks.MaxInterval` (24 h) connected; advisory repeat 1 h; vanished-metric memory 24 h; config check rewrite after 10 min; metric names `pg.databases_unreported`, `docker.containers_unreported`; error text `<metric> is no longer reported`; DTO fields `config_version`, `config_expected`, `config_checked_at`.
- Tests never dial default URLs or ports (a foreign service listens on 127.0.0.1:80).
- Repo rules: TDD; never `go test ./...` or `make test` (single packages `-race -timeout 900s`; internal/api `-timeout 1800s -run …`, plus ONE whole internal/api run in the task that touches it); `golangci-lint run` clean; web `npm test`, `npm run lint`, `npx tsc -b`; commit footer `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`; never touch Docker containers other than testcontainers the tests start; no state-changing git besides your own commits; temp files only in the scratchpad.

## Review Focus

1. A steady agent with a genuinely vanished item still gets it forgotten (3.12c must not stop expiry for good); an agent whose `connected_since` is NULL after the upgrade never makes items expire wrongly.
2. Not-asked filtering: item `""`, a check with no list param, and an item still listed stay judged; a synthetic check's tuples unaffected.
3. The vanished-metric helper never flags a metric on a round where the item failed or reported nothing, and never grows without bound (items that disappear are dropped too).
4. The advisory log never prints a secret plugin's error and does not log every round.
5. The config write does not add a row write per heartbeat when nothing changed.

---

### Task 1: Advisory errors (3.10c)

**Files:** `internal/agent/checks/secret.go` (`collect` returns the advisory too) and `scheduler.go` (log state per key, injected clock already in the scheduler), new `internal/agent/checks/advisory.go` (+ test); `internal/agent/checks/plugins/postgres/postgres.go`, `plugins/docker/docker.go` (+ tests); `internal/checkplugins/postgres.go`, `docker.go` descriptors (+ catalogue tests, any metric-count pins); SPA metric labels only if the catalogue requires them.

- [ ] Tests first: advisory logged once with the check key at warn; same text inside 1 h not logged; after 1 h logged again; changed text logged at once; nil clears; secret plugin never logged (captured `slog` handler, fake clock). postgres: `pg.databases_unreported` 0 normally, N with 256+N databases, counts refused names; docker: `docker.containers_unreported` 0 and with refused names. RED → GREEN → lint → `go test -race -timeout 900s` for `./internal/agent/...` and `./internal/checkplugins/...`, internal/api `-run 'Catalog|Plugin|Metric'` and one whole internal/api run → commit `feat(agent): log advisory errors and count the names a plugin leaves out`.

### Task 2: Item expiry after an agent outage (3.12c)

**Files:** `internal/db/migrations/00088_agent_connected_since.sql`; `internal/db/queries/agents.sql` (`TouchAgentBySerial`, `RecordAgentHeartbeat`, `UpsertHostAgent`), `checks.sql` (the item-newest query for the forget re-check, `ListExpiredItems`), sqlc regen; `internal/agents` (the gap constant, passed as arg); `internal/checks/forget.go` (`ItemNewest.AgentConnected`, `lastSignOfLife`) and its store; tests in `internal/checks` (unit + DB-backed) and `internal/agents` store tests.

- [ ] Tests first: Go `itemExpired` table (agent connected < 24 h → agent ignored, ≥ 24 h → counts; no agent unchanged); DB: `connected_since` set on first touch, kept on a touch within 30 min, reset after a gap > 30 min, NULL after re-enroll, set on first touch when NULL with a recent `last_seen` (upgrade case); `ListExpiredItems` and `ForgetExpired` end to end: outage longer than the period then reconnect → not expired; same after 24 h connected → expired; steady agent → expired as today. RED → GREEN → sqlc generate (no other diff) → lint → tests (`./internal/checks/...`, `./internal/agents/...`, `./internal/agentapi/...`) → commit `fix(checks): count an agent for the item expiry only after a day connected`.

### Task 3: Tuples nobody asks for (3.55 part 1)

**Files:** `internal/rules/load_sql.go` (`CheckContext.Asks map[string]func(item string) bool`; tuple interval 0 when the key's predicate says no), `internal/app/rules_ports.go` (fill it from each resolved agent and synthetic check: `checkplugins.AsksForItem(plugin, params, item)`), tests in `internal/app/rules_ports_test.go` and a DB-backed rules test.

- [ ] Tests first: adapter (a disk check with `mounts: ["/"]` asks for `/` and `""`, not `/data`; a check without a list asks everything); Load gives interval 0 to `/data`'s tuples and keeps `/` and `""`; end to end: an open alert on `/data` closes when `/data` is taken off the list (the existing unmeasured-path close), the host roll-up no longer counts it. Verify first how the interval-0 path closes alerts and pin that in the test. RED → GREEN → lint → tests (`./internal/rules/...`, `./internal/app/...`) → commit `fix(rules): leave items a check no longer asks for out of the evaluation`.

### Task 4: Vanished hardware metrics and smart.devices (3.55 parts 2–3)

**Files:** new helper in `internal/agent/checks` (e.g. `vanished.go`: a per-instance memory keyed by item → metric → last seen time, `Apply(now, obs) []Observation`), `plugins/temperature/temperature.go`, `plugins/smart/smart.go` (become `StatefulPlugin`; clock injectable for tests), smart's `smart.devices` text condition; tests beside each.

- [ ] Tests first: helper (metric reported then missing while the item reports a value → one error observation `<metric> is no longer reported` per round; item reports nothing or only errors → nothing; after 24 h missing → dropped; item gone 24 h → dropped; metric back → no error); temperature limit disappearing; smart field disappearing; two check keys do not share memory (NewInstance); `smart.devices` "no device with SMART data" absent when every device failed, present when one answered without data and none sleeps. RED → GREEN → lint → `go test -race -timeout 900s ./internal/agent/...` → commit `fix(agent): report hardware metrics that stop being reported`.

### Task 5: Config version per agent (3.13c)

**Files:** `internal/db/migrations/00089_agent_config_version.sql`; `internal/db/queries/agents.sql` (new `RecordAgentConfigCheck`, `UpsertHostAgent` reset, the agent list/host reads return the columns), sqlc regen; `internal/agents` (service method + store), `internal/agentapi/stream.go` (`processHeartbeat` calls it after the comparison; a store error is logged, never fatal); `internal/api` agent DTOs (+ apitest); `web/src/api/schema.d.ts`; SPA agents list and host page badge (find the components that show the agent version), i18n en/de, Vitest.

- [ ] Tests first: store (writes when a version changes; no write within 10 min when equal; rewrite after 10 min; reset on re-enroll); agentapi (comparison → call with both versions; throttled or failed resolve → no call); apitest (agents list and host agent carry the fields, omitted when NULL); SPA (current / outdated with both shortened versions / nothing when unknown). RED → GREEN → sqlc generate → api-gen → lint → Go tests (`./internal/agents/...`, `./internal/agentapi/...`, internal/api `-run 'Agent'` then the whole internal/api once), web `npm test`, `npm run lint`, `npx tsc -b` → commit `feat(agents): store and show each agent's configuration version`.

### Task 6: Documentation

**Files:** `docs/BACKLOG.md` (3.10c, 3.12c, 3.55, 3.13c done with a short summary; 3.9's validation list gains the SAS standby case; "Last updated"), `docs/upgrades.md` (migrations 00088/00089, expiry pause for 24 h after upgrade, new metrics, behaviour: unasked items leave the evaluation and their alerts close); noix-docs scratch clone `/private/tmp/claude-501/-Users-noidee--dev-pulsix/76b69f36-5040-49f9-a80e-08a1ef63bcdb/scratchpad/noix-docs-merge`, new branch `docs/pulsix-check-correctness` from `docs/pulsix-rules-any-key`: check plugin reference, monitoring page, agents page, changelog; `npm run build`. Push nothing.

- [ ] Edit, build, commit in both repos (`docs: record the check correctness bundle (backlog 3.10c, 3.12c, 3.55, 3.13c)`, `docs(pulsix): advisory counts, expiry after outages, config version`).
