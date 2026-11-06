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

7.88 adds `repo.repo_subpath` to a repo run's claim (in its JSON kind
payload; no proto change). The field is additive: a worker one minor older
ignores it and keeps 7.83's behaviour (a run finds only the roles its own
worker's last sync installed). A 7.88 worker under an older manager finds
no `repo_subpath` and derives it from the claim's `subpath` by removing the
`/<playbook path>` suffix (exact for every manager released so far); that
fallback is dropped one minor later. A worker downgraded below 7.88 ignores
`<repo>.requirements/` next to each clone (it stays on disk until removed by
hand) and installs into `<repo>.galaxy` again on its next sync.

## Behaviour changes to check after an upgrade

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
  reason, when it uses YAML anchors, aliases, merge keys or tags, or holds
  more than one YAML document; is not UTF-8 or holds invisible characters;
  uses `include:`; names a role whose name is not a plain directory name (a
  local `src` such as `./r.tar.gz` is its own name, so such a role needs a
  `name:`); names a collection that is not `namespace.name`; has a local
  source outside the repository (`../`, an absolute path, a link leading out);
  has a source (or a comma part of one) starting with `-`; or names a git
  source whose subdirectory leads out of its checkout. A repository subpath
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
