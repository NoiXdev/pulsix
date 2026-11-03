# Open v1.0 items 7.3, 7.37, 7.47, 7.48, 7.63 — design

Date: 2026-11-03. Owner decisions taken in chat the same day: 7.47 adopts a newer certificate after a restore; 7.3 is documentation plus guard rails; 7.37 is prepared (environment and workflow), the secrets move is the owner's; 7.4 is closed (the end-user documentation is docs.noix.dev, `NoiXdev/noix-docs` branch `pulsix`); 7.5 and 7.6 stay open until a release.

## 1. Backlog 7.47 — adopt a certificate issued after the restored backup

### Problem
The manager accepts a client certificate only when the database knows its serial (`internal/grpcsrv/auth.go:201-240`). A restore rolls the database back, so every agent and worker certificate issued after the backup — agent renewals at two-thirds of 90 days, worker renewals at one third left — is unknown and refused with "unknown or revoked certificate". With a two-month-old backup that is most of the fleet.

### Decision
After a restore, the manager adopts such a certificate on first use when all of these hold:

1. The TLS layer and `CA.VerifyClient` already accepted it: signed by the instance CA, unexpired, client EKU, valid SANs.
2. A restore marker exists (new, see below) and the certificate's `NotBefore` lies strictly between the restored backup's creation time and the restore time. Certificates issued before the backup are either known or were never registered; certificates issued after the restore are always registered by enrollment or renewal. Outside that window nothing is adopted.
3. The identity it names exists and is not revoked:
   - worker: the certificate's org and CN name resolve to exactly one row in `workers` (UNIQUE(org_id, name)) with `status = 'active'`, and its zone SAN equals that row's zone;
   - agent: the org and CN name resolve to exactly one `host_agents` row (agent names are not unique; zero or several matches refuse).
4. Its `NotBefore` is later than every certificate the database has for that worker/host (`not_before`, falling back to `created_at` where `not_before` is null). An older or equal certificate is never adopted.
5. Agents only: the certificate carries the same key as the host's newest registered certificate. An agent keeps its key across renewals (`internal/agent` `LoadOrGenerateKey`), and every agent certificate row records the hex SHA-256 of the leaf's SubjectPublicKeyInfo in `agent_certificates.public_key_sha256` (migration 00077, written by enrollment, renewal and adoption). A newest certificate without a fingerprint (a row from before 00077, e.g. after restoring an older backup) refuses. This keeps a certificate of a revoked agent from moving onto a new host enrolled under the same agent name.

Adoption registers the serial exactly like a renewal does: a worker certificate row is inserted (older serials stay valid until they expire, as today); for an agent, `host_agents.cert_serial` is updated, older certificates of that host are revoked and the new one inserted (the existing `register` path). Each adoption writes an audit event (`worker.certificate_adopted` / `agent.certificate_adopted`, actor the worker/agent, summary naming the serial) and an info log line. The call then proceeds as if the serial had been known.

A worker or agent removed or revoked after the backup has no row or a revoked row in the restored database only if the removal happened before the backup; a removal after the backup is undone by the restore like every other revocation (accepted in 7.54). Adoption does not widen that: it only accepts certificates the instance itself issued after the backup.

### Restore marker
`backup.Restore` writes, in the load transaction after the copy, an `instance_settings` row `restore` = `{"backup_created_at": <manifest header CreatedAt>, "restored_at": now()}`, replacing any earlier one. A restore without that row (none ever happened) adopts nothing. The marker is not read from the backup; the load writes it fresh. A marker missing either time, or whose restore is not after its backup, counts as none; one that does not decode is an error.

`internal/restoremark` owns the marker: the key, the JSON type, the `Window` with its strict `Contains`, and the fail-closed reader. `internal/backup` writes through it; `internal/workers` and `internal/agents` read through it.

### Where it lives
- `internal/grpcsrv/auth.go`: on ErrNotFound from the serial lookup, call an `Adopt` port before refusing.
- `internal/workers` and `internal/agents` services: `AdoptCertificate(ctx, cert *x509.Certificate, id ca.Identity) (…, error)` with the rules above; their SQL stores do the lookups and registration in one transaction.
- The restore marker read/write: `internal/restoremark` (see above), written by `internal/backup`, read by both registries' stores (`Store.RestoreWindow`) before any lock is taken.
- Restore output, `deploy/compose/README.md`, end-user docs: replace "enroll them again" with "they reconnect on their own; a worker or agent whose certificate was issued before the backup and expired since must be enrolled again".

## 2. Backlog 7.3 — upgrade path and migrations policy

