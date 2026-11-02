# Backup and Restore Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** One command to back Pulsix up and one to put it back, encrypted, to a folder or S3-compatible storage, with a scheduled, self-pruning backup an instance administrator drives from the web interface.

**Architecture:** A new `internal/backup` package owns the file format (plain header + age-encrypted, gzip-compressed, framed `COPY` stream), the dump and load against Postgres, the two storage back ends, retention and the service (settings, attempts, schedule). `cmd/manager` gains `backup`, `backup key` and `restore`; `internal/api` gains `/api/v1/admin/backups…`; the SPA gains a *Backups* tab under the instance settings.

**Tech Stack:** Go 1.27, pgx v5 (`CopyTo`/`CopyFrom` on `*pgconn.PgConn`), goose, `filippo.io/age` (new), `github.com/minio/minio-go/v7` (new), stdlib `crypto/hkdf` and `compress/gzip`; React/TanStack Router/Query, the 1.41 i18n catalogues.

**Spec:** `docs/superpowers/specs/2026-11-02-backup-restore-design.md`

## Global Constraints

- Tests: `make test`. Never one whole-module `go test ./...`; single packages with an explicit `-timeout` (e.g. `go test -race -timeout 900s ./internal/backup/...`).
- Before every commit: `golangci-lint run` (0 issues) and `gofmt -s -l .` (empty). SPA: `cd web && PATH=$HOME/.nvm/versions/node/v24.14.0/bin:$PWD/node_modules/.bin:$PATH` then `eslint .`, `tsc -b`, `vitest run`. Never run `prettier --write`.
- Generators: sqlc with `PATH=/private/tmp/claude-501/-Users-noidee--dev-pulsix/76b69f36-5040-49f9-a80e-08a1ef63bcdb/scratchpad/bin:$PATH sqlc generate`; SPA types with `bash scripts/api-gen.sh` from `web/` (same PATH). sqlc chokes on apostrophes and named params in query comments: use `sqlc.arg()`, no `'` in `--` comments.
- Migrations additive; the next free number is `00075`.
- Conventional Commits, footer `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. Commit only on the branch `feat/7.2-backup-restore`; never push, never tag.
- Layers: handlers decode/call/encode only; services depend on interfaces declared beside them; stores behind small interfaces; time injected (`now func() time.Time`).
- The master key never goes into a backup. Every backup is encrypted; there is no switch.
- Restore must not use `session_replication_role`, `CREATE/DROP EXTENSION` or any privilege the migrations themselves do not need.
- Every SPA string exists in `web/src/i18n/en/*.ts` and `web/src/i18n/de/*.ts` (glossary of 1.41: "Instanz", "Sicherung" is **not** used — the German UI says "Backup"/"Backups"; "wiederherstellen" for restore).
- Public docs (noix-docs) name no tech stack; "S3-compatible storage" is the product term and allowed.

## Review Focus

1. **A corrupt, truncated or wrong-key file given to `restore --replace`** — the live database must be untouched. Restore verifies the whole file (authentication, framing, row counts against the manifest) before it wipes anything; stdin is spooled to a temp file first. Test in Task 2/5.
2. **A table with a generated column or an identity column** — the load must skip generated columns and keep identity values. Test in Task 2 (a throwaway table created in the test schema before the dump).
3. **The schema owner is not a superuser and 57 tables use `FORCE ROW LEVEL SECURITY`** — `COPY FROM` is refused on a table whose RLS applies to the role, so the load runs `ALTER TABLE … NO FORCE ROW LEVEL SECURITY` inside its transaction and re-forces before commit; `relforcerowsecurity` must be identical before and after. Test in Task 2.
4. **Daily 03:00 Europe/Berlin across the DST switch** — exactly one scheduled slot per local day, none skipped, none doubled. Test in Task 4.
5. **A manager crash or a restore leaving an attempt row "running"** — the next "Back up now" must not be refused with 409 forever. Test in Task 4.

---

### Task 1: File format and encryption

**Files:**
- Create: `internal/backup/format.go`, `internal/backup/crypt.go`
- Test: `internal/backup/format_test.go`, `internal/backup/crypt_test.go`
- Modify: `go.mod`, `go.sum` (`filippo.io/age`)

**Interfaces:**
- Produces:
  - `const FormatVersion = 1`; magic line `"PULSIXBAK1\n"`.
  - `type Header struct { FormatVersion int; CreatedAt time.Time; Kind string /* "scheduled" | "manual" */; PulsixVersion string; SchemaVersion int64; IncludesMetrics bool }` — the only plaintext, JSON on one line after the magic.
  - `func WriteHeader(w io.Writer, h Header) error`, `func ReadHeader(r io.Reader) (Header, error)` (reads at most 4 KiB; `ErrNotABackup` on a wrong magic).
  - `func DeriveKey(masterKey [32]byte) string` — lowercase hex of HKDF-SHA256(masterKey, salt nil, info `"pulsix backup v1"`, 32 bytes).
  - `func Encrypt(w io.Writer, masterKey [32]byte) (io.WriteCloser, error)` — age scrypt recipient with passphrase `DeriveKey(masterKey)`, work factor 15, then gzip inside; closing flushes both.
  - `func Decrypt(r io.Reader, masterKey [32]byte) (io.Reader, error)` — `ErrWrongKey` when age reports no matching identity.
  - Frame stream inside the encrypted payload: `type FrameWriter`, `func NewFrameWriter(w io.Writer) *FrameWriter` with `Begin(name string, columns []string) error`, `Write(p []byte) (int, error)` (one table's COPY text), `End(rows int64) error`, `Manifest(m Manifest) error` (last frame); `type FrameReader`, `func NewFrameReader(r io.Reader) *FrameReader`, `Next() (Table, error)` returning `io.EOF` after the manifest, `Table{Name string; Columns []string; Data io.Reader}`, `Manifest() Manifest`.
  - `type Manifest struct { Header Header; Tables []TableCount; Sequences []SequenceValue }`, `TableCount{Name string; Rows int64}`, `SequenceValue{Name string; LastValue int64; IsCalled bool}`.
  - Errors: `ErrNotABackup`, `ErrWrongKey`, `ErrCorrupt` (bad framing, missing manifest, truncated stream).

- [ ] **Step 1: Write the failing tests**
  - `TestHeaderRoundTrip`: write then read a Header; all fields equal; `ReadHeader` on `"hello"` returns `ErrNotABackup`.
  - `TestEncryptDecryptRoundTrip`: 5 MiB of random bytes through `Encrypt`/`Decrypt` with key `[32]byte{1}` come back equal.
  - `TestDecryptWithOtherMasterKeyFails`: decrypt with `[32]byte{2}` → `errors.Is(err, ErrWrongKey)`.
  - `TestDecryptTruncatedFails`: drop the last 100 bytes of the ciphertext; reading to EOF returns an error (`ErrCorrupt` or an age error wrapped as `ErrCorrupt`).
  - `TestDeriveKeyIsStableAndLabelled`: `DeriveKey([32]byte{1})` is 64 hex chars, equal across calls, and differs from `DeriveKey([32]byte{2})`; pin the value for `[32]byte{1}` as a golden string so a label change is caught.
  - `TestFramesRoundTrip`: two tables (one with 0 rows, one with 3 lines of COPY text containing a tab, a `\N` and a backslash) plus a manifest; `FrameReader` returns both in order, byte-identical data, then `io.EOF`, then `Manifest()` equal.
  - `TestFramesWithoutManifestAreCorrupt`: a stream cut after the first table → `ErrCorrupt`.
- [ ] **Step 2: Run** `go test -race -timeout 300s ./internal/backup/` — FAIL (package does not exist).
- [ ] **Step 3: Implement** `format.go` and `crypt.go`. Frame encoding: one type byte (`'T'` table begin, `'D'` data chunk, `'E'` table end, `'M'` manifest), then a uint32 big-endian length and the payload (JSON for T/E/M, raw bytes for D, chunks ≤ 1 MiB).
- [ ] **Step 4: Run** the package tests — PASS; `golangci-lint run ./internal/backup/...`.
- [ ] **Step 5: Commit** `feat(backup): an encrypted, framed backup file format`

### Task 2: Dump and restore against Postgres

**Files:**
- Create: `internal/backup/dump.go`, `internal/backup/restore.go`, `internal/backup/catalog.go` (table, column, sequence, FK-order discovery), `internal/backup/wipe.go`
- Modify: `internal/db/db.go` (add `func (d *DB) MigrateTo(ctx context.Context, version int64) error` and `func (d *DB) SchemaVersion(ctx context.Context) (int64, error)`, both under the same advisory lock as `Migrate`), `internal/db/testdb/testdb.go` (add `FreshDatabase`)
- Test: `internal/backup/dump_restore_test.go`, `internal/db/testdb/testdb_test.go`, `internal/db/db_test.go`

**Interfaces:**
- Consumes: Task 1 (`Header`, `Manifest`, `FrameWriter`/`FrameReader`, `Encrypt`/`Decrypt`, `WriteHeader`/`ReadHeader`).
- Produces:
  - `type DumpOptions struct { Kind string; PulsixVersion string; IncludeMetrics bool; Now func() time.Time }`
  - `func Dump(ctx context.Context, pool *pgxpool.Pool, masterKey [32]byte, w io.Writer, opts DumpOptions) (Header, error)` — one `REPEATABLE READ READ ONLY` transaction; works on the owner pool and on a `pulsix_app` pool with no org scope (bypass policy).
  - `func Verify(r io.Reader, masterKey [32]byte) (Manifest, error)` — reads the whole file, checks framing and per-table row counts against the manifest.
  - `type RestoreOptions struct { Replace bool; BinarySchemaVersion int64 }`
  - `func Restore(ctx context.Context, d *db.DB, masterKey [32]byte, open func() (io.ReadCloser, error), opts RestoreOptions) (Manifest, error)` — calls `open` twice: once for `Verify`, once to load.
  - `func IsEmpty(ctx context.Context, pool *pgxpool.Pool) (bool, error)` — true when the schema is absent or `users` and `organizations` have no rows.
  - Errors: `ErrNotEmpty`, `ErrBackupTooNew` (message names the backup's Pulsix version).
  - `testdb.FreshDatabase(t *testing.T) (ownerURL string)` — creates database `pulsix_fresh_<random>` with the TimescaleDB extension created by the superuser, owned by a `NOSUPERUSER LOGIN` role `pulsix_restore_owner` that holds `pulsix_app` `WITH ADMIN OPTION`; drops the database in `t.Cleanup`.
- Algorithm decisions:
  - Tables: every ordinary or partitioned table in `public` except `goose_db_version`; hypertables are read with `COPY (SELECT <cols> FROM <t>) TO STDOUT` in text format; `check_samples` is skipped unless `IncludeMetrics`. Columns: `attnum > 0 AND NOT attisdropped AND attgenerated = ''`, in `attnum` order.
  - Sequences: every sequence in `public`, `last_value`/`is_called` into the manifest; restore calls `setval` for each.
  - Restore order: `Verify` → `IsEmpty` (refuse `ErrNotEmpty` unless `Replace`) → `ErrBackupTooNew` if `Header.SchemaVersion > BinarySchemaVersion` → wipe → `MigrateTo(Header.SchemaVersion)` → one transaction: `NO FORCE ROW LEVEL SECURITY` on every forced table, record every foreign-key constraint in `public` (`pg_get_constraintdef`) and drop them all, `COPY FROM` every table in any order, re-add every foreign key from its recorded definition (which validates the loaded rows), re-`FORCE`, `setval`s, mark `backup_runs` rows in state `running` as `abandoned` (if the table exists at that version) → commit → `Migrate` (the remaining migrations).
  - Wipe: drop every table, view, sequence, function and type in `public` that is not an extension member (`pg_depend.deptype = 'e'`), plus `goose_db_version`; never `DROP SCHEMA`, never touch extensions.

- [ ] **Step 1: Write the failing tests** (integration, testdb):
  - `TestFreshDatabaseIsEmptyAndOwnedByNonSuperuser` (testdb): connect with the URL; `rolsuper` false; no tables in `public`; `timescaledb` extension present.
  - `TestMigrateToStopsAtVersion` (db): on a fresh database `MigrateTo(20)` then `SchemaVersion` = 20; then `Migrate` reaches the newest.
  - `TestRoundTripRestoresEveryRow`: fill the shared test DB through a fixture helper that inserts at least one row into every table in `public` (organization, users, hosts, a job with `job_events`, `check_samples`, `check_state_changes`, instance CA, settings…; the helper fails the test naming any table left empty so a future table forces an update); `Dump` with `IncludeMetrics: true`; `Restore` into `FreshDatabase`; for every table compare `SELECT md5(string_agg(t::text, '|' ORDER BY t::text)) FROM <t> t` between source and target; compare every sequence's `last_value`.
  - `TestDumpedTablesEqualMigratedTables`: the manifest's table list equals the catalogue's table list for `public` minus `goose_db_version`.
  - `TestDumpWithoutMetricsLeavesCheckSamplesEmpty`: restored `check_samples` count 0, every other table restored.
  - `TestDumpAsAppRoleSeesEveryOrganization`: two orgs with hosts; `Dump` through `testdb.Open(t).Pool()` (pulsix_app, no scope) → restored `hosts` has both orgs' rows.
  - `TestRestorePreservesForcedRLS`: `relforcerowsecurity` per table identical between source and restored target, and 57 tables forced; likewise the set of `(table, constraint name, pg_get_constraintdef)` for every foreign key.
  - `TestRestoreSkipsGeneratedColumns` (Review Focus 2): before the dump, create in the source a table `zz_backup_probe (id bigint GENERATED ALWAYS AS IDENTITY, a int, b int GENERATED ALWAYS AS (a*2) STORED)` with two rows, and in the target after `MigrateTo` the same table via a test hook `RestoreOptions.afterMigrate func(*pgxpool.Pool) error` (unexported field set through `export_test.go`); restored rows equal, `b` recomputed. Drop the probe table in cleanup.
  - `TestRestoreRefusesNonEmptyWithoutReplace`: target with one user → `ErrNotEmpty`; with `Replace: true` succeeds and the old user is gone.
  - `TestRestoreRefusesNewerBackup`: `BinarySchemaVersion` one below the header → `ErrBackupTooNew`, target untouched.
  - `TestRestoreFromOlderSchemaVersion`: dump a fresh database migrated only to version 60 (via `MigrateTo`) with one org; restore into another fresh database with `BinarySchemaVersion` = newest → schema at newest, org present.
  - `TestCorruptFileLeavesTargetUntouched` (Review Focus 1): non-empty target, `Replace: true`, a backup with its last 1 KiB cut → error, and the target still has its user and its schema version.
  - `TestWrongKeyLeavesTargetUntouched`: same with master key `[32]byte{9}` → `ErrWrongKey`, target untouched.
  - `TestRestoreMarksRunningAttemptsAbandoned`: (needs Task 4's table; write it in Task 4 instead — see there).
- [ ] **Step 2: Run** `go test -race -timeout 900s ./internal/backup/ ./internal/db/...` — FAIL.
- [ ] **Step 3: Implement** `catalog.go`, `dump.go`, `wipe.go`, `restore.go`, `MigrateTo`/`SchemaVersion`, `FreshDatabase`. If migrations themselves cannot run as the non-superuser owner, stop and report DONE_WITH_CONCERNS with the failing statement rather than weakening the role.
- [ ] **Step 4: Run** the same command — PASS; lint and gofmt clean.
- [ ] **Step 5: Commit** `feat(backup): dump and restore the database without pg_dump`

### Task 3: Storage back ends and retention

**Files:**
- Create: `internal/backup/storage.go`, `internal/backup/storage_dir.go`, `internal/backup/storage_s3.go`, `internal/backup/naming.go`, `internal/backup/retention.go`
- Test: `internal/backup/storage_dir_test.go`, `internal/backup/storage_s3_test.go` (MinIO via testcontainers generic container `minio/minio`, pinned tag), `internal/backup/naming_test.go`, `internal/backup/retention_test.go`
- Modify: `go.mod`, `go.sum` (`github.com/minio/minio-go/v7`)

**Interfaces:**
- Produces:
  - `type Storage interface { Put(ctx context.Context, name string, r io.Reader) (int64, error); Open(ctx context.Context, name string) (io.ReadCloser, error); List(ctx context.Context) ([]Object, error); Delete(ctx context.Context, name string) error; Describe() string }`
  - `type Object struct { Name string; Size int64; Header Header; HeaderErr error }` — `List` reads each object's header (first 4 KiB) so listing never decrypts.
  - `func NewDirStorage(dir string) Storage` — writes to `<name>.partial` and renames on success; lists only `*.pxbak`.
  - `type S3Config struct { Endpoint, Region, Bucket, Prefix, AccessKey, SecretKey string; PathStyle bool }`; `func NewS3Storage(cfg S3Config) (Storage, error)`; `func S3ConfigFromEnv(getenv func(string) string, bucketURL string) (S3Config, error)` reading `PULSIX_BACKUP_S3_ENDPOINT`, `PULSIX_BACKUP_S3_REGION`, `PULSIX_BACKUP_S3_ACCESS_KEY`, `PULSIX_BACKUP_S3_SECRET_KEY`, `PULSIX_BACKUP_S3_PATH_STYLE` and parsing `s3://bucket/prefix`.
  - `func ObjectName(createdAt time.Time, kind string) string` → `pulsix-20261102T030000Z-scheduled.pxbak` (UTC); `func ParseObjectName(name string) (time.Time, string, bool)`.
  - `type Retention struct { Daily, Weekly, Monthly int }`; `func Prune(objs []Object, r Retention, loc *time.Location) (remove []string)` — grandfather-father-son over **scheduled** objects only, by local calendar day / ISO week / month in `loc`, keeping the newest object of each kept bucket; never returns a manual object; never returns the newest scheduled object.
  - `func Probe(ctx context.Context, s Storage) error` — put, list, open, delete a `pulsix-probe-<random>.tmp` object.

