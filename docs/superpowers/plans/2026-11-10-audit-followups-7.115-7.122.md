# Audit follow-ups 7.115–7.122 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close the follow-ups the audit-run-4 reviews left in `docs/BACKLOG.md`: 7.115, 7.116, 7.118, 7.119, 7.120, 7.121 and 7.122 (7.117 is an idea and stays out).

**Architecture:** Each row is a small, local change with its own tests:
- a NUL rule in the check-results contract;
- two account-row lock orders in `internal/auth`, composed in `auth.Enrolment`, which already owns the instance transaction;
- three test-only gaps;
- a manager-side number-range check and read guard for worker variable-source reports.

No migration: the one new query only locks an existing row.

**Tech Stack:** Go, PostgreSQL through sqlc (`internal/db/queries` → `internal/db/gen`), huma API, testcontainers via `internal/db/testdb`.

**Spec:** The backlog rows 7.115, 7.116, 7.118, 7.119, 7.120, 7.121 and 7.122 in `docs/BACKLOG.md`, each with its **Proposal**. The owner approved them on 2026-11-10 in chat ("die audit sachen"). Each task quotes the row it implements; where the plan rules differently from a proposal, it says so and why.

## Global Constraints

- Everything in the repository is English: code, comments, commits, docs.
- Conventional Commits. Every commit ends with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
- Never run one whole-module `go test ./...`. Run single packages with `go test -race -timeout 900s ./internal/<pkg>/...`; `internal/api` takes `-timeout 1800s`. Run DB-backed packages one at a time.
  - Testcontainers flakes (`port "5432/tcp" not found`, a Ryuk race) are re-run once on their own before they count as failures.
- `golangci-lint run ./...` and `gofmt -s -l .` must be clean before each commit.
- sqlc is not on PATH. Regenerate with `go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1 generate`, and commit `internal/db/gen` changes with the query.
- Migrations are additive only. This plan needs none.
- Layering (CLAUDE.md):
  - Handlers decode, call a service and encode. They never touch `gen.Queries`.
  - Services return sentinel errors.
  - Stores sit behind interfaces.
- Never push and never tag. Only touch Docker containers the tests themselves start.
- Each task sets its backlog row in `docs/BACKLOG.md` to `done`. The row gets a `**Done 2026-11-10.**` sentence naming what changed, ahead of the existing text, which stays.

## Review Focus

1. A batch where one observation's error text holds NUL and the others are ordinary stores every observation, with the NUL removed (Task 1).
2. The account's last two second factors are removed at once: deleting its only passkey and disabling TOTP, concurrently. At least one factor must remain; exactly one of the two calls fails with `ErrLastSecondFactor` (Task 2).
3. A wrong TOTP code at `/auth/totp/verify` still spends a factor attempt. Verification must stay outside the new transaction, since a rollback would refund the attempt and lift the brute-force budget (Task 3).
4. A current worker's report holding ordinary numbers is accepted by the manager unchanged: `0`, `-3`, `0.5`, `1e-6`, `1e20`, an int64 such as `9007199254740993` (Task 6).
5. A stored repository playbook result exactly at the read limit still decodes; one byte over reads as missing with a log line, never as an error that fails a sync (Task 6).

---

### Task 1: Keep a NUL in an agent's label or error from failing the batch (7.115)

**Files:**
- Modify: `internal/checks/samples.go`:
  - `withinContract` (~line 212)
  - `cutErr` (~line 238)
  - the `RecordObservations` call site (~line 363)
  - the `MaxLabelLen` and `MaxErrLen` doc comments (~lines 135–149)
- Test: `internal/checks/samples_test.go` (unit) and `internal/checks/store_sql_test.go` (database)
- Modify: `docs/BACKLOG.md` row 7.115

**Interfaces:**
- Produces: `func cleanErr(s string) string` in `internal/checks/samples.go`. It drops every `\x00` byte and then cuts the result with `cutErr`, in that order, so the cut sees the final text. It replaces `cutErr` at the `RecordObservations` call site; `cutErr` stays as is.
- `withinContract` gains a case: a non-nil `Label` holding `\x00` returns `fmt.Errorf("%w: label for check_key %q contains a NUL byte", ErrInvalidInput, o.CheckKey)`.
  - Ruling: refuse, as the row proposes. A label is an identifier from a fixed set, so a NUL there means a broken agent, the same as the item rule.
  - Error texts are stripped instead: the row's own rule is that a collection failure must reach the operator.

