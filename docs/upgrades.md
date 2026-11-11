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

- **Interpreter discovery warning:** every run's `ansible.cfg` now sets
  `interpreter_python = auto_silent`. Ansible still discovers each host's
  Python as before, but no longer prints its "using the discovered Python
  interpreter" warning, which reached the worker as a bare line beside the
  run's events and was dropped. A host or playbook that sets
  `ansible_python_interpreter` is unaffected.
- **Webhook body limit:** a webhook body of exactly 1 MiB is still accepted and
  one byte more is refused with 413, unchanged; the dependency update that moved
  the limit's off-by-one is compensated in the hook endpoints.
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
