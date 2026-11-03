# Backup and restore (backup 7.2)

Status: approved 2026-11-02. Decisions quoted as the user's come
from the brainstorming conversation; everything marked *(implementer's call)*
is mine and open to correction.

## Intent

An operator of a Pulsix installation must be able to take a backup and put
it back **as simply as possible, with nothing complex involved** (the
user's words). Backups can also run on a schedule that prunes old ones, can
go to a folder on the server or to S3-compatible storage, and are always
encrypted. An instance administrator sees and drives all of this in the web
interface; putting a backup back is a command, because the moment you need
it is usually the moment the instance is broken or brand new.

## Goals

- **One command to take a backup, one command to put it back.**
  `pulsix-manager backup` and `pulsix-manager restore <name>`, runnable as
  `docker compose run --rm cli …` against the shipped compose file. The
  one-off commands use the `cli` service, not `manager`, because a one-off
  container of the manager service would carry its Traefik labels. No
  `pg_dump`, no extension dance, no order of steps the operator has to know.
- **Web interface** (instance administrators only), a *Backups* tab under the
  instance settings beside SMTP and Language, as in the approved mockup:
  - a status strip: last backup (time, size, result), next scheduled
    backup, the destination, and a **Back up now** button;
  - **Destination:** a folder on the server, or S3-compatible storage
    (endpoint, region, bucket, path prefix, access key, secret key) with a
    **Test connection** button;
  - **Schedule and retention:** automatic backups on/off, daily at a time of
    day, keep *N* daily, *N* weekly and *N* monthly backups, and whether the
    metric history is included;
  - **Existing backups:** time, kind (scheduled, or manual with who),
    Pulsix version, size, status (a failure shows its reason), and per row
    *Download*, *Delete* and *Restore…*.
- **Restore is a command, not a button** (the user chose option A). The
  row's *Restore…* opens a dialog that shows the exact command to copy for
  that backup and destination, plus the reminder that the master key is
  needed.
- **S3 support** (the user's request): AWS S3 and S3-compatible services
  (MinIO, Hetzner, Wasabi and the like; path-style addressing must work).
- **Encryption of every backup** (the user's request), always on, no switch.
- **Pruning:** the schedule's retention rule deletes old scheduled backups in
  the folder and in the bucket alike.

## Non-goals

- Restoring from the web interface, uploading a backup through it, or a
  maintenance mode (rejected in favour of option A).
- Point-in-time recovery, WAL archiving, incremental or differential
  backups. A backup is a full snapshot.
- Backing up a single organization, or restoring one organization into
  another instance.
- Backing up the worker volumes. The worker bundle is re-enrolled with
  `pulsix-manager worker add`; the worker data directory is a cache.
- Other storage targets (SFTP, Azure, GCS). S3-compatible covers most of
  them through gateways.
- More than one destination at a time, or copying a backup between them.
- Server-produced texts stay English (as decided for backlog 1.41); the
  SPA's own strings exist in English and German.

## Anti-goals

- A backup that cannot be restored and nobody notices. A failed scheduled
  backup must be loud, and the format must be tested by restoring it.
- A stolen backup file being worth anything without the master key.
- Restore silently overwriting a live instance.
- Restore depending on the database that is being restored (for example
  reading the S3 credentials from it): restore must work on an empty
  database.

## What is in a backup

All of Pulsix's state lives in the Postgres database, including the
instance CA (migration 00016) and every secret, sealed with the master key.
So a backup is **the database's rows plus the schema version they belong
to**. The master key is **not** in the backup *(implementer's call)*: a
backup that carried it would hand everything to whoever steals the file. The
web interface and both commands say plainly that the master key must be
kept separately.

The metric history (`check_samples`) is optional (the setting above;
`--without-metrics` on the command); the switch covers that one table only.
Everything else is always included, run logs (`job_events`) and check state
history (`check_state_changes`) among them *(implementer's call)*. The
retention policies come back with the schema, so a restored instance prunes
rows older than 90 days on its next policy run, exactly as the live one
would have; that is intended.

## Decisions

### Pulsix exports the data itself *(implementer's call)*

The manager image is distroless and has no `pg_dump`, and a `pg_dump` of a
TimescaleDB database needs the `timescaledb_pre_restore()` /
`post_restore()` dance and matching extension versions on both sides. So
Pulsix writes its own logical dump:

- a manifest (format version, Pulsix version, goose schema version, creation
  time, kind, whether metrics are included, row count per table);
- each table's rows via `COPY (SELECT * FROM <table>) TO STDOUT` (binary or
  text is the builder's call), for every table of the `public` schema except
  goose's own, discovered from the catalogue rather than from a hand-kept
  list, so a new migration's table is never forgotten. The query form
  matters: a plain `COPY <hypertable> TO STDOUT` returns only the empty
  parent, because a TimescaleDB hypertable's rows (`job_events`,
  `check_samples`, `check_state_changes`) live in chunk tables, some of
  them compressed. Chunk tables and TimescaleDB's own schemas are never
  dumped. A test pins that the set of dumped tables equals the set of
  tables the migrations create.

The dump runs in one `REPEATABLE READ` read-only transaction so it is a
consistent snapshot. It must read every organization's rows. **The running
manager dumps as `pulsix_app` through the instance-wide bypass the
existing policies already provide** (every forced-RLS table carries its
`org_isolation_bypass` policy; 57 tables use `FORCE ROW LEVEL SECURITY`),
not through a second, owner-role pool kept open for backups: that would
undo the least-privilege design `assertCannotBypassRLS` guards
*(implementer's call)*. The `backup` command and `restore` run as the
owner role from `PULSIX_DATABASE_URL`, like the migrations, because they
are one-shot operator commands.

The snapshot also contains the attempt row of the backup that is taking it
(state "running"). Restore marks such rows as abandoned, and a manager
start marks attempts left running by a crashed manager the same way, so
neither blocks the next backup.

Restore:

1. checks the manifest: the backup's schema version must not be newer than
   the binary's migrations (refuse with "this backup is from Pulsix X; run
   restore with X or newer");
2. empties the database's `public` schema (see *What "empty" means* below)
   and runs the migrations **up to the backup's schema version**;
3. loads every table with `COPY … FROM STDIN` in one transaction, with
   foreign keys not getting in the way of load order, and resets every
   sequence. It must work **without superuser rights**, with only the role
   that owns the schema (managed Postgres with TimescaleDB is a supported
   setup), so `session_replication_role` is out; dependency order from the
   catalogue's foreign-key graph, with self-referencing tables handled, is
   the expected route *(implementer's call; the mechanics are the
   builder's)*;
4. runs the remaining migrations up to the binary's version, exactly as a
   manager start would.

So a backup from an older Pulsix restores into a newer one, which also gives
7.3 (upgrade path) a safe "back up, upgrade, restore if needed" story.

### Encryption *(implementer's call)*

The backup is encrypted with a key **derived from the master key** (HKDF
with a fixed, versioned label), not with a second password: restoring needs
the master key anyway to open the sealed secrets, so the operator keeps one
secret, not two. The construction is a standard authenticated streaming
format so a multi-gigabyte file never sits in memory and a truncated or
tampered file is detected; using the `age` format (`filippo.io/age`) with an
X25519 identity derived from the master key is the suggestion, because it
leaves an escape hatch (`pulsix-manager backup key` prints the identity, and
the stock `age` tool can then decrypt a file by hand). The manifest's
non-secret fields may also sit unencrypted in the file header or the object
name so `restore --list` can show them without decrypting; nothing else may.
The stream is compressed before it is encrypted (zstd or gzip, the
builder's call).

There is no master-key rotation in Pulsix today, so one derived key covers
every backup an instance makes.

### Storage

- **Folder:** `/var/lib/pulsix/backups` inside the manager container, a new
  named volume `pulsix-backups` in the shipped compose file, created in the
  image owned by the nonroot user like the worker-bundle directory.
- **S3:** a Go client library is acceptable (`minio-go` is the suggestion;
  the builder's call). The secret key is sealed with the master key like the
  SMTP password, in `instance_settings`.
- **The storage is the source of truth for which backups exist.** The list
  in the web interface and `restore --list` come from listing the folder or
  the bucket prefix, so they work on a fresh instance and after a restore.
  A small table records attempts (start, end, kind, who, result, error) so
  failures and "manual, by whom" show up; it is history, not the catalogue.
- Object names are sortable and self-describing, e.g.
  `pulsix-20261102T030000Z-scheduled.pxbak` (exact scheme is the builder's
  call).

### Schedule, retention and running backups

- One schedule per instance: daily at a time of day in an IANA time zone,
  defaulting to the browser's zone when first saved *(implementer's call)*.
- Retention (grandfather-father-son) applies to **scheduled** backups only:
  keep the newest *N* per day, week and month. **Manual backups are never
  pruned automatically** *(implementer's call)*; they go when someone
  deletes them. Defaults: 7 daily, 4 weekly, 6 monthly *(implementer's
  call)*.
- Pruning runs after a successful scheduled backup, never after a failed
  one, and never deletes the newest successful backup.
- Backups run inside the manager. With several managers (backlog 6.1) only
  one may take a given scheduled slot; the existing advisory-lock or
  row-claim pattern decides *(builder's choice)*. **Back up now** runs in the
  background on the manager that received the request and refuses (409)
  while another backup is running.
- A failed scheduled backup is loud: the attempt shows as failed with its
  reason in the list and in the status strip, and every instance
  administrator gets an e-mail when SMTP is configured *(implementer's
  call)*.

### The commands

- `pulsix-manager backup [--to <dir>|s3://bucket/prefix] [--without-metrics]`
  takes a manual backup. Without `--to` it uses the destination configured
  in the web interface; with it, S3 credentials come from environment
  variables (`PULSIX_BACKUP_S3_*`), so the command also works before
  anything is configured.
- `pulsix-manager restore --list [--from …]` lists backups with time, kind,
  version and size.
- `pulsix-manager restore [--from …] <name>` restores. It needs
  `PULSIX_DATABASE_URL` and `PULSIX_MASTER_KEY` like the manager, and S3
  credentials from the environment when restoring from a bucket (never from
  the database being replaced).
- **A downloaded file** goes in on standard input:
  `docker compose run --rm -T cli restore - < pulsix-….pxbak`
  *(implementer's call: no bind mount or copying into a volume to explain)*.
  Input on stdin cannot answer a prompt, so `--replace` then needs `--yes`.
  The *Restore…* dialog in the web interface shows both forms: by name from
  the configured destination, and from a downloaded file.

#### What "empty" means

A manager writes the instance CA on its first start, so a database a
manager has ever started on has rows even before setup. **A database counts
as empty when it has no user and no organization** *(implementer's call)*:
the state right after `docker compose up` on a new server. Restore always
rebuilds the `public` schema (drops every Pulsix object in it, recreating
the TimescaleDB extension if needed, then migrates up to the backup's
version), because that fresh install may already be at a newer schema than
the backup. On a database that is not empty, restore refuses unless given
`--replace`, which asks for confirmation (typing the word `restore` on a
terminal, or `--yes`).

Restore also refuses while a manager is running against the database and
says to stop it first. Nothing detects that today, so this needs a new
mechanism, e.g. managers setting `application_name` on their connections
and restore checking `pg_stat_activity`, or a session advisory lock every
manager holds while it runs (the builder's choice, but planned explicitly).

#### Workers after a restore

On the same host the worker bundle volume survives and workers and agents
reconnect on their own, since the CA comes back with the backup, **unless
they renewed their certificate after the backup was taken** (certificates
live 90 days and renew at two-thirds, so about every 60 days): the manager
accepts only serials the database knows, so those show as offline / not
connecting, their logs say to enroll anew, and they must be enrolled again
from the Agents and Workers pages (final review I1; backlog 7.47 plans a
fix). The bundled compose worker is the exception: `worker-enroll` re-enrols
it when the database does not know its bundle's serial, and the worker
re-reads its bundle before every handshake and after a refused certificate.
**On a new
server** the restored database already knows `worker-1`, so the compose
`worker-enroll` service would fail with "name taken". `worker add` gains a
way to re-enrol a worker of the same name, revoking the old identity, and
the shipped compose file uses it, so a restore on a new server needs no
extra step *(implementer's call; it serves "as simple as possible")*. Agents
on the hosts keep their certificates, which the restored CA still trusts.

- On success restore prints what was restored (backup time, version, row
  counts) and what happens next: start the stack; workers and agents
  reconnect, except those that renewed their certificate after the backup,
  which must be enrolled again.

### API (instance administrators, audited)

- `GET`/`PUT /api/v1/admin/backups/settings` — destination, schedule,
  retention, metrics flag. The S3 secret is write-only (as with SMTP).
- `POST /api/v1/admin/backups/settings/test` — test the destination without
  saving (write, list and delete a probe object).
- `GET /api/v1/admin/backups` — the listing merged with recent attempts.
- `POST /api/v1/admin/backups` — back up now (202).
- `GET /api/v1/admin/backups/{name}/download` — streams the encrypted file.
- `DELETE /api/v1/admin/backups/{name}`.

Every change, manual backup, download and deletion goes to the audit log.
Bearer tokens may not reach these routes *(implementer's call)*, as with the
other instance settings.

### Docs

End-user documentation in `NoiXdev/noix-docs`, branch `pulsix`: a new admin
page on backups (setting it up, the commands, keeping the master key, a
restore walk-through on a new server), and a pointer from the installation
pages.

## Constraints

- Additive migrations only, Conventional Commits, `make test`,
  `golangci-lint run`, ESLint and `tsc -b` before every commit.
- New logic comes with tests. Above all, an **end-to-end round trip** test
  against the real Postgres/TimescaleDB container: fill a database through
  the services, back up, restore into an empty database, and compare every
  table's rows; plus a restore of a backup taken at an older schema version.
- Public docs name no tech stack (no "Postgres", "TimescaleDB", "S3 SDK"; the
  phrase "S3-compatible storage" is the product term and is fine).
- The SPA strings exist in English and German (backlog 1.41's catalogues).

## Left to the builder

- The dump encoding (binary or text `COPY`), compression algorithm, exact
  file layout and object naming.
- The S3 client library.
- How the load avoids constraint ordering problems.
- How several managers agree on who runs a scheduled backup.
- Exact wording of the UI and the commands' output.
