# Upgrades and migrations

The rules a release follows so that an upgrade is "pull, restart", and what
an operator can count on. The operator's steps are in
`deploy/compose/README.md` ("Upgrading"); the migration file rules are also in
`internal/db/migrations/README.md`.

## Migrations

- **They run by themselves.** Every manager start (and `worker add`) applies
  the embedded goose migrations before anything else, under the PostgreSQL
  advisory lock `7256` (`migrationLockKey`, `internal/db/db.go`; the value
  must never change). Two managers starting together therefore serialise; the
  second finds nothing left to do. There is no separate migration step.
- **They only go up.** The manager and the `restore` command call goose's
  `Up` and `UpTo`, never `Down`. A `-- +goose Down` section exists in each
  file so a migration can be rolled back in a development database, and is
  never run against an installation. Rolling back a production database is a
  restore (see "Downgrading").
- **Additive only.** A migration adds tables, columns, indexes and
  constraints. A released migration is **never edited**: goose records the
  version, not the content, so an edit changes fresh installs and leaves every
  upgraded one as it was. The next change is a new file under the next free
  number (00030 is retired for good, see `internal/db/migrations/README.md`).
- **A constraint that validates existing rows repairs them first.** Otherwise
  the migration aborts the upgrade on exactly the installations that need it.
- **Renames and drops go expand, then contract, across two releases.**
  - Release N (expand): add the new column or table, write to both, read the
    new one, keep the old one. Nothing a shipped release reads is removed.
  - Release N+1 (contract): once no release that could still be running reads
    the old one, drop it in a new migration.
  The previous minor stays supported (below), so the contract step cannot land
  in the same release as the expand step. Backlog 2.93
  (`git_repos.vault_credential_id`) is an example.
- **A migration can block writes while it builds an index.** Migration 00079
  builds `jobs_host_ids_idx` with a plain `CREATE INDEX`, which blocks writes
  to `jobs` (new runs, job status updates) until it finishes. On a large run
  history expect the manager's first start after the upgrade, which runs the
  migration, to take correspondingly longer,
  and upgrade outside busy automation windows.
- **Migration 00082 fills a table from the run history.** It adds `job_hosts`,
  which links every run to the hosts it targeted and serves a host's run list,
  and fills it once from the existing runs in the same transaction. Its time
  grows with runs × hosts per run; as with 00079, upgrade outside busy
  automation windows. It then drops `jobs_host_ids_idx` (00079), which nothing
  reads any more. An older manager does not start on the new schema (see
  below), but one that is still running while the new version migrates keeps
  creating runs without `job_hosts` rows, and nothing fills them in later:
  those runs never appear in their hosts' run lists. Stop every manager
  instance before the new version starts.
- **Migration 00083 rewrites stored repository subpaths (7.94).** Every
  subpath of an organization's repository and of an installation's
  repository definition loses its empty and `.` segments: `/ansible/`,
  `./ansible` and `ansible//` become `ansible`, and `/` or `.` becomes none.
  The old spellings named the same directory, so an older release reads the
  new values the same way and a downgrade needs no reverse step. A
  definition could hold any subpath before; one with `..`, characters other
  than letters, digits, `.`, `_`, `/` and `-`, or more than 200 characters
  stays as it was: its repositories' runs and syncs fail as before, and an
  organization cannot save its projection (not even to set a credential)
  until the definition is saved with a valid subpath. A manager of an older
  version still running while the new one migrates stores a repository saved
  through it as sent again; stop every manager instance before the new
  version starts.
- **Migration 00087 adds the check key of an item rule (3.13e).** It adds the
  nullable column `check_rules.item_check_key` and a CHECK that it is set only on
  item-scope rules (`item_check_key IS NULL OR scope = 'item'`); nothing is
  rewritten, and an older release ignores the column. Stop every manager
  instance before the new version starts: an older manager that keeps
  evaluating alongside judges keyed item rules against the plugin's key and
  gives checks under other keys no org, group or host rule, so states and
  alerts can flap while both run, and an older manager that moves a keyed
  item rule to another scope hits the CHECK and answers 500.
- **Migration 00088 adds the agent's connection start (3.12c).** It adds the
  nullable column `host_agents.connected_since` (catalog only, nothing
  backfilled, nothing rewritten); an existing row reads as unknown until that
  agent's first heartbeat after the upgrade sets it. A heartbeat or touch that
  follows more than 24 hours without contact resets it, and so does re-enrolling
  an agent that was silent that long; a certificate renewal and shorter gaps (a
  nightly power-off, a manager restart, a flaky link) keep it. An older release
  never reads or writes the column (it lists the columns it knows). Stop every
  manager instance before the new version starts anyway: an older manager that
  still handles an agent's heartbeats neither sets the start nor resets it after
  a long silence, so that agent keeps a stale start (or none) and can count for
  the item expiry too early.
- **Migration 00089 adds the agent's configuration check (3.13c).** It adds
  three nullable columns to `host_agents`, `config_version`, `config_expected`
  and `config_checked_at` (catalog only, nothing backfilled). A row reads as
  not compared yet until the first heartbeat after the upgrade has been
  compared, so the GUI shows no configuration badge until then. Re-enrolling an
  agent or renewing its certificate resets the three columns. The manager writes
  the check only when a version changes or the stored check is 10 minutes old.
  An older release ignores the columns. There is no reason beyond the usual
  rule: an older manager that still handles an agent neither records the
  comparison nor resets it on re-enrollment, so the badge can be stale or absent
  until the new manager has handled that agent.
- **Migrations 00090 to 00092 add client hosts (3.57).** 00090 adds
  `hosts.kind` (`server` or `client`, default `server`, a CHECK; the constant
  default makes it a catalog change and every existing host reads `server`).
  00091 adds the table `org_monitoring_settings` (one row per organization, a
  missing row meaning the defaults; row-level security as `org_job_settings`).
  00092 adds the nullable column `host_agents.online_since` (catalog only,
  nothing backfilled): the start of the agent's current online period, which
  stays unknown until the agent's first touch after the upgrade. Stop every
  manager instance before the new version starts. An older release ignores the
  columns and the table (it lists the columns it knows) and creates hosts as
  servers, but it neither freezes a client nor writes `online_since`: an older
  manager that keeps evaluating alongside judges a host marked client as a
  server, so it turns unknown and alerts while its agent is off, and an older
  manager that handles an agent's heartbeats leaves its online period start
  stale or unset, so a returning client's long-interval checks can go unknown
  too early. These effects touch only hosts marked client during an overlap,
  and a stale online start heals at that agent's next gap of more than 90
  seconds.

- **Migration 00093 adds the retention choice per organization (3.15).** It adds
  the nullable column `org_monitoring_settings.retention_days` (30, 90 or 365, a
  CHECK; no default, so a catalog change). Null means no choice: every existing
  organization keeps the instance maximum and the upgrade deletes nothing. An
  older release ignores the column (it lists the columns it knows when it reads
  and writes the table, and its write leaves the stored choice alone), so the
  migration does not require stopping every manager instance; an older manager
  that keeps running during the roll only never deletes for the per-organization
  choice, which the new manager does. The new manager's start is the step that
  matters (see "Behaviour changes").

