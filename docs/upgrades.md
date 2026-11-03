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

## Roll order

1. **Manager** (with the database): back up, then start the new version. It
   migrates on start. Components one minor behind stay within the rule.
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
