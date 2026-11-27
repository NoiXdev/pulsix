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

The `docker` check's `resources` option (backlog 3.12a, off by default) needs an
agent at the version that ships it. An older agent ignores the option in a check the
manager pushes and keeps reporting the container states; the usage metrics simply never appear. An older
manager never sends the option, so a newer agent never reads usage for it. A
docker check in the agent's local `agent.yaml` that sets `resources` is refused by an
older agent (an unknown parameter to it), and an older manager drops such a local
check from a newer agent, reporting it as ignored by the server. There is no schema
or protocol change and no manager-side gating.

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

The database checks' TLS fields (backlog 3.10a) are agent-local: `mysql` and
`redis` secrets may carry `tls_ca`, `tls_server_name`, `tls_cert` and
`tls_key`, `postgres` secrets `sslcert` and `sslkey`, and `mysql` secrets a
Windows-only `pipe`. An older agent refuses a secret with one of them ("secret
field `tls_ca` is not known") and reports that on the check's up metric until
it is updated; there is no manager, protocol or schema change. One behaviour
change: `postgres`'s `sslrootcert` must now be an absolute path (or `system`);
a relative one, which resolved against the service's working directory, reads
"secret field `sslrootcert` is not an absolute path or system".

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

Since backlog 3.11a a `phpfpm` `urls` entry may also be the pool's own FastCGI
address with its status path (`fcgi://127.0.0.1:9000/status`,
`unix:/run/php/php8.3-fpm.sock:/status`), read with no web server in between.
No schema change and nothing seeded; existing entries behave as before. Version
skew: an older manager refuses such an entry when a check is saved. An older
agent hands it to its HTTP client, which refuses the scheme without connecting,
so that pool reads down ("connection failed", `phpfpm.pools_down` counts it)
until the agent is updated; it never reads anything else in its place. An older
agent also refuses a local `agent.yaml` `phpfpm` check with such an entry. Update
the agents before using the new entries. The agent needs access to the pool's
socket (usually membership of the pool's `listen.group`) or its TCP address
(`listen.allowed_clients`).

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

Assistant access (backlog 2.116) adds five migrations and no proto change.
Migration 00099 adds `api_tokens.kind` (`api` for every existing token) and
`api_tokens.areas`, with a CHECK that an assistant token has areas and an API token
has none; 00100 adds `org_assistant_settings` (no row means off); 00101 the nullable
`audit_events.actor_token_id`; 00102 `run_requests` (with row-level security);
00103 `run_requests.approved_fingerprint`, which the claim of an approved run checks.
Nothing is dropped or rewritten, and the switch is off for every organization. An
older manager that is still running while the new version migrates ignores `kind`
and would accept an assistant token at the REST API: **stop every manager instance
before the new version starts.** Workers and agents are not affected. A backup now
carries the switch and the run requests; after a restore, pending requests can be
approved again until they expire (the restore says so).

Web assertions and synthetic check rules (backlog 3.13b) add migration 00104: two
nullable columns (`alerts.detail`, `alert_events.detail`, both `text`) and seven
seeded organization rules (`Web assertion failed`, `Web assertion failed (critical)`,
`Ping check down`, `TCP check down`, `HTTP check down`, `TLS check down`,
`DNS check down`), backfilled for every existing organization that holds no rule of
that name. Nothing is dropped or rewritten. **A synthetic check that is failing at
upgrade time raises a critical alert on the first evaluation afterwards**, because
the new `<plugin>.up < 1` rules now exist; an organization that does not want them
deletes or disables the rule. An organization's own rule on `<plugin>.up` does not
suppress the new seeded critical rule unless it is at the same severity (`crit`), so
an own rule at `warn` leaves the seeded one alerting too; disable or delete the seeded
rule to opt out. A downgrade needs the usual restore of a backup from before the
upgrade (see Downgrading).

The same release changes two probe behaviours. An snmp check whose v3 credentials the
device refuses now reads `snmp.up = 0` with `snmp.failure = auth` (it was an error on
every reading, which read unknown); its `oids` items go stale instead of erroring, and
the seeded `SNMP device unreachable` rule (`snmp.up < 1`, crit, migration 00097) now
opens a critical alert for it with the reason `auth`. `http` checks read response
bodies as before: up to 1 MiB, kept only when something reads it. `http.content_bytes`
is the bytes read; past 1 MiB it is the response's declared Content-Length, or 1 MiB
when there is none, and never an error. Only a check with a `size` assertion reads on,
to 16 MiB, to count the body; past that its `size` assertion is judged on the declared
Content-Length, or is an error on that assertion alone without one. There is no new Go
dependency. Skew: an agent of 3.13a, which exists only in development, probes without
the new metrics (failure reason, breakdown, assertions); the manager accepts that.

Job chaining (backlog 2.121) adds migration 00105 and no proto change. Two new
tables, `job_template_follow_ups` (the follow-ups configured on a template) and
`run_follow_ups` (each run group's follow-ups and what became of them), both with
row-level security, and `jobs_trigger_check` widened by `chain`. Nothing is dropped
or rewritten, and no template has follow-ups until someone sets them, so runs behave
as before. Skew: an older manager still running during the rollout knows nothing of
follow-ups. It claims, runs and lists a `chain` run like any other (the trigger is a
plain string to it, and its SPA shows the raw `chain`), but its runs list refuses the
filter `trigger=chain` with 400. It plans no follow-ups for the runs it enqueues
(manual, schedule, webhook), so those runs start none. It has no follow-up starter and
leaves the rows to a new manager: the follow-ups of a run group that finishes on it
stay `pending` until a new manager's sweep resolves them (after about a minute). A
chained run of a scheduled chain that finishes on it sends no notification, and
deleting a template that another one names as a follow-up fails there with a 500 (the
foreign key refuses it, nothing is lost). **Set up follow-ups only once every manager
instance runs this version.** Workers and agents are not affected: a claim carries no
trigger, so an older worker runs a `chain` job like any other. A backup carries both
tables; after a restore, a follow-up that was due when the backup was taken is
started again by the starter.

Follow-ups of approved run requests (backlog 2.122) add migration 00106, one
boolean column `org_assistant_settings.approved_runs_start_follow_ups`, default
false. Nothing is dropped or rewritten. Behaviour change: an approved run request no
longer starts the template's follow-ups unless an admin turns the new setting on. A
request's fingerprint changes only when its picture has follow-ups: a run request
whose template names follow-ups, or a retry request whose original run had follow-ups
planned (whatever the template names now); every other fingerprint is unchanged. An
approved run with follow-ups in its picture that is still queued when the new version
starts fails its claim ("changed after it was approved") and must be requested again.
Skew: an older manager computes the old picture, so such a request approved on one
version and claimed on the other fails its claim, and an older manager approving
starts the follow-ups unseen, as before. **Approve run requests with follow-ups only
once every manager runs this version.** Workers and agents are not affected. A backup
carries the column with `org_assistant_settings`.

Session lock, expiry warning and version notice (backlog 3.93 to 3.95) add migration
00107, four columns: `users.lock_pin_hash` and `users.lock_pin_prompted_at` (both
NULL), `sessions.locked_at` (NULL) and `sessions.unlock_failures` (default 0). Nothing
is dropped or rewritten, no proto change, and workers and agents are not affected. A
backup carries the columns with `users` and `sessions`. The skew between managers and
the behaviour changes are under "Behaviour changes to check after an upgrade" (3.93 to
3.95).

List settings and appearance (backlog 3.97 and 3.98) add migrations 00108 and 00109,
both additive. 00108 creates `user_preferences` (one row per user: density, page size
and the per-list column document; no row means the defaults) and nothing else changes.
00109 adds the partial index `jobs_live_idx` on `jobs (org_id)` for the statuses
queued, claimed, running and cancelling, built with a plain `CREATE INDEX` like 00079:
it **blocks writes to `jobs` (new runs, status updates) while it builds**, and the
live rows are few, but the build still reads the whole table, so on a large run history
upgrade outside busy automation windows. An older manager ignores the table and the
index. The list endpoints gain `page`, `per_page`, `sort`, `order` and an answer
`total`; an older SPA keeps working against the new manager. Workers and agents are
not affected, and a backup carries `user_preferences`.

Personal tokens (backlog 3.100) add migration 00110, three new tables and nothing
changed in existing ones: `personal_tokens` (a user's own tokens, no row-level
security, like `sessions`), `personal_token_grants` (organization and role cap per
token, row-level security by organization; a foreign key to `org_memberships` deletes
a member's grants when the membership ends) and `org_token_settings` (the
"Allow personal tokens" switch, no row meaning on). Workers and agents are not
affected, and a backup carries the three tables. Skew: a personal token (`pxp_…`) is
unknown to an older manager, which answers 401 for it; organization tokens work on
both. Migration 00111 adds one partial index, `personal_token_grants_blocked_idx` on
`(org_id, user_id) WHERE blocked_at IS NOT NULL`, for the per-person block an
organization admin's revocation sets; nothing else changes. The assistant endpoint
gains `/api/mcp/o/{org}` for personal assistant tokens (an older manager answers 404
there; `/api/mcp` takes a personal token only while it holds exactly one live grant).
Organization assistant tokens keep `/api/mcp`; their tool list now leaves out the tools
the owner's current role there cannot use, and an owner who is no longer a member is
refused when connecting (403) rather than on every call.

Skew for this release (3.97 to 3.100), during a rolling upgrade. A new SPA reaching an
older manager: page-mode lists get cursor answers without `total`, so the pager hides
and only the first page is reachable; the preferences and personal token routes answer
404. API-visible changes of the new manager: a cursor-mode request with an invalid
`sort`, `order` or `per_page` now answers 422 instead of the value being ignored; the
bare `/api/mcp` counts grants in organizations whose personal token switch is off, so a
token with one live and one switched-off grant gets 400 "several organizations"; and a
user deactivated through an older manager keeps unrevoked personal tokens, which the
disabled check holds off and which work again if the user is re-enabled.

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

Playbook dry runs (backlog 3.126) add migrations 00115 and 00116 and additive proto fields.
00115 adds `jobs.check_mode boolean NOT NULL DEFAULT false` (a catalog change,
nothing rewritten; every existing run reads as an ordinary run), set when a dry
run is enqueued; a dry run's check, diff, tags and unsaved draft travel in the
run's sealed payload. The worker announces support on every claim
(`ClaimJobRequest.supports_check_mode`, alongside `cancel_stops_play`) and gets
`Job.check`, `Job.diff` and `Job.tags`, which it passes to ansible-playbook as
`--check`, `--diff` and `--tags` through ansible-runner's `--cmdline`. The claim
statement hands a `check_mode` job only to a claim that announced support.
**An older worker never gets a dry run**: it does not announce support, so a dry
run waits queued (the usual "no worker" and queued signals apply) while that
worker keeps claiming the ordinary jobs behind it; update the zone's workers to
run dry runs. **An older manager would hand a dry run to any worker, and the
worker would run the play for real**: an older manager still running while the
new version migrates does not know the column, claims a dry run like any run and
ships it without `check` (and, for a draft, ships the template's saved playbook,
not the draft). **Stop every manager instance before the new version starts**
(as for 00099 to 00103), and start no dry run until every manager is upgraded.
A run's stats gain `host_counts` (each host id's ok, changed, skipped, failed and
unreachable counts from the recap; the existing host lists are unchanged); it is
additive in `jobs.stats`, written by a new worker for every run, and an older
manager and an older SPA ignore it. A run reported by an older worker has no
`host_counts`. 00116 adds the nullable `jobs.dry_run jsonb` (a catalog change,
nothing rewritten), set with `check_mode` to a dry run's tags, diff and draft
flag (never the draft's text) for the run view, and NULL for every other job.
A downgrade needs the usual restore of a backup from before the
upgrade (see Downgrading).

Repository playbook texts and the playbook save conflict guard (backlog 3.127)
add migrations 00117 and 00118 and the additive proto field
`Job.want_playbook_texts`. Both migrations are additive catalog changes that
rewrite nothing: 00117 adds the nullable `repo_playbook_sources.content` and
`content_left_out` (NULL for every existing row), 00118 adds
`playbooks.revision bigint NOT NULL DEFAULT 1` (every existing playbook starts at
revision 1). An older release ignores the columns, since it lists the columns it
knows.
- **An older worker's syncs store no texts.** A new manager sets
  `want_playbook_texts` on every sync claim; an older worker ignores it and
  reports the variable sources without texts. The finish replaces the
  repository's rows as before, with `content` NULL, so a repository playbook's
  Editor tab says the last sync stored no text (`not_sent`: "the file may be
  gone from the repository, or the sync ran on an older worker") until a sync
  runs on an updated worker. Nothing else about the sync changes.
- **An older manager neither moves `playbooks.revision` nor checks `If-Match`.**
  A text saved through an older manager still running during the roll leaves the
  revision where it was, and that manager saves over whatever is stored. While
  one runs, a save over someone else's save is not detected: not through the
  older manager at all, and not through a new manager when the other save went
  through the older one. An older SPA (a tab opened before the upgrade) sends no
  `If-Match`, which a new manager takes as an unconditional save, as it does for
  API clients.
- **A sync claimed through a new manager and finished through an older one keeps
  its texts in the run.** The new manager asks for the texts on the claim; the
  older manager that receives the worker's report does not know the stats keys
  `playbook_texts` and `playbook_texts_left_out`, so it does not take them out:
  up to 4 MiB of playbook texts land in that sync's `jobs.stats` (returned with the
  job by the API to everyone who can read runs; the run view does not show them),
  and the repository's rows get no text. **Stop every older manager
  instance before the new version starts** (as the 00082 note recommends); the
  next sync through a new manager stores the texts as intended.

A downgrade needs the usual restore of a backup from before the upgrade (see
Downgrading).

The loose ends of dry runs and repository texts (backlog 3.129 and 3.130) add
no migration and no proto change; what they change at the API:
- **`If-Match: *` saves unconditionally on the routes where the header is
  optional** (templates, playbooks, inventory members, tags, SSH settings, …):
  RFC 9110's "any current revision", the same as no header. An older manager
  still running during a rolling upgrade parses `*` as a malformed ETag and
  answers 412, so a client that sends `*` may see a 412 until every manager is
  updated. The routes that require the header (inventory groups, schedules,
  alert routes, maintenance windows, …) still refuse `*` with 412, now with a
  message that says they need the concrete revision ETag.
- **The outline's OpenAPI component names changed** to `PlaybookOutlineDTO`,
  `PlaybookOutlinePlayDTO`, `PlaybookOutlineEntryDTO` and
  `PlaybookOutlineCountsDTO`. The JSON is unchanged; a client generated from the
  published schema must regenerate to pick up the new names. An outline entry
  without a usable name (an unnamed block, a keyword-only task, a role without
  a name, an `import_playbook` without a path) now has `name: ""`, which the SPA
  shows as a word for its kind; an unnamed block was `"block"` before. Once the
  tags it emits reach about 1 MiB, entries carry only their own tags and the
  outline says `truncated: true`.
- **`GET /api/v1/orgs/{org}/playbooks/{id}/content` gains `from_earlier_sync`**
  (additive): true when a limit left the playbook unread in the last sync and
  the text is from an earlier one. The SPA reads the flag, and an empty
  `commit` when an older manager sends no flag. `left_out: unreadable` now also
  covers a playbook a limit left unread with no earlier text to keep (it read
  `not_sent` before).

The loose ends of 3.131 add no migration and no proto change; what they change
at the API:
- **`GET /api/v1/orgs/{org}/hosts` gains a repeatable `id` filter** (additive):
  `?id=<uuid>&id=<uuid>` lists exactly those hosts of the organization, combined
  with the other filters. At most 500 ids (the dry run's host cap); more answer
  422. With ids given the answer is one page of at most 500 and `limit` is
  ignored in cursor mode. Unknown or foreign ids are simply absent.
- **Version skew:** an older manager ignores `id` and answers its first page of
  the whole list. The new SPA then shows names for those of the asked hosts that
  are on that page and ids for the rest; it never shows a wrong name. A template
  with a `limit` meanwhile drops the id stand-ins from the dry run's host choice
  until every manager is updated (the server still decides at run time).

The run request approval loose ends (2.120) add no migration, no table, no
backup change and no proto change; workers and agents are not affected. What they change:
- **Every run request fingerprint changes.** The picture gains the ids of the
  credentials, jump hosts and the repository's git credential, and the vars
  digest is now a keyed HMAC (key derived from the master key, bound to the
  request) over the real values the run uses, secrets included. So, once:
  - a request pending across the upgrade answers "changed since you looked at
    it"; the page shows the notice and *Review the new version*;
  - **every** run approved before the upgrade and not yet claimed fails its
    claim with "changed after it was approved; ask for the run again", not only
    runs with follow-ups.
- **Mixed manager versions: approve no run request until every manager runs
  this version.** A run approved on one version and claimed on the other fails
  its claim, and a detail and an approval served by different versions answer
  409 "changed". All managers already share the master key.
- **New route `GET /api/v1/orgs/{org}/run-requests/pending-count`** (`{count}`,
  the list's permission). A new SPA that reaches an older manager gets 400 there
  (rc.3 to rc.5 read `pending-count` as a request id) or 404 (earlier ones):
  the navigation badge, the Runs page and the run request list's pending count
  then count the pending list instead (backlog 2.126),
  which an older manager caps at 500 rows before filtering, so the number can
  run low until the request lands on an upgraded manager. An old SPA on a new
  manager works fully. `?status=pending` (and
  `expired`) are now filtered before the list's 500-row cap.
- **API and assistant clients see new answers.** Creating a request answers
  `422` when the template's hosts under its limit match nothing or the named host
  is outside them (accepted until approval before), and `429` when the token
  already has 20 pending, unexpired requests. Detail and approval answer `409`,
  not 500, when the targets' inventory cannot be rendered (a sealed value under
  a field that is no longer secret, a broken jump host chain); such a value
  anywhere in the organization's inventory blocks every request's detail until
  a person enters it again, as it already blocks every run.
- **New response fields** (additive): `requested_by_token_name` (always present,
  possibly empty) and `token_state` (optional: `active`, `revoked`, `expired`,
  `deleted`). Older clients ignore them.
- **Retry approvals carry an idempotency key** like run approvals. A retry
  request reverted to pending before the upgrade and approved after it reserves
  a fresh key.

The 2.120 review's deferred loose ends (2.126) ship in the same release and add
no migration, table, backup or proto change either:
- **The fingerprint changes once more for a build that ran 2.120 alone** (a
  local or pre-release build): the picture's targets are now the hosts of the
  zones the template's limit keeps (each such zone's full host list, which the
  claim renders; the limit then decides which of them the play runs on), and gathered host facts are left out of the vars
  digest, so a facts refresh between approval and claim no longer fails an
  approved run. From a release before 2.120 this is the same one-time change
  described above.
- **A job the approval check refuses at its claim** now fails with an error that
  starts with `refused at claim: `. When the reason is that the request is no
  longer approved (its first enqueue failed partway and put it back to
  pending), the job is left out of its run: approving the request again
  enqueues its zone again instead of replaying it (only when it was refused on
  its first claim; anything that may have run is never repeated), and the run's
  result, its notification and its follow-ups follow the jobs that do run. A run
  with only such jobs has no result yet; if its request is never approved
  again, its follow-ups are skipped as not started. Any other refusal (what the
  run uses changed after approval) still fails the run. A job refused before
  the upgrade carries the old text, so its zone is not refilled; ask for the run
  again.
- **The cap of 20 pending requests per token is exact** under concurrent
  creates (a transaction-scoped advisory lock per token). A request put back to
  pending after a failed enqueue is not counted against it again, so a token can
  briefly hold 21.
- **A run opens only the inventory secrets it carries.** A value that cannot be
  decrypted in a group or membership outside the run's hosts no longer fails
  the run, its detail or its approval; a sealed value under a field that is no
  longer secret still blocks everything, anywhere in the organization.

The approved-run follow-up loose ends (2.123) add no migration, table, backup or
proto change:
- **`PUT /api/v1/orgs/{org}/settings/assistant` takes either field on its own.**
  `assistant_enabled` and `approved_runs_start_follow_ups` are both optional; a
  field left out keeps its stored value, and a body with neither (`{}`) answers
  `422` (no body at all stays `400`).
  The update reads and writes both in one locked transaction, so two admins
  changing different settings at once both keep their change. An older SPA,
  which always sends `assistant_enabled`, keeps working; a new SPA on an older
  manager would be refused with `422` on the follow-ups switch alone (the field
  is required there) until the request reaches an upgraded manager.
- **A run request whose follow-up ids cannot be read fails its approval** with
  an internal error instead of silently starting fewer follow-ups. Ids are
  always written by the manager, so this guards against corruption only.

The token edit loose ends (2.125) add no migration, table, backup or proto
change. Two answers of the token edit change for API clients: an edit racing a
concurrent edit that moved the token's expiry into the past now answers `409`
like any expired token (it answered `404`), and a name of only spaces is refused with
`422` (it was stored). The SPA shows why an expired token's *Edit* is disabled
and describes each assistant area under its name.

Small loose ends (2.127 (2)(4), 2.129) add no migration, table, backup or proto
change. Job responses gain a read-only `withdrawn_at_claim` boolean (true for a
job refused at its first claim because its run request's approval was
withdrawn; such a job ran nothing, and the SPA shows it as "Not run — approval
withdrawn"); older clients ignore it. Creating an organization token with an
empty or blank name answers `422` (a blank name was stored before). The pending
badge on a new SPA treats an older manager's `400` or `404` on the count route
alike and remembers it for ten minutes instead of asking on every poll.

Backlog 3.117 and 3.118 add no migration, table, backup or proto change.
Forgetting an item or a metric now also removes its per-vantage probe rows, so
the vantage breakdown stops listing it. An agent that comes back after a gap and
replays its buffered results no longer has its metrics judged vanished while the
replay is in flight (for at most 15 minutes after it reconnects, ended by its
first live reading): alerts that used to close as `stale` and reopen with the
next replay chunk, with two extra notifications, now stay as they are.

Backlog 3.58, 3.67 and 3.71: migration 00119 adds the nullable column
`host_agents.heartbeat_interval_s` (additive; a backup carries it), and the
agent proto gains the additive field `Facts.heartbeat_interval_seconds`. An
agent now counts as online for three of its own heartbeat intervals after its
last contact, at least 90 s (`agents.OnlineWindow`); the agents list, the
online period start the replay hold reads, and the rule evaluation's presence
and unknown grace all use it; the online period restarts after a gap longer
than that window or the agent's 15 min disconnect buffer, whichever is shorter. Version skew: an older agent sends no interval,
so its window stays 90 s exactly as before; an older manager ignores the field
and keeps the fixed 90 s, so during a rolling upgrade it can still show a
rarely reporting agent offline between contacts. The first touch after an
agent restarts with a different interval still uses the stored one; the facts
that follow store the new one. The JSON stage of `http` checks now hands its
slots to organizations fairly (backlog 3.71), so one organization's slow JSON
targets no longer make another's checks report the manager too busy.

Backlog 4.39: migration 00120 adds the nullable column `alerts.continues_alert_id`
(NULL for every existing alert, a catalog change) with the composite foreign key
`(org_id, continues_alert_id)` to `alerts (org_id, id)`, `ON DELETE SET NULL
(continues_alert_id)`, and two partial indexes: `alerts_continues_idx` on
`(org_id, continues_alert_id) WHERE continues_alert_id IS NOT NULL` (empty at the
upgrade) and `alerts_host_closed_idx` on `(host_id, closed_at) WHERE closed_at IS NOT
NULL`, which the evaluation reads when a tuple alert opens. Both are built with a plain
`CREATE INDEX` like 00079 and read the whole alert history. Adding the column locks
`alerts` against **reads and writes** (as 00056 did), and the migration holds that lock
until it ends, through both index builds and the foreign key's validation scan: expect the manager's first start after the upgrade, which runs
the migration, to take correspondingly longer. A manager instance still running
alongside during the migration has its evaluation and its alert pages held up for that time, since alerts
open and close in the evaluation's transaction; on a large history, upgrade in a quiet
moment. The alert API gains `continues_alert_id` on an alert
and `continues_alert_id`, `ack_carried_over` and `snooze_carried_over` on its `opened`
timeline event (additive; an older SPA ignores them). A backup carries the column.
Skew: an older manager that keeps evaluating alongside opens a re-opened alert plain,
without the acknowledgement or snooze; stop every manager instance before the new
version starts if that matters. Workers and agents are not affected.

Backlog 4.42: migration 00121 adds the nullable column `check_states.unmeasured_at`
(NULL for every existing row, a catalog change; a backup carries it). The first
evaluation that finds a tuple unmeasured stamps it with its own moment (its check
disabled or taken off the host, the host no longer monitored, a selector edited so the
host drops out, a metric no longer asked for, or a metric the agent stopped reporting
while its item still reports, backlog 3.88; a metric held on its item's state, backlog
3.9b, is never stamped); an evaluation whose moment is not later
than the row's `evaluated_at` stamps nothing. While the mark is later than the row's
`evaluated_at`, the tuple starts fresh when it is measured again; the flapping count
leaves out the tuple's transitions at or before the mark, also after the tuple has been
judged again. Existing rows carry no mark, but a tuple still unmeasured at the upgrade
is marked on the new manager's first evaluation of its host (within seconds); only a gap that ends before that evaluation, or one that only an older
manager evaluated, resumes its old state once, as before. A metric held on its item's
state is never marked, so one whose alert a build without the mark already closed
stale — a sleeping disk's read as vanished by an older manager evaluating alongside, or
an exported pool's by a build from the main branch between backlog 3.74 and this
release — keeps its old severity in the roll-up while the item stays in its state, and
when the item leaves it opens a new alert dated at the old, pre-sleep since; this
affects only installations that ran such a build (no tagged release does). Skew: an older manager
evaluating alongside neither stamps nor reads the column (its gaps resume as before,
and it counts pre-gap transitions towards flapping); its state writes leave the column
alone and supersede a mark through `evaluated_at`, as the new manager's own do. Workers
and agents are not affected.

Run request overrides (spec 2026-11-25 assistant API gaps, A) add migration 00122:
the nullable `run_requests.overrides` (jsonb, NULL for every existing row, a catalog
change; a backup carries it). A run request (REST
`POST /job-templates/{id}/run-requests` and the assistant's `request_run`) now takes
`check_mode` (a dry run) with `diff`, `extra_vars`, `host_ids`, `limit` and
`expires_in` (minutes, 5 to 1440); the overrides are stored on the request, shown in
its picture (`check_mode`, `diff`, `extra_vars_override`, `limit_override`,
`requested_host_ids`, all left out when not set, so a picture without overrides keeps
its fingerprint) and enqueued with the approval. The vars travel as untrusted values
(every text in them reaches Ansible marked unsafe, as a webhook's do); the limit is
resolved into the hosts the run keeps to and never reaches Ansible. A retry of a run
an approved request started (a retry request or the retry route) repeats the
request's extra vars. **An older manager ignores the overrides**: a dry run it
approves runs for real, on every target and without the vars. Stop every manager
instance before the new version starts, as 00123 requires anyway, and upgrade every
manager before assistants use the new parameters. Workers and agents are not
affected.

Pinned commits of approved run requests (spec 2026-11-25 assistant API gaps, A)
add migration 00123 and the additive proto field
`ClaimJobRequest.supports_pinned_commit`. 00123 adds the nullable
`run_requests.pinned_commit` and `jobs.pinned_commit` (NULL for every existing row:
no existing run is pinned) and `repo_var_sources.synced_ref` (NULL for every existing
record), all catalog changes that rewrite nothing; a backup carries them. A run
request for a repository playbook shows the commit of the repository's last
successful sync and is approvable only while that commit stands for the repository
as it is configured now; the approval pins its jobs (and their retries) to it, and
the worker checks that commit out instead of the branch tip. `synced_ref` is the
digest of the URL, branch and subpath a sync's claim sent the worker; it is
written by a sync claimed by the new manager. **Every repository's existing record
has none, so every pending and new run request of a repository playbook waits for
one sync of its repository after the upgrade** (its picture says "sync it, then
approve"); a sync started by hand, a webhook or a schedule clears it. Requests
approved before the upgrade are not pinned and keep running the branch tip; their
approval stands (their picture has no commit, so its fingerprint is unchanged).
Requests still pending show a commit now, which changes their picture: an approver
with the page open is asked to review it again. **An older worker never gets a
pinned run**: it does not announce support, so a pinned job waits queued while that
worker keeps claiming the jobs behind it; update the zone's workers to run approved
requests of repository playbooks. A worker that cannot find the commit (it was
removed from the repository by a force-push and the server no longer hands it out)
fails the run with "commit <sha> is no longer in the repository". **An older
manager would hand a pinned job to any worker, which would run the branch tip**:
stop every manager instance before the new version starts, as for 00115. A
follow-up started by a pinned run runs the same commit when its template's playbook
comes from the same repository (and so also needs an updated worker); a follow-up of
another repository or an editor playbook runs as before, and the request page says
which. Manual, schedule and webhook runs are not pinned and are not affected. Agents
are not affected.

Run requests that sync first (spec 2026-11-25 assistant API gaps, A and G) add
migration 00124. It widens the `run_requests.status` CHECK by `waiting_for_sync` and
`failed` (dropped and added again with the wider set: every existing row passes),
makes `run_requests.expires_at` nullable with a CHECK that only a waiting request
lacks one (every existing row has one), and adds the nullable `sync_job_id`,
`expires_in_minutes` and `sync_error` columns, a CHECK that a waiting request names
its sync, and a partial index on the waiting requests; nothing is rewritten and a
backup carries the columns. A request made with `sync_first` is stored at once as
`waiting_for_sync` (no expiry, counted against its token's pending cap, not in the
approvers' badge), queues a sync of its template's repository (or attaches to the
one queued and not yet taken by a worker; one already running may have fetched
before the change the caller wants, so a new one is queued behind it) and becomes
`pending`, its expiry starting then, when that
sync succeeds; it becomes `failed` with the reason when the sync fails, is
cancelled, is gone or has not finished within an hour. The sync's finish moves its
requests in the same transaction; each manager also sweeps the waiting requests
every 10 seconds, which catches what the finish does not see (a queued sync
cancelled outright, a sync that ended before its request was stored, a manager
stopped in between); a request moves once however many managers sweep. A sync that
`sync_first` queues is recorded in the activity log as a repository sync by the
requester. `POST /repos/{id}/sync` with a sync of the repository queued (not yet
taken by a worker) now answers 200 with that job and `already_running: true`
instead of queuing another (202 and `already_running: false` for a new sync, as
before, also while a sync runs); a repository has at most one queued sync, and a
repository webhook keeps its own `on_active` rule. **An older manager cannot read a waiting request** (its
`expires_at` is NULL), so its run request list and detail fail while one exists:
stop every manager instance before the new version starts, as 00123 already
requires. Workers and agents are not affected.

The final review of part A adds migrations 00125 and 00126, both nullable columns
(catalog changes; a backup carries them). 00125 adds `repo_var_sources.sync_claimed_at`:
when the sync behind a repository's recorded read was last claimed by a worker (when
it was queued, for one never claimed), which approximates when it fetched; a sync the
reaper requeued fetches again at its new claim. Two syncs of one repository can finish
in either order; the read of the one that fetched earlier, finishing last, no longer
overwrites the newer one's commit (a record from before the column has none and is
replaced by any sync). 00126 adds `jobs.pinned_repo_id`: the repository a
pinned job's commit belongs to. A retry of a pinned run whose template has since moved
to another repository or to an editor playbook is refused (the retry route answers
422; a retry request's picture says so and its approval is refused) instead of failing
on the worker; jobs pinned before 00126 have none and are not checked that way. A
failure telling the waiting `sync_first` requests about a finished sync no longer
rolls the sync's finish back: it is logged and the sweep moves them. An older manager
alongside writes neither column, which only leaves the older behaviour in place.

Agent enrollment inside an approved run (spec 2026-11-25 assistant API gaps, B) adds
migration 00127: `job_templates.agent_enrollment` (boolean, NOT NULL DEFAULT false,
so every existing template has it off) and the nullable `run_requests.enroll_host_ids`
and `jobs.enroll_host_ids` (uuid arrays, NULL for every existing row); all catalog
changes that rewrite nothing, and a backup carries them. A template's **Enrol agents**
switch (`agent_enrollment` in the template API and `create_job_template` /
`update_job_template`; an update that leaves it out keeps it) is part of its revision.
Only a person signed in (no API or assistant token) with `agents.write` may switch it,
approve a run request of such a template, or start or retry one by hand. A run or
retry of such a template sends `enroll_host_ids`, the hosts its person confirmed
(`GET …/job-templates/{id}/enrollment-preview` and `GET …/jobs/{id}/enrollment-preview`
list the targets without an agent); without it, or with another list, the start
answers 409 with the current hosts in `enroll_hosts`. **An API client can no longer
start or retry such a template; a person confirms its hosts in the GUI.** Schedules,
chained follow-ups, webhook runs and dry runs started from the playbook page carry
no list, enrol nothing and need nothing more. An assistant's dry-run request of such a
template still needs an approver with `agents.write` (like any request of it); its
approval stores an empty list, so it enrols nothing either. A run request picture of such a template carries
`agent_enrollment`: the targets without an agent (`hosts`, `host_ids`;
`none_reason: dry_run` for a dry run, `approved_before_enrollment` for an approval
that stored no list), live while pending; the approval stores the list on the
request and its jobs, the approved picture shows the stored list, and the claim
refuses a job whose list differs from the approval's. A picture of any other
template is unchanged, so its fingerprint is. **An older manager ignores the flag**:
it computes a picture without the enrollment, which a newer manager's claim refuses
as changed, and it asks neither for `agents.write` nor for the confirmed list. Stop
every manager instance before the new version starts, as 00123 requires. Workers and
agents are not affected.

Migration 00128 hands those runs their tokens: `agent_enrollments.job_id` (nullable,
composite foreign key to `jobs (org_id, id)` with `ON DELETE SET NULL (job_id)`, and the
partial index `agent_enrollments_job_idx`, empty at the upgrade) and
`jobs.enroll_tokens_minted` (integer, NOT NULL DEFAULT 0); catalog changes plus a
validation scan of the token table, and a backup carries both. The claim of a job with
a confirmed list mints, for each listed host of the job that still has no agent, a
host-bound single-use enrollment token in the job's zone, valid for 24 hours — only
for hosts the run's limit keeps (the template's limit or the run's own; a limit Pulsix
cannot decide, or one naming a group, keeps every listed host) — and
hands it over only as that host's inventory variables `pulsix_agent_enroll_token`
(the token) and `pulsix_agent_enroll_tokens` (`{}`), which win over any group or
membership value of those names; the token is in the claim's redact list, so the
worker removes it from the run's events. A requeue (reaper or a revoked worker)
revokes the claim's unused tokens in its own transaction, a re-claim mints fresh ones,
and every end of the job (succeeded, failed, cancelled, error) revokes the unused ones
in the transaction that ends it; a used one stays. A minting failure fails the job
with "mint enrollment tokens: …" and hands nothing out. The activity log records
"enrollment tokens minted for N hosts by job …" (with the host names) and "revoked N
unused enrollment tokens of job …" as system entries. The job detail gains
`enroll_tokens: {minted, used}` (counts only) when the run minted at least one token,
shown on the run page; the pending enrollment token list gains `job_id` for tokens a
run minted, and the Agents page links them to their run. **An older manager alongside mints nothing** for an enrolling run it claims (the
role then stops the play for those hosts) and revokes nothing when its jobs end, so
tokens a newer manager minted for a job an older one ends live out their 24 hours.
Stop every manager instance before the new version starts. Workers and agents are not
affected: the token reaches the worker as an ordinary inventory variable.

Schedule requests (spec 2026-11-25 assistant API gaps, C) add migration 00129. It
widens the `run_requests.kind` CHECK by `schedule`, `trigger`, `schedule_review` and
`trigger_review` (`schedule` and `trigger` are used by this version) and adds the nullable
`run_requests.payload` (jsonb: what a request of another kind than `run` and
`retry_failed` creates or changes, never a secret) and `run_requests.target_id` (uuid,
no foreign key), with a CHECK that a run or retry has no payload and every other kind
has one; every existing row passes. It also adds `schedules.untrusted_limit` and
`schedules.untrusted_extra_vars` (booleans, NOT NULL DEFAULT false). All catalog changes that rewrite nothing, and a
backup carries them. The new token area `automation.request` lets an assistant ask for
a schedule to be created, changed or resumed (`create_schedule`, `update_schedule`,
`resume_schedule`; REST `POST …/schedule-requests`, `POST …/schedules/{id}/update-requests`,
`POST …/schedules/{id}/resume-requests`) and pause or delete one directly
(`pause_schedule`, `delete_schedule`; the existing routes accept `automation.request`
beside `templates.write`). A person approves such a request on the run request routes
and page, which needs `templates.write` beside `jobs.run`; the approval stores the
schedule exactly as pictured, created by the approver, in the transaction that
records the approval. The limit and the extra vars an assistant set each carry their
own mark (`untrusted_limit`, `untrusted_extra_vars`): the schedule's runs carry a
marked part as untrusted values, re-checked at the claim (and the extra vars marked
unsafe for Ansible), while a person's part stays trusted; a request that copies a
marked part keeps its mark, and a person's later edit keeps a mark while its part is
set. **An older manager alongside ignores the marks** and runs such a schedule's values as trusted, and it
shows a schedule request as a run of its template, whose approval there would start
that run instead of saving the schedule. Stop every manager instance before the new
version starts, as 00123 requires. Workers and agents are not affected.

Webhook trigger requests (same spec, C) extend 00129: `run_requests.template_id`
drops NOT NULL, and a new CHECK `run_requests_template_check` keeps it required for
every kind but `trigger` and `trigger_review` (a repository sync trigger runs no
template). Dropping NOT NULL and adding a CHECK every existing row passes rewrite
nothing. `automation.request` also asks for a webhook trigger to be created, changed or
given a new secret (`create_trigger`, `update_trigger`, `rotate_trigger_secret`; REST
`POST …/trigger-requests`, `POST …/triggers/{id}/update-requests`,
`POST …/triggers/{id}/rotate-requests`) and disables or deletes one directly
(`update_trigger` with only `enabled: false` → the new `POST …/triggers/{id}/disable`,
`delete_trigger`; both routes accept `automation.request` beside `templates.write` /
`repos.write`). The trigger routes now check a trigger's kind permission against an
assistant token's areas as well as its role. `list_triggers` and `get_trigger` read
with `templates.read` (`repos.read` for a repository sync trigger); over MCP both tools
are listed only for `templates.read`, so a token with `repos.read` alone reads no
trigger there. `automation.request` alone shows a trigger's or a schedule's current
settings (never a secret) in the picture of a request to change it, and a repository's
URL and branch in a repository sync trigger request's picture, without the read area
of either. An update request that changes nothing (no field, or every field as it is,
with the trust of a schedule's limit and extra vars unchanged) is refused with 422
"nothing to change". Approving a trigger
request needs `jobs.run` and `templates.write` (`repos.write` for a repository sync
trigger); the approval creates, changes or rotates the trigger in the transaction that
records the approval and returns a new secret once, in the approval's response only
(`trigger_secret`, `Cache-Control: no-store`): the request, the activity log, the audit
trail and every MCP answer never hold it. **An older manager alongside** cannot show or
approve a trigger request (it refuses the unknown kind), and its request list fails
while a repository sync trigger request exists (it cannot read the NULL template); stop
every manager instance first, as above.

Pausing after an assistant's change (same spec, C2, decision D5) adds migration 00130:
the nullable `schedules.paused_reason` and `job_triggers.disabled_reason` (text, only
`assistant_change`; a CHECK each allows a reason only on a paused schedule or a
disabled trigger, which every existing row passes with NULL) and a partial index
`run_requests_review_target_idx` on `(org_id, target_id)` for the two review kinds 00129
already admits. Catalog changes plus a small index; a backup carries both columns. When
a change made with an assistant token moves a run-relevant field of a job template
(playbook, scope, limit, extra vars values or schema, variables mode, credential, vault
identities, agent enrollment, follow-ups; through the template save, variables save,
variables apply or variables mode routes), of a repository (URL, branch, subpath,
credential, vault identities) or the text of an editor playbook (`PUT …/playbooks/{id}`),
the same transaction pauses every schedule of the affected templates (a repository's:
the templates whose playbook comes from it; an editor playbook's: the templates that
run it; in every case also each template that starts one of them as a follow-up, along
any chain) and disables every trigger that runs one of them or syncs the repository, each
with the reason
`assistant_change`, one activity entry (`pauseScheduleForReview`,
`disableTriggerForReview`, attributed to the token) and one review request
(`schedule_review`, `trigger_review`) whose picture shows the item and the changes, old
and new, secrets masked. Another change of an item already paused for review is added
to its open review instead. A schedule a person had paused, or a trigger a person had
disabled, gets the reason and a review as well, so the notice shows before anyone
resumes it; its picture names who had stopped it (the last `POST pauseSchedule` or
`POST pauseAllSchedules` after its last `POST resumeSchedule`, or the last
`POST disableTrigger` / `PUT updateTrigger` / `POST createTrigger` after its last
`POST enableTrigger`, in the activity log; a stop made with an assistant token names the
token), and approving it accepts the changes and leaves it stopped without the reason
(logged as `POST acceptScheduleReview` / `POST acceptTriggerReview`, which do not count as a
resume or enable for that lookup).
That fact carries over to a later review of the item after one was rejected or expired.
The reviewer locks each item's row before deciding, so a person's concurrent resume is
either seen or waits. Deleting a schedule or trigger rejects its open review. A schedule
or trigger request's picture lists the open change reviews of what it would run
(`open_change_reviews`, each with its item's kind and name): those of the template's
items and those whose changes name the template, a template it starts through
follow-ups, the playbook of one of them or that playbook's repository (for a
repository sync trigger: the repository). The new row locks are `FOR NO KEY UPDATE`. A person's change pauses nothing, inventory maintenance included; an assistant's inventory change does (decisions D6 and D7, see below). A review
does not count against its token's pending cap and expires after 30 days (the item
stays paused). Approving needs `jobs.run` and `templates.write` (`repos.write` for a
repository sync trigger) and resumes (enables) the item at the revision shown (one a
person had stopped stays stopped);
rejecting leaves it paused. A person may resume or enable such an item directly, which
clears the reason and closes the open review as approved by them; an assistant's
`resume_schedule`, or an `update_trigger` that enables it, is refused with 422. The
schedule and trigger reads carry `paused_reason` / `disabled_reason` and
`review_request_id`. **An older manager alongside** neither pauses anything after an
assistant's change, nor shows or approves a review (unknown kind), and its resume of a
schedule (enabling of a trigger) paused for review fails on the new CHECK, since it
does not clear the reason; stop every manager instance first, as above.

Inventory values an assistant writes (same spec, decision D6, backlog 2.136) add
migration 00131: `inventory_groups.assistant_keys`, `inventory_groups.assistant_default_keys`
and `inventory_memberships.assistant_keys` (text[], NOT NULL DEFAULT '{}'), catalog
changes with a constant default that rewrite nothing; a backup carries them. A write
made with an assistant token marks the top-level keys of group values, schema defaults
and membership values it sets or changes, and the run's inventory carries a marked value
(a marked default wherever it fills in) as plain text (`{"__ansible_unsafe": …}`), so
Ansible never templates, looks up or decrypts it; the worker is unchanged. A person's
save makes live only the values it changes: a value resent unchanged, by the browser or
by a script with an API token, keeps its mark. To make an assistant's value live as it
stands, a person accepts it (`POST …/inventory/groups/{id}/vars/accept`,
`…/schema/accept`, `…/members/{host}/accept`, body `{"keys": […]}`, If-Match; an
interactive session only, no API or assistant token; logged as
`POST acceptInventoryGroupVars`, `acceptInventoryGroupDefaults`, `acceptInventoryMemberVars`).
An assistant's inventory write is refused (422) for a key, a default or a new field
(sub-fields included) under a name Ansible or Pulsix act on (`ansible_…`, `pulsix_…`,
`__ansible…`, magic variables such as `hostvars`, `groups`, `omit`, `vars`) and for a
value holding an `__ansible…` object key; a person may still use such names. The group,
member and preview reads carry `assistant_keys`, `assistant_default_keys` and
`assistant_written`. **Values assistants wrote before this release stay unmarked and
live**: nothing marks them after the fact (the activity log names earlier assistant
inventory writes — `setInventoryGroupVars`, `setInventoryGroupSchema`,
`assignInventoryHost` by an assistant token — for an owner who wants to check them).
**An older manager alongside** ships claims without the marks (an assistant's values
run live) and records an assistant's inventory writes unmarked; stop every manager
instance first, as above. Workers and agents are not affected.

The names of a repository's vault-encrypted variables (backlog 2.134) add migration
00132: `repo_var_sources.vault_names` (jsonb, nullable, no default), a catalog change;
a backup carries it. A repository sync by an updated worker reports, beside the
variable sources, the names (never the values) of the variables the repository sets to
a value encrypted with Ansible Vault (a scalar tagged `!vault` or `!vault-encrypted`, or
any scalar whose text starts with `$ANSIBLE_VAULT;` whatever its tag, anywhere inside the
value) with the file that sets each: the top-level keys of the
files under `group_vars` and `host_vars`, of a role's `defaults` and `vars` (in the
repository, the installed roles and the installed collections' roles), of any file
under a directory named `vars` and of a play's static `vars_files`, and the keys of a
play's `vars`, a role entry's `vars` and parameters and an import's `vars`. Linked
files and directories are followed as Ansible follows them (a link inside `group_vars`,
a `group_vars` that is itself a link), read under the linking path, with loops ended;
a link that leaves the clone or leads nowhere is listed as unread. A file encrypted as
a whole (`ansible-vault encrypt`) hides its names: it is listed as such, as is a
variable file that could not be read. The report travels inside the sync
result's `var_sources` stats as the additive key `vault`; the protocol (`worker.proto`)
is unchanged. The manager stores it with the repository's variable sources, shows it
under `sources.vault` of `GET …/repos/{id}/playbooks` (and so the assistant's
`list_repo_files`), and refuses a run request's extra var named like one (422, naming
the file, never the value). **Version skew:** an older worker's sync report has no
`vault`, so the record's `vault_names` stays NULL and the names are unknown, never
"none" (the API then leaves `sources.vault` out); a record from before the upgrade is
the same until its repository is synced again. While the names are unknown, or the
last read was not complete (a file encrypted as a whole or unreadable, a link it could
not follow, a `vars_files` entry that is templated, a first-found list, absolute or
outside the subpath, a value nested past 64 levels or merging past 64 sources, a limit
or the time limit hit), a run request's extra var is refused only when the playbook declares
a variable of that name whose value could not be read; any other name passes, as
before the upgrade. Refusing every extra var on such repositories would stop
assistants' run requests on every repository until each zone's workers are updated;
the check cannot be stricter without breaking them, and person-started runs are
unaffected either way. **Full protection needs an updated worker and a fresh sync**:
after the upgrade, update every zone's workers and sync each repository that
assistants request runs for; until then, and for names in files encrypted as a whole,
declare such names secret in the template's extra vars schema, which refuses them
regardless. An older manager alongside ignores the key; a sync it records leaves
`vault_names` as an earlier read left it, so stop every manager instance first, as
above. Editor playbooks are not covered (they have no
repository variables; a `!vault` value in an editor playbook's play vars is not named).

An assistant's inventory change also pauses what runs unattended (decisions D6 and D7, no
migration). Writes made with an assistant token that pause:
- a group's values (`PUT …/inventory/groups/{id}/vars`: a key set, changed or removed);
- a group's schema (`PUT …/schema`): a default set, changed or removed, and any edit that
  changes a value as runs receive it (a field made inactive, retyped, removed, moved between
  group and host scope, made overridable or not), for the group and each member;
- a membership (`PUT …/members/{host}`): a host joining the group, with or without values,
  or a change of an existing membership's values; `DELETE …/members/{host}`: a host leaving;
- a group renamed (the `group_names` and `groups[…]` a run sees), moved to another parent or
  given another jump host (`PATCH …/inventory/groups/{id}`), and a group deleted with its
  members (`DELETE …/inventory/groups/{id}`);
- a managed host created (`POST …/hosts`), made managed (`PATCH …/hosts/{id}`,
  `PATCH …/hosts/{id}/capabilities`), moved to another zone or given another jump host
  (`PATCH …/hosts/{id}`);
- how a run reaches a host: its SSH user, port, credential or jump host
  (`PUT …/hosts/{id}/ssh`), and a zone's default jump host (`PUT …/zones/{id}/bastion`, which
  reaches every managed host of the zone).

Deleting a host pauses nothing (runs reach fewer hosts), nor does editing a jump host itself
(`PUT …/bastions/{id}`). A managed host renamed by an assistant pauses too (when it has an
address; see below), and a host an assistant creates managed or makes managed shows its address,
zone, SSH user, port, credential and jump host in the picture.

**Assistant limits on managed hosts' connection** (controller's ruling N1): an assistant token
may not change where a managed host is reached — its address, or its name while it has no
address (`PATCH …/hosts/{id}` answers 403 "a person has to change where a managed host is
reached"); an unmanaged host stays editable as before. An assistant's connection test
(`POST …/hosts/{id}/connection-test`) is refused (403 "the host key must be pinned before an
assistant tests with a password") while the host logs in with a password credential and its
host key is not pinned. People are not affected. The pause on an assistant's host rename hands
the change to the reviewer before the rename locks the host row, so a schedule tick enqueueing
for that host never waits on it.

Each pauses every active schedule and disables every enabled webhook trigger whose run then
receives other values or reaches other hosts, in the same transaction as the write, with the
C2 review requests above. Reached are the managed hosts of the changed group and of its
descendant groups (a membership: its host; a move: the moved subtree), and a template reaches
one when its scope selects it after the change or its scope group held it before (a host that
left, a group that moved away or was deleted). A template's or schedule's limit does not narrow
this: a limited run still renders every scoped host of its zones and a play reads their values
through `hostvars`. Templates that start a reached one through follow-ups are included; an
organization without any schedule or template trigger reads nothing more. The review's picture
lists each change as subject `inventory_group`, `inventory_membership` (with `host_id`,
`host_name`), `host` or `zone`, with fields `vars.<key>`, `<host>: vars.<key>`, `default.<field>`,
`member`, `members`, `name`, `parent`, `jump_host`, `host`, `managed`, `zone`, `ssh_user`,
`ssh_port` or `ssh_credential`, old and new value (secrets masked) and the
token, and says that approving leaves the assistant's values plain text until a person changes
or accepts them. A person's change, an accept, a rename and a write that moves nothing pause
nothing. These writes now run in one write transaction each; a membership write locks the row
(`FOR NO KEY UPDATE`) and tells a join from a change by the upsert itself.

**Host addresses** (decision D7, controller's call): `POST`/`PATCH …/hosts` now refuse an
address that is not empty, an IP address or a host name (the jump host address rule; 400), for
every caller. The check applies to new writes only: stored addresses are not rewritten, and
an address stored before the upgrade that is neither an IP address nor a host name (for
example one containing `{{`) stays as it is. **After the upgrade, review and correct such
addresses** (edit the host and save a valid address); runs carry them as plain text, but the
worker's own system jobs (bootstrap, key rotation) do not. Find them, read-only, with
`SELECT id, name, address FROM hosts WHERE address <> '' AND address !~ '^[A-Za-z0-9]([A-Za-z0-9.-]{0,251}[A-Za-z0-9])?$'`
(it also lists IPv6 literals, which the API accepts). Every run's inventory
now carries `ansible_host` as plain text (`{"__ansible_unsafe": …}`), so a stored address is
never evaluated; the worker is unchanged. The system jobs the worker renders itself (bootstrap,
key rotation) still carry the address as is; the write rule keeps new ones clean. **An older
manager alongside** pauses nothing after an assistant's inventory change, accepts any address
and renders `ansible_host` live; stop every manager instance first, as above.

A webhook trigger's settle delay (backlog 2.137) adds migration 00133:
`job_triggers.settle_seconds` (int, NOT NULL DEFAULT 0; a CHECK allows 0 or 5 to 3600)
and the nullable `job_triggers.max_wait_seconds` (a CHECK allows it only with a settle
delay, from the delay to 86400), the table `trigger_pending_runs` (one row per trigger
whose collected run waits; row-level security like `job_triggers`, deleted with its
trigger), the nullable `trigger_deliveries.pending_id` and the delivery outcome
`collected` (the outcome CHECK is widened). Catalog changes with a constant default
that rewrite nothing; every existing trigger keeps settle 0, so nothing changes until
someone sets a delay. A backup carries the columns and the table. With a delay set, a
delivery that passes every check a run would (address, signature, rate limit, mapping)
starts no run: it joins the trigger's collected run (outcome `collected`, its
`pending_id`), which starts `settle_seconds` after the last delivery, at the latest
`max_wait_seconds` (default ten times the delay) after the first, with the last
delivery's mapped values, for template runs and repository syncs alike. `on_active`
does not apply to it: when the moment comes while a run the trigger started is still
active, it waits for that run and then starts once with the newest values. A settle
ticker on every manager (every 5 seconds, `FOR UPDATE SKIP LOCKED`, so two managers
never start one twice) starts due collected runs; a disabled trigger drops its
collected run, and one whose delay is turned off starts it at the next tick (a delivery
that arrives first supersedes it: its deliveries are noted "superseded by run …").
API additions, all additive: `settle_seconds` and `max_wait_seconds` on the trigger
read, create and update (an update that leaves them out keeps them; `max_wait_seconds`
0 is the default; turning the delay off drops the maximum wait), on the trigger
request bodies (create and update) and in the trigger request picture (left out when
off or default, so the fingerprint of a picture without a delay is unchanged); the
delivery read's outcome `collected` with `pending_id` and `superseded_by` (and
`job_group_id`, the run it collapsed into, once started); the hook endpoints answer a
collected delivery with 202, `status: collected` and `due_at`; `GET …/triggers/{id}/pending`
(the waiting collected run: deliveries, first, last, due and deadline times; never the
collected values), `POST …/triggers/{id}/pending/run` (starts it now, as the person,
with the test call's permission; logged as `POST runPendingTrigger`) and
`DELETE …/triggers/{id}/pending` (discards it, with the permission that disables a
trigger; logged as `DELETE discardPendingTrigger`), both 409 when none waits. The MCP
trigger tools carry the two fields. **An older manager alongside** ignores the delay
(every delivery it receives starts a run as before, and its trigger update leaves the
two columns as they are), starts no collected run, and shows `collected` deliveries as
an unknown outcome; stop every manager instance first, as above.

Request methods, headers and a body on the `http` check (backlog 3.84) add no migration,
no proto change and no dependency. The `http` parameter `method` now also takes `POST`,
`PUT`, `PATCH`, `DELETE` and `OPTIONS`, and three parameters are new: `headers` (at most 16
`Name: value` entries of 1 024 bytes; reserved, hop-by-hop, routing, override and credential
headers refused; dropped when a redirect leaves the first origin), `body` (UTF-8 text without
NUL, at most 8 KiB, only with `POST`, `PUT` or `PATCH`) and `content_type` (only with a body;
`application/json` when a body is sent without one). `POST`, `PUT`, `PATCH`, `DELETE` and a
body need the new manager setting `PULSIX_PROBE_WRITE_METHODS` (default `false`; see the
behaviour changes below). Existing checks behave exactly as before. Two validation
messages change wording: `json_values` and body-reading assertions with `HEAD` now say
"needs a method other than HEAD" instead of "needs method GET". **Skew with agents:** an
agent of an earlier release would drop the headers and the body and send a plain GET, so a
check with a method other than GET or HEAD, headers or a body refuses `vantage_groups` (like
a check with a secret) and the probe runner never assigns one to an agent; such a check is
probed from the Pulsix server only, whatever agent versions run. **An older manager
alongside** refuses the new parameters on write, but if it holds the probe lock it probes a
stored check with headers or a body as a plain GET of its URL, and one with a new method as
"invalid method" (unknown); set these up only once every manager instance runs this version.

## Behaviour changes to check after an upgrade

- **http checks can send other methods, headers and a body (backlog 3.84, no
  migration):** any member who may write checks can now make the manager send
  `OPTIONS` and request headers to every address it reaches outside
  `PULSIX_PROBE_DENY_CIDRS` (the manager's own loopback, link-local and cloud
  metadata addresses stay refused). Requests that may change their target —
  `POST`, `PUT`, `PATCH`, `DELETE` or any request body — stay off until the
  operator sets `PULSIX_PROBE_WRITE_METHODS=true` on the manager: off, such a
  check is refused on save (400 naming the variable) and a stored one reads
  that error on every metric instead of probing, never a plain GET. Before
  turning it on, list in `PULSIX_PROBE_DENY_CIDRS` every internal network
  that organization members must not write to (container engines, databases,
  admin APIs, mail relays); a check sends its request every interval. Routing
  and override headers (`X-Forwarded-*`, `Forwarded`, `X-Real-IP`,
  `X-Original-URL`, `X-Rewrite-URL`, `X-HTTP-Method-Override`,
  `X-HTTP-Method`, `X-Method-Override`) are always refused. With the switch
  turned off, a check stored while it was on can still be disabled or edited
  as long as its method, headers, body and content type stay as they are.
- **A secret whose header names a reserved or routing header now fails
  (backlog 3.84, no migration):** an http check's secret in the "header and
  value" form may no longer name a header the probe sets (Host,
  Content-Length, Transfer-Encoding, Connection, User-Agent, Keep-Alive,
  Proxy-Connection, TE, Trailer, Upgrade, Expect, Accept-Encoding),
  Content-Type, or a routing or override header (`X-Forwarded-*`,
  `Forwarded`, `X-Real-IP`, `X-Original-URL`, `X-Rewrite-URL`, the method
  overrides). Such a check sends nothing and reads `secret "<name>": field
  "header" must not be …` on every metric until the secret names another
  header. Before, only Host, Content-Length, Transfer-Encoding, Connection
  and User-Agent were refused.

Sibling combinators and more pseudo-classes in `html(…)` assertions (backlog 3.90) add
no migration, no proto change and no dependency. The selector subset gains `+`, `~`,
`:only-child`, `:nth-last-child()`, `:first-of-type`, `:last-of-type`, `:only-of-type`,
`:nth-of-type()`, `:nth-last-of-type()`, `:empty`, `:root` and `:not()`, and the nth
pseudo-classes take `An+B`; every selector valid before selects what it selected
before, under the same work budgets. **Skew:** the grammar is not gated by version.
Every new construct is refused by the parser of earlier releases, so an older agent
used as a probe vantage, or an older manager still running during a rolling upgrade,
reports such an assertion as the error "the assertion is not valid" instead of judging
it with another meaning (the item reads unknown whenever the check's result comes from
that vantage); the check's other assertions run as before. Update the agents of a
check's vantage groups before using the new forms.

Assertions on `tcp`, `dns` and `tls` checks (backlog 3.87) add migration 00134 and no
proto change and no dependency. The migration seeds six organization rules (`TCP
assertion failed` and `TCP assertion failed (critical)`, the same for DNS and TLS, on
`<plugin>.assertion in ("warn")` and `in ("crit")`), backfilled for every existing
organization that holds no rule of that name; nothing is dropped or rewritten, and they
match nothing until a check has assertions. The three descriptors gain the optional
`assertions` parameter (formats `tcpAssertion`, `dnsAssertion`, `tlsAssertion`) and the
metrics `<plugin>.assertion` and `<plugin>.assertion_actual` on one item per entry.
Existing checks probe as before with two small differences: a `tcp`, `dns` or `tls` check
that cannot be attempted (a refused target, an unusable parameter, no vantage with a
recent result) now puts its error only on the metrics it reports, so a `tcp` check
without `expect` no longer gets an error on `tcp.banner_match`; and a `tcp` check reads
past `expect` only while an assertion looks at the response, to the first line break.
**Skew:** an agent older than this release, used as a vantage of a check with assertions,
ignores the parameter; the manager marks every entry its up rounds left out as the error
"the vantage point that answered did not evaluate this assertion; update its agent"
(the item reads unknown with that reason) instead of letting it go stale. It does so only
once the same vantage left entries out of two distinct rounds, both stamped more than about
32 minutes after the check's assertions last changed (as the manager holding the probe lock
saw the change, or after it started or took the lock): the longest an agent stays online,
and so assigned, without a heartbeat that could bring it the changed assignment (three of
its heartbeat intervals, at most 10 minutes each), its first-run window (1 minute) and a
minute's slack, so a current agent is never blamed for rounds it ran, or buffered during an
outage and replays, before it received the new parameters. A round inside that window, or a round still partly
stored, leaves the entry without a reading for that combine. So after adding an assertion
(or after a manager start or lock takeover) an older agent's entries read unknown with the
reason only after about half an hour; until then they have no reading. This applies to `http` checks too: an
`http` check probed from agent vantages whose agent ignores an entry now reads that entry
as this error instead of leaving it without a reading (in practice only agents from
before 3.13b, which were never released). Update the agents of a check's vantage groups
before adding assertions. A manager older than this
release refuses `assertions` on these plugins (unknown parameter); one still running
during a rolling upgrade while it holds the probe lock probes such a check without its
assertions, so the items go stale until a new manager probes it: stop every manager
instance before the new version starts. A downgrade needs the usual restore of a backup
from before the upgrade.

Assertions on `snmp` checks (backlog 3.134) add migration 00135 and no proto change and
no dependency. The migration seeds two organization rules (`SNMP assertion failed` and
`SNMP assertion failed (critical)`, on `snmp.assertion in ("warn")` and `in ("crit")`),
backfilled for every existing organization that holds no rule of that name; nothing is
dropped or rewritten, and they match nothing until an snmp check has assertions. The
descriptor gains the optional `assertions` parameter (format `snmpAssertion`: subjects
`value("name")` of an `oids` entry, `response_ms`, `uptime`) and the metrics
`snmp.assertion` and `snmp.assertion_actual` on one item per entry; an assertion may not
share its name with an `oids` entry, and `value("x")` must name one. Existing snmp checks
poll as before with one difference: an `oids` entry the device answers with an Opaque
float now reads its float's shortest spelling (a device's 0.15 was stored as
0.15000000596046448, it is now 0.15). A check that cannot be attempted with readable
params (an unusable secret, a refused target) also puts its error on its assertions' items.
**Skew:** snmp is probed from the manager only (it names a secret), so no agent is
involved. A manager older than this release refuses the parameter; one still running
during a rolling upgrade while it holds the probe lock polls such a check without its
assertions, so the items have no reading until a new manager polls it: stop every manager
instance before the new version starts. A downgrade needs the usual restore of a backup
from before the upgrade.

## Behaviour changes to check after an upgrade

- **Container resource usage on the docker check (backlog 3.12a, no migration):**
  a new option `resources` (off by default; existing checks are unchanged) makes the
  agent read each running or paused container's CPU, memory, network and block I/O
  once per round from the engine's one-shot stats, after the state readings and
  within the same 10-second bound, at most 8 requests at once. At most 100
  containers carry usage per round: running ones first, then stopped ones, each by
  name; the rest are counted in `docker.containers_unsampled` and forgotten. New
  per-container metrics: `docker.cpu_percent` (100 is one core on every platform;
  `docker stats` on Windows shows 100 for all cores instead), `docker.memory_bytes`
  (without the file cache, as `docker stats` shows it), `docker.memory_percent` (of
  the limit, or of the host's memory without one; never for Windows containers), `docker.net_rx_bytes_per_s`,
  `docker.net_tx_bytes_per_s` (only for a container with a network of its own, not
  `host`, `none` or `container:…`), `docker.block_read_bytes_per_s` and
  `docker.block_write_bytes_per_s`. Rates appear from the second round on, and again
  one round after a container is recreated, restarted or its counters reset. A container that is
  not running reads 0 instead of turning stale, on the values it reported while
  running (a container the agent has not seen running since it started reports none). Each option-on round adds up to seven
  tuples per container towards the per-host cap of 4 096; switching the option off
  reads the usage values as no longer asked and closes their alerts as `stale`. On an
  engine older than API 1.41 (Docker 20.10) each stats request waits a second or two,
  so with many containers some read "did not answer in time".
- **A sleeping disk reads `standby`, and its readings wait (backlog 3.9b, no
  migration):** the `smart` check leaves a disk in standby asleep, as before, but used
  to report nothing for it, so its readings turned stale, the host read unknown, and a
  disk asleep for the item expiry period (seven days by default) was forgotten with
  its open alerts. The updated agent reports a new discrete metric `smart.power_mode`
  for every device it queried that answered: `standby` for a disk it left asleep,
  `active` for one that answered with its readings (a device without SMART data, such
  as a virtual disk, reports none, and a device that could not be read reports its
  error as before). While a disk reads `standby`, its other metrics are held: the
  evaluation leaves their states exactly as they were (no new judgement, never stale,
  no fresh start), an open alert on one stays open and unchanged (no close, no
  severity change, so no new opening or close notification; an open alert that is
  not acknowledged still follows its escalation steps), and the host's roll-up counts
  their last states — a disk at crit keeps the host crit while it sleeps. A held value
  that was already unknown when the disk fell asleep (its reading had gone stale or
  failed) does not keep the host unknown. A held warn or crit stands only on the rule
  that judged it: when that rule is deleted, disabled or no longer the metric's
  winner, the value is judged ok at once and its alert closes resolved, as an awake
  disk's would; an edit of that rule reaches the value when the disk wakes. A reading
  taken after the disk was last reported asleep (a disk that then fails to answer
  reports an error) is judged as usual, not held. When the disk wakes, a rule duration
  ("for N minutes") that was counting when it fell asleep counts anew from the first
  new reading. The host page keeps showing a held value with its last
  verdict and a "waiting while standby" marker instead of a stale one, and the
  effective rules keep listing it. The hourly metric expiry leaves held metrics alone,
  so a disk asleep for longer than the item expiry period keeps its readings and
  alerts. When the disk reports `active` again, its first readings are judged on the
  states they held: the same alert continues, or closes resolved, rather than a new one
  opening. A disk taken off the check's `devices` list, or a check disabled, is not
  held: its alerts close stale as before. `smart.devices` still counts sleeping disks.
  To check after the upgrade: on hosts whose disks spin down, the advice to list only
  the always-on disks in `devices` or to ignore the sleeping ones is no longer needed
  once the agent is updated. Version skew: an agent older than this release never
  reports `smart.power_mode`, so its sleeping disks behave as before (stale, unknown,
  forgotten after the item expiry period) until it is updated. A manager older than
  this release stores the new metric but refuses a rule on it (an unknown metric) and
  keeps asking for a sleeping disk's other metrics: their readings fall behind the
  power mode and read as vanished after three intervals (backlog 3.88) rather than as
  held; stop every older manager instance before the new one starts if that matters.
- **Item decisions on a plugin key reach copied checks (backlog 3.13g, no migration):**
  an item decision (ignore or watch) whose check key is a plugin name now applies to
  every check of that plugin, the way rules have since 3.13e. An existing org-wide or
  host ignore on `disk` starts silencing the items of copied checks such as
  `disk-data`, and a watch on `disk` starts overruling an ignore there. Scope still
  ranks first (a host decision beats an organization one); within a scope a decision
  on the exact check key beats one on the plugin key. A check whose key equals another
  plugin's name (key `tcp` running `http`) is affected too: a decision on that key now
  also reaches every check of the `tcp` plugin. To check after the upgrade: the item decisions on a plugin name (Items page) and whether they were
  meant to cover your copied checks; where not, restate them on the exact check key.
  The host page marks such items as decided "via the plugin's decision".
- **A check measured again after a gap starts fresh (backlog 4.42, migration 00121):**
  a tuple that was not measured for a while (its check disabled or taken off the host,
  the host no longer monitored, a monitoring-group selector edited so the host drops
  out and back in, a metric or item no longer asked for, a metric the agent stopped
  reporting while its item still reports; not a metric held on its item's state, such
  as a sleeping disk's, backlog 3.9b) no longer resumes the state it
  had before the gap. Its state starts at the first evaluation after the gap: an alert
  that opens then is dated at that moment, after the previous episode's `stale` close,
  instead of at the moment the tuple first went bad before the gap; a rule's "for N
  minutes" duration counts anew, so a deterioration has to hold that long again before
  the state and its alert follow; a rule's clear condition no longer holds a warn or
  crit across the gap; and the flapping flag and its count start afresh. No state
  transition is recorded for the gap itself (the history shows the last transition
  before it and the next one after). A re-opened alert still takes over the closed
  episode's acknowledgement and snooze within the hour (backlog 4.39). A client host
  whose agent is offline is not unmeasured: its states stay frozen and resume as
  before. To check after the upgrade: nothing to change; expect a check switched back
  on with a duration rule to alert only after that duration again.
- **A check switched off and on within an hour keeps its alert's acknowledgement
  (backlog 4.39, migration 00120):** an alert whose subject stops being measured (its
  check disabled or taken off the host, the host no longer monitored, a monitoring-group
  selector edited so the host drops out, or the item ignored by a decision — a
  plugin-key ignore can do that to many checks at once) still closes as `stale`. When the same subject
  opens again within one hour of that close, the new alert takes over the closed one's
  acknowledgement (when the new severity is not above the acknowledged one) and its
  snooze (a timed snooze still running, or one until the severity changes when the
  severity is the same as at the close); when the new severity is above the acknowledged
  one, nothing is taken over. The new alert names the one it continues, and its timeline
  says "Acknowledgement carried over from the previous episode" or "Snooze carried over
  from the previous episode". A carried snooze holds the opening's notification as any
  snooze does; a carried acknowledgement does not: the opening is still notified (the
  problem is back), and escalation treats the alert as acknowledged, so no escalation
  step fires. An alert closed `resolved` or `forgotten` is never continued, and the
  peak severity starts afresh. To check after the upgrade: nothing to change; expect an
  acknowledged alert that comes back after a short switch-off to reach you once and not
  escalate.
- **A new SNMP failure label, `auth_or_lost` (backlog 3.70, no migration):** a SNMPv3
  poll whose first request is lost after a successful engine discovery now gets one
  more attempt before it is judged, and when that fails the same way `snmp.failure`
  is `auth_or_lost` ("credentials refused, or the requests were lost"). The SNMP
  library names a refusal only during the discovery, so practically every v3
  credential refusal seen afterwards (unknown user, wrong authentication password,
  wrong privacy password) now reads `auth_or_lost` where it used to read `auth`;
  `auth` remains only for a refusal the library names explicitly. To check after the
  upgrade: a rule or filter matching `auth` for SNMP v3 must match `auth_or_lost` as
  well (or instead); a rule on `snmp.up` is unaffected. Also, a first request cut
  off by the poll's deadline after the discovery now reads as that deadline's down
  reason (`timeout` or `error`) instead of `auth`. Real packet loss after the
  discovery now takes two request budgets (up to about four times `timeout_seconds`)
  before it is judged; when the poll's deadline falls inside the second attempt (a
  short interval), the reason reads `timeout` rather than `auth_or_lost`.
- **A ZFS pool named in `pools` that is not imported now reads `MISSING` (backlog
  3.74, no migration):** the `zfs` check used to report nothing for a listed pool
  `zpool list` does not print, so its last readings turned stale and left the host
  unknown. The updated agent reports `zfs.state` = `MISSING` for it, and nothing
  else; an existing rule "`zfs.state` is not `ONLINE`" therefore fires on it, as it
  should. A pool that may be exported on purpose (a backup pool on removable disks)
  goes into the new list parameter `optional_pools` instead: absent, it reads
  `EXPORTED` (a rule meant to stay quiet then must exclude `EXPORTED` too, e.g.
  `zfs.state != "ONLINE" and zfs.state != "EXPORTED"`). While a pool reads `EXPORTED`
  or `MISSING`, its other metrics are held, like a sleeping disk's (backlog 3.9b,
  above): their states and alerts stay as they were, the roll-up counts them, the host
  page shows them with a "waiting while EXPORTED" (or `MISSING`) marker, the metric
  expiry leaves them alone, and they are judged on those states again once the pool
  reports another state. On an installation that ran a build from the main branch
  between backlog 3.74 and this release (no tagged release), a pool exported under that
  build already had its alerts closed stale: such a pool keeps its old severity in the
  roll-up until it is imported again, and then opens a new alert dated at its old
  since. To check after the upgrade:
  listed pools that are not imported on purpose — move them to `optional_pools` or
  off the list. Version skew: an agent older than this release ignores
  `optional_pools` and reports nothing for an absent pool, as before. With `pools`
  set it does not report a pool named only in `optional_pools`, which the manager
  still asks for, so a pool moved from `pools` to `optional_pools` stops being
  reported by such an agent and its last readings go stale (the host reads unknown)
  until the agent is updated. An older agent also refuses a zfs check in its local
  `agent.yaml` that sets `optional_pools` (an unknown parameter to it).
- **Escalation steps wait after every maintenance window or silence that covered the
  alert (backlog 4.41, no migration):** the escalation follower used to look for
  covering windows at two sampled instants, so a window or silence that opened and
  closed between two follower ticks went unseen and the next step fired on its old
  schedule. It now reads every window stretch that ended within the next step's delay
  (only for a chain whose step is otherwise due) and gives the step its full delay
  again from that end, as design decision 5 always intended. A stretch counts only for
  the time after the window was created, so a window entered with past times holds
  nothing back. To check after the upgrade: a recurring window or silence whose
  stretches come back more often than a step's delay keeps that step from firing for
  as long as it recurs — look for frequent short silences on hosts whose routes have
  long-delay steps. Alerts on hosts without windows or silences are unaffected.
- **Optional second factor for demo instances (`PULSIX_MFA_OPTIONAL`,
  migrations 00113 and 00114):** off by default, so nothing changes unless it is set (`1`
  or `true`; any value `strconv.ParseBool` refuses stops the start). When on,
  an account without a second factor gets a complete session from its
  password (login, the first-run wizard and an invitation's new account, the
  latter only when the account has no factor), recorded as the audit action
  `signInWithoutSecondFactor`; for that account the password is the step-up
  (new `POST /api/v1/auth/password/reauth`, which shares the per-address
  failure budget of `POST /api/v1/auth/session/renew`) and the session
  renewal needs the password only. Accounts with a factor are unaffected.
  `GET /api/v1/setup/status` gains `mfa_optional` and `GET /api/v1/auth/me`
  gains `password_step_up` (true only on a complete session); the manager
  logs a warning at start.
  Migration 00113 adds `sessions.password_only` (boolean, default false; every
  existing session keeps false). A session completed on the password alone in
  this mode is marked, and keeps the mark through the password step-up and a
  renewal by password; only a real factor proof clears it: TOTP verify (a code
  or a recovery code), or a first enrolment in that session. The passkey
  step-up cannot, since it needs a complete session: a superseded session of a
  passkey-only account must sign in again. The session lookup
  reads a marked session as pending and its step-up as stale once the account
  has a confirmed TOTP secret or a passkey, so the owner's first enrolment
  sends every other password-only session to the factor prompt on its next
  request; the password step-up refreshes a session only while the account has
  no factor, checked in the same statement that writes. A first factor
  enrolled from a password-only session, or by any session of an account that
  has signed in on its password alone (migration 00114 adds
  `users.password_only_signin_at`, NULL for every existing account, set by
  every password-only session of an account that has no factor yet and
  cleared by the first factor, so turning the mode off in between changes
  nothing; an administrator's second-factor reset drops a mark it finds
  beside a factor), also revokes every API token of
  the account (organization and personal, the statements deactivation uses),
  in the enrolment's transaction, recorded as `revokeTokensOnFirstFactor`.
  Creating an organization or personal token re-reads the creating session
  under the account row's lock, after any first enrolment that holds it
  exclusively has committed, and refuses (403) a session it superseded; a
  token is therefore either revoked with the rest or never created. The
  answers of `POST /api/v1/auth/totp/confirm` and
  `POST /api/v1/auth/passkeys/register/finish` gain `tokens_revoked`, and the
  Security page shows a notice. What a password-only session created in an
  organization stays, within its role, and should be reviewed: invitations,
  memberships and roles, agent and worker enrolment tokens, organization
  secrets, stored credentials (SSH keys, passwords) and webhook triggers.
  The enrolment of an account's first factor takes the account row
  exclusively; from a password-only or a pending session it refuses (403) if
  the account has gained a factor since the request began. An organization
  token is inserted in a transaction bound to its organization, so row-level
  security checks it. Recovery codes now need a
  confirmed TOTP secret in every mode: `POST /api/v1/auth/recovery-codes` answers 409 without one, a
  recovery code is refused beside an unconfirmed secret, and a passkey that
  is an account's first factor deletes leftover codes. An administrator's
  second-factor reset leaves the account factor-less, so with the mode on it
  signs in with its password alone afterwards and is not made to enrol again.
  Nothing is dropped or rewritten, workers and agents are not affected, and a
  backup carries the columns with `sessions` and `users` (a restore ends the sessions as
  before). **Rolling upgrade or mixed settings:** every manager instance must
  run this version with the same value, or a sign-in lands complete on one and
  pending on another. Turning it off leaves the sessions it opened usable until
  they expire or are revoked (`deploy/compose/README.md`).
- **Secret audit summary:** the activity log entry of `PUT /api/v1/orgs/{org}/secrets/{name}`
  now reads "created secret ..." or "replaced secret ..." (it was "stored secret ...").
  Anything that filters or exports the activity log by that text needs the new wording.
- **Local check problems:** the problems list of a host agent now also holds one line
  per local check the server ignores, `<key>: ignored by the server (<reason>)`, after
  the agent's own problems; the list stays capped at 32 lines.
- **Four-digit lock PINs:** a new lock PIN is exactly four digits (it was 4 to 8). A
  PIN set under the previous release keeps working and is reported as older
  (`lock_pin_legacy` on `/api/v1/auth/me`) so the interface can ask for a new one.
  Migration 00112 adds the nullable `users.lock_pin_digits`; a NULL there means the
  length was not recorded. An older manager ignores the column, and a PIN set by
  the new release still unlocks on it. Removing a PIN or an administrator's
  second-factor reset clears the column with the hash. During a rolling upgrade,
  change lock PINs only once every manager instance runs the new version: an older
  one does not record the length, so a 5 to 8 digit PIN changed there would be
  treated as 4 digits.
- **3.93 to 3.95, session lock, expiry warning and version notice:** a person can
  lock their session behind a PIN (4 to 8 digits; the fifth wrong PIN ends the
  session), is warned five minutes before the session's 12-hour end and can renew it
  in place, and an open tab offers a reload once the manager runs a newer version.
  **Rolling upgrade with several managers:** an older manager knows nothing of
  `sessions.locked_at`, so a request of a locked session that it serves is answered
  as if the session were unlocked; **the lock is only reliable once every manager
  instance runs this version.** The new routes (`/api/v1/auth/lock…`,
  `/api/v1/auth/session/renew…`, `/api/v1/auth/passkeys/reauth/…`) answer 404 on an
  older manager, so locking, unlocking, renewing and setting a PIN fail there until
  it is upgraded. An older manager's second-factor reset does not clear the PIN.
  **SPA tabs opened before the upgrade** keep the old page code until reloaded: they
  do not know the `session_locked` refusal (what they already show stays on screen;
  only new requests fail), they show no expiry warning, they still step up through
  the discoverable passkey sign-in, and they get no version notice: the notice works
  only in tabs loaded from this release. **Existing accounts** are offered the "set
  a lock PIN" step once, at their next visit to `/`; that includes a session that
  was already signed in before the upgrade and goes back to `/` (accepted: the step
  can be skipped). An administrator's second-factor reset now also clears the
  account's lock PIN, and the offer comes back at the next sign-in. A renewal
  replaces the session with a new one and stamps the account's last sign-in time
  (`last_sign_in_at`, shown in the account administration), like a sign-in. A locked
  session still ends at its normal 12-hour end. New audit actions: `setLockPin`,
  `removeLockPin`, `skipLockPinPrompt`, `lockSession`, `unlockSession`,
  `unlockSessionFailed`, `unlockSessionExhausted`, `renewSession` and
  `renewSessionWithPasskey`; the PIN, passwords and codes never appear in them.
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
- **3.88, metrics a check stops reporting:** a metric whose last reading trails
  its own item's newest error-free reading (a value or a label) by more than
  three of the check's intervals is *vanished*: the evaluation no longer judges
  it (an alert open on it closes as `stale`, with the usual notifications, on the
  first evaluation after the upgrade), it no longer counts towards the host's
  state, and the host-check API marks the value row `vanished: true` (never on a
  disabled check). This is not specific to `http`: it applies to every plugin
  whose item keeps reporting some values while one metric stops — a metric a
  changed configuration, a renamed or dropped metric or a changed host no longer
  produces, or the timing metrics a down probe does not measure. An error proves
  only that the collection ran, so a round that reports nothing but errors (a
  SMART disk the agent can no longer open, a check whose secret cannot be
  resolved) vanishes nothing: the item's other metrics turn stale and unknown as
  before, and their alerts stay open. An `http` check without `body_contains` no
  longer asks for `http.body_match`. During an http outage longer than three
  intervals the timing metrics a down probe does not report become vanished too
  (the `http.up` and failure alerts carry the outage).
  - **About one interval of unknown before a metric vanishes.** Staleness is
    measured against the clock, vanishing against the item's newest reading, so
    a metric that stopped first reads stale and unknown for up to about one
    interval (plus the evaluation lag) and only then leaves the evaluation. The
    host's state turns unknown for that time when its other values are ok. On a
    server, or a client without an agent (only a client with an agent has a
    grace before its unknown host alert opens), the `unknown` host alert opens
    and closes again: two notifications. Where a warn or crit outranks unknown,
    as during an http outage, nothing shows.
  - **Frozen clients:** a client host that is frozen is not evaluated, so an
    alert open on one of its vanished metrics closes only when the agent is back
    (as `stale`), or as `forgotten` when the metric expiry forgets the metric.
  - **Metric expiry:** the hourly expiry (`PULSIX_ITEM_EXPIRY`, same period,
    same off switch) now also forgets a metric whose reading trails its item's
    newest error-free reading by more than that period — current value, rule
    state and history, any open alert closed as `forgotten`; graphs and the item
    itself stay. The first round after the upgrade, an hour after start, can
    therefore forget many such metrics at once. With `PULSIX_ITEM_EXPIRY` below
    three times a check's interval (possible only for checks that run less
    often than every 16 h, since the period is at least 48 h), a metric of that
    check can be forgotten before it would vanish, while it is still judged: its
    alert then closes as `forgotten` rather than `stale`. The default of 168 h is
    unaffected.
  - **Forget route:** `DELETE /api/v1/orgs/{org}/hosts/{id}/items` takes an
    optional `metric` query parameter that forgets just that metric (additive;
    an old SPA never sends it). Forgetting a metric that is its item's last
    current value is refused with `409`: forget the item instead.
  - **Stop every manager instance before the new version starts.** An older
    manager that keeps evaluating alongside judges vanished metrics as unknown
    again, so states and host alerts flap while both run. It also ignores the
    `metric` query parameter it does not know, so a metric **Forget** from the
    new host page that reaches an older instance forgets the **whole item**:
    every metric's current value, state and history, its discovery row, and its
    alerts closed as `forgotten`.
- **Run requests of repository playbooks wait for one sync (spec 2026-11-25 A, migration 00123):**
  every pending and new run request whose template runs a playbook from a repository
  cannot be approved until that repository has been synced once by the new version
  (the request page says "did not record what it fetched. Sync it again, then
  approve."). To check after the upgrade: sync each repository that assistants request
  runs for (the repository page's Sync, a webhook or a schedule), and update every
  zone's workers, since a worker older than this release never takes an approved run
  of a repository playbook (the run view says it waits for one).
- **A repository's Sync no longer queues a second sync (spec 2026-11-25 G, migration
  00124):** while a sync of the repository is queued and not yet taken by a worker,
  Sync (the repository page, the API and the assistant's `sync_repo`) returns that
  sync with `already_running: true` (HTTP 200) and the repository page says so; while
  one runs, a new one is still queued behind it. A script that expected 202 for every
  sync call must accept 200.
- **`expires_in: 0` on a run request is refused (spec 2026-11-25 A, migration 00122):**
  REST and the assistant's `request_run` answer 422 for `expires_in` outside 5 to
  1440; leave it out for the default of 60 minutes.
- **Run request extra vars need declared, non-secret variables (spec 2026-11-25 A):**
  a run request with `extra_vars` on a template without an extra vars schema is
  refused (422 "this template declares no extra variables"); declare the variables an
  assistant may set on the template. A key is also refused when the run's variable
  sources hold a secret of that name: a secret field of the template, of an inventory
  group (or its ancestors) of a target host, or a variable the playbook declares
  secret, or (since backlog 2.134) a variable the playbook's repository sets to a
  value Ansible Vault encrypts, once an updated worker has synced the repository (see
  the 2.134 note under version skew: until then such a name is caught only when the
  template's schema declares it secret, so declare it secret there).
  Variable names Ansible or Pulsix act on are refused too (below, plus Ansible's
  playbook keywords such as `tags`, `name`, `port`, `environment`).
- **Webhook mappings onto names Ansible acts on are refused (spec 2026-11-25 A):** a
  trigger whose mapping sets one of these variables is refused when saved, every
  delivery is answered 422 naming the field, and a run already queued fails at its
  claim: every `ansible_…` name (beyond the connection-security names refused before,
  for example `ansible_python_interpreter`, `ansible_become_exe`,
  `ansible_shell_executable`), every `pulsix_…` name, the magic variables
  (`hostvars`, `groups`, `group_names`, `inventory_hostname`,
  `inventory_hostname_short`, `inventory_dir`, `inventory_file`, `play_hosts`,
  `playbook_dir`, `role_name`, `role_names`, `role_path`, `role_uuid`) and the
  template globals plus `vars` (`cycler`, `dict`, `joiner`, `lipsum`, `lookup`,
  `namespace`, `now`, `omit`, `q`, `query`, `range`, `undef`, `vars`). To check
  after the upgrade: look at your triggers' mappings and map such a value onto
  another variable name. Mappings onto playbook keywords (`tags`, `name`,
  `environment`, …) keep working.
- **Webhook mappings onto secret names are refused (backlog 2.139, no migration):** a
  mapping target named like a secret of the template's run (as for run request extra
  vars above: an inventory secret of any host the template's scope reaches, a variable
  the playbook declares secret, or one its repository encrypts with Ansible Vault) is
  refused when the trigger is saved (400), and every delivery to a trigger saved
  before with such a target fails (422 naming the field, outcome `invalid_payload`;
  no run starts, nothing is collected). A run a settle delay collected is checked
  again when it is due and is not started, its deliveries failed, when a secret of a
  mapped name appeared meanwhile. A template whose scope names something that no
  longer exists cannot be checked, so a trigger with mapped variables for it cannot
  be saved (400) until the scope is fixed. To check after the upgrade: rename such a
  mapping target.
- **Numbers in run overrides stay exact (spec 2026-11-25 A):** a run with overrides
  (a schedule's, a webhook's, a run request's) sends the template's and the
  overrides' numbers as written: `1.0` stays `1.0` and an integer past 2^53 keeps
  every digit, where they used to pass through a float.
- **A job's log is paged, and the job and request reads can wait (backlog 2.133, no
  migration):** `GET …/jobs/{id}/events` now returns at most `limit` events per call
  (1 to 1000, default 1000), in sequence order, and answers `next_after`, the sequence
  number of the last event returned (the request's `after` when there was none). A client
  that read a whole log in one call gets the first 1000 events of a longer log; it
  continues with `after=<next_after>` until a call returns fewer than `limit`. An empty
  list means nothing new yet: ask again with the same `after`. The route also takes the
  filters `host` (exact name), `task` and `role` (case-insensitive text), and `state`
  (repeatable or comma-separated), all applied in the database. `GET …/jobs/{id}` and
  `GET …/run-requests/{id}` accept `wait` (seconds, 0 to 25, default 0) and `unless`
  (the status the caller already knows) and answer as soon as the status differs, or
  when the time is up. Additive: `last_synced_commit`, `last_synced_at`,
  `last_synced_commit_current` and `last_synced_commit_stale` on the repository read, and
  `sources` (variable to group name, or `host`) on `GET …/hosts/{id}/inventory/preview`,
  which now also carries the values of the host's groups and their parent groups.
- **An approved run's job is refused when it would reach a host the approval did not
  include (backlog 2.141, no migration):** a job keeps the hosts it was enqueued for,
  and the claim of a run request's job now also checks them against the approved run
  picture's targets. A host that joined the template's hosts between the approval's
  look at the picture and the enqueue fails that job at its claim with `refused at
  claim: the run's hosts changed after it was approved; ask for the run again (host
  <id> was not part of the approved run)`; the run reports failed, like any other
  change after approval. Fewer hosts than approved still run. Runs without a run
  request are unaffected. A job enqueued before the upgrade is checked the same way
  when it is claimed after it.
- **More database metrics (backlog 3.10b, no migration):** the updated agent reports
  new metrics; nothing existing changes. `postgres`: `pg.replicas` on item "" every
  round (an error "no privilege to see replication" for a login without `pg_monitor`,
  and a failure on a failed round like the other host metrics), and per replica item
  `replica:<application_name>@<client address>` (`replica:<name>@local` over a Unix
  socket) `pg.replica_lag_bytes`, `pg.replica_lag_seconds` and `pg.replica_state`;
  these items are asked whatever `databases` lists. Only senders that replay count:
  `pg_basebackup`, `pg_receivewal`, `barman_receive_wal` and logical table-sync workers
  are left out, and a standby counts from its first reply. A failed replica read errors
  `pg.replicas` only; the round stays up. A replica that is retired or changes address
  leaves a stale item behind; **Forget** it, or let the item expiry remove it. `redis`: seven persistence metrics
  on item "" (`redis.rdb_last_bgsave_status`, `redis.rdb_last_save_age_seconds`,
  `redis.rdb_changes_since_last_save`, `redis.aof_enabled`, `redis.aof_last_write_status`,
  `redis.aof_last_bgrewrite_status`, `redis.loading`); a Redis-protocol server whose INFO
  lacks them reports an error on each. `mysql`: a new optional parameter
  `schema_sizes` (default false) adds `mysql.schema_size_bytes` per schema and
  `mysql.schemas_unreported` (a schema without tables reads 0); the login needs a privilege on the tables
  (`GRANT SHOW VIEW ON *.*` suffices and reads no data). Version skew: an older agent
  reports none of these and ignores `schema_sizes`; a manager older than this release
  stores the new metrics but refuses rules on them, and refuses a `mysql` check that
  carries `schema_sizes` (unknown parameter), so roll the managers before setting it.
  Two more skew cases: a `mysql` check defined in a host's `agent.yaml` with
  `schema_sizes` is ignored as a whole by an older manager (the plugin refuses its
  parameters), `mysql.up` included, until the manager is updated; and an older manager
  has no rule that asks for replica items on a `postgres` check with a `databases`
  list, so it shows them as not asked until it is updated.

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
   old binary would keep running. Each release's page also carries the binaries
   and `.sha256` files of that version, identical to what the manager serves;
   the manager stays the documented source.

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

Backlog 3.60 changes the two installers again, still with no migration and no manager
change beyond the scripts it serves. A re-run on a host whose data folder already holds
the agent's identity (`agent.crt`, `agent.key` and `agent.ca.pem`, each a non-empty
regular file) no longer asks for an enrollment token and no longer stops without one; it
prints that it reuses the identity and writes the env file without
`PULSIX_ENROLL_TOKEN`. A token given with `--token`/`--token-file` (`-Token`/`-TokenFile`)
is written as before. Automation that passes a token on every upgrade keeps working; it
may drop the token, but then an upgrade no longer heals an agent whose certificate has
expired (the old flow enrolled it again with the token it was given). An expired
certificate needs a re-run with a new token; a revoked agent keeps a locally valid
certificate and never uses a token, so it needs an uninstall without `--keep-data` and
a fresh install, as before.

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
