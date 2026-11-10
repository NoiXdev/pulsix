# Security Audit Run 4 Follow-ups Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix every confirmed finding of security audit run 4 and close the code-level leads (backlog 7.101–7.106, 7.109–7.114).

**Architecture:** Independent, small fixes at the last trusted decision point of each path: the worker's git update path, the self-service reset, repository URL validation and DTOs, the run-output redactor, the requirements source check, the variable-source encoder and walker, the check-ingest contract and rate, the rule ticker's deadline handling, and the editor-playbook variable sources. No new subsystem; one additive migration at most (Task 2's sqlc query change needs none).

**Tech Stack:** Go 1.27 (chi + huma, pgx v5, sqlc, goose, yaml.v3), PostgreSQL 18 + TimescaleDB, React/TypeScript SPA (Vitest), ansible-core 2.21 in the worker image.

**Spec:** `docs/BACKLOG.md` section "v1.0 · security audit run 4 follow-ups" (rows 7.101–7.114) and `docs/security/2026-11-09-audit-run-4/` — `findings.json` holds the trace, evidence and suggested remediation of every record, looked up by the fingerprint each backlog row names; `FINDINGS-DETAIL.md` and `NEEDS-VALIDATION.md` are the readable form. Where this plan and a record's suggested code differ, this plan wins.

## Global Constraints

- Everything in the repository is English: code, comments, commit messages, docs.
- Conventional Commits; every commit ends with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. Commit only the task's own files.
- TDD: failing test first, watched failing, then the minimal fix.
- Tests run per package with a timeout, e.g. `go test -race -count=1 -timeout 900s ./internal/varsource/`; `internal/api` needs `-timeout 1800s`. Never run one whole-module `go test ./...`. The full suite is `make test`.
- Before every commit: `golangci-lint run` (0 issues) and `gofmt -s -l .` (no output).
- SPA: `PATH=$HOME/.nvm/versions/node/v24.14.0/bin:$PATH`, then in `web/`: `npx vitest run <file>`, `npx eslint <files>`, `npx tsc -b`. No prettier.
- Migrations are additive only. sqlc output is regenerated with `make sqlc`; never hand-edit generated files.
- Database tests use `internal/db/testdb` (testcontainers). Only touch Docker containers the tests start themselves; about 50 unrelated containers run on this machine.
- User-visible SPA strings exist in English and German (`web/src/i18n/en|de/*.ts`).
- Comments name the backlog row (`backlog 7.10x`) the way existing fixes do (e.g. `internal/checks/samples.go` naming 7.57).
- Backlog rows and the end-user docs in `NoiXdev/noix-docs` are updated by the controller after the branch, not by tasks.

## Review Focus

1. An account that never had a second factor but does have a password (a fresh invitee who set one, an instance without enforced MFA) must keep a working forgot-password flow unless the plan's rule says otherwise — Task 2 decides it explicitly and tests both kinds.
2. A repository row stored before Task 3 with `https://user:token@host/x` must still load, list and sync (the worker gets the full URL from the claim); only API responses hide the secret, and an update that leaves the URL untouched must not fail validation.
3. A clone created before Task 1 whose stored origin equals the claim URL must keep updating incrementally (no re-clone, no extra network round trip on every job).
4. Redaction must not start masking innocent output: a secret without `'` or URL-special characters adds no new spellings (Task 4 test).
5. A legitimate agent at the shortest check interval (10 s) with the full 4096 tuples must never hit the ingest budget (Task 9 test).

---

### Task 1: A changed repository URL takes effect on workers that already hold a clone (7.101, medium)

**Files:**
- Modify: `internal/worker/runner/gitrepo.go` (`fetchGit`, update path)
- Test: `internal/worker/runner/gitrepo_test.go` (or the existing fetch tests next to it)

- [ ] **Step 1: Write the failing test** `TestFetchGitUpdateUsesClaimURL`: clone from local bare repo A into the target, then call the fetch again with `spec.URL` = local bare repo B (different content on the same branch). Assert the checked-out tree is B's commit and that A was never contacted (make A unreadable before the second call, `chmod 000`, so a fetch from it fails). Also `TestFetchGitUpdateSameURLStaysIncremental`: second fetch with the same URL succeeds and keeps the existing `.git` (inode of `.git` unchanged).
- [ ] **Step 2: Run** `go test -race -count=1 -timeout 900s -run 'TestFetchGitUpdate' ./internal/worker/runner/`; the first test fails.
- [ ] **Step 3: Fix** the update path to fetch from `spec.URL` explicitly (`git fetch --quiet -- <spec.URL> <branch>` then reset to `FETCH_HEAD`, keeping today's credential, ssh pin and `--` handling) and set `remote.origin.url` to `spec.URL` so later tooling agrees. The credential helper or `GIT_SSH_COMMAND` must apply to `spec.URL` only.
- [ ] **Step 4: Run** the package tests; all pass.
- [ ] **Step 5: Commit** `fix(worker): fetch an existing clone from the repository's current URL`.

### Task 2: No self-service reset link while the account has no second factor (7.102)

**Files:**
- Modify: `internal/db/queries/accounts.sql` (the `LockAccount` query: add the `has_second_factor` expression `ListAccounts` uses), then `make sqlc`
- Modify: `internal/accounts/store.go` or wherever `LockedAccount` is declared (add `HasSecondFactor bool`), `internal/accounts/store_sql.go:~140` (map it), the fake store in the package's tests
- Modify: `internal/accounts/reset.go` (`requestReset`)
- Test: `internal/accounts/reset_test.go`, plus one SQL-store test (`internal/accounts/*_sql_test.go` with testdb) that `LockAccount` reports `HasSecondFactor` true for a TOTP account and a passkey account and false after `ResetSecondFactor`

**Decision (Review Focus 1):** the forgot path issues no link (and sends no mail, and answers exactly as for an unknown address) when the account has no confirmed second factor. This deliberately includes accounts that never enrolled one: their recovery is the admin's reset link or temporary password. The admin's `SendResetLink` is unchanged.

- [ ] **Step 1: Write the failing tests** `TestRequestResetRefusesAccountWithoutSecondFactor` (fake store: `HasSecondFactor=false` → no token stored, no mail, same response as unknown email) and `TestRequestResetAfterResetSecondFactorIssuesNothing` (sequence: factor present → `ResetSecondFactor` → `RequestReset` issues nothing; `SendResetLink` by an admin still issues a token), and the SQL mapping test above.
- [ ] **Step 2: Run** `go test -race -count=1 -timeout 900s ./internal/accounts/`; the new tests fail.
- [ ] **Step 3: Implement** the query change, the field, the mapping and the check in `requestReset` under the row lock it already holds.
- [ ] **Step 4: Run** `./internal/accounts/` and the reset routes in `./internal/api/` (`-run 'Reset|Forgot' -timeout 1800s`); all pass.
- [ ] **Step 5: Commit** `fix(accounts): issue no self-service reset link to an account without a second factor`.

### Task 3: Repository URLs carry no credentials (7.103)

**Files:**
- Modify: `internal/repos/service.go` (`validateURL`, exported as `ValidateURL` for reuse), the shared-definition service's `CreateRepo`/`UpdateRepo` (find it with `grep -rn "func (s \*Service) CreateRepo" internal/shared`), the repository DTO mappers `toRepoDTO` / `toRepoDefinitionDTO` in `internal/api`
- Test: `internal/repos/service_test.go`, the shared service's tests, one API test for the DTO redaction

**Rule:** refuse a URL whose userinfo has a password (`https://u:p@h/x`, `ssh://u:p@h/x`) or is a bare user on http(s) (`https://tok@h/x`, a token in the user slot); accept a bare user on `ssh://` and scp-style URLs (`git@h:x`, `ssh://git@h/x`). The error names the git credential as the right place. The DTOs replace userinfo with `***` for rows stored earlier (keep the user for ssh / scp-style, drop only a password). The claim to the worker still sends the stored URL unchanged. An update that does not change the URL skips the userinfo refusal (Review Focus 2).

- [ ] **Step 1: Write the failing tests** `TestValidateURLRefusesEmbeddedCredentials` (table: the four refused forms and two accepted forms above), the same table through the shared `CreateRepo`/`UpdateRepo`, `TestUpdateKeepsLegacyCredentialURL` (stored legacy URL, update of another field passes), and `TestRepoDTOHidesURLPassword` (a viewer GET of a legacy row shows `https://***@host/x`).
- [ ] **Step 2: Run** them; they fail.
- [ ] **Step 3: Implement** the shared validator, the shared-definition checks (also applying the existing scheme / leading-dash / white-space rules there if they are missing) and the DTO redaction.
- [ ] **Step 4: Run** `./internal/repos/`, the shared package, and `./internal/api/ -run 'Repo' -timeout 1800s`; all pass.
- [ ] **Step 5: Commit** `fix(repos): refuse credentials in repository URLs and hide stored ones`.

### Task 4: Redaction covers YAML single-quoted and URL-encoded secrets (7.104)

**Files:**
- Modify: `internal/worker/runner/redact.go` (`newRedactor`)
- Test: `internal/worker/runner/redact_test.go`

- [ ] **Step 1: Write the failing tests**, mirroring `TestRedactsPythonReprSpellings`: for the secret `'@Pa'ss!w0rd'`, its `to_yaml` / `to_nice_yaml` form (`'''@Pa''ss!w0rd'''`), its Jinja2 `urlencode` form (`%27%40Pa%27ss%21w0rd%27`, Python `quote(safe='/')`, uppercase hex) and its `url.QueryEscape` form are masked, each also inside JSON. `TestRedactAddsNoSpellingsForPlainSecret`: a secret of only letters and digits yields exactly today's set of spellings.
- [ ] **Step 2: Run** `go test -race -count=1 -timeout 900s -run 'Redact' ./internal/worker/runner/`; they fail.
- [ ] **Step 3: Implement** the three extra bodies (`strings.ReplaceAll(v, "'", "''")` when `v` contains `'`; a local `pyQuote(v)` that percent-encodes every byte except ASCII letters, digits and `_.-~/`; `url.QueryEscape(v)`), each added only when it differs from `v`, with their JSON forms through the existing helper.
- [ ] **Step 4: Run** the package; all pass.
- [ ] **Step 5: Commit** `fix(worker): redact YAML-quoted and URL-encoded spellings of secrets`.

### Task 5: Requirements fields must be strings (7.105)

**Files:**
- Modify: `internal/worker/requirements/shape.go` (role `src`, role name used as the source, collection `name` and `source`)
- Test: `internal/worker/requirements/shape_test.go` (or the existing plan tests)

**Rule:** refuse the file (`notRequirements`, the error `decodePlain` already uses for YAML the two loaders read differently) when one of those fields is a scalar whose yaml.v3 tag is neither `!!str` nor `!!null`.

- [ ] **Step 1: Write the failing test** `TestPlanRefusesNonStringSourceScalars`: table over the four fields × `1e3`, `1e+3`, `.5e3`, `0o17`, `true` → refused; the quoted forms (`"1e3"`) still plan and go through `checkSources`.
- [ ] **Step 2: Run** `go test -race -count=1 -timeout 900s ./internal/worker/requirements/`; it fails.
- [ ] **Step 3: Implement** the tag check.
- [ ] **Step 4: Run** the package; all pass.
- [ ] **Step 5: Commit** `fix(worker): refuse requirements whose source fields are not strings`.

### Task 6: Encode the variable-sources report within its cap (7.106)

**Files:**
- Modify: `internal/varsource/report.go` (`Encode`; optionally `ParseRepoInstalled` stopping once the running size passes the cap)
- Test: `internal/varsource/report_test.go`

- [ ] **Step 1: Write the failing test** `TestEncodeBoundsAllocation`: a report of 1000 playbooks built from the record's alias fixture (each playbook's own JSON ≈ MaxReportBytes/8 or larger); measure `runtime.MemStats.TotalAlloc` around `Encode` and assert it stays under `4*MaxReportBytes`; the result has the playbooks that fit, `incomplete=true`, and the rest under `LeftOut` with reason `report_too_large`. Existing Encode tests keep their exact output.
- [ ] **Step 2: Run** `go test -race -count=1 -timeout 900s -run TestEncode ./internal/varsource/`; it fails.
- [ ] **Step 3: Implement** per-playbook sizing (marshal each playbook once), the longest prefix whose summed size plus the envelope fits, one final marshal of that prefix.
- [ ] **Step 4: Run** the package; all pass.
- [ ] **Step 5: Commit** `fix(varsource): never encode more of the report than its cap`.

### Task 7: Enrolment finishes only on a live session (7.109)

**Files:**
- Modify: `internal/api/passkey_handlers.go` (`register/finish`), `internal/auth/passkey.go` (`FinishRegistration`), `internal/auth/enrolment.go`, and `internal/db/queries/sessions.sql` (`MarkSessionMFAComplete`) if it matches a revoked row
- Test: `internal/auth/*_test.go` (testdb), `internal/api/passkey_handlers_test.go`

**Invariant:** the credential write and the session upgrade happen in one instance transaction, and both refuse when the session is revoked or gone; `ResetSecondFactor` revokes the sessions, so an enrolment begun before the reset cannot finish after it. TOTP's `ConfirmTOTP` already shares one transaction: verify that `MarkSessionMFAComplete` matches no revoked row (fix the query if it does) and pin that with a test.

- [ ] **Step 1: Write the failing tests** `TestPasskeyFinishAfterSecondFactorResetStoresNothing` (begin with session S, `ResetSecondFactor`, finish with the stale principal → error, `ListPasskeys(U)` empty) and `TestMarkMFACompleteRefusesRevokedSession` (`Revoke(S)` then `MarkMFAComplete(S)` → `ErrNotFound`), and `TestConfirmTOTPAfterResetStoresNothing`.
- [ ] **Step 2: Run** them against testdb; the passkey one fails.
- [ ] **Step 3: Implement** a `Passkeys`/`Enrolment` method that adds the credential and upgrades the session in one `WithInstanceTx`, and use it from the handler; fix the session query if Step 2 showed it matches revoked rows.
- [ ] **Step 4: Run** `./internal/auth/` and `./internal/api/ -run 'Passkey|TOTP|Enrol' -timeout 1800s`; all pass.
- [ ] **Step 5: Commit** `fix(auth): finish a factor enrolment only on a session that is still live`.

### Task 8: Bound check labels and error texts (7.110)

**Files:**
- Modify: `internal/checks/samples.go` (constants next to `MaxItemLen`, `withinContract`, and the error cut)
- Test: `internal/checks/samples_test.go`

**Values:** `MaxLabelLen = 512` (refused like an over-long item); `MaxErrLen = 2048` (an over-long error is cut at a UTF-8 boundary, not refused, so the failing state still records). Check the agent's plugins under `internal/agent/checks/` stay within both; if one could exceed them, cut there too and say so in the report.

- [ ] **Step 1: Write the failing tests** `TestWithinContractRefusesLongLabel` (513 bytes refused, 512 accepted) and `TestRecordObservationsCutsLongError` (a 1 MiB error is stored as at most 2048 bytes of valid UTF-8; a multi-byte rune at the boundary is not split).
- [ ] **Step 2: Run** `go test -race -count=1 -timeout 900s ./internal/checks/`; they fail.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `./internal/checks/` and `./internal/agent/checks/`; all pass.
- [ ] **Step 5: Commit** `fix(checks): bound the label and error text an agent may store`.

### Task 9: A per-stream budget for check-result ingest (7.111)

**Files:**
- Modify: `internal/agentapi/stream.go` (`receiveMessages`)
- Test: `internal/agentapi/stream_test.go`

**Values:** a token bucket per agent stream counted in observations: refill `checks.MaxTuplesPerHost` (4096) per 10 s (the shortest check interval, `internal/checks/definitions.go` `minInterval`), burst `2 * checks.MaxTuplesPerHost`. A batch that exceeds the remaining budget is dropped whole, not stored, counted, and logged at most once a minute per stream; the stream stays open. Inject the clock. Read how the agent treats an unacknowledged or refused batch before choosing what to answer, and keep its behaviour (no retry storm); say in the report what you found.

- [ ] **Step 1: Write the failing tests** `TestCheckResultsWithinBudgetAreStored` (4096 observations every 10 s for 10 rounds on a fake clock: all stored — Review Focus 5) and `TestCheckResultsOverBudgetAreDropped` (back-to-back 1000-observation batches: after the burst the store sees no more calls until the clock advances).
- [ ] **Step 2: Run** `go test -race -count=1 -timeout 900s ./internal/agentapi/`; the second fails.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** the package; all pass.
- [ ] **Step 5: Commit** `fix(agentapi): give each agent stream a check-result ingest budget`.

### Task 10: The host the batch deadline cuts off is postponed (7.112)

**Files:**
- Modify: `internal/rules/due_sql.go` (`evaluateOne` and the batch loop), `internal/rules/ticker.go` if the deadline lives there
- Test: `internal/rules/due_sql_test.go` (testdb)

**Invariant:** the host whose evaluation was running when the batch deadline fired gets the same `retry_after` backoff a failed host gets (7.58), and the whole per-host work, including `ContextForHost`, runs under the per-host deadline, so a slow host cannot head every following claim.

- [ ] **Step 1: Write the failing test** from the record's validation plan: a `HostChecks` fake whose `ContextForHost` blocks until its context ends; host A (org 1, oldest due) and host B (org 2); two ticks with a short deadline. Assert A has `retry_after` set after the first tick and B is evaluated in the second.
- [ ] **Step 2: Run** `go test -race -count=1 -timeout 900s ./internal/rules/`; it fails.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** the package; all pass.
- [ ] **Step 5: Commit** `fix(rules): postpone the host a batch deadline cut off`.

### Task 11: The playbook walk keeps no role parse trees (7.113)

**Files:**
- Modify: `internal/varsource/roles.go` (`walk.spec` and the role/import cache)
- Test: `internal/varsource/roles_test.go`

**Invariant:** what the walk keeps per role is the extracted result (variables, defaults, argument specs), not the parsed `yaml.Node` tree or the raw bytes; a role reached twice is still parsed once. The walker's output is byte-identical on every existing fixture.

- [ ] **Step 1: Write the failing test** `TestWalkDoesNotRetainRoleTrees`: 31 roles of ~1.5 MB YAML each (generated in the test); after the walk of a playbook that includes all of them, `runtime.GC()` then `HeapAlloc` stays under a small multiple of one file's parse (assert `< 3 * peakOfOneParse`, measured in the same test). Keep it out of `-short` if it takes more than a few seconds.
- [ ] **Step 2: Run** it; it fails.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `./internal/varsource/` (all golden tests unchanged); all pass.
- [ ] **Step 5: Commit** `perf(varsource): drop each role's parse tree once its results are extracted`.

### Task 12: Editor-playbook variable sources are bounded and not loaded by list routes (7.114)

**Files:**
- Modify: `internal/playbooks/varsource.go` (`inlineSource`), the playbook and job-template list queries in `internal/db/queries/` (then `make sqlc`) and their stores/handlers
- Test: `internal/playbooks/varsource_test.go`, an API test for the list routes

**Values:** the stored inline result is capped at 1 MiB with the same encoder and `incomplete` / `LeftOut` semantics as Task 6 (reuse it). List routes (`GET /orgs/{org}/playbooks`, `GET /orgs/{org}/job-templates`) neither select nor decode the variable-source column; whatever the list DTO shows today from it (if anything, e.g. a count) comes from a cheap column or is dropped if no SPA code reads it — check `web/src/features/` before dropping, and keep the SPA green.

- [ ] **Step 1: Write the failing tests** `TestInlineSourceCapsResult` (the record's 67 101-byte alias text → stored result ≤ 1 MiB, marked incomplete) and a list-route test asserting the list query does not read the column (the store fake records which fields were requested, or the SQL test checks the generated query text).
- [ ] **Step 2: Run** them; they fail.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `./internal/playbooks/`, `./internal/api/ -run 'Playbook|JobTemplate' -timeout 1800s`, and the SPA tests for the playbook and template pages; all pass.
- [ ] **Step 5: Commit** `fix(playbooks): bound editor-playbook variable sources and keep them out of lists`.