- [ ] **Step 1: Write the failing tests**
  - `TestObjectNameRoundTrip`; `ParseObjectName("foo.txt")` → false.
  - `TestDirStoragePutListOpenDelete`: put two backups (real headers via `WriteHeader`), `List` returns both with headers, sorted newest first; `Open` returns the bytes; `Delete` removes one; a stray `x.pxbak.partial` and `notes.txt` are not listed.
  - `TestDirStorageFailedPutLeavesNoObject`: a reader that errors halfway → error, `List` empty, no `.partial` left.
  - `TestS3StoragePutListOpenDelete` against MinIO with `PathStyle: true` and prefix `pulsix/`; skip with `t.Skip` only when Docker is unavailable, same rule as testdb.
  - `TestS3ConfigFromEnv`: `s3://firma-backups/pulsix` → bucket `firma-backups`, prefix `pulsix/`; missing access key → error naming `PULSIX_BACKUP_S3_ACCESS_KEY`.
  - `TestPruneKeepsDailyWeeklyMonthly`: 120 daily scheduled objects ending 2026-11-02 in Europe/Berlin, `Retention{7,4,6}` → kept = newest 7 days + newest of the 4 newest ISO weeks + newest of the 6 newest months (assert exact names); `remove` contains no manual object of 3 interleaved manual ones.
  - `TestPruneZeroRetentionStillKeepsNewest`: `Retention{0,0,0}` → everything but the newest scheduled object.
  - `TestProbeAgainstDirStorage`: succeeds; read-only dir → error.
