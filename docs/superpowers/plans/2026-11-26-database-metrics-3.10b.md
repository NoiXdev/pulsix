# More database metrics (backlog 3.10b) — implementation plan

**Goal:** close backlog 3.10b: a primary reports each replica's lag, Redis reports its persistence state, MySQL/MariaDB can report per-schema sizes.

**Spec (the backlog row):** "More database metrics: Redis persistence status, PostgreSQL per-replica lag on the primary, MySQL per-schema sizes — `postgres` reports lag only on a standby (0 on a primary), so a primary cannot alert on a replica that fell behind; `redis` reads no persistence fields (last save status, AOF state); `mysql` reports no sizes. Split from 3.10". Base design: `docs/superpowers/specs/2026-11-12-database-checks-design.md`.

**Architecture:** agent plugins `internal/agent/checks/plugins/{postgres,redis,mysql}` (one more query or INFO field set each, behind the existing `session` fakes); manager descriptors `internal/checkplugins/{postgres,redis,mysql}.go` (metrics, one `ItemAsked` hook for postgres, a bool parameter and a `MetricAsked` hook for mysql); golden catalogue `internal/rules/catalog_test.go`; SPA fixture `web/src/test/checkPlugins.json`; for the new mysql parameter the OpenAPI doc string test, `web/src/api/schema.d.ts` (`npm run api:gen`) and en/de i18n.