## A database newer than the binary is refused

goose does nothing for a database whose recorded version is above the newest
embedded migration, so an older manager would start against tables it does not
know. `db.CheckSchemaNotNewer` (`internal/db/db.go`) prevents that: it
compares `SchemaVersion` with `LatestVersion()` and returns an error wrapping
`db.ErrSchemaNewer`:

> the database schema is at version N, this Pulsix knows up to M: run the
> newer version, or restore a backup taken by this one

It is called right before `Migrate`, in the manager start
(`internal/app/app.go`) and in `worker add` (`internal/app/workeradd.go`). A
database with no schema yet, or at or behind the newest migration, passes.
The check reads under the migration lock, so it never sees a migration
another manager is half way through. `restore` has its own guard: a backup
from a newer Pulsix is refused up front with `backup.ErrBackupTooNew`, before
the database is touched. The `cli backup` command does not migrate and is not
affected.

## Version skew between the manager, workers and agents

The rule: the manager accepts a worker or agent at its own minor version and
at the previous minor version. It is **shown, not enforced**. Refusing an
older component would cut a fleet off in the middle of an upgrade, so a
component outside the rule keeps working and is flagged.

`versioncompat.Of(manager, component)` (`internal/versioncompat`) compares
`MAJOR.MINOR` only; the patch, a pre-release suffix (`-rc.1`) and a build
suffix (`+abc`, `-12-gabc1234`) are ignored, and a leading `v` is optional.

| Status (`version_status`) | Component compared with the manager | Shown |
|---|---|---|
| `current` | same minor | nothing |
| `previous_minor` | exactly one minor below | nothing |
| `update_required` | two or more minors below, or an older major | "Update required" badge |
| `newer` | a higher minor or major | "Newer than manager" badge |
| `unknown` | either version is empty or `dev`, the embedded worker, or not `MAJOR.MINOR.PATCH` | nothing |

The badges are on the Workers page, the Agents page and the host agent card
(`web/src/i18n/*/workers.ts`, `versionStatus`). The manager also logs one
warning per worker or agent, the first time in a process that it sees the
component as `update_required` or `newer` (`versioncompat.Warner`), not one
per heartbeat or claim. `newer` normally means the manager was rolled back or
has not been updated yet; `update_required` means the component was skipped
in an upgrade.

An agent one minor older does not know the `network` and `uptime` check plugins
(backlog 3.7): it skips those two checks, logs a warning per configuration
push (the manager re-pushes on every heartbeat while the agent has not
converged, so about one warning every 30 seconds at the default heartbeat
interval), and keeps running the rest until it is updated.

An agent that predates the `docker` check plugin (backlog 3.12) behaves as with
`network` and `uptime` above: its scheduler skips a `docker` check, the agent
logs a warning per configuration push (`internal/agent/session.go`,
`consumeManagerMessages`; about one every 30 seconds at the default heartbeat
interval, because it does not report that configuration as converged), and it
keeps running the rest. A `docker` check therefore needs an agent at the version
that ships the plugin or later. The manager does not gate on this: the check
can be bound to any host, and the check shows no readings on the host page until
its agent is updated. Nothing is seeded into organizations, so no existing host
gains the check by itself, and there is no schema change.

The same holds for the `temperature`, `smart` and `raid` plugins (backlog 3.9):
an older agent skips such a check, warns on every configuration push and shows
no readings until it is updated, and the manager does not gate on it. Nothing is
seeded and there is no schema change. `smart` also needs smartmontools on the
host (the agent reports "smartctl not found" until it is installed, and finds a
later install without a restart).

The same holds for the `zfs` plugin (backlog 3.9a): an older agent skips a `zfs`
check, warns on every configuration push and shows no readings until it is
updated, and the manager does not gate on it. Nothing is seeded and there is no
schema change. The check needs the OpenZFS tools (`zpool`, `zfs`) on the host;
a host without ZFS reports zero pools.

The same holds for the `postgres` plugin (backlog 3.10): an older agent skips a
`postgres` check, warns on every configuration push and shows no readings until
it is updated, and the manager does not gate on it. The check also needs the
host's `agent.yaml` secret it names (backlog 3.5), which only an agent that
reads that file can resolve. Nothing is seeded and there is no schema change.

The same holds for the `mysql` plugin (backlog 3.10, MySQL and MariaDB): an
older agent skips a `mysql` check, warns on every configuration push and shows
no readings until it is updated, and the manager does not gate on it. It needs
the host's `agent.yaml` secret it names (backlog 3.5) as `postgres` does.
Nothing is seeded and there is no schema change.

The same holds for the `redis` plugin (backlog 3.10, Redis and Valkey): an
older agent skips a `redis` check, warns on every configuration push and shows
no readings until it is updated, and the manager does not gate on it. Its
`secret` parameter is optional: a check without one reads `127.0.0.1:6379`
without logging in, and a check that names one needs the host's `agent.yaml`
secret (backlog 3.5) as `postgres` does. Nothing is seeded and there is no
schema change.

The same holds for the `nginx` plugin (backlog 3.11, the first of the web
server checks): an older agent skips an `nginx` check, warns on every
configuration push and shows no readings until it is updated, and the manager
does not gate on it. Its `secret` parameter is optional: a check without one
sends no login, and a check that names one needs the host's `agent.yaml`
secret (backlog 3.5) with the fields `basic_auth_user` and
`basic_auth_password`. Nothing is seeded and there is no schema change.

The `apache` plugin (backlog 3.11) follows the same rule: an older agent
skips an `apache` check, warns on every configuration push and shows no
readings until it is updated, and the manager does not gate on it. Its
optional `secret` is the same basic-authentication login (`basic_auth_user`,
`basic_auth_password`), and the agent adds the query key `auto` to the status
URL when it is missing. Nothing is seeded and there is no schema change.

The `caddy` plugin (backlog 3.11) follows the same rule: an older agent skips
a `caddy` check, warns on every configuration push and shows no readings until
it is updated, and the manager does not gate on it. Its optional `secret` is
the same basic-authentication login (`basic_auth_user`,
`basic_auth_password`). The agent reads Caddy's metrics in the Prometheus text
format; Caddy must have its HTTP metrics switched on, otherwise the three
metrics besides `caddy.up` report that they are not enabled. Nothing is seeded
and there is no schema change.

The `phpfpm` plugin (backlog 3.11) follows the same rule: an older agent skips
a `phpfpm` check, warns on every configuration push and shows no readings until
it is updated, and the manager does not gate on it. Its optional `secret` is
the same basic-authentication login (`basic_auth_user`,
`basic_auth_password`), sent to every pool. Each `urls` entry is one pool and
the item its metrics are reported on (the agent adds the query key `json` when
it is missing); item `""` carries `phpfpm.pools_down`, which is also where a
missing secret is reported. Nothing is seeded and there is no schema change.