- [ ] **Step 2: Run** `go test -race -timeout 900s ./internal/backup/` — FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** — PASS; lint, gofmt.
- [ ] **Step 5: Commit** `feat(backup): folder and S3-compatible storage with retention`

### Task 4: Settings, attempts, schedule and the running manager

**Files:**
- Create: `internal/db/migrations/00075_backup_runs.sql`, `internal/db/queries/backups.sql`, `internal/backup/service.go`, `internal/backup/settings.go`, `internal/backup/schedule.go`, `internal/backup/ticker.go`, `internal/backup/store_sql.go`, `internal/app/backup_ports.go`
- Modify: `internal/app/app.go` (wire service and ticker; set `application_name=pulsix-manager` on the app pool), `internal/db/db.go` (`func WithApplicationName(url, name string) (string, error)`), regenerated `internal/db/gen/*`
- Test: `internal/backup/service_test.go` (fakes), `internal/backup/schedule_test.go`, `internal/backup/store_sql_test.go`, `internal/backup/restore_attempts_test.go`, `internal/app/backup_ports_test.go`

**Interfaces:**
- Consumes: Tasks 1–3.
- Produces:
  - Table `backup_runs (id uuid pk default gen_random_uuid(), kind text check in ('scheduled','manual'), slot timestamptz null, started_at timestamptz not null, finished_at timestamptz, state text not null check in ('running','succeeded','failed','abandoned'), object_name text, size_bytes bigint, error text, actor_id uuid references users(id) on delete set null)`, unique index on `slot` where `kind = 'scheduled'`; no org_id, no RLS, grants to `pulsix_app` like `instance_settings` (00009).
  - `type Settings struct { Destination string /* "folder" | "s3" */; S3 S3Settings; Enabled bool; TimeOfDay string /* "HH:MM" */; TimeZone string /* IANA */; Retention Retention; IncludeMetrics bool }`, `S3Settings{Endpoint, Region, Bucket, Prefix, AccessKey string; SecretKeySet bool; PathStyle bool}`; defaults: folder, disabled, `03:00`, `UTC`, `Retention{7,4,6}`, metrics included. Stored in `instance_settings` key `"backup"` as JSON with the secret sealed by the master-key `crypto.Box` (AAD `"backup.s3.secret"`), like SMTP.
  - `type Service struct`; `func NewService(store Store, box *crypto.Box, masterKey [32]byte, dump DumpFunc, storageFor func(Settings, secret string) (Storage, error), mailer AdminMailer, version string, now func() time.Time, log *slog.Logger) *Service`
  - methods: `Settings(ctx) (Settings, error)`; `SaveSettings(ctx, in Settings, secretKey *string /* nil keeps stored */) error` (`ErrInvalid` for bad time, zone, destination, negative retention, missing S3 fields); `Test(ctx, in Settings, secretKey *string) error`; `List(ctx) ([]Entry, error)` (storage objects merged with the last 50 attempts; an attempt whose object exists is shown once); `StartManual(ctx, actor uuid.UUID) (uuid.UUID, error)` (`ErrBusy` while an attempt is `running` and started less than 6 h ago); `Delete(ctx, name string) error` (`ErrNotFound`); `Open(ctx, name) (io.ReadCloser, int64, error)`; `RunDue(ctx) error` (one tick); `AbandonStale(ctx) error` (marks `running` attempts older than 6 h, and all `running` attempts at manager start, `abandoned`).
  - `type Entry struct { Name string; CreatedAt time.Time; Kind string; Actor *uuid.UUID; PulsixVersion string; Size int64; State string; Error string }`
  - `type AdminMailer interface { SendToInstanceAdmins(ctx context.Context, subject, body string) error }` — implemented in `internal/app/backup_ports.go` over `mail.SMTP.SendMany` and the instance-admin user list; a not-configured SMTP is a no-op.
  - `func SlotFor(now time.Time, timeOfDay string, loc *time.Location) time.Time` — the most recent local occurrence of `timeOfDay` at or before `now`; on a DST gap the slot moves to the first valid instant after it; on a DST overlap the first occurrence wins.
  - `func NewTicker(s *Service, every time.Duration) *Ticker` with `Run(ctx)`; `every` = 1 min. A tick claims the current slot by inserting a `scheduled` attempt with `ON CONFLICT DO NOTHING` (the unique index is the multi-manager lock), runs the dump, records the result, prunes after success only, and mails instance admins on failure (subject `Pulsix backup failed`, body with time, destination and error; English, server text).
  - `app.Build` wires the service, calls `AbandonStale` once at start, starts the ticker with `startLoop`, and stops it in `Close`; the app pool URL gets `application_name=pulsix-manager`.