- [ ] **Step 1: Write the failing unit tests** in `samples_test.go`:
  - `TestCleanErrDropsNUL`:
    - `cleanErr("dis\x00k full") == "disk full"`
    - `cleanErr(strings.Repeat("\x00", MaxErrLen+10) + "x") == "x"`
    - `cleanErr(strings.Repeat("a", MaxErrLen+5)) == strings.Repeat("a", MaxErrLen)`
  - `TestWithinContractRefusesNULInLabel`: an otherwise valid observation with `Label` `"act\x00ive"` gives an error with `errors.Is(err, ErrInvalidInput)`. The same observation with `"active"` gives nil.
- [ ] **Step 2: Run** `go test -race -timeout 900s -run 'TestCleanErr|TestWithinContractRefusesNUL' ./internal/checks/`. Expected: FAIL (`cleanErr` undefined).
- [ ] **Step 3: Implement** `cleanErr`, the `withinContract` case and the call-site switch. Extend the `MaxErrLen` comment by one sentence: NUL is dropped because Postgres `text` cannot hold it. Extend `MaxLabelLen` the same way: a NUL is refused.
- [ ] **Step 4: Write the database test** `TestRecordObservationsStoresABatchWithANULError` in `store_sql_test.go`, through the service on the SQL store with the package's existing testdb helpers.
  - The batch has three valid observations for one host. The second carries `Err: "read /proc\x00/x: denied"`.
  - Assert `Accepted == 3`, no error, and the stored error text is `"read /proc/x: denied"`, read back from `check_current` or `check_samples` with the store's own read method.
- [ ] **Step 5: Run** `go test -race -timeout 900s ./internal/checks/`. Expected: PASS.
- [ ] **Step 6: Mark 7.115 done** in `docs/BACKLOG.md`. Lint, gofmt, then commit `fix(checks): drop a NUL from an agent's error text and refuse one in a label`.

### Task 2: Remove a second factor under the account's exclusive lock (7.116)

**Files:**
- Modify: `internal/db/queries/auth.sql` (new query after `LockUserForEnrolment`), then regenerate `internal/db/gen`
- Modify: `internal/auth/session.go` (new method next to `LockAccount`)
- Modify: `internal/auth/enrolment.go` (two new methods)
- Modify: `internal/api/passkey_handlers.go:188` and `internal/api/mfa_handlers.go:223` (call the new methods)
- Test: `internal/auth/enrolment_reset_sql_test.go` or a new `internal/auth/factor_removal_sql_test.go`; `internal/auth/second_factor_test.go` stays as is
- Modify: `docs/BACKLOG.md` row 7.116

**Interfaces:**
- New query, with a comment saying why it is FOR UPDATE and not KEY SHARE:
  ```sql
  -- name: LockUserForFactorRemoval :one
  SELECT id FROM users WHERE id = $1 FOR UPDATE;
  ```
- Produces: `func (s *Sessions) LockAccountForRemoval(ctx context.Context, userID uuid.UUID) error`. It is `ErrNotFound` for an unknown account, as `LockAccount` is.
- Produces: `func (e *Enrolment) DeletePasskey(ctx context.Context, userID, id uuid.UUID) error`. Inside `e.tx.WithInstanceTx` it runs `e.sessions.LockAccountForRemoval`, then `SecondFactors{TOTP: e.totp, Passkeys: e.passkeys}.DeletePasskey`.
- Produces: `func (e *Enrolment) DisableTOTP(ctx context.Context, userID uuid.UUID) error`. Same shape, around `SecondFactors.DisableTOTP`.
- Ruling, against the row's "`Sessions.LockAccount`, the order enrolment uses":
  - The lock must be `FOR UPDATE`. `LockAccount` takes `FOR KEY SHARE`, and two key-share locks do not conflict, so a delete holding one would not serialise with a passkey login that holds one too.
  - `FOR UPDATE` is the lock the administrator's reset takes. It conflicts with the login's key share, and taking the users row first keeps the order every users/sessions path follows (7.109).
- Ruling: `DisableTOTP` gets the same treatment. The row notes the last-factor check "shares the same unlocked read", and two concurrent removals of the last two factors are the same race.

