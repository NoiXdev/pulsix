# Session lock, expiry warning and new-version notice — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Lock a session with a PIN (enforced by the server), warn before a session expires and renew it in place, and tell an open tab when the manager was upgraded (backlog 3.93, 3.94, 3.95).

**Architecture:** The lock is a session state (`sessions.locked_at`) read with every session lookup and refused by the shared session gates in `internal/api/session_gates.go`, the way `must_change_password` already is; its rules live in a new `internal/screenlock` service. Renewal re-proves password plus second factor (or a passkey) for the session's own user and replaces the session. The SPA reacts to a `session_locked` refusal like it already reacts to `password_change_required`, keeps an expiry timer from `/auth/me`, and polls `/setup/status` for the version.

**Tech Stack:** Go (huma, sqlc, pgx, go-webauthn), PostgreSQL migrations (goose), React + TanStack Router/Query, Vitest, Playwright.

**Spec:** `docs/superpowers/specs/2026-11-17-session-lock-expiry-version-design.md` — read it first; it is the authority on behaviour. This plan fixes names, files and tests.

## Global Constraints

- Layers: handlers decode/call/encode only; lock and renewal rules in services behind interfaces declared next to them; stores are sqlc. Clock injected.
- Migration additive, next free number. Main is at `00106`; another session works on main in parallel — rebase onto main before the migration task and take the next free number then.
- The PIN, passwords and codes never appear in logs, audit summaries or error texts.
- PIN: digits only, 4 to 8 (`^[0-9]{4,8}$`). Wrong-PIN limit: **5**, then the session is revoked.
- Refusal detail strings are contract: `session_locked` (403), `lock_pin_missing` (409).
- Warning lead time **5 minutes**; version poll **5 minutes** plus on tab visibility.
- Release-notes link: `https://github.com/NoiXdev/pulsix/releases/tag/v<version>`, only when `<version>` matches `^\d+\.\d+\.\d+(-[0-9A-Za-z.]+)?$`.
- Every user-visible string in `web/src/i18n/en` and `web/src/i18n/de`.
- Tests: `make test` (never a whole-module `go test ./...`); single packages as `go test -race -timeout 900s ./internal/<pkg>/...` (`internal/api` with `-timeout 1800s`). `golangci-lint run` clean. SPA: `npm test`, `npm run lint`, `npx tsc -b`. Coverage: auth ≥ 90 %, new `internal/screenlock` ≥ 90 %.
- Regenerate after API changes: `make sqlc` (queries), `cd web && npm run api:gen` (SPA client).
- Conventional Commits, footer `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. No push, no tag.
- Docker: ~50 foreign containers run on this machine — touch only what this work starts. Testcontainers are fine.

## Review Focus

1. **Parallel wrong PINs.** Ten unlock requests fired at once with wrong PINs must not get more than 5 guesses: the attempt is claimed atomically before the hash is checked (Task 2 test `TestUnlockClaimsAttemptsAtomically`).
2. **A second tab of the same browser.** A tab that did not click lock must land on the lock screen on its next request, and must not show the page's data meanwhile after a reload (Task 6 tests).
3. **Lock vs. forced password change.** An administrator sets a temporary password on an account whose session is locked: every gate answers `session_locked`, not `password_change_required` (Task 3 test `TestLockWinsOverPasswordChange`).
4. **Renewal with another account's passkey.** Must be refused; the current session stays as it was (Task 5 test `TestReauthRefusesOtherUsersPasskey`).
5. **Version poll while the server restarts.** `/setup/status` failing (network error, 502) must not show the dialog or reset the remembered version (Task 9 test).

---

### Task 1: Schema, session lookup and principal

**Files:**
- Create: `internal/db/migrations/00107_session_lock.sql` (or the next free number)
- Modify: `internal/db/queries/auth.sql` (`GetSession` returns the new columns; new queries below), regenerate `internal/db/gen`
- Modify: `internal/auth/session.go` (`Principal`, `Lookup`, `StillValid`)
- Modify: `internal/api/job_sse.go:207` (stream ends on a locked session)
- Test: `internal/auth/session_test.go` (or a new `session_lock_sql_test.go` against `internal/db/testdb`), `internal/api` job SSE test

**Interfaces:**
- Produces: columns `users.lock_pin_hash text NULL`, `users.lock_pin_prompted_at timestamptz NULL`, `sessions.locked_at timestamptz NULL`, `sessions.unlock_failures integer NOT NULL DEFAULT 0`.
- Produces: `auth.Principal.Locked bool`, `auth.Principal.ExpiresAt time.Time`; `auth.ErrSessionLocked` (sentinel); `(*Sessions).StillValid` returns `ErrSessionLocked` for a locked session.
- Produces sqlc queries (names fixed, SQL the implementer's): `LockSession(id) :execrows` (sets `locked_at = now()`, `unlock_failures = 0`, only on a live, unlocked session), `ClaimUnlockAttempt(id) :one` (atomically `unlock_failures = unlock_failures + 1` where `< 5` and locked; returns the new count; no row when exhausted or not locked), `UnlockSession(id) :execrows` (clears both), `GetLockPIN(user_id) :one` (hash and prompted_at), `SetLockPIN(user_id, hash) :exec` (also sets `lock_pin_prompted_at = now()`), `RemoveLockPIN(user_id) :exec` (hash to NULL, prompted_at kept), `ClearLockPIN(user_id) :exec` (hash and prompted_at to NULL — the admin reset), `MarkLockPINPrompted(user_id) :exec`.

- [ ] **Step 1: Write the failing tests**
  - `TestLookupCarriesLockAndExpiry`: create a session, `LockSession`, `Lookup` → `p.Locked == true`, `p.ExpiresAt` equals the created session's `expires_at` (to the second).
  - `TestStillValidRefusesLockedSession`: locked → `errors.Is(err, auth.ErrSessionLocked)`.
  - `TestJobLogStreamEndsWhenSessionLocks` (internal/api): open a job log stream with a cookie session, lock the session, the stream ends within one validity poll.
- [ ] **Step 2: Run them** — `go test -race -timeout 900s ./internal/auth/...` → FAIL (columns/fields missing).
- [ ] **Step 3: Migration, queries, `make sqlc`, then `Principal`/`Lookup`/`StillValid`; in `job_sse.go` treat `ErrSessionLocked` like `ErrUnauthenticated`.**
- [ ] **Step 4: Run** `go test -race -timeout 900s ./internal/auth/... ./internal/db/...` and `go test -race -timeout 1800s -run JobLog ./internal/api/` → PASS.
- [ ] **Step 5: Commit** `feat(auth): carry a session's lock state and expiry`