- [ ] **Step 1: Write the failing tests**
  - `TestSlotForDailyBerlinAcrossDST` (Review Focus 4): for every hour from 2026-03-27 to 2026-04-01 and 2026-10-23 to 2026-10-28 in Europe/Berlin with `"03:00"` and with `"02:30"`, the set of distinct slots has exactly one per local day; for `"02:30"` on 2026-03-29 the slot is 03:00 CEST.
  - `TestSaveSettingsKeepsStoredSecret`: save with secret `"s1"`, save again with `nil` → stored secret still `"s1"`; returned `Settings.S3.SecretKeySet` true; the stored JSON contains no `"s1"`.
  - `TestSaveSettingsRejectsInvalid`: table — `TimeOfDay "25:00"`, `TimeZone "Mars/Base"`, destination `"ftp"`, `Retention{-1,0,0}`, s3 without bucket → `ErrInvalid`.
  - `TestStartManualRefusesWhileRunning`: fake store with a `running` attempt 1 min old → `ErrBusy`.
  - `TestStaleRunningAttemptDoesNotBlock` (Review Focus 5): a `running` attempt 7 h old → `StartManual` succeeds and the old row is `abandoned`; `AbandonStale` at start marks a 1-min-old `running` row `abandoned`.
  - `TestRestoreMarksRunningAttemptsAbandoned` (`restore_attempts_test.go`, integration): a dump taken while a `running` attempt exists restores with that row `abandoned`.
  - `TestRunDueClaimsSlotOnce` (integration, two services on one DB): both call `RunDue` for the same slot → one dump, one `succeeded` attempt.
  - `TestRunDuePrunesOnlyAfterSuccess`: fake dump failing → no `Delete` on storage, mailer called once with subject `Pulsix backup failed`; succeeding → `Prune`'s names deleted.
  - `TestRunDueDisabledDoesNothing`.
  - `TestListMergesAttemptsAndObjects`: one object from a manual attempt by actor X, one failed attempt without object, one object with no attempt (from another instance) → three entries with the right kinds, actor and states.
  - `TestAppPoolSetsApplicationName` (`internal/app`): `SELECT application_name FROM pg_stat_activity WHERE pid = pg_backend_pid()` through the built app pool → `pulsix-manager`.