- [ ] **Step 1: Write the failing database tests**, reusing `resetWorld`, `seedLoginKey`, `loginPasskeys`, `beginLogin`, `hookedSessions` and `waitForLockWaiters` from the 7.109 tests:
  - `TestPasskeyLoginWaitingBehindPasskeyDeletionOpensNoSession`:
    1. Seed an account with two passkeys and begin a login with passkey A.
    2. Open a transaction that holds the account lock through `LockAccountForRemoval`.
    3. Start the login in a goroutine and wait until it waits on the lock (`waitForLockWaiters`).
    4. Delete passkey A and commit.
    5. The login returns `auth.ErrInvalidCredentials`, and `sessionCount` is unchanged.
  - `TestConcurrentRemovalOfTheLastTwoFactorsKeepsOne`: an account with one confirmed TOTP and one passkey. Run `Enrolment.DeletePasskey` and `Enrolment.DisableTOTP` concurrently, ordered deterministically by holding the lock as above. Exactly one returns `auth.ErrLastSecondFactor`, and the account still has a factor (`SecondFactors.Enrolled` is true).
- [ ] **Step 2: Run** `go test -race -timeout 900s -run 'WaitingBehindPasskeyDeletion|LastTwoFactors' ./internal/auth/`. Expected: FAIL (the methods are undefined).
- [ ] **Step 3: Implement** the query (regenerate sqlc), `LockAccountForRemoval` and the two `Enrolment` methods, each with a doc comment naming backlog 7.116. Switch the two handlers to `deps.Enrolment.DeletePasskey` and `deps.Enrolment.DisableTOTP`. Error mapping stays as it is. Remove `SecondFactors.DeletePasskey` and `SecondFactors.DisableTOTP` only if no other caller is left, and keep their unit tests either way.
- [ ] **Step 4: Run** `go test -race -timeout 900s ./internal/auth/`, then `go test -race -timeout 1800s ./internal/api/`. Expected: PASS.
- [ ] **Step 5: Mark 7.116 done**, with the FOR UPDATE ruling in one sentence. Lint, then commit `fix(auth): remove a second factor under the account's exclusive lock`.

### Task 3: Complete a TOTP-verified session in the account-first lock order (7.118)

**Files:**
- Modify: `internal/auth/enrolment.go` (new method)
- Modify: `internal/api/mfa_handlers.go:168-182` (`/api/v1/auth/totp/verify`)
- Test: `internal/auth/enrolment_reset_sql_test.go`, `internal/api/` (an existing MFA handler test file)
- Modify: `docs/BACKLOG.md` row 7.118

**Interfaces:**
- Produces: `func (e *Enrolment) CompleteVerifiedSession(ctx context.Context, userID, sessionID uuid.UUID) (cookie string, err error)`.
  - Inside `e.tx.WithInstanceTx`: first `e.sessions.LockLive(ctx, userID, sessionID)` (account row first, in key-share mode), then `e.sessions.MarkMFAComplete(ctx, sessionID)`.
  - A session that is no longer live is `ErrNotFound`, the answer `MarkMFAComplete` gives today.
- The handler keeps `verifyFactorCode` **before** and outside this call, then calls `deps.Enrolment.CompleteVerifiedSession(ctx, p.UserID, p.SessionID)` in place of `deps.Sessions.MarkMFAComplete`.
  - Ruling: verifying inside the transaction would roll back the attempt `TOTP.attempt` spends on a wrong code (`ClaimFactorAttempt`), which lifts the 7.26 budget.

- [ ] **Step 1: Write the failing test** `TestVerifiedSessionCompletionAndSecondFactorResetSerialise`, modelled on `TestEnrolmentAndSecondFactorResetSerialise`:
  1. Hold the administrator's reset (`resetWorld.reset` path or its lock) after it has taken the account row.
  2. Start `CompleteVerifiedSession` for a pending session of that account and wait for it to queue on the lock.
  3. Let the reset commit.
  4. Completion returns `auth.ErrNotFound`, no error is a deadlock (`40P01`), and the session stays revoked.
- [ ] **Step 2: Run** `go test -race -timeout 900s -run TestVerifiedSessionCompletion ./internal/auth/`. Expected: FAIL (undefined).
- [ ] **Step 3: Implement** the method and the handler switch.
- [ ] **Step 4: Add API test** `TestTOTPVerifyWithAWrongCodeStillSpendsAnAttempt`: a wrong code at `/api/v1/auth/totp/verify` answers 401, and the account's factor-attempt counter (the column `ClaimFactorAttempt` increments; read it with SQL) went up by one.
- [ ] **Step 5: Run** `go test -race -timeout 900s ./internal/auth/` and `go test -race -timeout 1800s ./internal/api/`. Expected: PASS.
- [ ] **Step 6: Mark 7.118 done.** Lint, then commit `fix(auth): complete a TOTP-verified session with the account row locked first`.