### Task 2: `internal/screenlock` service and store

**Files:**
- Create: `internal/screenlock/service.go`, `internal/screenlock/store_sql.go`, tests `service_test.go` (fake store) and `store_sql_test.go` (testdb)

**Interfaces:**
- Consumes: Task 1 queries; `auth.HashPassword(string) (string, error)`, `auth.VerifyPassword(hash, pw string) (bool, error)`.
- Produces:
  ```go
  const MaxUnlockFailures = 5
  var (ErrInvalidPIN, ErrNoPIN, ErrWrongPIN, ErrExhausted, ErrNotLocked, ErrNotFound error)
  type Status struct{ PINSet, Prompt bool } // Prompt: no PIN and never prompted
  type Store interface { /* the Task 1 queries plus RevokeSession(id) */ }
  func NewService(store Store) *Service
  func (s *Service) Status(ctx, userID uuid.UUID) (Status, error)
  func (s *Service) SetPIN(ctx, userID uuid.UUID, pin string) error      // ErrInvalidPIN
  func (s *Service) RemovePIN(ctx, userID uuid.UUID) error
  func (s *Service) SkipPrompt(ctx, userID uuid.UUID) error
  func (s *Service) Lock(ctx, userID, sessionID uuid.UUID) error          // ErrNoPIN
  // Unlock: right PIN → nil. Wrong → ErrWrongPIN with AttemptsLeft > 0,
  // or ErrExhausted after revoking the session. Not locked → ErrNotLocked.
  func (s *Service) Unlock(ctx, userID, sessionID uuid.UUID, pin string) (attemptsLeft int, err error)
  ```
- Unlock order (the algorithm the tests pin): claim the attempt (`ClaimUnlockAttempt`) → no row means exhausted (revoke, `ErrExhausted`) → verify the PIN → right: `UnlockSession` → wrong: count == 5 → revoke, `ErrExhausted`; else `ErrWrongPIN`, `5 - count` left. A PIN that does not match `^[0-9]{4,8}$` still spends an attempt (no free probes).