- [ ] **Step 2: Run** `go test -race -timeout 900s ./internal/backup/ ./internal/app/` — FAIL.
- [ ] **Step 3: Implement** migration, queries (sqlc generate), store, service, schedule, ticker, ports, wiring.
- [ ] **Step 4: Run** — PASS; then `make test`; lint, gofmt.
- [ ] **Step 5: Commit** `feat(backup): scheduled backups, settings and attempts in the manager`

### Task 5: The commands, worker re-enrolment and the compose file

**Files:**
- Create: `internal/app/backupcmd.go` (`RunBackup`, `RunBackupKey`, `RunRestore`), `internal/app/managers.go` (`func ManagersConnected(ctx context.Context, pool *pgxpool.Pool) (int, error)`)
- Modify: `cmd/manager/main.go` (dispatch `backup`, `backup key`, `restore`; usage text), `internal/app/workeradd.go` (`WorkerAddOptions.Replace bool`, flag `--replace`), `deploy/compose/docker-compose.yml` (volume `pulsix-backups` at `/var/lib/pulsix/backups` on `manager`; `worker-enroll` gets `--replace`), `deploy/docker/Dockerfile.manager` (create `/var/lib/pulsix/backups` owned by 65532), `deploy/compose/README.md` (backup/restore section)
- Test: `cmd/manager/main_test.go`, `internal/app/backupcmd_test.go`, `internal/app/workeradd_test.go`