The `script` plugin (backlog 3.6g) follows the same rule for the check: an older
agent skips a `script` check, warns on every configuration push and shows no
readings until it is updated, and the manager does not gate on it. **The
`commands:` key in `agent.yaml` is stricter.** An agent older than this release
does not know the key and refuses the whole file, so a host whose `agent.yaml`
gains `commands:` also loses its `secrets:` until that agent is updated. Update
the agent first, then add `commands:`. Migration 00094 adds two default rules,
"Script reports critical" and "Script reports warning", to every existing
organization (skipped where a rule of that name exists); they judge nothing
until a `script` check reports.

Local checks in `agent.yaml` (backlog 3.5a) skew both ways. **The `checks:` key is
stricter for an older agent.** An agent older than this release does not know the
key and refuses the whole file, so a host whose `agent.yaml` gains `checks:` also
loses its `secrets:` and `commands:` until that agent is updated. Update the agent
first, then add `checks:`. A new agent under an older manager runs its local
checks and the manager stores their values, but does not show or judge them until
the manager is updated. Migration 00095 adds two nullable columns to
`host_agents` (`local_checks`, `local_check_problems`); NULL means an agent that
has not reported (one older than `agent.yaml`, 3.5, or one that has not sent a
heartbeat since it enrolled), and nothing is seeded. An agent from 3.5 reads
`agent.yaml` but predates `checks:`, so it is stored as reporting no local checks
and no problems (`[]`), not as unknown. A certificate renewal keeps both columns
and the secrets report; a re-enrollment resets all four until the first
heartbeat.

SNMP checks and the organization secret store (backlog 3.13d) add two migrations
and a dependency, and no version skew: the `snmp` check runs on the manager and is
never sent to agents, so agents need no update. Migration 00096 adds the table
`org_secrets` (sealed fields, clear field names, row-level security like every
organization table); it is part of backups as it is, the sealed column included,
and needs the same master key on restore as the other sealed data. Migration 00097
adds two default rules, "SNMP device unreachable" and "Interface down", to every
existing organization (skipped where a rule of that name exists); they judge
nothing until an `snmp` check reports. **Existing hosts get no SNMP checks**: a
check has to be added to a host or a monitoring group, with a secret created under
Settings, Secrets first. The manager binary now depends on
`github.com/gosnmp/gosnmp` v1.47.0 (an internal detail; public docs do not name it).
A downgrade is a restore of a backup from before the upgrade (see Downgrading);
that backup has no secrets, so secrets created since have to be created again after
the next upgrade.

JSON values on the `http` check (backlog 3.6h) add no migration, no dependency and no
version skew: the check runs on the manager and is never sent to agents. Existing
`http` checks behave as before until `json_values` or `secret` is set. The manager now
reads at most `min(2, GOMAXPROCS)` JSON responses at once; a check waits for a turn, and
only one whose probe deadline passes while it waits (more JSON responses due at once than
that, each slow to read) reports "the manager was too busy to read the JSON response", on
its JSON values only (backlog 3.71).

Probe vantage points (backlog 3.13a) add one migration, additive proto fields and
a version skew that only limits the feature. Migration 00098 adds three tables
(`probe_assignments`, `probe_current`, `probe_samples`, the last a hypertable with
90-day retention, all with row-level security) and one column
(`host_agents.probes_supported boolean NOT NULL DEFAULT false`); nothing is dropped
or rewritten, and no rule is seeded. A check that names no vantage group behaves and
writes exactly as before. **Only agents of this release probe**: they report
`probes_supported` in the heartbeat, and only those are ever assigned a probe. An
older agent keeps working unchanged, is never assigned and never receives a probe
configuration; a check whose groups contain only older agents is probed from the
other vantages only (the manager, when enabled; with none left the check reports
unknown until agents are updated). An older manager ignores the new heartbeat fields,
so a new agent that only ever talked to older managers probes nothing. During a
rolling upgrade of several managers, an agent that already received assignments
from an upgraded manager keeps probing; results that reach a not-yet-upgraded
manager are dropped, so that vantage may read "no result" until all managers are
upgraded. The agent binary grows by about
4.4 % (the SNMP library is linked in although agents never run `snmp`, backlog 3.78),
and a probing agent carries up to 200 assignments, four probes at once. The proto
change is additive (`buf breaking` clean). A downgrade needs the usual restore of a
backup from before the upgrade (see Downgrading).

A worker one minor older reports no playbook variable sources with a repo
sync (backlog 7.75): the manager keeps the repository's previous record and
flags it (`repo_var_sources.worker_reads = false`) instead of replacing it.

A worker that predates the 7.114 number rule (v0.1.0-rc.1, for one) and reads
a playbook holding a number of 1e21 or more, or below 1e-6 (zero aside), has
its whole report ignored and logged by the manager (7.122) until the worker is
upgraded.

7.88 adds `repo.repo_subpath` to a repo run's claim (in its JSON kind
payload; no proto change). The field is additive: a worker one minor older
ignores it and keeps 7.83's behaviour (a run finds only the roles its own
worker's last sync installed). A 7.88 worker under an older manager finds
no `repo_subpath` and derives it from the claim's `subpath` by removing the
`/<playbook path>` suffix (exact for every manager released so far); that
fallback is dropped one minor later. A worker downgraded below 7.88 ignores
`<repo>.requirements/` next to each clone (it stays on disk until removed by
hand) and installs into `<repo>.galaxy` again on its next sync.

7.95 adds the WorkerService call `ListRepositories` (additive proto change).
A worker asks it at start and then every six hours (five minutes after a
failed call), between jobs, and removes
the repo cache of every repository the answer does not list. A manager older
than the call answers `Unimplemented`; the worker then removes nothing, logs
that once per attempt at info level, and keeps the caches of deleted
repositories as before. An older worker never asks.

## Behaviour changes to check after an upgrade

- **3.15, retention per organization:** the new variable `PULSIX_SAMPLE_RETENTION`
  (whole days, 7 to 365, default 90, the previous fixed value) is the longest time
  measurements and state history (`check_samples`, `check_state_changes`) are
  kept. Without it nothing changes, with one exception: **a retention policy
  changed by hand in the database (for example to 180 days) is replaced by the
  variable's value at the first start, and the database job then drops the older
  chunks. Set `PULSIX_SAMPLE_RETENTION` to that value before upgrading.** A hand-made
  policy without `drop_after` (`drop_created_before`) stops the start with a message
  telling you to remove it. Every manager start sets the retention policy
  of both tables to it (logged at info; a value outside the range, or a failed
  sync, stops the start), and so does `restore`: it re-runs the old 90-day
  migrations, then applies the value from its own environment, so give `restore`
  the same value as the manager (in the compose stack `docker compose run --rm cli`
  passes it from `.env`). The restore prints the retention it applied, and says so
  when it fell back to the 90-day default because the variable is not set; the
  database job then drops older data at its next run (within a day), not at the
  next manager start. **Lowering the value deletes the older rows**: the
  database drops chunks older than the new maximum right after the next start (at
  the latest within a day). Raising it does not bring deleted data back. Every
  manager instance must use the same value, otherwise each start flips the
  policy. An organization admin can choose 30, 90 or 365 days for the
  organization (Settings, General, Monitoring, "Keep measurements for", and
  `retention_days` in `PUT /api/v1/orgs/{org}/settings/monitoring`: omitted keeps,
  null clears); the effective value is the smaller of the choice and the maximum,
  and a choice above the maximum is stored and capped. A manager deletes the older
  rows of every organization whose effective retention is below the maximum once
  an hour (the first run an hour after start; advisory lock 8317, one manager at
  a time), in batches of 10 000 rows with a 100 ms pause, at most two minutes per
  run, so a large backlog drains over several runs; rows that arrive late and are
  older than the drained mark go at the next daily full sweep. Alerts, alert
  events, current values, items and the audit log are untouched. The API's
  response gains `retention_days`, `retention_max_days` and
  `retention_effective_days` (the request field is optional), and the host
  metric graph accepts the ranges `30d` and `90d` (about 168 points each).
