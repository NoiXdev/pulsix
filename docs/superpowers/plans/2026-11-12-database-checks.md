# Database Checks Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Three agent check plugins — `postgres`, `mysql`, `redis` — that read a database server's health with credentials from the host's `agent.yaml` (3.5), plus the four safety preconditions recorded on backlog row 3.10.

**Architecture:** Each plugin is its own package under `internal/agent/checks/plugins/<name>/` exporting only `New()`, implementing `checks.SecretPlugin` (mysql and redis also `checks.StatefulPlugin` for their rates). Shared field parsing, error classification and rate keeping live in `internal/agent/checks/plugins/internal/dbcheck`. Each plugin task is vertical: agent package, descriptor, golden tests, SPA fixture, `schema.d.ts`, i18n — the "Adding a plugin" note in `internal/agent/checks/plugins/registry.go` is the checklist.

**Tech Stack:** Go (`github.com/jackc/pgx/v5` already in the module; new `github.com/go-sql-driver/mysql`; an own minimal RESP client for Redis, no dependency), testcontainers, React/TypeScript.

**Spec:** `docs/superpowers/specs/2026-11-12-database-checks-design.md`

## Global Constraints

- Plugin names/check keys `postgres`, `mysql`, `redis`; SPA order after `raid`. Parameters, secret fields, defaults, metric names and kinds exactly as the spec's tables.
- `secret` is `checkplugins.SecretParam()` with `Required: true` for postgres and mysql, optional for redis; postgres `databases`: list, ≤ 64 entries, each 1–63 bytes, `ItemParam`, `HostItemAlwaysAsked: true`.
- `SecretErrorMetric()` is `pg.up`, `mysql.up`, `redis.up`.
- Driver config built from fields directly (pgx `ParseConfig("")` is NOT allowed — it reads PG* environment variables; build `pgconn.Config` fields or parse a conn string that the code assembles with proper quoting only if no other way, and say which); never format a DSN from untrusted values without quoting.
- Error texts: only the fixed classified texts of the spec plus server error codes; never a driver message, never a field value, host, user or password. Tests assert a planted password and user never appear in any observation or advisory error.
- One collection ≤ 10 s total (`dbcheck.CollectTimeout`); per-query server timeout 5 s where available; one connection per round, always closed.
- Caller context ending → return nothing but `ctx.Err()` (the Plugin contract); own deadline → `up` = 0 with "timed out".
- Every item and label passes `checks.ReportableItem` / `checks.ReportableLabel`.
- No rule seeding. Integration tests use testcontainers images: the module's existing Postgres image (`internal/db/testdb`'s, or a plain container of the same image), `mysql:8.4`, `mariadb:11.4`, `redis:7.4-alpine`; nothing else runs real servers. Never touch any other Docker container on this machine.
- Repo rules: TDD; never `go test ./...` (single packages `-race -timeout 900s`; internal/api `-timeout 1800s -run …`); `golangci-lint run` and `GOOS=windows golangci-lint run ./internal/agent/...` clean; `GOOS=linux|darwin|windows go vet ./internal/agent/...`; web `npm test`, `npm run lint`, `npx tsc -b`; commit footer `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`; public docs name no tech stack; never touch `~/_dev/noix-docs`.

## Review Focus

1. A secret value (password, user, host) reaching an observation, advisory error, log line or panic message — through a driver error, a TLS error naming the host, a DSN parse error, or `fmt.Errorf("%v", cfg)`.
2. A server that accepts TCP but never answers (hung handshake, Redis that never replies to AUTH) — the round ends within 10 s with `up` = 0 "timed out" and the connection closed.
3. Role changes: a PostgreSQL standby promoted, a MySQL replica reset, a Redis replica turned primary — every item-`""` metric keeps being reported (no stranding).
4. Counter resets (server restart) and the first round — no negative or huge rate, metric absent that round.
5. A huge or hostile reply (Redis INFO of many MiB, a database list of thousands, names with control characters) — bounded reads, items skipped and named.

