# Retention per organization (backlog 3.15) — design

Status: the owner accepted the proposal on 2026-11-13 ("ja gerne, wie du
vorgeschlagen hast"): an instance maximum set by the operator, a per-org
choice of 30/90/365 days never above it, an hourly deletion for shorter org
settings, 30/90-day graph ranges where the retention covers them, and the
5 m / 1 h aggregate tiers moved to 3.14a. Every other decision below is
**(Claude's call)**.

## Today

`check_samples` (00044) and `check_state_changes` (00046) are hypertables
with one-day chunks and a fixed, instance-wide 90-day retention policy set
in the migration. Graphs offer 1 h / 24 h / 7 d and bucket server-side
(`SeriesForHost`, `time_bucket`). Alerts and alert events have no
retention and keep none.

## Instance maximum

- `PULSIX_SAMPLE_RETENTION`: a whole number of days, 7–365, default 90
  (today's value, so nothing changes without the variable). An invalid
  value fails the manager's start with a clear message, like the other
  `PULSIX_*` settings (`internal/config`).
- At start, the manager makes the TimescaleDB retention policy of both
  hypertables equal to it (`remove_retention_policy(..., if_exists =>
  true)` + `add_retention_policy`) only when the current policy differs
  (read from `timescaledb_information.jobs`), in one transaction, logged
  at info. Chunks older than the maximum are then dropped by the database
  job as today. Every manager instance must use the same value; the docs
  say so (two instances with different values would flip the policy at
  each start — harmless but noisy, logged).
- Lowering the maximum deletes older data at the job's next run — the
  docs and `docs/upgrades.md` say this plainly.

## Per organization

- `org_monitoring_settings` (3.57's table) gains `retention_days integer
  NULL` with a CHECK `retention_days IN (30, 90, 365)` (additive
  migration). NULL means "the instance maximum".
- **Effective retention** of an org = `min(retention_days, instance
  maximum)`, or the instance maximum when NULL. The API returns the
  stored choice, the instance maximum and the effective value.
- **Deletion:** a ticker in the manager (hourly, injected clock, one
  instance at a time via an advisory lock or the existing leader pattern —
  follow what other periodic manager jobs use) deletes, for every org
  whose effective retention is below the instance maximum, rows of both
  tables older than `now() - effective retention`, in batches (e.g. 10 000
  rows per statement, looping until a batch deletes fewer, with a per-run
  time budget so a large backlog drains over several runs). Org-scoped
  statements; errors logged and retried next run.
- Settings UI: Settings → General → Monitoring (3.57's card) gains a
  "Keep measurements for" select: "Instance default (N days)", 30, 90,
  365 days; choices above the instance maximum are disabled with a hint.
  Lowering it warns that older measurements and state history are deleted
  within the hour. Admin writes, viewer reads (as the absent setting).

## Graphs

- Ranges 30 d and 90 d join 1 h / 24 h / 7 d on the host metric graph,
  offered only when the org's effective retention is at least the range.
  The server already buckets; check that the bucket count keeps a 90-day
  query's response size equal to today's ranges and that the query uses
  the lookup index (EXPLAIN in a test or a note in the report).
- 365 d is not offered (no aggregate tiers; 3.14a).

## Not affected

Alerts, alert events, check_current, items, item decisions, audit logs.
The item expiry (`PULSIX_ITEM_EXPIRY`) is independent of this retention.

## Tests

- Config: default 90, bounds, invalid values.
- Policy sync (DB-backed): set when different, untouched when equal, both
  tables.
- Settings: CHECK, API read/write, effective value with and without a
  stored choice and with a lower instance maximum; permissions; isolation.
- Deletion ticker (DB-backed): deletes only the org with the shorter
  retention, only older rows, both tables, in batches; respects the
  budget; another org untouched.
- SPA: select options and disabling, warning on lowering, graph ranges
  offered by effective retention.

## Documentation

`docs/BACKLOG.md` (3.15 done; 3.14a gains the aggregate tiers),
`docs/upgrades.md` (migration; the new variable; lowering deletes),
noix-docs (configuration reference: `PULSIX_SAMPLE_RETENTION`; org
settings: retention; monitoring graphs: new ranges; changelog).

## Amendments (amended after review)

- **Choice above the maximum:** the API stores it and caps it in the effective
  value instead of refusing it, so lowering `PULSIX_SAMPLE_RETENTION` never
  invalidates stored choices. The SPA still disables those options.
- **Restore sync:** `restore` re-runs the 90-day migrations 00044/00046, so it
  applies the retention policy again from its own environment right after the
  data is restored; a failure there is reported and the manager's next start
  sets it again. A failed sync at manager start is fatal.
- **Purge details:** the ticker's first run is an hour after start. Rows are
  deleted per hypertable chunk range (a ctid is only unique within a chunk), in
  batches of 10 000 with a 100 ms pause and a 2-minute budget per run, advisory
  lock 8317 (policy sync uses 8316). The purger keeps an in-memory mark per
  organization and table of what it has drained, so a steady-state run touches
  only newer chunk ranges; the marks are empty after a start and are cleared
  every 24 hours (a full sweep that catches late rows older than a mark). The
  list of organizations is narrowed in SQL and the effective value is computed
  once in code.
- **Graphs:** 30 d and 90 d use 168 buckets each (about 168 points, one more at
  an epoch-aligned edge), so the response size equals the shorter ranges; the
  query uses the lookup index on every chunk. Task 3 added the server ranges.
- **Aggregate tiers:** the 5 m / 1 h tiers and 365-day graphs moved to 3.14a.