- **3.57, client hosts:** nothing changes until a host is marked client: every
  existing and newly enrolled host is a server, and the evaluation, the alerts
  and the notifications of a server are unchanged. A host marked client (host
  form or `kind` in the API, which gains the field on hosts, the fleet overview
  and the host query) whose agent is offline (no contact for 90 s) is frozen:
  its states and alerts stay as they are, no unknown host alert opens, and an
  open one closes as `stale` only when the absence caused it (an unknown from
  while the client was on stays open). A client without an agent behaves like a server.
  After a reconnect a client is judged from the start of its current online
  period, and a client shutting down gets 90 s before an unknown host alert.
  A new organization setting `client_absent_days` (default 14, 0 off) opens a
  host alert with severity `warn`, "not seen since <time>", for a client that
  has been absent that long; the setting is under Settings → General →
  Monitoring and in the API. Limitation: an agent whose heartbeat and config
  interval are both about 90 s or longer reads offline between its contacts, so
  a client like that is frozen most of the time (backlog 3.58).
- **3.13e, rules for every check key:** every existing org, group and host
  rule, the seeded ones included, now judges all checks of its metric's plugin
  whatever their key, not only the check keyed with the plugin name. Checks
  under other keys (a copied disk check, a second http or nginx check) start
  to be judged and can raise alerts they never raised before. Item decisions
  (ignore, watch) now accept any well-formed check key, so such an item can be
  ignored per item. The API's rule objects gain `item_check_key` (item scope
  only; empty or absent means the plugin key); a rule update is a full replace,
  so a client that omits it resets the rule to the plugin key. Item decisions
  still name exactly one check key: an existing ignore on `disk` or `service`
  (for example `/snap/*` org-wide) does not cover the same item of a copy
  under another key such as `disk-data` or `service-2`, so those items alert
  until a decision for that key is added after upgrading (older releases
  refuse such keys, so it cannot be done beforehand). Consider a maintenance
  window around the upgrade if you run copied checks on many hosts.