---

### Task 1: Scheduler hardening and the shared `dbcheck` package

**Files:** `internal/agent/checks/secret.go` (+ `scheduler_secret_test.go`), `internal/agent/checks/plugins/registry_test.go`, new `internal/agent/checks/plugins/internal/dbcheck/` (+ tests).

**Produces (exact names):**
- `dbcheck.CollectTimeout = 10 * time.Second`, `dbcheck.QueryTimeout = 5 * time.Second`
- `type dbcheck.Field struct { Name, Default string; Required bool; Valid func(string) bool; Want string }` and `dbcheck.Parse(fields map[string]string, spec []Field) (map[string]string, error)` — returns the defaulted values; errors exactly `` secret field `<name>` is required ``, `` secret field `<name>` is not <Want> `` (e.g. "a valid port"), `` secret field `<name>` is not known ``; a nil map behaves as empty.
- `dbcheck.Port(s string) bool`, `dbcheck.OneOf(opts ...string) func(string) bool`
- `dbcheck.Classify(err error) string` → one of `connection refused`, `timed out`, `host not found`, `TLS handshake failed`, `connection failed` (network layer only; plugins add `authentication failed`, `permission denied` and codes from their own driver error types)
- `dbcheck.Down(prefix, up string, others []string, reason string) []checks.Observation` → `up`=0 plus an error observation `<prefix>: <reason>` on each of `others`, item `""`
- `type dbcheck.Counter struct{…}`; `(*Counter).Rate(at time.Time, v uint64) (float64, bool)` — false in the first call and when `v` < previous (reset) or time did not advance.

- [ ] Tests first: `secret.go` recovers a panic in `CollectWithSecret` → exactly one `ErrObservation("", SecretErrorMetric(), "<name>: collection failed unexpectedly")`, panic value absent from it (plant a value in the panic and assert); a stateful secret plugin (fake implementing both `StatefulPlugin` and `SecretPlugin` on its instance) still gets its fields through the per-key clone; `registry_test.go` gets a test that every `checks.SecretPlugin` in `Registry()` has a non-empty `SecretErrorMetric()` (vacuous until Task 2). Table tests for every `dbcheck` function, including `Classify` on `*net.OpError` with ECONNREFUSED, `*net.DNSError`, deadline errors, `tls.RecordHeaderError`/`x509` errors, wrapped errors. RED → GREEN → lint (three GOOS) → commit `feat(agent): recover panicking secret plugins and add shared database check helpers`.

### Task 2: `postgres`

**Files:** `internal/agent/checks/plugins/postgres/` (plugin, a querier seam over pgx for unit tests, integration test), registry + `registry_test.go`; `internal/checkplugins/postgres.go` + registry/golden tests + `testdata`/SPA fixture; `internal/checks` constant + plugin-list test; `internal/rules/catalog_test.go` golden; `web/src/api/schema.d.ts` (api-gen); i18n en/de (plugin name in both picker maps, `checks.params.postgres.*`); `internal/checkplugins` cross-check test that a plugin is a SecretPlugin exactly when its descriptor `UsesSecret()` and that `SecretErrorMetric()` is one of its descriptor's metrics; `internal/api` positive `missing_secret` test (precondition 3: an agent reporting secret names without the check's secret → the host check carries `missing_secret`); `docs/upgrades.md` version-skew paragraph.

- [ ] Unit tests: every metric from fake rows (primary, standby caught up, standby lagging, oldest transaction none/some), `databases` filter, 256-database cap, a database without size privilege, an unreportable database name, field parsing (each default, bad port, bad sslmode, unknown field, socket directory host), every failure class (refused, timeout, auth `28P01` → "authentication failed (SQLSTATE 28P01)", permission `42501`, other SQLSTATE), no secret value in any output. Integration test against a real server: connect with a dedicated `pg_monitor` user, assert `pg.up` = 1, connection counts, sizes of a created database, wrong password → `up` 0 "authentication failed (SQLSTATE 28P01)". RED → GREEN → lint (three GOOS) → `go test` for agent/checkplugins/checks/rules packages and `internal/api -run` the new and enum tests → commit `feat(agent): report PostgreSQL health`.