**Interfaces:**
- Consumes: Tasks 1–4.
- Produces (command lines, exact):
  - `pulsix-manager backup [--to <dir>|s3://bucket/prefix] [--without-metrics]` — without `--to`, the configured destination (settings from the database); prints the object name and size; records a `manual` attempt with no actor.
  - `pulsix-manager backup key` — prints `DeriveKey(masterKey)` and one line explaining it decrypts backups with the stock age tool (`age -d`).
  - `pulsix-manager restore --list [--from <dir>|s3://bucket/prefix]` — table: name, created (UTC), kind, Pulsix version, size, metrics yes/no.
  - `pulsix-manager restore [--from …] [--replace] [--yes] <name>|-` — `-` spools stdin to a temp file in `os.TempDir()` first, and then `--replace` requires `--yes`; without `--yes` on a terminal it asks to type `restore`. Default `--from` is `/var/lib/pulsix/backups`. Refuses with exit 1 and "stop the manager first" when `ManagersConnected > 0` (sessions with `application_name = 'pulsix-manager'` other than its own). Prints backup time, version and per-table row counts on success, then "Start the stack; workers and agents reconnect on their own."
  - The commands open the database with `application_name=pulsix-manager-cli`.
  - `worker add --replace`: when the name is taken in that org, revoke and delete the existing worker of that name, then enrol as usual.