- **3.11c, non-finite and out-of-range values:** the manager's ingest now
  counts an observation whose value is ±Inf, NaN or beyond ±1e100 as malformed
  (`rejected_malformed` in the stream's log line) and drops it alone; the rest
  of the batch is stored. A current agent never sends one: it reports such a
  value as the error "value is not a finite number" or "value is out of range"
  on the same item and metric. An older agent with a plugin bug that yields one
  loses that reading instead of storing it. A current value stored before the
  upgrade reads as that same error on the host page, and graphs and rule
  windows leave out every stored sample beyond ±1e100 (NaN and ±Infinity
  included), so one stored before the upgrade can no longer make an average
  overflow and fail a graph or postpone a host's rules. No schema change.
- **3.5, agent secrets:** the agent's heartbeat gains two fields
  (`secret_names`, `secrets_state`; additive, no proto break) with the names
  of the secrets in its optional local `agent.yaml` and the file's state,
  never a value. Migration 00086 adds the nullable columns `secret_names` and
  `secrets_state` to `host_agents` (catalog-only, no rewrite). An agent older
  than this change sends no state, so the manager stores nothing and the host
  page shows the secrets as unknown; the same holds for a registration that
  has not sent a heartbeat yet. Re-registering an agent resets its report to
  unknown until its next heartbeat. An older manager ignores the new fields.
  Nothing is seeded and no existing check uses a secret.
- **3.54, check plugin descriptors:** new route `GET /api/v1/check-plugins`
  (additive; a signed-in session only, no API token, no org in its path) serves
  every check plugin's descriptor: where it runs, its parameters with their
  constraints, the parameter that lists its items and its metrics with their
  kind. Check parameters are now refused by one validator built from those
  descriptors: the status codes are unchanged (400 for refused parameters), but
  some 400 texts are worded differently. For example, a `service` check without
  units now reads `service: units is required` and one with an empty list
  `service: units must have at least one entry` (one combined sentence before),
  and a synthetic check (`icmp`, `tcp`, `http`, `tls`, `dns`) with an unknown
  parameter now names that parameter even when a required one is missing too (it
  named the missing one before). An API client that matched on the exact text
  must follow; one that checked the status code is unaffected. **Mixed versions
  during a rolling upgrade:** a new SPA, served by an upgraded manager, whose
  request reaches an older manager behind the same load balancer gets 404 on
  `/check-plugins`. Until its request lands on an upgraded manager, the check
  dialog and the shared check form say the plugins failed to load ("Could not
  load the check plugins.") and offer no form, and the host page shows graph
  buttons on the error rows of discrete metrics (which have no graph) meanwhile.
  An old SPA on a new manager works fully: it never asks the new route.
- **3.12b, forgetting items that are gone:** new setting `PULSIX_ITEM_EXPIRY`
  (duration, at least `48h` — twice the longest check interval — `0` turns it
  off, default `168h`; a value below `48h` refuses startup): an item that
  delivered nothing for that long while its host kept reporting is forgotten, by
  an hourly round on one manager (the first one hour after the server starts).
  "Kept reporting" means another item of the same check reported, or the host's
  agent was seen, or — for a host without an agent — any check of it reported;
  a host whose agent is offline keeps its values even while checks from the
  server keep running against it. Migration `00085` widens the alert close
  reasons with `forgotten` (additive; a client that switches on the reason
  should accept it, and notifications treat it like a resolve). **The first
  round after the upgrade can forget many items at once** on a long-running
  installation, because items that went quiet long ago qualify immediately:
  their current values, rule states and state history go and their open alerts
  close as `forgotten`; graphs stay until retention. **That first round can
  therefore close many open alerts at once, and each one sends a close
  notification on every route that notifies on closes** — expect a burst of
  "closed" messages. Set `PULSIX_ITEM_EXPIRY=0` before upgrading to opt out (or
  to review the stale items first and forget them by hand). New endpoint
  `DELETE /api/v1/orgs/{org}/hosts/{id}/items` (additive).
- **3.50, agent token name hint:** the API now refuses an agent enrollment
  token whose `name_hint` is not a valid host name (lower-case letters, digits,
  dots and hyphens, not starting or ending with a dot or hyphen, at most 64
  characters) with HTTP 400; a hint over 64 characters is still answered 422 by
  the request schema's `maxLength`, as before. An API client that sent upper-case or free text
  must change what it sends; an empty hint is still fine. Tokens created
  earlier are not changed.
- **3.2, 3.3, 3.8, agents for Windows and macOS:** the manager serves a new
  `GET /install.ps1` and the agent token response gains `install_command_windows`
  (both additive; an older SPA ignores the field). The manager image grows by
  about 60 MB, the four agent binaries for darwin and windows (amd64, arm64) it
  now embeds. Linux agents need no update. The default agent name is now the
  hostname lower-cased, without a trailing `.local`, other characters replaced
  by `-`; it applies to new enrollments only, enrolled agents keep their name.
  The agent gains the flags `--env-file` and `--log-file` (additive; the
  Windows and macOS services use them, the systemd unit does not). The seeded
  `sshd` service check reads unknown on macOS and on Windows without OpenSSH
  server and raises no alert.
- **3.13, synthetic checks:** the `icmp`, `tcp`, `http`, `tls` and `dns`
  plugins are new and run on the manager, not on agents, so agents need no
  update and nothing is seeded into existing organizations. The manager
  always refuses its own loopback, link-local and cloud metadata addresses
  (the same list notification webhooks refuse, seen through
  `PULSIX_NOTIFY_NAT64_PREFIXES`; `PULSIX_NOTIFY_ALLOW_CIDRS` does not apply
  to probes), and the CIDRs of `PULSIX_PROBE_DENY_CIDRS` on top; the bundled compose file
  defaults that to the stack's own network (`PULSIX_NETWORK_SUBNET`), so a
  check cannot probe the database or the manager's neighbours. Setting it
  replaces that default, so include the subnet when you override it. ICMP uses
  unprivileged sockets: on Linux `net.ipv4.ping_group_range` must include the
  manager's group (Docker's default does), otherwise ICMP checks fail.
- **Interpreter discovery warning:** every run's `ansible.cfg` now sets
  `interpreter_python = auto_silent`. Ansible still discovers each host's
  Python as before, but no longer prints its "using the discovered Python
  interpreter" warning, which reached the worker as a bare line beside the
  run's events and was dropped. A host or playbook that sets
  `ansible_python_interpreter` is unaffected.
- **Webhook body limit:** a webhook body of exactly 1 MiB is still accepted and
  one byte more is refused with 413, unchanged; the dependency update that moved
  the limit's off-by-one is compensated in the hook endpoints.
- **3.7, network and uptime checks:** migration 00084 adds two seed checks to
  every existing organization at the organization-wide scope, `network` (every
  60 seconds: per interface receive and transmit bytes, errors and drops per
  second) and `uptime` (every 5 minutes: `uptime.seconds`); new organizations
  get them too. They show up as ordinary checks on every host. An organization
  that already has its own organization-wide check under the key `network` or
  `uptime` keeps it and gets no seed for that key. A group- or host-bound
  check an operator already has under `network` or `uptime` does not block the
  seed (00084 only skips organization-wide rows), and it keeps overriding the
  new organization-wide check on the hosts it covers, so those hosts run the
  operator's definition, not the seed. The same holds for a key an
  installation-wide shared check definition already holds, in every
  organization: before this release only `cpu`, `memory`, `disk` and `service`
  were reserved, so such a definition may exist. It keeps the key and stays
  editable under it, but it cannot be renamed onto a reserved key. If you later
  delete or rename it, the organizations that existed at the upgrade are left
  without that check (only organizations created afterwards get the seed); add
  the check by hand there. An agent older than this release does not know the
  plugins: it skips those two checks without a result, logs one warning per
  configuration push naming the keys, and does not report that configuration as
  converged, so the manager keeps pushing it on every heartbeat: about one
  warning every 30 seconds at the default heartbeat interval, until the agent
  is updated. Its other checks keep running.
  Update the agents to get the data. The migration is rolled back by deleting
  the seed rows under those two keys.
- **7.123, the agent port in the compose file:** the bundled
  `docker-compose.yml` now publishes the agent and worker endpoint on the host
  at port 8444 (`PULSIX_GRPC_HOST_PORT`); before, agents on other hosts could
  not connect until it was published by hand. A `docker-compose.override.yml`
  that already publishes `8444:8444` merges with the new entry and keeps
  working; the override entry can go. The port only accepts agents and workers the
  instance enrolled; open it in the firewall to where they run, and set
  `PULSIX_GRPC_PUBLIC_ADDR` if you publish it on another host port.
- **7.122, numbers in a worker's variable sources:** the manager now refuses a
  repository sync's variable sources holding a number of 1e21 or more or below
  1e-6 (zero aside), whatever the worker's version. A worker older than this
  release that reads a playbook holding such a number has its whole report
  ignored, with a "variable sources ignored" warning in the manager log, and
  the repository keeps its previous variables until the worker is upgraded and
  the repository has synced again. A stored repository playbook result longer
  than 16 MiB as the database renders it (only possible from such an older
  worker) is no longer decoded: that playbook reads as missing, with a warning
  in the manager log, until the next sync replaces it.
- **7.112, host evaluation over 30 seconds:** one host's rule evaluation (its
  context included) is now bounded at 30 seconds, where before only the 60
  second batch was. A host that needs longer, a very large one, fails every
  time: it is skipped for 60 seconds, the manager logs an error naming it, and
  its alerts are not evaluated until it fits. Hosts behind it in the batch, of
  every organization, are no longer starved by it. If the log shows it, look at
  that host's check and rule volume.
- **7.114, numbers in variable sources:** a number of 1e21 or more or below
  1e-6 (zero aside), integers of 22 digits or more included, is no longer a form
  value, because the database writes such a number out in full and the stored
  result would grow far past its cap. A marked variable holding one is not a
  form field and gets a warning (`marker_value_type`); a role spec default is
  dropped with `default_invalid`; in a choice or as the default of a text option
  the number stays the text as written. Editor playbooks are parsed on the
  manager, so new saves follow once it is upgraded; a row saved before the
  upgrade keeps its stored result, and is still read and transferred in full,
  until the playbook is saved again. Repository playbooks are parsed on the
  worker, so for them the rule takes effect only once the worker is upgraded
  and the repository has synced again; until then the manager stores whatever
  an older worker sends, unchecked (7.122).
- **7.114, rollback:** the stored result of an editor playbook is now a report
  envelope. There is no schema change, but a manager of an older version started
  on the same database without a restore decodes it without error and reads
  every saved editor playbook as having no variables (it does not parse the text
  instead). A downgrade is a restore (see "Downgrading"), which avoids this.