### Task 4: Close the 7.109 and 7.103 test gaps (7.119, 7.120)

Test-only. If a test fails against current code, stop and report it: that is a bug for its own task, not something to fix here.

**Files:**
- Test: `internal/auth/passkey_login_reset_sql_test.go`
- Test: `internal/api/` (MFA tests, and `repo_url_credentials_test.go`)
- Modify: `docs/BACKLOG.md` rows 7.119 and 7.120

**Interfaces:** none produced.

- [ ] **Step 1: 7.119 (1)** `TestPasskeyLoginCreatesItsSessionUnderTheAccountLock`:
  - Wrap the real sessions in a `hookedSessions` whose `Create` hook runs, on a **separate** pool connection, `SELECT id FROM users WHERE id = $1 FOR UPDATE NOWAIT`.
  - The hook asserts that query fails with SQLSTATE `55P03` (lock_not_available), then delegates.
  - The login succeeds and the hook ran exactly once.
- [ ] **Step 2: 7.119 (2)** API test `TestTOTPEnrolOnASessionEndedByResetIs401`:
  1. A pending session of an account with no factor.
  2. An instance administrator resets its second factor through the API.
  3. `POST /api/v1/auth/totp/enrol` with the old cookie answers 401.
- [ ] **Step 3: 7.120**, in `repo_url_credentials_test.go`, two tests in the style of `TestRepoDTOHidesURLPassword`. For each, find the route in the handlers.
  - `TestProjectionUpdateEchoingRedactedURLKeepsIt`:
    1. A shared definition with a credential-bearing URL, set by SQL as the existing test does.
    2. Its projection in an organization.
    3. A projection update that sends the redacted URL back with a changed credential or vault field answers 200.
    4. The stored definition URL is unchanged.
  - `TestSharedDefinitionPutEchoingRedactedURLKeepsIt`:
    1. `PUT` of the definition with its redacted URL and a changed branch answers 200.
    2. The stored URL is byte-identical and the response URL is redacted.
- [ ] **Step 4: Run** `go test -race -timeout 900s ./internal/auth/` and `go test -race -timeout 1800s ./internal/api/`. Expected: PASS.
- [ ] **Step 5: Mark 7.119 and 7.120 done.** Lint, then commit `test: pin the passkey login's lock and the URL echo at the API`.

### Task 5: Pin one role entry point over its file's budget (7.121)

**Files:**
- Test: `internal/varsource/roles_spec_test.go`
- Modify, only if the test shows a re-read: `internal/varsource/roles_spec.go`
- Modify: `docs/BACKLOG.md` row 7.121

**Interfaces:** none produced.

- [ ] **Step 1: Write** `TestOneEntryPointOverTheFileBudget`, next to `TestRereadsAreCharged`.
  - Build a spec file whose **first** read alone passes the file's 1 000 000 steps: the same `&D` 10 000-key mapping, merged 128 times.
  - Use it from two entry points, `e0,e1`, through `rolesUsing`.
  - Assert:
    - `wantRoles(t, pb.Roles, RoleUse{"r0", "e0", RoleUnreadable}, RoleUse{"r0", "e1", RoleUnreadable})`
    - `wantNotes` holds exactly one `warning file_too_complex roles/r0/meta/argument_specs.yml:0`
    - the file was read once: by the same means `TestRepeatedEntryPointIsNotReread` uses to see a read, or via the walk's charged raw bytes equalling one file's size
- [ ] **Step 2: Run** `go test -race -timeout 900s -run TestOneEntryPointOverTheFileBudget ./internal/varsource/`.
  - If it passes, the behaviour is pinned.
  - If the file is read twice, mark it failed when its own budget runs out (in `parseSpec`, set `s.failed = true` on that path) and re-run.
- [ ] **Step 3: Run** `go test -race -timeout 900s ./internal/varsource/`. Expected: PASS.
- [ ] **Step 4: Mark 7.121 done.** Lint, then commit `test(varsource): pin one entry point over its role spec file's budget` (or `fix(varsource): …` if Step 2 needed the change).

### Task 6: Check a worker report's number range and bound a stored result's read (7.122)