- [ ] **Step 1: Write the failing tests**
  - `TestDispatchKnowsBackupAndRestore` (`cmd/manager`): `backup --help`, `restore --help`, `backup key --help` exit 0/1 with usage, never start a server; unknown `backup foo` → exit 2.
  - `TestRunBackupToDirThenRestoreList` (`internal/app`, testdb + temp dir): `RunBackup --to dir` writes one `.pxbak`; `RunRestore --list --from dir` prints its name and `manual`.
  - `TestRunRestoreFromStdinRequiresYesWithReplace`: stdin source + `--replace` without `--yes` → error, target untouched.
  - `TestRunRestoreRefusesWhileManagerConnected`: open a pool with `application_name=pulsix-manager` on the target → exit 1, message contains `stop the manager first`.
  - `TestRunRestoreFromStdinIntoFreshDatabase`: pipe a backup file into `RunRestore -` with `FreshDatabase` → success, rows present.
  - `TestRunBackupKeyPrintsDerivedKey`: output contains `DeriveKey(cfg.MasterKey)`.
  - `TestWorkerAddReplaceReenrolsSameName`: existing worker `worker-1` → without `--replace` the existing `ErrNameTaken` error; with `--replace` a new serial is registered and the old one is revoked.
- [ ] **Step 2: Run** `go test -race -timeout 900s ./cmd/manager/ ./internal/app/` — FAIL.
- [ ] **Step 3: Implement.** `docker compose config` on `deploy/compose/docker-compose.yml` with dummy env must still parse.
- [ ] **Step 4: Run** — PASS; `make test`; lint, gofmt.
- [ ] **Step 5: Commit** `feat(manager): backup and restore commands, and re-enrolling a restored worker`

### Task 6: API

**Files:**
- Create: `internal/api/backup_handlers.go`, `internal/api/backup_download.go`
- Modify: `internal/api/api.go` (`Deps.Backups`, register next to the locale settings routes), `internal/api/openapi_doc.go` if it lists tags, `web/src/api/schema.d.ts` (regenerated)
- Test: `internal/api/backup_handlers_test.go`

**Interfaces:**
- Consumes: Task 4's `Service` through an interface in `internal/api` (`type BackupService interface { Settings…; SaveSettings…; Test…; List…; StartManual…; Delete…; Open… }`).
- Produces (all `requireInstanceAdmin`, so session-only and never a bearer token; audited with `withAudit`):
  - `GET /api/v1/admin/backups/settings` → `{destination, s3:{endpoint, region, bucket, prefix, access_key, secret_key_set, path_style}, enabled, time_of_day, time_zone, retention:{daily, weekly, monthly}, include_metrics}`
  - `PUT /api/v1/admin/backups/settings` body as above plus optional write-only `s3.secret_key` (absent keeps) → 204; audit `updateBackupSettings`/`backup_settings`; 422 on `ErrInvalid`.
  - `POST /api/v1/admin/backups/settings/test` same body → 204 or 422 with the storage error as detail; not audited.
  - `GET /api/v1/admin/backups` → `{items:[{name, created_at, kind, actor_id, actor_name, pulsix_version, size_bytes, state, error}], next_run_at, destination_label}`.
  - `POST /api/v1/admin/backups` → 202 `{attempt_id}`; 409 on `ErrBusy`; audit `createBackup`/`backup`.
  - `GET /api/v1/admin/backups/{name}/download` — raw chi handler (huma cannot stream), same instance-admin + interactive-session guard as the huma routes, `Content-Disposition: attachment`, `Content-Type: application/octet-stream`; name must match `ParseObjectName` (else 404, no path traversal); audit `downloadBackup`.
  - `DELETE /api/v1/admin/backups/{name}` → 204; 404 unknown; audit `deleteBackup`.