### Guard: refuse a database newer than the binary
Today the manager starts against a schema newer than it knows (goose finds nothing to apply). New: before migrating, compare `ReadSchemaVersion` with `LatestVersion()`; if the database is newer, refuse with a clear error ("the database schema is at version N, this Pulsix knows up to M: run the newer version, or restore a backup taken by this one"). Applies to the manager start (`internal/app/app.go:203`), `worker add` (`internal/app/workeradd.go:108`) and `restore`'s final `Migrate` (already guarded up front by `ErrBackupTooNew`). The `cli` backup command does not migrate and is unaffected.

### Version skew: shown, not enforced
The architecture spec (2026-09-10, lines 877-882) says the manager accepts the same and the previous minor version. Rejecting older components would cut a fleet off mid-upgrade, so this design shows instead of refusing:
- A pure function `versioncompat.Status(manager, component string) Status` with `Current`, `PreviousMinor`, `UpdateRequired` (older than the previous minor, or a different major), `Newer` (newer than the manager), `Unknown` (empty, `dev`, unparseable on either side).
- The worker and agent API responses gain `version_status`; the Workers page, Agents page and host agent card show a badge for `UpdateRequired` and `Newer` (en + de).
- The manager logs one warning per component when it first sees an `UpdateRequired` or `Newer` version in a process lifetime.

### Policy and documentation
- `docs/upgrades.md` (internal): migrations are additive; a released migration is never edited; renames and drops go expand-then-contract across two releases; goose `down` is never used; the manager refuses a newer schema; the version-skew rule and the roll order (manager, then workers, then agents); a downgrade is a restore of a backup taken before the upgrade.
- `deploy/compose/README.md` "Upgrading": take a backup first (`docker compose run --rm cli backup`), roll order, no downgrade except by restore, what the badges mean.
- End-user docs in noix-docs: the same, in the upgrade/operations page.

## 3. Backlog 7.48 — backup robustness

1. **Stall guard.** The transfer is bounded by progress: if no byte reaches the destination for 10 minutes (`stallTimeout`), the run is cancelled and recorded as failed with "stalled: no progress for 10m". Implemented as a watchdog on the pipe between dump and `Put`, cancelling the run's context; the lock is released as on any failure.
2. **Mail when a day had only interrupted attempts.** When the ticker finds that a past scheduled slot has attempts and all of them are `abandoned` and none succeeded, it mails the instance admins once ("the scheduled backup of <date> did not complete; the manager was stopped during it"). Once is recorded with an additive nullable `backup_runs.notified_at` set on the slot's newest attempt; a slot with a mailed attempt is not mailed again.
3. **Stale `*.partial` files.** The folder destination removes every `*.partial` file in its directory at the start of each backup run, while the run holds the backup lock (so no other backup is writing one).

## 4. Backlog 7.63 — leftovers

1. The restore output names agent and worker enrollment tokens among what is at the backup's state (and that a used or withdrawn token is redeemable again until it expires).
2. `internal/rules/due_sql.go` `runHost`: the commit-failure and panic log lines say "postponing" only when the batch context is still live; otherwise they say the host is left due (reuse the ticker's `failedHostNext` wording).
3. The agent drops a check result whose check_key, item or metric is not valid UTF-8 in `Loop.handleResults` before buffering or sending, with one warning per dropped result kind per minute at most; the session's drop of an unencodable chunk stays as the backstop.
4. Hygiene: the displaced `panicStore` doc comment in `internal/rules/ticker_test.go`; the oversize-claim test asserts the computed payload size, not a regex the limit also matches; the stale "distinct valid keys" comments in the facts tests; the at-cap tuple test sends a label update as its comment claims.

## 5. Backlog 7.37 — release environment (prepared)

- Create the GitHub environment `release` on `NoiXdev/pulsix` with a deployment policy that allows only tags matching `v*` (`gh api`), done by the controller with the owner's go-ahead of 2026-11-03.
- `release.yml`: the publishing job declares `environment: release`; the guard's CI query is narrowed to `event=push&branch=main`.
- `tag.yml` keeps `secrets: inherit` (pinned) until the shared workflow declares its secrets (backlog 1.38); the row says so.
- Left to the owner and recorded in the row: move `DOCKERHUB_USERNAME`/`DOCKERHUB_TOKEN` into the environment (or narrow the organization secrets' repository access), add `HARBOR_USERNAME`/`HARBOR_PASSWORD` there, optionally add a required reviewer.

## Non-goals
Rejecting old worker/agent versions; adopting certificates without a restore marker; a release (7.5) or making the repository public (7.6).
