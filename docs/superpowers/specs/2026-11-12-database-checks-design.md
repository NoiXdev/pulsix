# Database checks: PostgreSQL, MySQL/MariaDB, Redis (backlog 3.10) — design

Status: the owner decided on 2026-11-12 to build 3.10 on the agent secrets of
3.5 ("Erst 3.5/3.6 bauen") and accepted the proposed metrics ("dann los"):
PostgreSQL — reachable, connections against the maximum, replication lag,
database sizes, oldest open transaction; MySQL/MariaDB — reachable,
connections, replication lag and state, slow queries; Redis — reachable,
memory against the maximum, clients, replication, evicted keys. Every other
decision below is **(Claude's call)**.

## Intent

The databases on a monitored host are what most outages are about, and the
agent can now hold credentials without the manager ever seeing them (3.5).
Three agent plugins read the health of a local (or host-reachable) database
server, the way Zabbix agent 2's PostgreSQL/MySQL/Redis plugins and
Checkmk's `mk_postgres`/`mk_mysql` do.

## The plugins

Names and check keys: `postgres`, `mysql` (MySQL and MariaDB), `redis`
(also Valkey and other Redis-protocol servers). They run on Linux, macOS and
Windows; Unix sockets on Linux and macOS only.

### Parameters (GUI)

- `secret` — the name of a secret in the host's `agent.yaml`
  (`checkplugins.SecretParam()`). **Required** for `postgres` and `mysql`;
  optional for `redis`, where no secret means `127.0.0.1:6379` without
  authentication.
- `postgres` only: `databases` — optional list of database names whose size
  to report (at most 64 entries, each 1–63 bytes); empty means every
  database that accepts connections and is not a template. It is the
  plugin's item parameter; item `""` (the server) stays asked
  (`HostItemAlwaysAsked`).

Where the server is and how to log in is the host's knowledge, so it lives
in the secret, never in the manager.

### Secret fields

All values are strings (the 3.5 file format). Unknown fields are refused, so
a typo is loud; a field with a bad value is named, its value never.

| Plugin | Field | Default | Meaning |
|---|---|---|---|
| postgres | `host` | `localhost` | host name or IP; a value starting with `/` is a Unix socket directory |
| | `port` | `5432` | 1–65535 |
| | `user` | — (required) | |
| | `password` | empty | |
| | `database` | `postgres` | database to connect to |
| | `sslmode` | `prefer` | `disable`, `prefer`, `require`, `verify-ca`, `verify-full` |
| | `sslrootcert` | system roots | absolute path to a CA file for `verify-ca`/`verify-full`, or `system` (absolute since 3.10a) |
| | `sslcert`, `sslkey` | — | client certificate and its unencrypted key, absolute paths, both or neither, not with `sslmode: disable` or a socket-directory `host` (3.10a) |
| mysql | `host` | `127.0.0.1` | |
| | `port` | `3306` | |
| | `socket` | — | Unix socket path; when set, `host`/`port` are not used |
| | `user` | — (required) | |
| | `password` | empty | |
| | `tls` | `preferred` | `false`, `preferred`, `true`, `skip-verify` |
| | `tls_ca`, `tls_server_name` | system roots, `host` | CA file (absolute path) and the name the server certificate is verified against; only with `tls: true` (3.10a) |
| | `tls_cert`, `tls_key` | — | client certificate and its unencrypted key, absolute paths, both or neither, not with `tls: false` (3.10a) |
| | `pipe` | — | Windows only: a named pipe (`MySQL` or `\\.\pipe\MySQL`), local only, not with `socket`; `host`/`port` are not used; use `tls: true` with `tls_ca` (any local user can create a pipe under the server's name while it is down) (3.10a) |
| redis | `host` | `127.0.0.1` | |
| | `port` | `6379` | |
| | `socket` | — | Unix socket path |
| | `user` | — | ACL user (Redis 6+); without it `AUTH <password>` |
| | `password` | — | without it no `AUTH` is sent |
| | `tls` | `false` | `false`, `true`, `skip-verify` |
| | `tls_ca`, `tls_server_name`, `tls_cert`, `tls_key` | — | as for mysql (`tls_cert`/`tls_key` with `true` or `skip-verify`) (3.10a) |

The driver configuration is built from these fields directly — never by
formatting a connection string — so a value cannot inject an option.

### Metrics

Item `""` unless stated. "Always" means every successful round, so no
metric strands when a server changes role.

**postgres** (PostgreSQL 10+)

| Metric | Kind | Value |
|---|---|---|
| `pg.up` | numeric | 1 connected and queried, 0 not |
| `pg.connections` | numeric | client backends in `pg_stat_activity` |
| `pg.max_connections` | numeric | `max_connections` |
| `pg.connections_pct` | numeric | the two above as a percentage |
| `pg.in_recovery` | numeric | 1 on a standby, 0 on a primary |
| `pg.replication_lag_seconds` | numeric | standby: 0 when everything received is replayed, else now − last replayed transaction; primary: 0 |
| `pg.replication_lag_bytes` | numeric | standby: received − replayed WAL; primary: 0 |
| `pg.oldest_xact_seconds` | numeric | age of the oldest open transaction of any other client backend, autovacuum and other workers not counted (changed during implementation); 0 when none |
| `pg.database_size_bytes` | numeric | per database, item = database name |

At most 256 databases are reported; the rest are named in the advisory
error. A database whose size the user may not read gets an error
observation on its item ("no privilege to read the size"). A database name
the manager's item contract refuses (`checks.ReportableItem`) is skipped and
named, as docker does.

**mysql** (MySQL 8.0+, MariaDB 10.5.9+ (changed during implementation: the documented grant `REPLICA MONITOR` exists from 10.5.9); older versions best effort)

| Metric | Kind | Value |
|---|---|---|
| `mysql.up` | numeric | 1/0 |
| `mysql.connections` | numeric | `Threads_connected` |
| `mysql.max_connections` | numeric | `max_connections` |
| `mysql.connections_pct` | numeric | percentage |
| `mysql.replication_state` | discrete | `none` (not a replica), `running` (both threads running), `connecting` (I/O thread connecting, SQL thread running), `stopped` (anything else) |
| `mysql.replication_lag_seconds` | numeric | seconds behind the source; 0 when not a replica; an error observation "replication is not running" when the server reports none |
| `mysql.slow_queries_per_s` | numeric | rate of `Slow_queries`; absent in the first round and after a counter reset |

`SHOW REPLICA STATUS`, falling back to `SHOW SLAVE STATUS` on a syntax
error; columns read by either name. Several channels: the worst state and
the largest lag. `Last_Error` and similar texts are never reported (they
can quote statements and data).

**redis** (Redis 5+, `INFO` only)

| Metric | Kind | Value |
|---|---|---|
| `redis.up` | numeric | 1/0 |
| `redis.memory_used_bytes` | numeric | `used_memory` |
| `redis.memory_max_bytes` | numeric | `maxmemory`, or `total_system_memory` when `maxmemory` is 0 |
| `redis.memory_pct` | numeric | used against the line above |
| `redis.clients` | numeric | `connected_clients` |
| `redis.role` | discrete | `primary` or `replica` |
| `redis.replication_link` | discrete | `none` on a primary, `up` or `down` on a replica |
| `redis.replication_lag_seconds` | numeric | replica: `master_last_io_seconds_ago`; primary: 0; an error observation "replication link is down" when the link is down |
| `redis.evicted_keys_per_s` | numeric | rate of `evicted_keys`; absent in the first round and after a reset |

### Failure reporting

- Cannot connect, authenticate or query: `<p>.up` = 0, and every other
  item-`""` metric of the plugin gets an error observation with the reason.
  Per-database items report nothing that round.
- A missing secret (3.5): the scheduler's error observation on the plugin's
  `SecretErrorMetric`, which is `<p>.up` — a configuration error, distinct
  from 0 "down".
- **Error texts never carry a driver's message.** They are classified into
  fixed texts — `connection refused`, `timed out`, `host not found`,
  `TLS handshake failed`, `authentication failed`, `permission denied`,
  `connection failed` — plus the server's error code where there is one
  (`SQLSTATE 28P01`, `error 1045`). A driver's message can quote the user,
  host or, in a parse error, the connection settings; a code cannot. A bad
  secret field reads "secret field `port` is not a valid port".
- One collection is bounded to 10 s in total (connect, TLS and queries), the
  minimum check interval. Per statement: postgres sets a 5 s server-side
  `statement_timeout` (SQLSTATE 57014 reads "timed out"), mysql runs each
  query under a 5 s context (the driver has no read timeout, which it would
  report as a failed connection), redis has the round's 10 s only. A hung
  query reads "timed out". One connection per round, closed afterwards.
- A postgres user without `pg_read_all_stats` cannot see other roles'
  sessions: `pg.connections`, `pg.connections_pct` and `pg.oldest_xact_seconds`
  are error observations ("no privilege to see other sessions"). A
  `sslrootcert` that is not PEM reads "is not a CA file". IPv6 hosts may be
  given with or without brackets.

### Safety preconditions from 3.5 (backlog 3.10 row)

1. The scheduler recovers a panic in `CollectWithSecret` and reports one
   error observation on the plugin's `SecretErrorMetric` with the fixed text
   "<plugin>: collection failed unexpectedly"; the panic value and stack are
   never logged or reported.
2. A test over `plugins.Registry()`: every SecretPlugin has a non-empty
   `SecretErrorMetric` that is one of its descriptor's metrics, and a
   plugin is a SecretPlugin exactly when its descriptor takes the secret
   parameter. A SecretPlugin that is also a StatefulPlugin (mysql, redis)
   keeps getting its secret through the clone.
3. A positive HTTP test through `internal/api/apitest`: a host check whose
   `secret` the agent did not report carries `missing_secret`.
4. The check form's hint on `secret`: it names a secret in `agent.yaml` on
   each host, not a value stored in Pulsix.

## Manager and SPA

One descriptor per plugin in `internal/checkplugins`, the constants, golden
tests, the rules catalogue, the SPA fixture, `schema.d.ts` and the i18n
texts, all per the "Adding a plugin" note. The discrete metrics follow from
the descriptors. No rule seeding.

## Tests

- Unit tests with fakes for every metric, state and failure mapping and for
  the field parsing.
- Integration tests against real servers started by testcontainers:
  PostgreSQL (the module's existing image), MySQL 8.4, MariaDB 11.4,
  Redis 7.4 — a primary each; replica rows and INFO texts from fixtures.
- Error classification tests that a password or user given in the secret
  never appears in any observation or advisory error.

## Documentation

- `docs/BACKLOG.md`: 3.10 done; follow-ups as rows (client certificates and
  custom CAs for mysql/redis, more metrics such as persistence status).
- `docs/upgrades.md`: version-skew paragraph per plugin.
- noix-docs: check-plugins reference (three plugins, parameters, metrics),
  the agent configuration page (fields per plugin, the monitoring user to
  create on each server with its grants), changelog.

## Non-goals

- Query checks with user SQL, per-table or per-schema statistics, MySQL
  per-schema sizes, Redis keyspace statistics.
- Client certificates; custom CAs for mysql and redis; Windows named pipes
  (done in 3.10a, plan `docs/superpowers/plans/2026-11-26-db-tls-client-certs-3.10a.md`;
  named pipes for mysql only).
- Discovering servers on the host.