## Global Constraints
- Package tests only: `go test -race -timeout 900s ./internal/agent/checks/plugins/{postgres,redis,mysql}/... ./internal/checkplugins/... ./internal/rules/...`, `./internal/api/...` with `-timeout 1800s -run`; never `make test`, never whole-module, never `internal/e2e`.
- Unit tests use the existing fakes (postgres/mysql `fakeSession`, mysql `fakeDB`, redis in-process RESP server); real servers only in the existing testcontainers integration tests.
- No migration, no protocol change: new metric names and one optional parameter only.
- `golangci-lint run` and `gofmt -s -l` clean on touched packages before each commit. Conventional Commits, footer `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.

## Rulings
- **R1 Replica item = `replica:<application_name>@<client host>`** (`local` for a Unix-socket standby), prefix exported as `checkplugins.PostgresReplicaItemPrefix` — unique when several standbys keep the default name `walreceiver`, stable across reconnects, and kept apart from database items — cost if wrong: renaming later leaves the old items stale once.
- **R2 Replica items are always asked**, whatever `databases` lists (postgres `ItemAsked` hook: prefix, or the usual list rule) — the list names databases, not replicas — cost: a database literally named `replica:…` is always asked.
- **R3 Two senders with the same key** (same name and host): the one with the largest byte lag is reported; all count in `pg.replicas` — the worst one is what an alert needs.
- **R4 Which senders:** every `pg_stat_replication` row except `state = 'backup'` (pg_basebackup); logical-replication senders are included (their lag is the subscriber's) — cost: a subscriber shows up as a "replica" item.
- **R5 Byte lag** = current WAL position (primary `pg_current_wal_lsn()`; cascading standby last received, else replayed) minus `replay_lsn`, floored at 0; a NULL `replay_lsn` is an error observation ("replica reports no replay position"), never 0.
- **R6 Time lag** = `replay_lag`; NULL with 0 byte lag is 0 (PostgreSQL clears it once caught up and idle); NULL with bytes behind is an error ("replication lag time is unknown"), never 0.
- **R7 `pg.replica_state`** (discrete) carries the sender's state (`streaming`, `catchup`, `startup`, `stopping`).
- **R8 `pg.replicas`** on item "" every round, part of `hostMetrics` (failure on a failed round). Without `pg_read_all_stats` the view hides every detail: `pg.replicas` reports "no privilege to see replication" and no replica item is reported (the same treatment as `pg.connections`).
- **R9 At most 64 replica items per round** (`LIMIT`), `pg.replicas` stays exact (`count(*) OVER ()`) — no realistic primary has more; no separate "unreported" metric.
- **R10 No postgres parameter:** the view read is cheap, so per-replica lag is always on.
- **R11 Redis persistence metrics on item ""** from the INFO the plugin already reads (no new command, no new ACL): `redis.rdb_last_bgsave_status` and `redis.aof_last_write_status`, `redis.aof_last_bgrewrite_status` (discrete, Redis's own `ok`/`err`), `redis.rdb_last_save_age_seconds`, `redis.rdb_changes_since_last_save`, `redis.aof_enabled` and `redis.loading` (0/1). A missing or unreadable field is an error observation (the plugin's existing pattern); AOF statuses are reported even with AOF off (Redis keeps them `ok`).
- **R12 Save age clock:** server time from `server_time_usec` (Redis 6.2+), else the agent's clock; negative clamps to 0 — avoids skew between agent and a remote server. A server without snapshots configured reports a growing age (it is the time since start); not detected, because `CONFIG GET save` needs more than the documented `+info` ACL — documented instead.
- **R13 MySQL sizes are opt-in:** bool parameter `schema_sizes`, default false — `information_schema.TABLES` opens every table on MariaDB (slow with many tables) and needs a grant; existing checks are unchanged after the upgrade.
- **R14 Source and scope:** `information_schema.TABLES`, `DATA_LENGTH + INDEX_LENGTH` summed per schema, `mysql`, `information_schema`, `performance_schema`, `sys` excluded; MySQL 8 serves these from its statistics cache (`information_schema_stats_expiry`, default 24 h) — the server's setting is left alone and documented.
- **R15 Grant:** the docs name `GRANT SHOW VIEW ON *.*` — verified on MySQL 8.4 and MariaDB 11.4 to expose table sizes without the right to read data (pinned in the integration test). A schema the user cannot see is not reported (indistinguishable from absent).
- **R16 A failing sizes query never takes the round down:** `mysql.up` stays 1, `mysql.schemas_unreported` carries the classified error, no size items that round.
- **R16a (controller, fix round 1) The sizes query is bounded on the server too:** MariaDB `SET STATEMENT max_statement_time=<s> FOR SELECT …` first, MySQL's `/*+ MAX_EXECUTION_TIME(<ms>) */` hint after a 1064 (the replication chain's way of telling the dialects apart); the bound is the client's per-statement deadline minus a tenth (4.5 s of 5 s); the servers' own timeout errors (1969, 3024) read "timed out" — so a slow query does not keep running after the client gives up. Pinned by `TestBoundedSelectPerDialect`, `TestSchemasQueryPicksTheDialectBySyntax` and the real-server subtest `the server stops a bounded statement itself`.
- **R17 Cap 256 schemas**, `mysql.schemas_unreported` = total − reported (refused names included), exact via `COUNT(*) OVER ()`. No `schemas` filter list (follow-up if asked for).
- **R18 Both mysql schema metrics are asked only while `schema_sizes` is true** (`MetricAsked`), so switching sizes off reads as "not asked", not stale.
- **R19 Version skew:** an older agent ignores `schema_sizes` and reports none of the new metrics; an older manager stores the new metrics, refuses rules on them and refuses a mysql check carrying `schema_sizes` (unknown parameter) — upgrade note.

## Rulings amended after the independent review (fix round 2, controller)
- **R4a Only replaying senders count** (amends R4): left out besides `state = 'backup'` are `application_name` `pg_basebackup` (its `-X stream` sender), `pg_receivewal`, `barman_receive_wal`, logical table-sync workers (`application_name ~ '_[0-9]+_sync_[0-9]+(_[0-9]+)?$'`: PostgreSQL 14+ `pg_<subid>_sync_<relid>_<sysid>`, 10 to 13 `<slot>_<subid>_sync_<relid>`), and any sender with `replay_lsn IS NULL` (a standby that just connected counts from its first reply). Subscriptions' apply senders stay. Pinned by `TestReplicasQueryFiltersNonReplicaSenders` and the real-server `a base backup alongside is no replica` (fails without the filter).
- **R6a Time lag** (amends R6): 0 when the byte lag is 0; otherwise `COALESCE(replay_lag, flush_lag, write_lag)`; when all are NULL while bytes are behind (the window after an idle spell until the standby's next reply) the observation is left out of that round, no error. Pinned by `TestReplicaTimeLag`.
- **R8a A failed replica read** (amends R8) errors `pg.replicas` only and leaves out replica items that round; `pg.up` and the rest stay (like mysql's R16). `TestReplicaQueryFailureErrorsOnlyTheCount`.
- **R14a Empty schemas report 0** (amends R14): the query starts from `SCHEMATA` and LEFT JOINs `TABLES`; they count in the cap like the others. A login sees only schemas it holds a privilege on (accepted, documented); one that sees a schema but not its tables reads 0.
- **R19a Version skew** (amends R19): an agent-local `mysql` check with `schema_sizes` is ignored as a whole by an older manager; an older manager shows replica items of a `postgres` check with a `databases` list as not asked. Both in `docs/upgrades.md`.
- Cascading standby branch of the byte lag pinned by the real-server `a cascading standby reports its own replica`.

## Review Focus
- Primary without privileges: `pg.replicas` error, no items, rest unchanged — `TestReplicasWithoutPrivilege`.
- NULL replay position / NULL replay_lag never read as 0 lag — `TestReplicaLagUnknownIsNeverZero`.
- Duplicate default names keep the worst — `TestReplicaDuplicateKeyKeepsTheWorst`; databases filter does not unask replicas — `TestPostgresAsksForReplicaItems`.
- Real streaming standby (cascading-free, Unix socket) seen by a pg_monitor user — integration `replica on the primary`.
- Redis without Persistence fields (other Redis-protocol servers): errors, not zeros — `TestPersistenceFieldsMissing`; save age uses server time — `TestSaveAgeUsesServerTime`.
- MySQL sizes off by default: no query sent — `TestSchemaSizesOffSendsNoQuery`; a failing sizes query keeps the server up — `TestSchemaSizesFailureKeepsTheRoundUp`; cap — `TestSchemaCap`; integration with `SHOW VIEW` only on both flavours.

## Task 1: postgres per-replica lag
**Files:** `internal/agent/checks/plugins/postgres/{session,postgres,replicas}.go` (+ `replicas_test.go`, `helpers_test.go`, `integration_test.go`), `internal/checkplugins/postgres.go` (+ test), `internal/rules/catalog_test.go`, `web/src/test/checkPlugins.json`.
Interface: `session.Replicas(ctx, limit) ([]replica, total int, err error)`; `replica{Name, Host, State string; LagBytes, LagSeconds *float64}`.
- [ ] Commit `feat(postgres): report each replica's lag on the primary`.

## Task 2: redis persistence
**Files:** `internal/agent/checks/plugins/redis/{redis,persistence}.go` (+ `persistence_test.go`, integration), `internal/checkplugins/redis.go`, catalogue golden, SPA fixture.
- [ ] Commit `feat(redis): report persistence state`.

## Task 3: mysql per-schema sizes
**Files:** `internal/agent/checks/plugins/mysql/{session,mysql,schemas}.go` (+ tests, integration), `internal/checkplugins/mysql.go` (+ test), `internal/api/check_plugin_schema_internal_test.go`, `web/src/api/schema.d.ts`, `web/src/i18n/{en,de}/checks.ts`, catalogue golden, SPA fixture.
- [ ] Commit `feat(mysql): report per-schema sizes when asked`.

## Task 4: repo docs
`docs/BACKLOG.md` row 3.10b done; `docs/upgrades.md` behaviour note (R19).
- [ ] Commit `docs: backlog 3.10b done`.