- [ ] **Step 1: Write the failing tests** (apitest): instance admin gets/puts settings and the secret never appears in GET; a non-admin member gets 403 on every route; a bearer token gets 401/403 on every route; PUT with `time_of_day "25:00"` → 422; POST twice quickly → 202 then 409; download streams the exact bytes with the attachment header and `../etc/passwd` → 404; DELETE then GET list no longer has it; each audited action appears in the audit log with the right action name.
- [ ] **Step 2: Run** `go test -race -timeout 1800s ./internal/api/ -run Backup` — FAIL.
- [ ] **Step 3: Implement**; regenerate `schema.d.ts`.
- [ ] **Step 4: Run** — PASS; `make test`; lint, gofmt.
- [ ] **Step 5: Commit** `feat(api): instance backups`

### Task 7: The Backups tab

**Files:**
- Create: `web/src/features/instance/BackupsPage.tsx`, `web/src/features/instance/BackupSettingsForm.tsx`, `web/src/features/instance/BackupList.tsx`, `web/src/features/instance/RestoreDialog.tsx`, `web/src/api/hooks/useBackups.ts`, `web/src/i18n/en/backups.ts`, `web/src/i18n/de/backups.ts`
- Modify: `web/src/router.tsx` (route `/settings/backups` beside `/settings/language`), `web/src/features/settings/SettingsTabs.tsx` (tab for instance admins), `web/src/i18n/en/index.ts`, `web/src/i18n/de/index.ts`
- Test: `web/src/features/instance/BackupsPage.test.tsx`, `web/src/features/instance/BackupsPage.de.test.tsx`, `web/src/features/instance/RestoreDialog.test.tsx`

**Interfaces:**
- Consumes: Task 6's endpoints via the generated types.
- Produces: the page of the approved mockup — status strip (last backup with time, size and result; next run; destination; *Back up now*), *Destination* (folder `/var/lib/pulsix/backups` or S3 fields, secret field shows "stored" when `secret_key_set`, *Test connection*), *Schedule and retention* (enabled, time, time zone defaulting to `Intl.DateTimeFormat().resolvedOptions().timeZone` when nothing is saved, daily/weekly/monthly, include metrics), the master-key notice, and the list (time via `useFormat`, kind with actor name, version, size, state with error text; *Download*, *Delete* with confirm, *Restore…*). The list refetches every 5 s while any entry is `running`.
  - `RestoreDialog` shows two copyable commands for the row: `docker compose run --rm manager restore --replace <name>` (folder) or `docker compose run --rm -e PULSIX_BACKUP_S3_ACCESS_KEY -e PULSIX_BACKUP_S3_SECRET_KEY -e PULSIX_BACKUP_S3_ENDPOINT manager restore --from s3://<bucket>/<prefix> --replace <name>` (S3), and for a downloaded file `docker compose run --rm -T manager restore --replace --yes - < <name>`; plus "The master key of the instance that made this backup must be set in PULSIX_MASTER_KEY."
- [ ] **Step 1: Write the failing tests**: renders settings from a mocked GET; saving sends PUT without `secret_key` when the field is untouched and with it when typed; *Test connection* shows the server's 422 detail; *Back up now* posts and shows the new running entry; a failed entry shows its error; *Restore…* for an S3 destination shows the `s3://` command with the bucket and prefix; the German test renders the page title "Backups", the button "Jetzt sichern" and the restore dialog in German.
- [ ] **Step 2: Run** `vitest run src/features/instance/` — FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `vitest run`, `eslint .`, `tsc -b` — all clean.
- [ ] **Step 5: Commit** `feat(web): a Backups tab for instance administrators`

### Task 8: Docs and backlog

**Files:**
- Modify: `docs/BACKLOG.md` (7.2 done with a summary; 3.22 done, superseded by 1.41)
- noix-docs (separate repo, worktree under the scratchpad from `origin/pulsix`, local branch `docs-pulsix-backups`, **not pushed**; never touch `~/_dev/noix-docs` itself): new `src/content/docs/pulsix/admin/operations/backups.md` (set up in the web interface, the commands, keeping the master key, restore walk-through on a new server, restore from a downloaded file), and a pointer from the installation page. No tech-stack names.

- [ ] **Step 1:** Update `docs/BACKLOG.md` with the row closer script `close_planned.py 7.2 "<summary>"` and mark 3.22 done.
- [ ] **Step 2:** Write the noix-docs page and pointer; check that the commands match Task 5's usage text verbatim.
- [ ] **Step 3: Commit** in pulsix `docs: close backlog 7.2 and 3.22`; in noix-docs `docs(pulsix): back up and restore Pulsix`.