- [ ] **Step 1: Failing tests** (table-driven where it fits):
  - `TestSetPINRejectsNonDigitsAndLength` — `"123"`, `"123456789"`, `"12a4"`, `" 1234"` → `ErrInvalidPIN`; `"1234"`, `"12345678"` ok; stored value is not the PIN (`!= "1234"`) and verifies with `auth.VerifyPassword`.
  - `TestSetPINMarksPrompted`, `TestSkipPromptClearsPromptWithoutPIN`, `TestRemovePINKeepsPromptedOff` (removing does not re-offer the sign-in step: `RemovePIN` uses `RemoveLockPIN`; `ClearLockPIN` is the admin reset's).
  - `TestLockWithoutPINIsErrNoPIN`.
  - `TestUnlockRightPINClearsLockAndFailures`.
  - `TestUnlockWrongPINCountsDown` — attempts left 4,3,2,1, then the 5th wrong → `ErrExhausted` and the session is revoked (`Lookup` fails).
  - `TestUnlockAfterRightPINResetsCounter` — 3 wrong, 1 right, lock again → 5 attempts again.
  - `TestUnlockClaimsAttemptsAtomically` (testdb) — 10 goroutines with wrong PINs on one locked session → exactly 5 `ClaimUnlockAttempt` successes, session revoked, no goroutine gets `nil`.
  - `TestUnlockNotLocked` → `ErrNotLocked`.
- [ ] **Step 2: Run** `go test -race -timeout 900s ./internal/screenlock/...` → FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** again → PASS; coverage ≥ 90 % (`go test -cover`).
- [ ] **Step 5: Commit** `feat(screenlock): lock a session with a PIN`

### Task 3: Session gates refuse a locked session; `/auth/me` fields

**Files:**
- Modify: `internal/api/session_gates.go` (new `refuseWhileLocked`, applied in `requireEnrolmentSession`, `requireMFAComplete`, `requirePasswordChangeSession`; `requireOrg` in `internal/api/authz.go`; `/auth/totp/verify` in `internal/api/mfa_handlers.go`)
- Modify: `internal/api/auth_handlers.go` (`meOutput`), `Deps` gains `ScreenLock`
- Test: `internal/api/session_lock_gates_test.go` (through `apitest`)

**Interfaces:**
- Consumes: `Principal.Locked`, `Principal.ExpiresAt`; `screenlock.Service.Status`.
- Produces: `const sessionLocked = "session_locked"`; `func refuseWhileLocked(p auth.Principal) error` (nil for a bearer token); `/auth/me` body gains `locked bool`, `lock_pin_set bool`, `lock_pin_prompt bool`, `session_expires_at string (date-time)`.
- Order inside each gate: session → locked → MFA-complete → password change. The lock is checked **before** `refuseUntilPasswordChanged`.

- [ ] **Step 1: Failing tests:**
  - `TestLockedSessionIsRefusedEverywhere` — table over one route per gate: `GET /api/v1/orgs` (MFA-complete), an org route (`requireOrg`), an instance-admin route, `POST /api/v1/auth/totp/enrol` (enrolment), `POST /api/v1/auth/password`, `POST /api/v1/auth/totp/verify`, the backup download, `GET /api/explorer`, `GET /api/openapi.json` → all 403 with detail `session_locked`.
  - `TestLockedSessionReachesMeAndLogout` — `/auth/me` 200 with `locked: true`; `/auth/logout` 200.
  - `TestLockWinsOverPasswordChange` — locked session, then the account gets `must_change_password` → `GET /api/v1/orgs` is `session_locked`.
  - `TestBearerTokenIsNeverLocked` — a token of a user whose cookie session is locked still works.
  - `TestMeReportsExpiryAndPINState` — `session_expires_at` within a second of the session's expiry; `lock_pin_set`/`lock_pin_prompt` follow `screenlock.Status`.
- [ ] **Step 2: Run** `go test -race -timeout 1800s -run 'Locked|LockWins|Bearer.*Locked|MeReports' ./internal/api/` → FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** the same, then the whole `internal/api` package → PASS.
- [ ] **Step 5: Commit** `feat(api): refuse every route but unlock to a locked session`

### Task 4: Lock routes, audit and the admin reset

**Files:**
- Create: `internal/api/screenlock_handlers.go`, `internal/api/screenlock_handlers_test.go`
- Modify: `internal/api/audit_middleware.go` (instance-level failure audit), `internal/api/audit_coverage_test.go` (allowlist only where justified), `internal/accounts/actions.go:86` (`ResetSecondFactor` also clears the PIN), `cmd/manager` wiring
- Test: as above, plus `internal/accounts` reset test

**Interfaces:**
- Consumes: Task 2 service; `requireFreshFactor(ctx, deps, p)` (existing step-up check).
- Produces routes (cookie sessions only; a bearer token gets 403 like the other credential routes):

  | Route | Gate | Answer |
  |---|---|---|
  | `PUT /api/v1/auth/lock/pin` `{pin}` | `requireInteractiveSession`; `requireFreshFactor` **only if** `Status.PINSet` | 200; 422 on `ErrInvalidPIN` |
  | `DELETE /api/v1/auth/lock/pin` | `requireInteractiveSession` + `requireFreshFactor` | 200 |
  | `POST /api/v1/auth/lock/prompt-skipped` | `requireInteractiveSession` | 200 |
  | `POST /api/v1/auth/lock` | `requireInteractiveSession` | 200; 409 `lock_pin_missing` |
  | `POST /api/v1/auth/lock/unlock` `{pin}` | `requireSession`, cookie only, `withAuthRateLimit` | 200; 422 `{detail:"wrong pin", attempts_left:n}`; 401 + cleared cookie on `ErrExhausted`; 409 on `ErrNotLocked` |

- Audit: `withAudit` actions `setLockPin`, `removeLockPin`, `skipLockPinPrompt`, `lockSession`, `unlockSession` (object type `session` or `user`, object id the session/user id). Failures through a new `recordInstanceAuditFailure(ctx, deps, userID uuid.UUID, action, objectType, objectID, summary string)` — `OrgID` nil, recorded the way `withAudit` records a route that has no organization (e.g. `confirmTOTP`): actions `unlockSessionFailed` (summary `"wrong PIN, N attempts left"`) and `unlockSessionExhausted`.
- `accounts.Service.ResetSecondFactor` clears `lock_pin_hash` and `lock_pin_prompted_at` (`ClearLockPIN`) in its transaction; `SetTemporaryPassword` does not touch them.

- [ ] **Step 1: Failing tests:**
  - `TestFirstPINNeedsNoStepUpButReplacingDoes` — a session older than `auth.StepUpWindow` sets its first PIN (200); setting it again → 403 `step_up_required`.
  - `TestLockWithoutPINIs409`, `TestLockThenUnlock` (lock → `GET /api/v1/orgs` 403 `session_locked` → unlock with right PIN → 200).
  - `TestUnlockWrongPINReportsAttemptsLeft` (422, `attempts_left` 4) and `TestFifthWrongPINEndsSession` (401, `Set-Cookie` clears, `/auth/me` 401 afterwards).
  - `TestUnlockFailuresAreAudited` — two audit events, never containing the PIN (search every stored field for the PIN string).
  - `TestAdminFactorResetClearsLockPIN`, `TestTemporaryPasswordKeepsLockPIN` (internal/accounts or internal/api).
  - `TestEveryMutatingRouteIsAuditedOrAllowlisted` keeps passing.
- [ ] **Step 2: Run** → FAIL.
- [ ] **Step 3: Implement and wire** (`cmd/manager` builds `screenlock.NewService` and puts it in `Deps`).
- [ ] **Step 4: Run** `go test -race -timeout 1800s ./internal/api/` and `go test -race -timeout 900s ./internal/accounts/... ./internal/screenlock/...` → PASS. `golangci-lint run` → 0 issues.
- [ ] **Step 5: Commit** `feat(api): lock and unlock a session, set the lock PIN`

### Task 5: Session renewal and user-bound passkey re-authentication

**Files:**
- Modify: `internal/auth/service.go` (`Renew`), `internal/auth/session.go` (`Replace`), `internal/auth/passkey.go` (`BeginReauth`, `FinishReauth`, ceremony kind `"reauth"`), `internal/db/queries/auth.sql` if a query is needed
- Create: `internal/api/session_renew_handlers.go`, tests in `internal/auth` and `internal/api`

**Interfaces:**
- Produces:
  ```go
  // VerifyOwnPassword checks userID's password with Login's rules (disabled,
  // expired temporary password → ErrInvalidCredentials), no session created.
  func (s *Service) VerifyOwnPassword(ctx context.Context, userID uuid.UUID, password string) error
  // Replace creates a new MFA-complete session (mfa_at now, fresh SessionTTL)
  // for userID and revokes oldID in one transaction; old not live → ErrNotFound.
  func (s *Sessions) Replace(ctx context.Context, userID, oldID uuid.UUID) (cookie string, err error)
  func (p *Passkeys) BeginReauth(ctx context.Context, userID uuid.UUID) (options json.RawMessage, ceremonyID uuid.UUID, err error)
  // FinishReauth verifies an assertion of one of userID's own credentials and
  // updates its counter; any other account's credential → ErrInvalidCredentials.
  func (p *Passkeys) FinishReauth(ctx context.Context, userID, ceremonyID uuid.UUID, response json.RawMessage) error
  ```
- Routes (all `requireInteractiveSession` — so a locked session is refused — and `withAuthRateLimit`):
  - `POST /api/v1/auth/session/renew` `{password, code?, recovery_code?}` → `VerifyOwnPassword` first (a wrong password spends no factor attempt), then `verifyFactorCode`, then `Sessions.Replace`; `Set-Cookie` with the new session; body `{session_expires_at}`. Audit `renewSession`.
  - `POST /api/v1/auth/passkeys/reauth/begin` → `{ceremony_id, options}` (allowlisted in the audit coverage test: a ceremony start changes nothing).
  - `POST /api/v1/auth/passkeys/reauth/finish` `{ceremony_id, response, purpose: "step_up"|"renew"}` → `FinishReauth`; then `step_up` → `Enrolment.CompleteVerifiedSession` (refreshes `mfa_at`, rotates the cookie, same session); `renew` → `Sessions.Replace`. Audit `renewSession` for renew; step-up allowlisted like `/auth/totp/verify`.

- [ ] **Step 1: Failing tests:**
  - `TestRenewReplacesTheSession` — new cookie works, old cookie 401, new `expires_at` ≈ now + 12 h.
  - `TestRenewWrongPasswordSpendsNoFactorAttempt` — wrong password then right password + right code succeeds; the factor attempt counter unchanged by the first call.
  - `TestRenewWithRecoveryCode`, `TestRenewReplayedTOTPCodeFails` (a code used at sign-in is refused).
  - `TestRenewRefusedWhileLocked` (403 `session_locked`), `TestRenewRefusesBearerToken`.
  - `TestReauthRefusesOtherUsersPasskey` (internal/auth, virtual authenticator as in `passkey_test.go`): user B's assertion on user A's ceremony → `ErrInvalidCredentials`; A's session unchanged.
  - `TestPasskeyStepUpKeepsTheSession` — `purpose: step_up` → same session id, `mfa_at` refreshed, no second session row.
  - `TestPasskeyRenewReplacesTheSession`.
- [ ] **Step 2: Run** → FAIL.
- [ ] **Step 3: Implement**, reusing `openLoginSession`'s owner check and counter update rather than copying them.
- [ ] **Step 4: Run** `go test -race -timeout 900s ./internal/auth/...`, `go test -race -timeout 1800s ./internal/api/` → PASS; `golangci-lint run` clean.
- [ ] **Step 5: Commit** `feat(auth): renew a session in place and re-prove a passkey for the own account`

### Task 6: SPA — client regen, lock screen, lock icon

**Files:**
- Modify: `web/src/api/schema.d.ts` (`npm run api:gen`), `web/src/api/client.ts` (`SESSION_LOCKED`, `onSessionLocked`), `web/src/routes/RootLayout.tsx`, `web/src/ui/shell.tsx` (lock button)
- Create: `web/src/api/hooks/useScreenLock.ts` (mutations: lock, unlock, set PIN), `web/src/features/lock/LockScreen.tsx`, `web/src/features/lock/SetPinDialog.tsx`, `web/src/features/lock/lockChannel.ts` (BroadcastChannel `pulsix-session`, messages `{type:'locked'}`, `{type:'unlocked'}`, `{type:'renewed', expiresAt}` — Task 8 reuses it), tests beside each, i18n `lock.*` in en and de

**Interfaces:**
- Produces: `onSessionLocked(listener): () => void` (same shape as `onPasswordChangeRequired`); `useLockState(): { locked: boolean; setLocked(b): void }` (context provided in `RootLayout`); `postSessionMessage(msg)` / `onSessionMessage(cb)` in `lockChannel.ts`.
- Behaviour: when locked, `RootLayout` renders `<LockScreen />` **instead of** `<Outlet />` (the routed page unmounts — its data leaves the DOM) and clears the TanStack Query cache except `['me']`. Unlock → refetch active queries. The shell's lock button: label `t.shell.lockSession`; with `me.lock_pin_set` false it opens `SetPinDialog` (PIN twice, must match, digits 4–8, then sets and locks).
- Lock screen: PIN input (`inputMode="numeric"`, `autoComplete="off"`), "Unlock", "Sign out", attempts left after a wrong PIN; on 401 (exhausted) → `/login` with the notice `t.lock.endedAfterFailures`.

- [ ] **Step 1: Failing tests** (Vitest + MSW as the existing tests do):
  - `client.test.ts`: a 403 `session_locked` calls `onSessionLocked` listeners and still throws.
  - `RootLayout.test.tsx`: after a `session_locked` refusal the outlet's content is gone and the lock screen shows; `/auth/me` `locked: true` on load → lock screen without rendering the page first.
  - `LockScreen.test.tsx`: wrong PIN shows "4 attempts left"; right PIN unmounts it; exhausted goes to `/login`.
  - `SetPinDialog.test.tsx`: mismatch and `"12a4"` are refused client-side; matching PIN calls set then lock.
  - `shell.test.tsx`: the lock button exists with its accessible name.
  - `lockChannel.test.ts`: a `locked` message from another tab switches to the lock screen.
- [ ] **Step 2: Run** `cd web && npx vitest run src/features/lock src/routes/RootLayout.test.tsx src/api/client.test.ts src/ui/shell.test.tsx` → FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `npm test`, `npm run lint`, `npx tsc -b` → PASS.
- [ ] **Step 5: Commit** `feat(web): lock the session from the top bar`

### Task 7: SPA — PIN on the account security page and after sign-in

**Files:**
- Modify: `web/src/features/account/AccountSecurityPage.tsx` (section "Lock PIN": set / change / remove; change and remove through `useStepUp`), `web/src/routes/RootRedirect.tsx` (after the password-change check: `me.lock_pin_prompt` → `/account/lock-pin`), router registration of the new route
- Create: `web/src/features/lock/LockPinPromptPage.tsx` (bare page like `EnrolmentLayout`: PIN twice, "Set PIN", "Skip"; both continue to `/`), tests, i18n

**Interfaces:** Consumes Task 6's `useScreenLock` hooks and Task 4's routes.

- [ ] **Step 1: Failing tests:** `RootRedirect.test.tsx` — `lock_pin_prompt: true` → `/account/lock-pin`; false → the org. `LockPinPromptPage.test.tsx` — "Skip" posts `prompt-skipped` then navigates to `/`; "Set PIN" puts the PIN then navigates. `AccountSecurityPage.test.tsx` — without a PIN shows "Set"; with one shows "Change"/"Remove", and a `step_up_required` answer opens the step-up dialog.
- [ ] **Step 2: Run** → FAIL. **Step 3: Implement.** **Step 4: Run** `npm test`, lint, tsc → PASS.
- [ ] **Step 5: Commit** `feat(web): set the lock PIN at sign-in and on the account page`

### Task 8: SPA — expiry warning, in-place renewal, passkey step-up fix

**Files:**
- Create: `web/src/features/session/ExpiryWatcher.tsx` (mounted in `RootLayout` for an MFA-complete, unlocked session), `web/src/features/session/RenewDialog.tsx`, `web/src/features/auth/passkeys.ts` additions (`reauthWithPasskey(purpose: 'step_up' | 'renew', t)`), tests, i18n `session.*`
- Modify: `web/src/features/account/StepUpDialog.tsx` (passkey button calls `reauthWithPasskey('step_up')` instead of `loginWithPasskey`)

**Interfaces:**
- Consumes: `me.session_expires_at`; Task 5 routes; `lockChannel` (`renewed`).
- Behaviour: the timer fires at `expires_at − 5 min` (immediately if already inside the window), shows `RenewDialog` with a mm:ss countdown; password + TOTP code (or recovery code), or passkey. Success: dialog closes, `['me']` refetched, `postSessionMessage({type:'renewed'})` so other tabs close theirs. Reaching zero: clear the query cache, `/login` with notice `t.session.expired`. Not shown while locked. `/auth/me` is re-read on tab focus and after every step-up (the `useStepUp` success path).
- Clock skew: compute the remaining time from the server's `Date` response header of the `/auth/me` answer, not the local clock alone.

- [ ] **Step 1: Failing tests** (fake timers): dialog appears at 5:00 left, not at 5:01; renew success closes it and refetches `me`; wrong password shows the error and keeps the dialog; a `renewed` message from another tab closes it; zero → `/login` with the notice; locked → no dialog; `StepUpDialog` passkey path posts `/auth/passkeys/reauth/*` with `purpose: 'step_up'` and never `/auth/passkeys/login/*`.
- [ ] **Step 2: Run** → FAIL. **Step 3: Implement.** **Step 4: Run** `npm test`, lint, tsc → PASS.
- [ ] **Step 5: Commit** `feat(web): warn before the session ends and renew it in place`

### Task 9: SPA — new-version notice

**Files:**
- Create: `web/src/features/version/VersionWatcher.tsx` (mounted in the app shell, not on `/login`, `/setup` or the lock screen), `VersionDialog.tsx`, `VersionBanner.tsx`, tests, i18n `version.*`
- Modify: `web/src/api/hooks/useSetupStatus.ts` only if a polling variant is needed (`refetchInterval: 5 * 60_000`, `refetchOnWindowFocus: true` scoped to the watcher's own query key)

**Behaviour:** remember the first successfully read `version`; on a later successful read with a different non-empty version → dialog "Pulsix was updated to v{version}" with "Reload" (`location.reload()`), "Later", and the release-notes link (only for a version matching the Global Constraints pattern). "Later" → banner with "Reload" until reload; the dialog is not shown again for that version (sessionStorage key, wrapped in try/catch). Failed reads change nothing.

- [ ] **Step 1: Failing tests:** same version → nothing; new version → dialog with link `…/releases/tag/v0.1.0-rc.4`; version `local-abc123` → dialog without a link; "Later" → banner, no dialog on the next poll; a 502 or network error between two reads → nothing, and the remembered version is kept.
- [ ] **Step 2: Run** → FAIL. **Step 3: Implement.** **Step 4: Run** `npm test`, lint, tsc → PASS.
- [ ] **Step 5: Commit** `feat(web): tell an open tab when the server was upgraded`

### Task 10: End-to-end, backlog, user docs

**Files:**
- Create: `web/e2e/session-lock.spec.ts`, `web/e2e/session-expiry.spec.ts`, `web/e2e/version-notice.spec.ts`
- Modify: `docs/BACKLOG.md` (rows 3.93, 3.94, 3.95 → done, with what was built), `docs/upgrades.md` only if the migration needs a note
- Docs repo: scratch clone `/private/tmp/claude-501/-Users-noidee--dev-pulsix/76b69f36-5040-49f9-a80e-08a1ef63bcdb/scratchpad/noix-docs-merge`, new branch `docs/pulsix-session-lock` from its `main`: account-security page (lock PIN), a "Locking your session" section, the expiry dialog, the update notice, changelog "Unreleased" line. Never `~/_dev/noix-docs`. No tech-stack names.

**E2E:**
- Lock: sign in (helpers `enrol.ts`/`otp.ts`), skip or set PIN on the prompt page, lock from the top bar, reload → still the lock screen, open a second page in the same context → lock screen, wrong PIN shows attempts left, right PIN → back on the page.
- Expiry: `page.route('**/api/v1/auth/me', …)` rewrites `session_expires_at` to now + 4 min → dialog shows; renew with password and a fresh TOTP code (wait for the next window as `otp.ts` does) → dialog gone, the old cookie value now gets 401 from `/api/v1/auth/me` (request with the saved cookie).
- Version: `page.route('**/api/v1/setup/status', …)` returns a different version on the second call; trigger by `visibilitychange` (or a short poll via `page.clock`) → dialog → "Later" → banner.

- [ ] **Step 1: Write the specs.** **Step 2: Run** `cd web && npm run e2e -- session-lock session-expiry version-notice` → PASS (ports 55432, 45025, 48025, 48444, 48090 belong to the e2e compose project; nothing else is touched).
- [ ] **Step 3: Full verification:** `make test` (read the log: exit line, FAIL lines, ok count), `golangci-lint run`, `cd web && npm test && npm run lint && npx tsc -b && npm run e2e`.
- [ ] **Step 4: Commit** `test(e2e): lock, expiry and version notice` and `docs: backlog 3.93–3.95`; docs repo commit `docs(pulsix): session lock, expiry warning, update notice` on its branch (not pushed).