- **7.114, editor playbook variable results:** the parse result stored with
  an editor playbook is capped at 1 MiB (a text of 67 KB can parse to several
  MiB through aliases). A playbook saved with a larger result shows as
  incomplete with its fields left out, and the note says why. Rows saved
  before the upgrade are not rewritten: one longer than 2 MiB reads as
  incomplete and left out until the playbook is saved again; shorter ones read
  as before. `GET /playbooks` no longer carries the stored result, and the job
  template list reads each linked playbook's source one at a time.
- **7.111, check-result pacing:** the manager paces the check results of one
  agent across all its streams: 4096 observations per 10 seconds, with a burst
  of 8192. A legitimate agent never reaches it, and a replay after an outage
  is delayed, not dropped. An agent over its budget is slowed and the manager
  logs "agent check results throttled by the ingest budget" at most once a
  minute per stream; its host stays online. A single batch of more than 1000
  observations is refused and ends the stream, as before.
- **7.110, check label and error text bounds:** a check result whose label is
  longer than 512 bytes is refused with its whole batch (the stream ends with
  `InvalidArgument`), and an error text longer than 2048 bytes is cut on a
  character boundary and stored. Agents of this version cut error texts
  themselves, and the labels of every shipped agent are short fixed words, so
  an older agent is unaffected: its long error texts are cut by the manager,
  and only a label over 512 bytes, which no plugin produces, would be refused.
- **7.105, requirement sources:** a `requirements.yml` whose role `src`, role
  `name` used as the source, or collection `name` or `source` is a number
  (`1e3`, `0o17`) is refused as not a requirements file. Quote the value: a
  string still installs, and a local path is still checked against the clone.
- **7.104, run output redaction:** run output also masks a secret printed
  through `to_yaml` or `to_nice_yaml` (the single-quoted form, with every `'`
  doubled) and through `urlencode` (percent-encoded, both the path and the query
  form), each also in its JSON-escaped forms. More `[redacted]` can appear in
  the logs of new runs; a secret without those characters adds nothing.
- **7.103, credentials in repository URLs:** a repository URL, in an
  organization or a shared definition, is refused when it carries a password
  or, on https, any user (`https://user:token@host/x`, `https://token@host/x`);
  attach a git credential instead. A bare user on `ssh://` and scp-style URLs
  (`git@host:path`) stays. URLs stored earlier keep working and keep being
  sent to the workers in full, but the API and the SPA show them redacted
  (`https://***@host/x`; on ssh the password is dropped). Rotate any token that
  was embedded in a URL: every member of the organization, and of every
  organization a shared definition reaches, could read it. Saving the form
  with the redacted URL keeps the stored URL; a legacy ssh URL with a password
  therefore cannot be scrubbed by saving it: change the URL or rotate the
  password.
- **7.102, forgot-password without a second factor:** the forgot-password
  link is issued only to an account with a confirmed second factor (a TOTP
  secret or a passkey). For any other account, including one an administrator
  reset and one that never enrolled a factor, no link is sent and the answer
  is the same 202 as for an unknown address. An administrator resets such an
  account instead, with a reset link or a temporary password, which work as
  before.
- **7.101, changed repository URL:** a worker that already holds a clone now
  fetches from the URL of the repository's current definition and updates the
  clone's `origin` to it. Before, it kept fetching from the URL it had cloned
  from. After an upgrade, a repository whose URL was changed starts to follow
  the new one on its next sync, so check that its credential matches the new
  host.

- **7.96, roles inside collections:** the variable read now reads roles of
  collections, as a run finds them: a fully qualified name
  (`namespace.collection.role`), or a short name through a play's, block's or
  task's `collections:` list, is looked up in the collections beside the
  playbook and in the collections the commit's `requirements.yml` installed,
  before the role search. After the next repository sync, templates can gain
  fields from those roles; check the templates' variables pages. A qualified
  name whose collection exists but lacks the role is now the warning
  `role_not_found` instead of the hint `role_collection`, and so is a dotted
  name that is no valid collection reference (`my-ns.col.role`,
  `ns.class.role`), which Ansible looks up as a plain role. A short name found
  nowhere whose `collections:` list names a collection in neither place is the
  hint `role_collection` instead of `role_not_found`. A `collections:` value
  that is no name or list of names is the new warning `collections_invalid`.

- **7.95, repo cache sweep:** a worker now removes the clone,
  `<repo>.requirements/` and the legacy `<repo>.galaxy` of every repository its
  organization no longer has, and the directories of other organizations under
  `repo-cache/` (left from an earlier enrollment on the same data dir). Only
  directories the worker created go: a canonical uuid name (with one of those
  suffixes inside the organization's directory) that is a real directory, not
  a symlink. Each one is renamed to `.removing-*` before it is removed; a
  leftover of that name is removed by the next sweep. Without a valid answer
  from the manager nothing is removed. Expect a one-off drop in the data
  volume's usage after the first sweep, and a log line per removed directory.

- **7.91, role lookup in the variable read:** the lookup now follows Ansible's
  order for every role name: `<playbook dir>/roles/<name>`, then the installed
  roles, then `<playbook dir>/<name>`. A regular file at a candidate is a role
  without content. More roles can be found, and a role can also lose its
  fields: a regular file at an earlier candidate now shadows a role directory
  further down (it becomes a role without content), and an earlier candidate
  now wins (`./name` resolves to an installed role before the repository
  directory; a slash name prefers `<playbook dir>/roles/a/b` over
  `<playbook dir>/a/b`). So a template's variable sources can gain or lose
  fields after the next repository sync; check the templates' variables pages.
- **7.91, relative worker data dir:** the worker resolves a relative
  `PULSIX_WORKER_DATA_DIR` against its working directory at start. It logs the
  resolved path, and a resolved path with unsafe characters (anything but
  letters, digits and `. _ / + @ = -`) is refused at start.
- **7.98, credentials in logged URLs:** run logs, galaxy failure tails and
  git diagnostics now remove a URL's credentials even when they hold an
  unencoded `/`, `?` or `#`: everything from `://` to the last `@` before the
  next white space goes. A URL that carries an `@` in its path or query loses
  its host in those messages too (`https://registry/@scope/pkg` is shown as
  `https://[redacted]@scope/pkg`), and so does everything up to the last `@`
  in the same run of text without white space (compact JSON, a comma-joined
  list of URLs).
- **7.92, umask under the play uid:** plays and requirements installs under
  `PULSIX_WORKER_PLAY_UID` create files the worker's group can read even when
  the worker runs with umask 077: the setuid helper clears only the group
  read and execute bits of the worker's umask (077 becomes 027; 022, 002 and
  007 stay). Requirements installs that failed under umask 077 with
  `openat: permission denied` work after updating the worker image.
- **7.97, Mercurial requirements sources:** a role whose source starts with
  `hg+` or that sets `scm: hg`, and a collection named or sourced `hg+…`, is
  refused before galaxy runs ("Mercurial sources are not supported"). The
  worker image never had `hg`; a worker installed elsewhere now refuses them
  too.
- **7.97, cancelled during git:** a run or sync cancelled while the worker
  fetches the repository or reads its commit ends as cancelled instead of
  error.
