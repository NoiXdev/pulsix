# Security Audit Run 3 Follow-ups Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix every confirmed finding of security audit run 3 and harden the leads and deferred units the owner decided on (backlog 7.49–7.62).

**Architecture:** Independent, small fixes at the last trusted decision point of each path: an extra session gate on two REST routes, a MAC over the backup header that the retention and listing honour, tighter input contracts on agent-reported data, a per-rule window cap plus a per-host time budget in the rule ticker, and restore steps that recheck and clean up. No new subsystem; one additive migration at most.

**Tech Stack:** Go 1.27 (chi + huma, pgx v5, sqlc, goose), PostgreSQL 18 + TimescaleDB, React/TypeScript SPA (Vitest), gopsutil v4, ansible-core 2.21 in the worker image.

**Spec:** `docs/BACKLOG.md` section "v1.0 · security audit run 3 follow-ups" (rows 7.49–7.62, each with the owner's decision of 2026-11-03) and `docs/security/2026-11-03-audit-run-3/` (`findings.json` holds the trace, evidence and remediation of every record; `REPORT.md` the summary).

## Global Constraints

- Everything in the repository is English: code, comments, commit messages, docs.
- Conventional Commits; every commit ends with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. Commit only the task's own files.
- TDD: failing test first, watched failing, then the minimal fix.
- Tests run per package with a timeout, e.g. `go test -race -count=1 -timeout 900s ./internal/backup/`; `internal/api` needs `-timeout 1800s`. Never run one whole-module `go test ./...`. The full suite is `make test`.
- Before every commit: `golangci-lint run` (0 issues) and `gofmt -s -l .` (no output).
- SPA: `PATH=$HOME/.nvm/versions/node/v24.14.0/bin:$PATH`, then in `web/`: `npx vitest run <file>`, `npx eslint <files>`, `npx tsc -b`. No prettier.
- Migrations are additive only (new files; never edit a released one). sqlc output is regenerated with `make sqlc`, the OpenAPI types with the repo's existing generate target for `web/src/api/schema.d.ts` (look it up in the Makefile; do not hand-edit generated files).
- Database tests use `internal/db/testdb` (testcontainers). Only touch Docker containers the tests start themselves; about 50 unrelated containers run on this machine.
- User-visible SPA strings exist in English and German (`web/src/i18n/en|de/*.ts`).
- Backlog rows and the end-user docs in `NoiXdev/noix-docs` (restore ends sessions, enrollment tokens need a browser session, the compose snippet) are updated by the controller after the branch, not by tasks.

## Review Focus

1. A backup written before this branch has no header MAC: it must still list, download and restore, show as unverified, and never be pruned (Task 4, Task 5 tests).
2. A legitimate scheduled backup whose name stamp and header time differ only below the second must count as verified (Task 4 test `TestMarkVerifiedAcceptsSubSecondCreatedAt`).
3. An existing session-authenticated admin must still create worker and agent enrollment tokens from the SPA; only bearer tokens are refused (Task 1 tests).
4. A host whose stored facts are already above the new 128-key cap must not lose its next push; the push replaces the set (Task 8 test).
5. A host that already has exactly the cap of check tuples keeps updating those tuples (Task 9 test).

---

### Task 1: Enrollment-token creation needs an interactive session (7.49)

**Files:**
- Modify: `internal/api/worker_handlers.go` (the `POST /api/v1/orgs/{org}/worker-enrollments` handler, ~line 212)
- Modify: `internal/api/agent_handlers.go` (the `POST /api/v1/orgs/{org}/agents/tokens` handler, ~line 222)
- Test: `internal/api/worker_handlers_test.go`, `internal/api/agent_handlers_test.go` (or the existing test files for these routes)

**Interfaces:** Consumes the existing `requireInteractiveSession(ctx)` (`internal/api/auth_handlers.go:265`), exactly as `internal/api/token_handlers.go:107` uses it, called before `requireOrg`.

- [ ] **Step 1: Write the failing tests** `TestWorkerEnrollmentCreateRefusesBearerToken` and `TestAgentTokenCreateRefusesBearerToken`: an admin-role `pxt_` token gets 403 with the same error `requireInteractiveSession` returns elsewhere; the same token still gets 200 on the list route and on the withdraw route of each resource; an MFA-complete admin session still gets 201 on both creates.
- [ ] **Step 2: Run them** (`go test -race -count=1 -timeout 1800s -run 'TestWorkerEnrollmentCreateRefusesBearerToken|TestAgentTokenCreateRefusesBearerToken' ./internal/api/`), expect the 403 assertions to fail.
- [ ] **Step 3: Add the gate** in both create handlers, before `requireOrg`, with a comment that names the "credentials do not mint credentials" rule and backlog 7.49 in the style of `token_handlers.go:100-106`.
- [ ] **Step 4: Run the tests and the package's existing worker/agent route tests**; all pass.
- [ ] **Step 5: Commit** `fix(api): refuse bearer tokens when minting worker and agent enrollment tokens`.

### Task 2: A mount point that is not valid UTF-8 cannot poison the agent stream (7.52)

**Files:**
- Modify: `internal/agent/checks/disk.go` (`reportableItem`, line 37, and the plugin's skip message)
- Modify: `internal/checks/samples.go` (`withinContract`, ~line 176)
- Modify: `internal/agent/session.go` (`activateSender` ~line 321 and the send-failure path ~line 363-385)
- Test: `internal/agent/checks/disk_test.go`, `internal/checks/samples_test.go`, `internal/agent/session_test.go`

- [ ] **Step 1: Write the failing tests.**
  - `TestReportableItemRefusesInvalidUTF8`: `reportableItem("/home/u/q\"\xffz")` is false; `reportableItem("/home/u/ok")` stays true.
  - `TestDiskCollectSkipsInvalidUTF8Mount`: a mountinfo fixture with a mount point containing `"` and byte `0xFF` (gopsutil leaves it raw) produces no observation for it, and every observation `Collect` returns marshals with `proto.Marshal`.
  - `TestWithinContractRefusesInvalidUTF8`: an observation whose `Item`, `CheckKey` or `Metric` is not valid UTF-8 returns `ErrInvalidInput`.
  - `TestUnencodableChunkIsDroppedNotRebuffered`: a buffered result whose item is invalid UTF-8 is dropped (logged) by the replay instead of re-buffered; the heartbeat after it is sent, and the buffer does not grow across three reconnects (use the bufconn stream pattern of the existing session tests).
- [ ] **Step 2: Run them** per package; expect failures.
- [ ] **Step 3: Implement.** `reportableItem` and `withinContract` add `utf8.ValidString` (the `withinContract` error text names valid UTF-8). In the session, a send error that is a client-side encode failure (`status.Code(err) == codes.Internal` with the marshal message, before anything reached the wire) drops the offending chunk with a warning instead of returning it to the buffer; any other error keeps today's re-buffer behaviour.
- [ ] **Step 4: Run the three packages' tests**; all pass.
- [ ] **Step 5: Commit** `fix(agent,checks): refuse mount points that are not valid UTF-8 and drop chunks the agent cannot encode`.

### Task 3: Redaction covers Python repr spellings (7.53)

**Files:**
- Modify: `internal/worker/runner/redact.go` (`newRedactor`, line 51)
- Test: `internal/worker/runner/redact_test.go`

- [ ] **Step 1: Write the failing test** `TestRedactorCoversPythonReprSpelling` with the two captured `runner_on_ok` event lines from the audit (secret `Pa"ss'w0rd-9`; `res.msg` = `cfg: {'p': 'Pa"ss\'w0rd-9'}`, stdout containing `Pa\"ss\\'w0rd-9`, see `docs/security/2026-11-03-audit-run-3/findings.json`, record `internal/worker/runner/redact.go:newRedactor:python-repr-quote-escape`). After `eventPayload`, neither the secret nor any of its spellings appears and `[redacted]` does. Add table cases: a secret with only `'`, only `"`, a backslash, and a non-printable byte, each redacted in repr form.
- [ ] **Step 2: Run it**, expect the both-quotes case to fail.
- [ ] **Step 3: Add a `reprSpelling(secret string) string`** that reproduces CPython's `repr()` string body for the chosen quote (single quotes unless the string contains `'` and no `"`; inside single quotes `'` becomes `\'`; `\\`, `\n`, `\r`, `\t`, and `\xNN`/`\uNNNN` for non-printables), and add it plus its JSON-escaped forms to the redactor's spellings exactly where the existing JSON spellings are added.
- [ ] **Step 4: Run the package tests**; pass.
- [ ] **Step 5: Commit** `fix(worker): redact secrets in Python repr spelling`.

### Task 4: Authenticated backup headers (7.50 part 1, 7.51 part 1)

**Files:**
- Modify: `internal/backup/format.go` (`Header`, `WriteHeader`, `ReadHeader`)
- Modify: `internal/backup/crypt.go` (key derivation)
- Modify: `internal/backup/dump.go` (sign before writing header and manifest)
- Modify: `internal/backup/storage.go` (`Object`)
- Modify: `internal/backup/service.go` (`NewService` gains the header key), `internal/backup/listing.go` (mark objects, `Entry.Verified`)
- Modify: `internal/app/backup_ports.go`, `internal/app/backupcmd.go` (pass the key)
- Modify: `internal/api/backup_handlers.go` (expose `verified`), `web/src/api/schema.d.ts` (regenerated), `web/src/features/instance/BackupList.tsx`, `web/src/i18n/en/backups.ts`, `web/src/i18n/de/backups.ts`
- Test: `internal/backup/format_test.go`, `internal/backup/crypt_test.go`, `internal/backup/listing` tests, `internal/api/backup_handlers_test.go`, `web/src/features/instance/BackupsPage.test.tsx`

**Interfaces (produced, used by Tasks 5–7):**
- `func HeaderKey(masterKey [32]byte) []byte` — HKDF-SHA256, nil salt, info `"pulsix backup header v1"`, 32 bytes.
- `Header.MAC string` with JSON tag `mac,omitempty` — hex HMAC-SHA256 over `json.Marshal` of the header with `MAC == ""` and `FormatVersion == FormatVersion`.
- `func SignHeader(h Header, key []byte) Header` and `func (h Header) Authentic(key []byte) bool` (constant-time compare; false when `MAC` is empty).
- `Object.Verified bool`; `func MarkVerified(objs []Object, key []byte)` sets `Verified` when `HeaderErr == nil`, `Header.Authentic(key)`, and `ParseObjectName(o.Name)` returns the header's kind and a time equal to `Header.CreatedAt.UTC().Truncate(time.Second)`.
- `NewService(store Store, box *crypto.Box, headerKey []byte, dump DumpFunc, ...)` (new third parameter); `Entry.Verified bool`, JSON `verified` in the API list items.

- [ ] **Step 1: Write the failing tests.**
  - `TestSignedHeaderIsAuthentic`, `TestAuthenticRejectsTamperedField` (change `CreatedAt`, `Kind`, `PulsixVersion` each), `TestAuthenticRejectsMissingMAC`, `TestAuthenticRejectsOtherKey`.
  - `TestHeaderKeyDiffersFromBackupKey`: `HeaderKey(k)` is not the bytes of `DeriveKey(k)`.
  - `TestReadHeaderRefusesUnknownKind` and `TestReadHeaderRefusesUnprintableVersion` (a `\x1b` in `pulsix_version`): both return `ErrCorrupt`, so such objects list as damaged.
  - `TestDumpSignsHeaderAndManifest` (DB test): a dump's plaintext header is `Authentic` under `HeaderKey(masterKey)` and `Verify` still accepts the file (manifest header equals plaintext header, MAC included).
  - `TestMarkVerifiedAcceptsSubSecondCreatedAt`, `TestMarkVerifiedRejectsNameKindMismatch`, `TestMarkVerifiedRejectsNameStampMismatch`.
  - `TestListMarksUnsignedBackupUnverified`: an object written without a MAC (a pre-branch backup) lists with `Verified == false`, still downloads, and `Verify`/restore of it still succeeds.
  - API: the list response carries `verified`. SPA: an unverified backup row shows the label "Unverified" (de: "Nicht bestätigt") with a tooltip explaining the header cannot be authenticated.
- [ ] **Step 2: Run them**, expect failures.
- [ ] **Step 3: Implement.** `Dump` signs the header with `HeaderKey(masterKey)` before writing it and uses the signed header in the manifest. `ReadHeader` accepts only kinds `scheduled`/`manual` and a `pulsix_version` made of printable runes. `Service.List` and `find` call `MarkVerified` on the storage listing; `Entry.Verified` follows. Wire the key from `cfg.MasterKey` in both app call sites.
- [ ] **Step 4: Run** `./internal/backup/`, `./internal/app/`, `./internal/api/` (1800 s) and the SPA test/lint/tsc; all pass.
- [ ] **Step 5: Commit** `feat(backup): authenticate the plaintext backup header` (SPA part may be its own `feat(web): mark unverified backups` commit).

### Task 5: Retention trusts only verified headers and keeps the new backup (7.50 part 2)

**Files:**
- Modify: `internal/backup/retention.go` (`Prune`), `internal/backup/ticker.go` (`RunDue` → `prune`, lines 118-148)
- Test: `internal/backup/retention_test.go`, `internal/backup/service_test.go`

**Interfaces:** Consumes `Object.Verified` and `MarkVerified` from Task 4. Produces `func Prune(objs []Object, r Retention, loc *time.Location, keep string, now time.Time) (remove []string)`.

- [ ] **Step 1: Write the failing tests.**
  - `TestPruneNeverRemovesKeep`: the name passed as `keep` is never in `remove`, whatever the other objects claim.
  - `TestPruneIgnoresUnverified`: unverified scheduled objects are never removed and never fill a daily/weekly/monthly slot.
  - `TestPruneIgnoresFutureCreatedAt`: a verified object claiming a `CreatedAt` after `now` neither fills a slot nor is removed.
  - `TestRunDueForgedHeadersCannotPruneAuthenticBackups` (service test with the fake storage): seven header-only forged scheduled objects dated in 2031 plus same-day forged objects, default retention 7/4/6, two authentic backups; after two `RunDue` runs every authentic backup, including each just-written one, still exists. This is the audit's reproduction (record `internal/backup/retention.go:Prune:...` in `findings.json`).
- [ ] **Step 2: Run them**, expect failures.
- [ ] **Step 3: Implement.** `prune` marks the listing with the service's header key and passes the run's own object name and `s.now()`; `Prune` considers only `Verified` scheduled objects with `CreatedAt <= now`, and drops `keep` from the result.
- [ ] **Step 4: Run** `./internal/backup/`; pass.
- [ ] **Step 5: Commit** `fix(backup): prune only authenticated scheduled backups and never the one just written`.

### Task 6: One bad storage entry cannot fail or block a backup listing (7.61)

**Files:**
- Modify: `internal/backup/storage_dir.go` (`List`, `describe`, lines 88-127), `internal/backup/storage_s3.go` (`List`/`describe`, lines 183-234)
- Test: `internal/backup/storage_dir_test.go`, `internal/backup/storage_s3_test.go`

- [ ] **Step 1: Write the failing tests.**
  - `TestDirListSkipsNonRegularEntries`: a FIFO (`syscall.Mkfifo`) and a symlink, each named like a backup, are not listed, and `List` returns promptly (no open on the FIFO; guard the test with a short timeout).
  - `TestDirListMarksUnreadableEntryDamaged`: a `*.pxbak` file with mode 0000 is listed with a non-nil `HeaderErr` and the other backups list normally (skip when running as root).
  - `TestS3ListMarksUnreadableObjectDamaged` (MinIO test): an object whose ranged GET fails is listed with `HeaderErr` set instead of failing the whole `List`.
- [ ] **Step 2: Run them**, expect failures.
- [ ] **Step 3: Implement.** The folder listing uses the directory entry's type (`Lstat` semantics) and skips anything that is not a regular file; `describe` opens with `O_NOFOLLOW`. In both backends, a per-entry open/read/describe error becomes that object's `HeaderErr`; only a failure to enumerate the folder or bucket fails `List`. `Open` (download/restore) keeps refusing non-regular files.
- [ ] **Step 4: Run** `./internal/backup/`; pass.
- [ ] **Step 5: Commit** `fix(backup): list damaged or special storage entries instead of failing the listing`.

### Task 7: Restore rechecks, cleans up and says what it restored (7.51 part 2, 7.54, 7.55)

**Files:**
- Modify: `internal/app/restorecmd.go` (`listBackups` ~line 269-294, the flow around line 97-125, `restoreNextStep` ~line 66)
- Modify: `internal/backup/restore.go` (`Restore`/`load`, lines 63-246), `internal/app/managers.go` if a helper is needed
- Modify: `deploy/compose/README.md` (restore section and the "revoked worker's certificate stops working at once" sentences, lines ~88 and ~191)
- Test: `internal/app/restorecmd_test.go`, `internal/backup/restore_attempts_test.go` or a new `internal/backup/restore_guard_test.go`

**Interfaces:** `backup.RestoreOptions` gains `BeforeWipe func(ctx context.Context) error` (nil means none); `RunRestore` sets it to the manager check.

- [ ] **Step 1: Write the failing tests.**
  - `TestRestoreListEscapesControlBytes`: a header with `kind` containing `\x1b[8m` (or, after Task 4, an object listed as damaged) and an object name containing `\x1b[2J` produce `restore --list` output with no byte `0x1b` and with the escaped text visible (e.g. `\x1b`).
  - `TestRestoreRechecksManagersBeforeWipe` (DB test): a `BeforeWipe` that fails stops the restore before anything is dropped; the database is untouched.
  - `TestLoadRefusesNonEmptyCoreTables` (DB test): using the existing `afterMigrate` hook, insert one row into `users` between migrate and load; the restore fails with a clear error and does not merge the row.
  - `TestRestoreEndsAllSessions` (DB test): a backup that contains rows in `sessions` restores with `sessions` empty.
  - The restore success text (`restoreNextStep`) names that everyone must sign in again and that API tokens, worker/agent certificates, memberships and disabled users are at the backup's state.
- [ ] **Step 2: Run them**, expect failures.
- [ ] **Step 3: Implement.** `listBackups` prints names and header strings through one `printable(s string) string` helper (`strconv.Quote`-style escaping of non-printable runes). `Restore` calls `BeforeWipe` right before the wipe; `RunRestore` passes the `ManagersConnected` check. `load` checks that `users`, `organizations` and `org_memberships` are empty before the first `COPY` and deletes every row of `sessions` after the last one, inside the same load transaction. Update the README's restore steps and the two certificate sentences.
- [ ] **Step 4: Run** `./internal/backup/`, `./internal/app/`; pass.
- [ ] **Step 5: Commit** `fix(backup,app): recheck managers before the wipe, refuse a non-empty load, end sessions after a restore` and `fix(app): escape control bytes in restore --list` (two commits are fine).

### Task 8: Bounded host facts and an oversize claim response (7.56, 7.62)

**Files:**
- Modify: `internal/agents/facts.go` (`RecordFacts`, lines 67-83), `internal/db/queries/agents.sql` (`UpsertHostFacts`, lines 109-122), regenerated `internal/db/gen`
- Inspect and, if needed, modify: `internal/grpcsrv/claim.go`, `internal/jobs/queue_sql.go` (claim snapshot), `internal/pb/workerpb/limits.go`
- Test: `internal/agents/facts_test.go`, `internal/agents/store_sql_test.go`, `internal/grpcsrv/claim_test.go`

**Interfaces:** `MaxFactsKeyLen = 32` and `MaxStoredFacts = 128` in `internal/agents/facts.go`.

- [ ] **Step 1: Write the failing tests.**
  - `TestRecordFactsRefusesKeyOutsideContract`: keys longer than 32 characters, with uppercase letters, `-`, `.` or non-ASCII are `ErrInvalidInput` (the agent's contract is `[a-z0-9_]`, 1–32, `internal/agent/facts/facts.go:59-67`).
  - `TestUpsertHostFactsReplacesWhenMergeExceedsCap` (DB test): a host holding 100 keys, then a push of 64 different keys, ends with exactly those 64 keys; a push that keeps the total at or under 128 merges as today.
  - Review-focus case: a host that already stores more than 128 keys (written directly) gets its next push stored, replacing the set.
  - 7.62: `TestClaimJobFailsJobWhosePayloadExceedsWorkerLimit` — first read what happens today when the claim response exceeds `MaxManagerToWorkerBytes`; write the test for the behaviour you find wrong (the job must end failed with an error that names the size, and the org's next queued job must be claimable). If today's code already fails the job cleanly, the test pins that and no production change is made; say so in the report.
- [ ] **Step 2: Run them**, expect failures (except a 7.62 test that pins existing correct behaviour).
- [ ] **Step 3: Implement.** `RecordFacts` validates each key against the contract. `UpsertHostFacts` merges with `||` only when the merged object has at most 128 keys, otherwise stores the pushed set (`CASE` on `(SELECT count(*) FROM jsonb_object_keys(...))`); update the query's doc comment. `make sqlc`.
- [ ] **Step 4: Run** `./internal/agents/`, `./internal/agentapi/`, `./internal/grpcsrv/`, `./internal/jobs/`; pass.
- [ ] **Step 5: Commit** `fix(agents): bound fact keys and the facts a host can store` (and `fix(grpcsrv,jobs): ...` if 7.62 needed a change).

### Task 9: Bounded check tuples per host (7.57)

**Files:**
- Modify: `internal/checks/samples.go` (constants, `Ingest`), `internal/checks/store_sql.go` (`RecordObservations`, ~line 251-360), `internal/db/queries/checks.sql` (`UpsertCheckCurrent`, line 77), regenerated `internal/db/gen`; an additive migration only if an index is needed for the count
- Test: `internal/checks/store_sql_test.go`, `internal/checks/samples_test.go`

**Interfaces:** `MaxTuplesPerHost = 4096` in `internal/checks/samples.go`; `Ingest` gains `OverCap int` (observations dropped because their tuple would exceed the cap).

- [ ] **Step 1: Write the failing tests** (DB): `TestRecordObservationsCapsNewTuplesPerHost` — with 4096 distinct tuples stored for a host, an observation for a new tuple is not stored (no `check_current` row, no sample) and is counted in `OverCap`; `TestRecordObservationsUpdatesExistingTuplesAtCap` — at the cap, observations for existing tuples still update `check_current` and insert samples; other hosts are unaffected.
- [ ] **Step 2: Run them**, expect failures.
- [ ] **Step 3: Implement** the cap inside the existing per-host savepoint: count the host's tuples once per batch, admit new tuples only while the count stays at or under 4096. Log `OverCap` once per batch at warn level with org and host.
- [ ] **Step 4: Run** `./internal/checks/` and `./internal/agentapi/`; pass.
- [ ] **Step 5: Commit** `fix(checks): cap the check tuples one host can store`.

### Task 10: A rule cannot starve the evaluation ticker (7.58)

**Files:**
- Modify: `internal/rules/catalog.go` (type check near the window bounds, ~line 240-250), `internal/rules/due_sql.go` (`evaluateOne`, ~line 190+), `internal/db/queries/rules.sql` (new `PostponeEvalCursor`), regenerated `internal/db/gen`
- Test: `internal/rules/catalog_test.go` (or `service_test.go`), `internal/rules/due_sql_test.go`

**Interfaces:** `MaxWindowsPerRule = 16`, `hostStatementTimeout = 10 * time.Second`, `failedHostBackoff = 60 * time.Second` in `internal/rules`.

- [ ] **Step 1: Write the failing tests.**
  - `TestRuleRefusesMoreThan16DistinctWindows`: an expression with 17 distinct (metric, aggregate, window) uses is refused at save with an error naming the limit; 16 is accepted; repeating the same window does not count twice (same dedup key as `WindowRequests`).
  - `TestEvaluateOneSetsStatementTimeout` (DB): inside `fn`, `SHOW statement_timeout` returns `10s`.
  - `TestFailedHostIsPostponed` (DB): when `fn` returns an error, the host's `next_due_at` is at least `now + 60s` afterwards and the host is ordered after a healthy due host on the next claim.
- [ ] **Step 2: Run them**, expect failures.
- [ ] **Step 3: Implement.** Count distinct window aggregates in the same pass that checks `MinWindow`/`MaxWindow`. `evaluateOne` runs `SET LOCAL statement_timeout = '10s'` first in the host's transaction. On a failed host (not a cancelled batch context), run `PostponeEvalCursor` (`UPDATE check_eval_cursor SET next_due_at = now() + make_interval(secs => $3) WHERE org_id = $1 AND host_id = $2`) on the pool after the rollback.
- [ ] **Step 4: Run** `./internal/rules/` (900 s); pass.
- [ ] **Step 5: Commit** `fix(rules): cap windows per rule and bound each host's evaluation`.

### Task 11: Block the Akamai/Linode and Scaleway IPv6 metadata ranges (7.60)

**Files:**
- Modify: `internal/outbound/policy.go` (`blockedRanges`, lines 43-49)
- Test: `internal/outbound/policy_test.go` (`TestPolicyRefusesIPv6CloudInstanceServices`)

- [ ] **Step 1: Extend the test**: `fd00:a9fe:a9fe::1` and `fd00:42::42` are refused as cloud metadata; `fd12:3456::1` stays allowed.
- [ ] **Step 2: Run it**, expect failure.
- [ ] **Step 3: Add** `fd00:a9fe:a9fe::/64` (comment: Akamai/Linode, `fd00:a9fe:a9fe::1`) and `fd00:42::/64` (comment: Scaleway, `fd00:42::42`) beside the existing provider ranges, and name backlog 7.60 in the block comment.
- [ ] **Step 4: Run** `./internal/outbound/`; pass.
- [ ] **Step 5: Commit** `fix(outbound): refuse the Akamai/Linode and Scaleway IPv6 metadata ranges`.

### Task 12: The compose worker snippet keeps the token out of the file (7.59)

**Files:**
- Modify: `internal/api/worker_snippets.go` (compose branch, line 41-58), `internal/api/worker_handlers.go` (output struct, lines 82-83, 232)
- Modify: `web/src/api/schema.d.ts` (regenerated), `web/src/features/workers/AddWorkerDialog.tsx`, `web/src/i18n/en/workers.ts`, `web/src/i18n/de/workers.ts`
- Modify: `deploy/compose/README.md` (remote worker example, lines ~173-184)
- Test: `internal/api/worker_snippets_test.go`, `web/src/features/workers/AddWorkerDialog.test.tsx`

**Interfaces:** `workerSnippets` returns `(docker, compose, composeUp string)`; the create response gains `compose_up_command`.

- [ ] **Step 1: Write the failing tests.** The compose snippet does not contain the token and has `PULSIX_ENROLL_TOKEN: ${PULSIX_ENROLL_TOKEN:?set PULSIX_ENROLL_TOKEN}`; `compose_up_command` is `PULSIX_ENROLL_TOKEN=<token> docker compose up -d`; the docker snippet is unchanged. SPA: the compose tab shows the YAML and, below it, the start command with its own copy button and a one-line hint that the token stays out of the file (en + de).
- [ ] **Step 2: Run them**, expect failures.
- [ ] **Step 3: Implement**, regenerate the OpenAPI types, and change the README example to the same environment form as the docker snippet (token as `PULSIX_ENROLL_TOKEN=… docker run … -e PULSIX_ENROLL_TOKEN`, plus `--init` and `-e PULSIX_WORKER_PLAY_UID=65533`).
- [ ] **Step 4: Run** the api test (1800 s), the SPA test, eslint and tsc; pass.
- [ ] **Step 5: Commit** `fix(api,web): keep the enrollment token out of the compose snippet`.