### Task 3: `mysql`

**Files:** as Task 2 for `mysql` (no item param); `go.mod`/`go.sum` (`github.com/go-sql-driver/mysql`, latest release); the plugin is a `StatefulPlugin` whose instances keep a `dbcheck.Counter`. Build the driver config with `mysql.NewConfig()` and its fields; register custom TLS configs under a name that carries no field value; set the driver's logger to discard (`mysql.SetLogger`) so it never prints to stderr.

- [ ] Unit tests: replica status from fixture rows under both column spellings (`Replica_IO_Running`/`Slave_IO_Running`, `Seconds_Behind_Source`/`Seconds_Behind_Master`), the `SHOW REPLICA STATUS` → `SHOW SLAVE STATUS` fallback on error 1064, multi-channel worst/max, NULL lag → error observation, every state, slow-query rate across rounds and reset, field parsing, failure classes (1045 → "authentication failed (error 1045)", 1227/1142 → "permission denied (error N)"), no secret in output. Integration tests against `mysql:8.4` and `mariadb:11.4` with a user granted `PROCESS, REPLICATION CLIENT` (MariaDB: also `SLAVE MONITOR` / `REPLICA MONITOR` if needed — find out and document in the report): `up` 1, counts, `replication_state` `none`, lag 0, wrong password. RED → GREEN → lint → tests → commit `feat(agent): report MySQL and MariaDB health`.

### Task 4: `redis`

**Files:** as Task 2 for `redis`; an own RESP2 client in the package (dial TCP/Unix with optional TLS, `AUTH` as a RESP array, `INFO`, reply bounded to 1 MiB, deadline on the connection); `StatefulPlugin` for the eviction rate.

- [ ] Unit tests against an in-process fake server: INFO parsing (primary, replica link up/down, maxmemory 0 → total_system_memory, `master_last_io_seconds_ago` -1), AUTH with and without user, `-NOAUTH`/`-WRONGPASS` → "authentication failed", a server that never answers (deadline), an oversized reply, eviction rate and reset, no secret in output, no secret → no AUTH and default address. Integration test against `redis:7.4-alpine` with `requirepass`: `up` 1 with the password, `authentication failed` without. RED → GREEN → lint → tests → commit `feat(agent): report Redis health`.

### Task 5: SPA hint and form tests

**Files:** `web/src/features/checks/ParamFields.tsx` (or wherever the generic field hint renders) and i18n for the `secret` hint (precondition 4: "Name of a secret in agent.yaml on each host — the value never leaves the host", German alike), Vitest.

- [ ] Tests: each of the three plugins selectable in both forms, `secret` shown with the hint and required for postgres/mysql (client validation refuses empty), optional for redis, `databases` list round-trip, `mysql.replication_state`/`redis.role`/`redis.replication_link` not graphable. RED → GREEN → `npm test`, `npm run lint`, `npx tsc -b` → commit `feat(web): offer the database checks`.

### Task 6: Documentation

**Files:** `docs/BACKLOG.md` (3.10 done with a short note; new rows: client certificates and custom CAs for mysql/redis and Windows named pipes; more database metrics such as Redis persistence status and PostgreSQL per-replica lag on the primary), the "Adding a plugin" note if a step was missing; noix-docs scratch clone `/private/tmp/claude-501/-Users-noidee--dev-pulsix/76b69f36-5040-49f9-a80e-08a1ef63bcdb/scratchpad/noix-docs-merge`, new branch `docs/pulsix-database-checks` from `docs/pulsix-agent-secrets`: check-plugins reference (three plugins, parameters, metrics, versions), agent configuration page (secret fields per plugin with an example each, the monitoring user and grants per server), monitoring guide example rules, changelog. `npm run build`. Push nothing.

- [ ] Edit, build, commit in both repos.