- **7.94, repository subpaths:** a subpath is stored without leading,
  trailing or doubled slashes and without `.` segments, and the API answers
  with the stored form (`/ansible/` comes back as `ansible`). Repositories
  whose runs failed on a subpath starting with `/` run again after the
  upgrade; sync each one whose variable sources say "the repository subpath
  could not be opened" to read them. An installation's repository
  definition now refuses the subpaths an organization's repository always
  refused (`..`, characters other than letters, digits, `.`, `_`, `/` and
  `-`, more than 200 characters) with 400.
- **7.75, shared job template definitions:** a run receives only the values
  of fields the extra-vars schema declares. A value a shared definition held
  under a key its schema does not declare is no longer sent since 7.75, and
  saving such a definition is refused (422 at `body.extra_vars.<key>`). To
  fix one, declare the field in the definition's schema (or remove the
  value) and save it again; the save propagates to every projection.
- **7.90, worker data dir:** a worker refuses to start when
  `PULSIX_WORKER_DATA_DIR` contains anything but letters, digits and
  `. _ / + @ = -` (a space, `:`, `,`, a quote, `%`, `#`, `;`, a line break
  or a non-ASCII letter would split or end a setting in a run's
  `ansible.cfg`, SSH arguments or inventory wrapper); move the data dir
  before upgrading such a worker. Move it with its contents, or set
  `PULSIX_WORKER_IDENTITY_DIR` to the old `<data dir>/identity`: the
  enrolled certificate lives there by default, and a worker that does not
  find it needs a new enrollment token. The manager's embedded worker
  (`PULSIX_EMBEDDED_WORKER=1`) reads `PULSIX_WORKER_DATA_DIR` from the
  manager's environment: with a refused data dir it does not start, while the manager
  keeps running and logs only `embedded worker: configuration`; runs in the
  default zone it serves then stay queued unless another worker serves
  that zone.
- **7.90, variable editor:** a role a playbook in a subdirectory uses is no
  longer read from the repository subpath's root `roles/` — runs never found
  it there — and shows as not found after the next sync, as the run reports
  it; a role directory next to the playbook is now read, as the run finds it.
  The worker reads the variable sources, so this shows only after a sync
  run by an upgraded worker. For a template of such a playbook, the fields
  the role declared disappear from the source; since a role not found means
  the source was not read completely, no template marks them gone: automatic
  and manual templates alike keep them unchanged, and the template's sync
  review shows a `role_not_found` note and a `not_checked` warning naming
  the role. Stored values are never dropped. Fields of a role newly found
  next to the playbook come as additions: an automatic template takes them
  (those that need review wait for a person), a manual template shows them
  in its sync review. To keep a role found, move it to `<playbook
  dir>/roles/` or install it from `requirements.yml`.
- **7.88, requirements:** runs install the roles and collections of
  `requirements.yml`, `roles/requirements.yml` and
  `collections/requirements.yml` per commit, into `<repo>.requirements/` next
  to the clone, and a sync installs into the same cache. The first run (or
  sync) of each commit on a worker installs, which takes longer and needs
  outbound access from the worker to the galaxy source (galaxy.ansible.com or
  the git and http hosts the files name); later runs of that commit on that
  worker reuse the install. A repository's `ansible.cfg` no longer steers the
  install (private galaxy servers are not supported). A `requirements.yml`
  holding only comments no longer fails a sync. A requirements file is now
  refused, failing the sync and every run of that commit with the file and the
  reason, when it uses YAML anchors, aliases, merge keys or tags; holds more
  than one YAML document or duplicate keys; uses the `%YAML 1.2` directive or
  the `\/` escape (the parser accepts YAML 1.1 only); is not UTF-8 text or
  holds a NUL, a line break other than `\n` and `\r\n`, or an invisible
  character; is larger than 512 KiB; uses `include:`; names a role whose name
  is not a plain directory name (a local `src` such as `./r.tar.gz` is its
  own name, so such a role needs a `name:`); names a collection that is not
  `namespace.name` where galaxy uses the name as a path (`type: galaxy`, a
  `source:` beside it, or no type and a name with one dot); has a local
  source outside the repository (`../`, an absolute path, `.git`, a link
  leading out) or with a glob character (`*`, `?`, `[`); has a directory
  source holding a link that leads out of the repository, more than 50000
  files and directories, or nesting deeper than 64, or needs more than
  1000000 lookups to check; has a version, scm, name,
  type or source (or a comma part of a role string) starting with `-`; or
  names a git source whose `#subdirectory` leads out of its checkout. The
  worker refuses to take over an install, failing it by the path, when it
  holds a hard link, an absolute symlink or one leading out of the install, a
  FIFO, device or socket, more than 500000 files and directories, more than
  2 GiB of files, nesting deeper than 64, a chain of more than 40 symlinks,
  more names than it holds in memory (64 MiB), or a file that changed while it
  was copied. A repository subpath
  into git's own files (`.git`, or a link to it) now fails the sync as it
  fails the runs. With `PULSIX_WORKER_PLAY_UID` set, `ansible-galaxy` runs as
  the play uid and can write only into its temporary install directory: a role
  or collection that tries to write elsewhere, or whose files the worker
  cannot read, fails the install by name; the sync opens the clone read-only
  to the play group, as runs already do. Without a play uid the install is not
  isolated, and the worker log warns so for every `ansible-galaxy` command. A
  leftover `<repo>.galaxy` is removed by the first install of that repository;
  it stays for a repository that no longer has requirements and may be removed
  by hand.
- **7.88, failed requirements installs:** a failed install now fails every
  run of a repository with requirements files, not only its sync, with
  `Requirements: install requirements: <reason>` as the last line of its log
  (an install that failed leaves nothing behind, so the next run of that
  commit tries again and fails the same way); this includes a
  worker without network access to the galaxy source or the git hosts the
  files name (an air-gapped worker), and there is no setting to skip the
  install, so for offline workers vendor the roles and collections into the
  repository (local `src`/`source` paths, collections as `type: dir`).
- **7.88, galaxy's environment:** `ansible-galaxy` runs with a narrower
  environment than a play: git reads no system or global config
  (`/etc/gitconfig`, `~/.gitconfig`), so proxies, CA settings and `insteadOf`
  mirrors set there no longer apply and must come from environment variables;
  galaxy inherits only `PATH`, `LD_LIBRARY_PATH`, `PYTHONPATH`, `PYTHONHOME`,
  `LANG`, `LANGUAGE`, `LC_*`, `TZ`, `SSL_CERT_FILE`, `SSL_CERT_DIR`,
  `REQUESTS_CA_BUNDLE`, `CURL_CA_BUNDLE`, `GIT_SSL_CAINFO`, `GIT_SSL_CAPATH`
  and the proxy variables (`HTTP_PROXY`, `HTTPS_PROXY`, `NO_PROXY`,
  `ALL_PROXY`, each also in lower case) from the worker; git sources speak
  only https, http, ssh and git (no `file://`); and `PYTHONNOUSERSITE=1` is
  set, so an Ansible installed with `pip --user` is not found by galaxy.
  `ansible-galaxy` is the one next to `PULSIX_ANSIBLE_BIN` when that is a
  path, and the one on `PATH` otherwise.