**Files:**
- Modify: `internal/varsource/scalars.go` (export the range check)
- Modify: `internal/repos/sources.go`: `scanJSON` (~line 219) and `decodeReport` (~line 173)
- Modify: `internal/repos/sources_store_sql.go`: `decodePlaybookSource` (~line 174)
- Test: `internal/repos/` (the existing sources tests: unit for `decodeReport`, SQL for the read guard)
- Modify: `docs/BACKLOG.md` row 7.122 and `docs/upgrades.md` (one bullet, newest first)

**Interfaces:**
- Produces: `func PlainFloat(x float64) bool` in `internal/varsource/scalars.go`.
  - True for NaN, ±Inf, 0, and `|x|` in `[plainNumberMin, plainNumberMax)`.
  - The existing `plainNumber(v any)` delegates to it for a float64.
- `scanJSON` becomes `scanJSON(raw []byte) (maxDepth int, nul bool, unplain bool)`.
  - For every number token outside a string, it parses the token with `strconv.ParseFloat(tok, 64)`. `unplain` is true when that fails with a range error or the result is not `varsource.PlainFloat`.
  - A token runs from `-` or a digit through the following `[0-9eE.+-]` bytes.
- `decodeReport` returns `fmt.Errorf("the report holds a number of 1e21 or more, or below 1e-6, which a current worker never sends")` when `unplain`. `RecordVarSources` already logs such an error and keeps the stored record and rows untouched.
  - Ruling: refuse the whole report rather than mark single playbooks unread. It is the existing path for a malformed report, needs no new note code or SPA string, and only a worker older than 390f7248 can send it; `docs/upgrades.md` already tells operators to upgrade workers.
- New constant in `sources_store_sql.go`: `maxStoredResultReadBytes = 2 * varsource.MaxReportBytes`. Its comment names the jsonb re-rendering argument and points at `playbooks.maxInlineReadBytes`.
  - `decodePlaybookSource` treats a longer `result` as missing (`false`), with a warn log `"stored playbook variable sources too large to read; treated as missing"` and the same attrs as the decode-failure log, without unmarshalling.

- [ ] **Step 1: Write the failing tests**:
  - `TestPlainFloat` (in `internal/varsource`), table-driven:
    - true: `0, 1e-6, 0.5, -3, 1e20, 9.99e20, math.NaN(), math.Inf(1)`
    - false: `1e21, -1e21, 9e-7, 1e300, 5e-324`
  - `TestDecodeReportRefusesAnUnplainNumber`: a minimal valid report JSON whose one playbook field default is `1e300` gives an error. The same with `1e-7` gives an error. The same with `100000000000000000000` (1e20 written out) is accepted. A number inside a string (`"1e300"`) is accepted.
  - `TestDecodeReportAcceptsWhatTheCurrentWorkerSends` (Review Focus 4):
    1. `varsource.ParseInline` (or the repo parser the worker uses) over a playbook whose vars hold `0`, `-3`, `0.5`, `1e-6`, `1e20`, `9007199254740993` and `1e300`, the last refused as a form value by the parser.
    2. Encode it as the worker does (`varsource.Encode` into a report).
    3. `decodeReport` accepts it.
  - `TestStoredPlaybookResultOverTheReadLimitReadsAsMissing` (SQL, Review Focus 5):
    1. Store one row's `result` by SQL with a JSON object padded to exactly `maxStoredResultReadBytes`; it decodes.
    2. Pad another to one byte more; it is absent from `ListPlaybookSources`, and `GetPlaybookVarSource` returns a nil playbook, with no error.
    3. If the 16 MiB rows make the test too slow (over 10 s), expose the limit to the test through `export_test.go` as a variable and lower it there. Say so in the report.
- [ ] **Step 2: Run** `go test -race -timeout 900s ./internal/varsource/ ./internal/repos/` one package at a time. Expected: FAIL.
- [ ] **Step 3: Implement** `PlainFloat`, the `scanJSON` extension, the `decodeReport` refusal, the constant and the guard.
- [ ] **Step 4: Run** both packages again. Expected: PASS.
- [ ] **Step 5: Mark 7.122 done.** Add the `docs/upgrades.md` bullet: a worker older than this release that reads a playbook holding such a number has its report ignored (logged) until it is upgraded. Lint, then commit `fix(repos): refuse a worker report with numbers jsonb would write out in full, and bound a stored result's read`.