- **7.88, play uid for requirements:** set `PULSIX_WORKER_PLAY_UID` on every
  worker that runs repositories with requirements files (the worker image
  leaves it unset; the bundled compose file sets 65533): without it every
  requirements install (the first run or sync of a commit on a worker) runs
  galaxy as the worker's own uid, not isolated, and the worker log warns so
  for every `ansible-galaxy` command.
- **3.12c, automatic item expiry pauses after an upgrade or an outage:** an
  agent counts as a host's sign of life for the automatic item expiry
  (`PULSIX_ITEM_EXPIRY`) only once it has been connected for `checks.MaxInterval`
  plus a 30-minute margin, about 24.5 h. So the automatic expiry of agent items
  pauses for about 24.5 h after the upgrade, after a re-enrollment that follows
  more than 24 h without contact, and after any reconnect that follows more than
  24 h of silence. A certificate renewal and shorter gaps (a nightly power-off, a
  manager restart, a flaky link) do not restart it. Only items whose whole check
  stopped reporting on a host with an agent are affected; items of hosts without
  an agent and items with a still-reporting sibling are forgotten as usual. Items
  that really vanished are forgotten that much later. A host that is silent for
  more than 24 h at least once every 24.5 h never lets its agent count, so such
  items are not forgotten automatically there (**Forget** still works).
- **3.55, items taken off a check's list leave the evaluation:** when a mount,
  interface, unit, database, sensor, device or array is removed from a check's
  item list, the evaluation no longer judges its last reported values, as for a
  removed or disabled check. It stops counting towards the host's state, and an
  alert open on it closes as `stale` (with the usual notifications) on the first
  evaluation after the upgrade. Its stored current values are kept until the item
  expiry forgets them. Checks without an item list and the check-level item
  (empty name) are judged as before. The effective rules view still lists rules
  for such items (backlog 3.56).
- **3.10c, new count metrics:** the `postgres` check reports
  `pg.databases_unreported` and the `docker` check `docker.containers_unreported`
  on every collected round, including failed connections (item with an empty name);
  a round whose secret cannot be resolved, or a plugin panic, reports only `pg.up`,
  and the count keeps its last value. They count the
  databases or containers the plugin left out; a rule can alert on them. The agent
  also logs a plugin's advisory error, once per change and again after an hour.
  Older agents do not report the metrics.
- **3.55, hardware metrics that vanish:** `temperature` and `smart` now report the
  error "<metric> is no longer reported" for a metric that was reported before and
  is gone, or changed type, while its item still reports other values (for example
  a temperature limit a sensor stops stating). It is reported for at most 24 h
  and is not remembered across an agent restart. `smart.devices` shows
  "no device with SMART data" only when at least one device answered without SMART
  data and none had data or is asleep; a round where every device failed shows the
  device count again.
- **3.13c, configuration badge:** the agent objects of the API gain
  `config_version`, `config_expected` and `config_checked_at` (omitted until the
  first comparison), and the Agents page and the host's agent card show whether
  the agent runs the current configuration. Re-enrollment and certificate renewal
  reset the check, so the badge is absent until the next comparison, normally
  within seconds.

## Roll order

1. **Manager** (with the database): back up, then start the new version. It
   migrates on start. Components one minor behind stay within the rule. A
   scheduled backup interrupted by the upgrade's own stop of the manager is
   not mailed as interrupted: a graceful stop records the attempt as
   abandoned, and migration 00078 marks existing abandoned scheduled attempts
   as notified.
2. **Workers**: they claim jobs from the manager, so they are updated after
   it. The bundled `worker` in the compose stack comes with `up -d`; a worker
   added by hand pins the image it was added with and is updated by pulling
   the new image.
3. **Agents**: last, host by host. There is no auto-update yet (backlog 3.18).
   Per host: download `$MANAGER/agents/bin/linux/<amd64|arm64>` and its
   `.sha256` from the manager, verify the checksum, `install -m 0755` the
   binary over `/usr/local/bin/pulsix-agent` and
   `systemctl restart pulsix-agent`; the exact commands are in
   `deploy/compose/README.md` ("Upgrading"). The identity in `/etc/pulsix`
   survives, so no token is needed. Do **not** re-run `install.sh` for an
   update: it prompts for an enrollment token (and fails when piped without
   a terminal), overwrites `/etc/pulsix/agent.env`, and ends with
   `systemctl enable --now`, which does not restart an active unit, so the
   old binary would keep running.

Because a component one minor behind is accepted, the manager can move to a
new minor first and the rest follow at the operator's pace. A component that
falls two minors behind (a release skipped) shows "Update required".

## PULSIX_PUBLIC_URL is validated at startup

A manager whose `PULSIX_PUBLIC_URL` carries user info (`user:pass@`), a query, a
fragment, an upper-case scheme (`HTTPS://`) or any other refused character now
refuses to start, with an error naming the variable and the character. The rule:
the value starts with lower-case `http://` or `https://`, has a host, and every
character is one of `A-Z a-z 0-9 . _ ~ : / % + = , -` or `[` `]`, the brackets only around an
IPv6 host such as `http://[2001:db8::1]:8080` (never in the path). Fix the value in `.env` before
upgrading.

## Agent install and uninstall scripts (no migration)

Backlog 3.49 and 3.51 change `install.sh` and `install.ps1` only; there is no
migration and no manager change. The scripts gain `--uninstall` / `-Uninstall` with
`--keep-data` / `-KeepData`, and the Windows upgrade waits for the old process.
**Linux hosts should run the install command once more** after the manager is
upgraded: the new unit adds `AF_NETLINK` to `RestrictAddressFamilies` (without it the
agent probably cannot read its network interfaces under systemd, so IP facts and the
network check fail), and the re-run now restarts the agent onto the new binary. An
agent that is not re-run keeps working as before.

## Downgrading

There is none in place. A newer schema is refused (above), and migrations do
not go down. To go back to an older release, restore a backup taken **before**
the upgrade with the older version. The database is in use, so the restore
needs `--replace` (it asks you to type `restore`; `--yes` in a script):

```bash
docker compose down
# in .env: set PULSIX_VERSION back to the old release
docker compose run --rm cli restore --replace <name>
docker compose up -d
```

Everything written after that backup is lost, as with any restore, including
workers and agents enrolled after it; see "Restoring" in
`deploy/compose/README.md` for what a restore brings back and what it does
not. That is why the upgrade steps start with a backup.

## Checklist for a change that touches the schema

- A new migration file with the next free number; no edit of an existing one.
- Nothing a shipped release reads is dropped or renamed in the same release;
  open the contract step in the backlog instead.
- A working `Down` for development databases.
- A user-visible change to the upgrade path is also documented in the
  end-user docs (`NoiXdev/noix-docs`, Pulsix section).
