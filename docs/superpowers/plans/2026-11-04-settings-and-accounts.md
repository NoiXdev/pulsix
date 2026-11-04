# Settings navigation, account administration and organizations — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Settings get a grouped sidebar; shared resources get list and form pages; instance admins get a Users page, an account page with password, second-factor, session and activation actions, and an Organizations page; people get forgot and change password; only instance admins create organizations.

**Architecture:** A new Go domain package `internal/accounts` owns everything account-administrative and password-related: admin reads, admin actions, reset tokens, password change, and notice mails. It sits behind a `Store` interface, with a SQL store and fakes. `internal/auth` gains the shared password floor, the `MustChangePassword` principal flag and the sign-in stamp. The API adds `/admin/users…`, `/admin/orgs`, `/auth/password…`. The SPA adds a settings layout with a side navigation and reuses the 7.65 building blocks (`DetailLayout`, `Section`, `SidebarFacts`, `ListRow`, `RowMenu`, `Switch`).

**Tech Stack:**
- Go 1.27, huma v2, sqlc (`go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1 generate`), goose, Postgres 18 with RLS.
- React/TS with TanStack Router and Query, Tailwind, Vitest, Playwright.
- `npm run api:gen` regenerates `web/src/api/schema.d.ts`.

**Spec:** `docs/superpowers/specs/2026-11-04-settings-and-accounts-design.md`

## Global Constraints

- **Language and copy:**
  - Code, comments, commits and docs are English.
  - Every new user-visible string has en and de i18n (`web/src/i18n/{en,de}/*.ts`).
  - Public docs never name the tech stack.
- **Layering** (CLAUDE.md): handler (decode, call service, encode) → `internal/<domain>` service (no HTTP, no SQL) → store behind an interface declared next to the service. The clock and randomness are injected.
  - SPA layering is `src/api` → `src/features` → `src/ui`; `ui` fetches nothing.
- **Migrations:** additive only. The next file is `internal/db/migrations/00080_accounts.sql`.
- **Tests and checks:**
  - Before every commit: `make test` (never a module-wide `go test ./...`; single packages with `-timeout 900s`, `internal/api` with `-timeout 1800s`), `golangci-lint run`, and `gofmt -s -l .` empty.
  - SPA: `cd web && npx vitest run`, `npx eslint .`, `npx tsc -b`. Node lives at `$HOME/.nvm/versions/node/v24.14.0/bin`.
- **Commits:** Conventional Commits, footer `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. Never push, never tag.
- **Values pinned by the spec:**
  - password floor **12** characters, everywhere;
  - temporary password lifetime **24 h**;
  - reset link lifetime **60 min**, single-use, stored as a SHA-256 hash; a newer link voids older ones and a completed reset voids all;
  - the reset link is `{PublicURL}/reset-password#token=<token>`, with the token in the **fragment**;
  - step-up is the existing `requireFreshFactor` (`stepUpRequired = "step_up_required"`).
- **Account actions:**
  - Instance admin only (`requireInstanceAdmin`), cookie session only, step-up required.
  - Refused on one's own account: `409`, detail `not_on_own_account`.
  - Audited with org nil, the admin as actor and the account as object, the way the existing deactivate route is (`internal/api/member_handlers.go` `registerAdminRoutes`).
- **Secrets:**
  - No password, temporary password or reset token ever goes into a log line, an audit entry, an error message or a URL the server receives.
  - The temporary-password response carries `Cache-Control: no-store`.
- **Must-change-password gate:**
  - It is the account flag `users.must_change_password`, read per request through the session lookup, and it fails closed.
  - It applies to cookie sessions however they were signed in, a passkey included.
  - API tokens are not gated. They do not sign in with a password, and "end all sessions" also leaves tokens alone. *(Controller's ruling.)*
  - Refusal: `403`, detail `password_change_required`.
- **Organizations:** only instance admins create one (`POST /orgs` → `requireInstanceAdmin`).
- The shared job template page keeps `features/inventory/schema/SchemaBuilder`. Do not build a second variable editor (backlog 7.75 replaces it).

## Review Focus

1. **A must-change session reaching a route with its own gate** (stream, download, explorer, org-scoped group) must get 403 `password_change_required`, except for the allow-list. Pinned by Task 2's route walk.
2. **A reset link used twice, used after a newer link was issued, or used after 60 min** fails with 400 and leaves the password unchanged. Pinned in Task 4.
3. **An admin sets a temporary password or resets the factor while the person is signed in elsewhere:** that browser's next request is 401. Pinned in Task 3.
4. **Forgot password for an unknown, deactivated or invite-pending address** gets the same status and body as for a real one, and no mail is sent. Pinned in Task 4.
5. **Admin actions on edge accounts:**
   - the admin's own account → 409 `not_on_own_account`, for every action;
   - an invite-pending account → 409 `invite_pending`, for everything except deactivate;
   - deactivating the last active instance admin → still refused.

   Pinned in Task 3.

---

### Task 1: Accounts migration, shared password floor, sign-in stamp

**Files:**
- Create: `internal/db/migrations/00080_accounts.sql`
- Modify:
  - `internal/db/queries/auth.sql`: `CreateSession`, `MarkSessionMFAComplete`, `GetSession`;
  - `internal/auth/password.go`;
  - `internal/setup/service.go` (`minPasswordLength`);
  - `internal/tenancy/service.go` (`minInvitePasswordLen`);
  - `internal/auth/session.go`;
  - regenerate `internal/db/gen`.
- Test: `internal/auth/session_test.go` or a new `internal/auth/last_sign_in_sql_test.go`, `internal/tenancy/service_test.go`, `internal/setup/service_test.go`

**Interfaces:**
- Produces:
  - `auth.MinPasswordLength = 12` (exported const in `internal/auth/password.go`).
  - `func auth.CheckPasswordPolicy(pw string) error`, which returns `auth.ErrPasswordTooShort` (wrapping `auth.ErrInvalidInput`) when `utf8.RuneCountInString(pw) < 12`.
  - `auth.Principal.MustChangePassword bool`, filled by `Sessions.Lookup` from `GetSession`. Its enforcement is Task 2.
  - Columns `users.must_change_password boolean NOT NULL DEFAULT false`, `users.temp_password_expires_at timestamptz NULL`, `users.last_sign_in_at timestamptz NULL`.
  - Table `password_reset_tokens(id uuid PK, user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE, token_hash bytea NOT NULL UNIQUE, created_at timestamptz NOT NULL DEFAULT now(), expires_at timestamptz NOT NULL, used_at timestamptz NULL)` plus an index on `(user_id, created_at)`. No RLS: like `users` and `sessions`, it is instance-level.

- [ ] **Step 1: Write the failing tests.**
  - `TestCheckPasswordPolicy`, table-driven: 11 characters → `ErrPasswordTooShort`; 12 → nil; 12 multibyte runes (`"äääääääääääää"[:12 runes]`) → nil.
  - `TestAcceptInviteRejectsElevenCharacterPassword`: tenancy `AcceptInvite` with an 11-character password fails. Today 8 passes.
  - `TestSetupRejectsElevenCharacterPassword` stays green, now using the shared constant.
  - `TestLastSignInStampedOnCompletion` (SQL test, testdb):
    - a password session (`Create(…, false)`) leaves `last_sign_in_at` NULL;
    - `MarkMFAComplete` sets it;
    - a second `MarkMFAComplete` on the same, already complete session (a step-up) does **not** move it;
    - `Create(…, true)` (the passkey path) sets it.
  - `TestLookupCarriesMustChangePassword`: with the column set true, `Lookup` returns `MustChangePassword == true`.
- [ ] **Step 2: Run them.** `go test -race -timeout 900s ./internal/auth/ ./internal/tenancy/ ./internal/setup/`. Expected: compile failures or FAIL.
- [ ] **Step 3: Implement.**
  - Write the migration with a Down section.
  - Add `u.must_change_password` to `GetSession`'s select.
  - `CreateSession`: when `mfa_complete` is true, also stamp `users.last_sign_in_at = now()`. Use a data-modifying CTE in the same statement.
  - `MarkSessionMFAComplete`: stamp `last_sign_in_at` only when the row's previous `mfa_complete` was false (a CTE over the old row).
  - Replace `minPasswordLength` and `minInvitePasswordLen` with `auth.MinPasswordLength` / `auth.CheckPasswordPolicy`. Keep each package's own error wording if tests pin it.
  - Regenerate sqlc.
- [ ] **Step 4: Run** the same command plus `go test -race -timeout 900s ./internal/db/...`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(auth): add the accounts columns, one password floor and a sign-in stamp`.

### Task 2: Must-change-password gate and changing one's own password

**Files:**
- Modify:
  - `internal/api/auth_handlers.go`: `requireMFAComplete`, `requireInteractiveSession`, the `/auth/me` DTO;
  - `internal/auth/service.go`: `Login` refuses an expired temporary password.
- Create:
  - `internal/accounts/service.go`: package doc, `Service`, `Store` interface, `Mailer` interface, constructor;
  - `internal/accounts/password.go`: `ChangePassword`;
  - `internal/accounts/store_sql.go`;
  - `internal/api/password_handlers.go`;
  - `internal/db/queries/accounts.sql`;
  - wiring in `internal/api/api.go` (`Deps.Accounts *accounts.Service`) and `cmd/manager`.
- Test: `internal/accounts/password_test.go` (fakes), `internal/accounts/store_sql_test.go`, `internal/api/password_handlers_test.go`, `internal/api/must_change_password_test.go`, `internal/auth/service_test.go`

**Interfaces:**
- Consumes: Task 1's `auth.MinPasswordLength`, `auth.CheckPasswordPolicy`, `Principal.MustChangePassword`, and the columns.
- Produces:
  - `accounts.NewService(store Store, mailer Mailer, publicURL string, now func() time.Time, rnd io.Reader) *Service`.
  - `accounts.Mailer` interface: `Send(ctx, to, subject, body string) error` and `Configured(ctx) (bool, error)`. `*mail.SMTP` satisfies it.
  - `func (s *Service) ChangePassword(ctx, userID, keepSessionID uuid.UUID, current, next string) error`. It verifies `current`; `next` must pass the policy and must differ from `current` (`ErrSamePassword`). It sets the hash, clears `must_change_password` and `temp_password_expires_at`, and revokes every session of the user except `keepSessionID`, all in one transaction.
  - Sentinels: `accounts.ErrInvalidCredentials`, `ErrSamePassword`, `ErrNotFound`, `ErrForbidden`, `ErrNotOnOwnAccount`, `ErrInvitePending`, `ErrMailNotConfigured`, `ErrInvalidResetToken`. Later tasks use the rest.
  - API: `POST /api/v1/auth/password` with body `{current_password, new_password}` → 204. Gate: `requireSession`, then not a token, then MFA-complete, deliberately **not** the must-change gate, then `requireFreshFactor`. Audit `changePassword` with org nil and actor self.
  - `/auth/me` gains `must_change_password: bool`.
  - Gate: `requireMFAComplete` returns `403 password_change_required` when `p.MustChangePassword && p.TokenID == nil`. Routes on `requireSession` (`/auth/me`, `/auth/logout`, `/auth/totp/verify`), the enrolment routes (`requireEnrolmentSession`, which already admits MFA-complete sessions without calling `requireMFAComplete`) and `POST /auth/password` stay reachable.
  - `auth.Service.Login`: a correct password on an account whose `temp_password_expires_at` is in the past → `ErrInvalidCredentials`, after the argon2id verification, as the disabled check does.

- [ ] **Step 1: Write the failing tests.**
  - `TestChangePassword` (fakes), table-driven:
    - wrong current → `ErrInvalidCredentials`;
    - 11-character next → `ErrPasswordTooShort`;
    - next == current → `ErrSamePassword`;
    - success → the store saw `SetPassword(hash≠old, clearMustChange=true)` and `RevokeSessionsExcept(user, keep)`.
  - `TestMustChangePasswordRouteWalk` (apitest): for a cookie session that is MFA-complete with `must_change_password = true`, iterate the registered OpenAPI operations and call each with dummy path params. Every operation answers 403 with detail `password_change_required`, except this allow-list:

    ```
    GET  /api/v1/auth/me
    POST /api/v1/auth/logout
    POST /api/v1/auth/password
    POST /api/v1/auth/totp/verify
    POST /api/v1/auth/totp/enrol
    POST /api/v1/auth/totp/confirm
    POST /api/v1/auth/passkeys/register/begin
    POST /api/v1/auth/passkeys/register/finish
    ```

    Also skip the routes that never take a session: setup, login, passkey login, invite accept, hooks, health, the public `/auth/password/*` routes from Task 4, and the agent and worker endpoints. List them by path prefix in the test, so that a new route defaults to "must answer 403".
  - `TestMustChangePasswordPasskeySession`: a session created already complete (the passkey path) on a must-change account → `GET /api/v1/orgs` is 403 `password_change_required`.
  - `TestMustChangePasswordTokenUnaffected`: a bearer token of a must-change account → its org route answers 200.
  - `TestChangePasswordEndpoint`:
    - 204 on success;
    - the current session still works and another session of the user is now 401;
    - the response and the server log (captured `slog` handler) contain neither password string.
  - `TestLoginRefusesExpiredTemporaryPassword` (auth unit, fake store): with `temp_password_expires_at = now-1s` → `ErrInvalidCredentials`.
- [ ] **Step 2: Run them.** `go test -race -timeout 900s ./internal/accounts/ ./internal/auth/` and `go test -race -timeout 1800s ./internal/api/ -run 'MustChange|ChangePassword'`. Expected: FAIL.
- [ ] **Step 3: Implement as in Interfaces.** Then `cd web && npm run api:gen`.
- [ ] **Step 4: Run** Step 2's commands, then `make test`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(auth,api): gate sessions that must change their password and let people change their own`.

### Task 3: Admin users: reads, actions, notices; tighten deactivate

**Files:**
- Create:
  - `internal/accounts/admin.go`: reads;
  - `internal/accounts/actions.go`: temporary password, second-factor reset, end sessions, reactivate;
  - `internal/accounts/notices.go`: the en and de mail texts;
  - `internal/api/admin_user_handlers.go`.
- Modify:
  - `internal/db/queries/accounts.sql`;
  - `internal/api/member_handlers.go`: the deactivate route gets the step-up and the self-refusal and returns a notice;
  - `internal/tenancy/service.go`: `DeactivateUser` takes `actor uuid.UUID` and refuses `actor == target` with `tenancy.ErrNotOnOwnAccount`.
- Test: `internal/accounts/admin_test.go`, `actions_test.go`, `store_sql_test.go`; `internal/api/admin_user_handlers_test.go`; update `internal/tenancy/service_test.go`.

**Interfaces:**
- Consumes: Task 2's `accounts.Service`, the sentinels and `Mailer`.
- Produces (service):
  - `ListUsers(ctx, actorIsAdmin bool, q, cursor string, limit int) ([]UserSummary, string, error)`:
    - default limit 50, max 200;
    - ordered by email, case-insensitively;
    - `q` matches email or display name, case-insensitive substring.
  - `UserSummary{ID, Email, DisplayName, InstanceAdmin bool, State AccountState, OrgCount int, LastSignInAt *time.Time}`.
  - `AccountState` is a string enum: `active | deactivated | invite_pending | must_change_password | no_second_factor`. Precedence, first match wins: deactivated, then invite_pending (`password_hash IS NULL`), then must_change_password, then no_second_factor, then active.
  - `GetUser(ctx, actorIsAdmin bool, actor, id uuid.UUID) (UserDetail, error)`. `UserDetail` is `UserSummary` plus:
    - `CreatedAt`, `DisabledAt`, `MustChangePassword`, `TempPasswordExpiresAt`;
    - `Memberships []Membership{OrgID, Slug, Name, Role string, ActorIsMember bool}`;
    - `Factors{TOTP bool, Passkeys int, RecoveryCodesLeft int}`;
    - `ActiveSessions int`, counting only complete, unrevoked, unexpired sessions.
  - `SetTemporaryPassword(ctx, actorIsAdmin bool, actor, target uuid.UUID) (TemporaryPassword{Password string, ExpiresAt time.Time}, Notice, error)`:
    - the password is 20 characters from `abcdefghjkmnpqrstuvwxyz23456789`, read from the injected `rnd` and grouped `xxxxx-xxxxx-xxxxx-xxxxx`;
    - it sets the hash, `must_change_password = true` and `temp_password_expires_at = now + 24h`, and revokes all of the target's sessions, in one transaction.
  - `ResetSecondFactor(ctx, actorIsAdmin bool, actor, target uuid.UUID) (Notice, error)`: deletes the TOTP secret, the recovery codes and every passkey, and revokes all sessions, in one transaction.
  - `EndSessions(ctx, actorIsAdmin bool, actor, target uuid.UUID) error`: revokes sessions only, never tokens.
  - `Reactivate(ctx, actorIsAdmin bool, actor, target uuid.UUID) error`: clears `disabled_at`.
  - `NotifyDeactivated(ctx, target uuid.UUID) Notice`: the deactivate handler calls it after `tenancy.DeactivateUser` succeeds; it sends the deactivation notice. Deactivation itself stays in tenancy.
  - Common checks for every action: not an admin → `ErrForbidden`; `actor == target` → `ErrNotOnOwnAccount`; unknown target → `ErrNotFound`; for everything but deactivate, `password_hash IS NULL` → `ErrInvitePending`.
  - `Notice` is a string enum: `sent | not_configured | failed`. It reports the best-effort mail to the person. The mail goes to the person's effective locale, resolved as `/auth/me` resolves it via `internal/locale`. A mail failure is logged without the body and never undoes the action.
- Produces (API, all `requireInstanceAdmin`):
  - `GET /api/v1/admin/users?q=&cursor=&limit=` → `{users: [...], next_cursor}`.
  - `GET /api/v1/admin/users/{userId}`.
  - Actions, each with step-up via `requireFreshFactor`, each audited as `setTemporaryPassword`, `resetSecondFactor`, `revokeUserSessions` and `reactivateUser`, with org nil and object = target:
    - `POST /api/v1/admin/users/{userId}/temporary-password` → 200 `{password, expires_at, notice}` with header `Cache-Control: no-store`;
    - `POST /api/v1/admin/users/{userId}/second-factor/reset` → 200 `{notice}`;
    - `POST /api/v1/admin/users/{userId}/sessions/revoke` → 204;
    - `POST /api/v1/admin/users/{userId}/reactivate` → 204.
  - `POST /api/v1/admin/users/{userId}/deactivate`, the existing route, now also needs the step-up, answers 409 `not_on_own_account` for the caller themselves, and returns 200 `{notice}`.
  - Error mapping: `ErrNotOnOwnAccount` → 409 `not_on_own_account`; `ErrInvitePending` → 409 `invite_pending`; `ErrNotFound` → 404; `tenancy.ErrLastInstanceAdmin` → 409, as today.

- [ ] **Step 1: Write the failing tests.**
  - `TestAccountStatePrecedence`: a table over the five states, including a deactivated account that is also invite-pending, which yields `deactivated`.
  - `TestSetTemporaryPassword`:
    - with a fixed `rnd`, the generated value matches `^[a-hj-km-np-z2-9]{5}(-[a-hj-km-np-z2-9]{5}){3}$`;
    - the expiry is now+24h;
    - the store saw the revoke;
    - `Notice` is `not_configured` with a mailer whose `Configured` is false, and `failed` when `Send` errors, with no error returned.
  - `TestAdminActionsRefuseOwnAccount`: every action gets `ErrNotOnOwnAccount`.
  - `TestAdminActionsRefuseInvitePending`: temporary password, factor reset, end sessions and reactivate get `ErrInvitePending`.
  - `TestResetSecondFactorSQL` (testdb): after the reset, `totp_secrets`, `recovery_codes` and `passkeys` are empty for the user and untouched for another user, and the user's sessions are revoked.
  - API `TestAdminTemporaryPasswordEndsOtherSessions`: the target's existing cookie gets 401 on `GET /api/v1/auth/me` after the action. The response has `Cache-Control: no-store`. The captured log and the audit row's fields contain no password.
  - API `TestAdminUserRoutesRequireInstanceAdminAndStepUp`:
    - an org owner who is not an instance admin → 403;
    - a bearer token → 403;
    - a stale factor → 403 `step_up_required` on each POST;
    - GETs do not need the step-up.
  - API `TestDeactivateRefusesSelfAndKeepsLastAdminRule`.
  - API `TestAdminListUsersSearchAndPaging`: 3 users, `limit=2`, gives two pages, and `q` matches on display name.
- [ ] **Step 2: Run them.** `go test -race -timeout 900s ./internal/accounts/ ./internal/tenancy/` and `go test -race -timeout 1800s ./internal/api/ -run 'Admin|Deactivate'`. Expected: FAIL.
- [ ] **Step 3: Implement as in Interfaces.** Then `npm run api:gen`.
- [ ] **Step 4: Run** Step 2's commands, then `make test`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(accounts,api): let instance admins manage accounts`.

### Task 4: Forgot password, reset link, admin "send reset link"

**Files:**
- Create:
  - `internal/accounts/reset.go`;
  - `internal/api/password_reset_handlers.go`;
  - `internal/api/forgot_rate_limit.go`.
- Modify:
  - `internal/db/queries/accounts.sql`;
  - `internal/accounts/notices.go`: the reset mail, en and de;
  - `internal/api/admin_user_handlers.go`.
- Test: `internal/accounts/reset_test.go`, `internal/accounts/store_sql_test.go`, `internal/api/password_reset_handlers_test.go`

**Interfaces:**
- Consumes: Tasks 2–3 (`Service`, `Mailer`, the sentinels, the admin checks).
- Produces (service):
  - `ResetAvailable(ctx) (bool, error)` is the mailer's `Configured`.
  - `RequestReset(ctx, email string) error`:
    - with mail not configured, it does nothing;
    - an unknown, deactivated or invite-pending account gets nothing;
    - an account that already has **3** tokens created in the last hour gets nothing (the per-address limit, read from `password_reset_tokens`);
    - otherwise it creates a 32-byte token (base64url), stores its SHA-256, sets the expiry to now+60min, marks the user's older unused tokens used, and mails `{publicURL}/reset-password#token=<token>`.
    - It returns nil in every one of these cases, and an error only on a store failure. A send failure is logged without the link.
  - `SendResetLink(ctx, actorIsAdmin bool, actor, target uuid.UUID) error`: the admin variant. It applies the common checks from Task 3, returns `ErrMailNotConfigured` without mail, skips the per-address limit, and otherwise behaves like `RequestReset`.
  - `ResetPassword(ctx, token, next string) error`:
    - the token must hash to an unused, unexpired row whose user is not disabled; otherwise `ErrInvalidResetToken`;
    - `next` must pass the policy;
    - in one transaction it sets the hash, clears `must_change_password` and `temp_password_expires_at`, marks this token and every other token of the user used, and revokes all of the user's sessions;
    - it never creates a session.
- Produces (API):
  - `GET /api/v1/auth/password/reset-available` → `{available: bool}`. Public.
  - `POST /api/v1/auth/password/forgot` with body `{email}` → always 202 `{}`. Public. Uses the new `withForgotRateLimit(deps)`: per client IP (`internal/clientip`, as the auth limiter resolves it), **10 requests per 15 min**, every request counted, 429 with `Retry-After` beyond that.
  - `POST /api/v1/auth/password/reset` with body `{token, new_password}` → 204, or 400 `invalid_or_expired_link`, or 422 for the policy. Public, on the existing `withAuthRateLimit`. Audit `resetPassword` with org nil and actor = the user, only on success.
  - `POST /api/v1/admin/users/{userId}/password-reset-link` → 204, or 409 `mail_not_configured`. Step-up required. Audit `sendPasswordResetLink`.

- [ ] **Step 1: Write the failing tests.**
  - `TestRequestResetSilentCases`, a table: unknown, deactivated, invite-pending, the 4th request in an hour, mail not configured. Each returns nil, with no mail and no new token row (with mail not configured, no row either).
  - `TestRequestResetMailsFragmentLink`: the mail body contains `https://pulsix.example/reset-password#token=` and no `?token=`. The stored hash is the SHA-256 of the mailed token. An earlier unused token is now used.
  - `TestResetPassword`, a table:
    - a valid token gives success; the old sessions are revoked and the flag is cleared;
    - the same token again → `ErrInvalidResetToken`;
    - an older token after a newer one was issued → `ErrInvalidResetToken`;
    - 61 minutes later (fake clock) → `ErrInvalidResetToken`;
    - a disabled user → `ErrInvalidResetToken`;
    - an 11-character password → `ErrPasswordTooShort`, with the token **not** consumed.
  - API `TestForgotPasswordConstantShape`: an existing address, an unknown one, a deactivated one and an invite-pending one give an identical status (202) and identical body bytes.
  - API `TestForgotPasswordRateLimit`: the 11th request from one IP within 15 min → 429.
  - API `TestResetDoesNotSignIn`: after the 204 there is no `Set-Cookie`. Logging in with the new password gives a session that still needs the second factor (`mfa_required: true`).
  - API `TestAdminSendResetLinkWithoutMail` → 409 `mail_not_configured`.
- [ ] **Step 2: Run them.** `go test -race -timeout 900s ./internal/accounts/` and `go test -race -timeout 1800s ./internal/api/ -run 'Forgot|Reset'`. Expected: FAIL.
- [ ] **Step 3: Implement as in Interfaces.** Then `npm run api:gen`.
- [ ] **Step 4: Run** Step 2's commands, then `make test`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(accounts,api): add forgot password and reset links`.

### Task 5: Only instance admins create organizations; list all organizations

**Files:**
- Modify:
  - `internal/api/org_handlers.go`: `POST /orgs` → `requireInstanceAdmin`;
  - `internal/tenancy/service.go`: add `ListAllOrgs`;
  - `internal/db/queries/tenancy.sql`.
- Create: `internal/api/admin_org_handlers.go`
- Test: `internal/api/org_handlers_test.go`, `internal/tenancy/store_sql_test.go`

**Interfaces:**
- Produces:
  - `tenancy.Service.ListAllOrgs(ctx, actorIsAdmin bool, actor uuid.UUID) ([]OrgOverview{ID, Slug, Name, MemberCount int, CreatedAt, ActorIsMember bool}, error)`, ordered by name. `org_memberships` has an RLS bypass when no org scope is set (`00002_rls.sql`), so run the query outside `WithOrgScope`.
  - `GET /api/v1/admin/orgs` → `{orgs: [...]}`, `requireInstanceAdmin`, no step-up.
  - `POST /api/v1/orgs` for a non-admin → 403 `instance admin required`.

- [ ] **Step 1: Write the failing tests.**
  - `TestCreateOrgRequiresInstanceAdmin`: a plain MFA-complete user → 403; an instance admin → 201 and owner.
  - `TestAdminListOrgsCountsMembersAcrossOrgs`: two orgs, the admin a member of one → both listed with correct counts and `actor_is_member` true/false.
  - Update every existing test that creates an org through the API as a non-admin. Find them with `grep -rn '"/api/v1/orgs"' internal/api/*_test.go internal/e2e`.
- [ ] **Step 2: Run them.** `go test -race -timeout 1800s ./internal/api/ -run 'Org'`. Expected: FAIL.
- [ ] **Step 3: Implement.** Then `npm run api:gen`.
- [ ] **Step 4: Run** `make test`. Expected: PASS. The e2e group may create orgs, so check it.
- [ ] **Step 5: Commit** `feat(api): restrict creating organizations to instance admins`.

### Task 6: Settings layout with a side navigation and the General page

**Files:**
- Create:
  - `web/src/ui/side-nav.tsx`: presentational. Groups of `{to, params, label, badge?}` links with an active state. Below 900 px it renders a `Select` that navigates.
  - `web/src/features/settings/SettingsLayout.tsx`: builds the groups from the role and `instance_admin`, and renders `<SideNav/>` plus `<Outlet/>`.
  - `web/src/features/settings/OrgGeneralPage.tsx`.
- Delete: `web/src/features/settings/SettingsTabs.tsx` and its test.
- Modify:
  - `web/src/router.tsx`: `settingsRoute` gets the `SettingsLayout` component; add `/general`; the index redirect stays on `/members`.
  - Every page importing `SettingsTabs`: drop the import. Here is the list: ActivityPage, SmtpSettingsPage, LanguageSettingsPage, BackupsPage, SharedResourcesPage, MembersPage, RotationsPage, AlertRoutesPage, ChannelsPage, TokensPage.
  - `web/src/api/hooks/useOrgs.ts`: add `useUpdateOrg(org)` for `PATCH /orgs/{org}`.
  - `web/src/i18n/{en,de}/settings.ts`.
- Test: `web/src/ui/side-nav.test.tsx`, `web/src/features/settings/SettingsLayout.test.tsx`, `web/src/features/settings/OrgGeneralPage.test.tsx`, `web/src/router.test.tsx`

**Interfaces:**
- Produces:
  - `SideNav({ groups: SideNavGroup[] })` with `SideNavGroup = { label: string; items: SideNavItem[] }` and `SideNavItem = { to: string; params?: Record<string,string>; label: string }`.
  - The settings group and item keys used by Tasks 7–10:
    - `organization`: general, members, activity, tokens;
    - `alerting`: notifications, alertRoutes, rotations;
    - `instance`: users, organizations, mail, language, backups, sharedRepos, sharedTemplates, sharedChecks.
  - Routes `/o/$org/settings/users` and `/o/$org/settings/organizations` are added by Tasks 8 and 10. Until then their nav items are left out. Each later task adds its own item.

- [ ] **Step 1: Write the failing tests.**
  - `SettingsLayout.test`:
    - a viewer sees Organization only;
    - an operator also sees Alerting;
    - an instance admin also sees Instance;
    - the item for the current route has `aria-current="page"`.
  - `side-nav.test`: at narrow width (mock `matchMedia`) a select is rendered and choosing an item navigates.
  - `OrgGeneralPage.test`:
    - the name saves through PATCH;
    - the slug is read-only;
    - a role without `org.manage` sees the name read-only.
  - `router.test`: `/o/acme/settings` still redirects to `/settings/members`; `/o/acme/settings/general` renders.
- [ ] **Step 2: Run them.** `cd web && npx vitest run src/ui/side-nav.test.tsx src/features/settings src/router.test.tsx`. Expected: FAIL.
- [ ] **Step 3: Implement.** The layout is a two-column grid: a nav of about 220 px, sticky (`self-start sticky top-4`), and the content area. The cards keep the 7.65 `Card` (no fixed heights).
- [ ] **Step 4: Run** `npx vitest run`, `npx eslint .`, `npx tsc -b`. Expected: all pass.
- [ ] **Step 5: Commit** `feat(web): replace the settings tabs with a side navigation and add the General page`.

### Task 7: Shared resources as list and form pages

**Files:**
- Create:
  - `web/src/ui/form-page.tsx`: `FormPage({ title, backTo, children, onSubmit, submitting, dirty })`, with the form in full width, a sticky footer (Save, Cancel), and an unsaved-changes guard via TanStack Router `useBlocker` with `window.confirm`.
  - `web/src/features/instance/shared/`:
    - `SharedReposPage.tsx`, `SharedRepoFormPage.tsx`;
    - `SharedTemplatesPage.tsx`, `SharedTemplateFormPage.tsx`;
    - `SharedChecksPage.tsx`, `SharedCheckFormPage.tsx`.
- Delete: `web/src/features/instance/SharedResourcesPage.tsx` and its test. Move its form fields and validation into the form pages; do not rewrite them.
- Modify:
  - `web/src/router.tsx`:
    - `/settings/shared` redirects to `/settings/shared/repos`;
    - add `/settings/shared/{repos,templates,checks}`, `…/new` and `…/$id`.
  - `SettingsLayout.tsx`: the nav items.
  - The i18n files `instance.ts`.
- Test: one `*.test.tsx` per page, `web/src/ui/form-page.test.tsx`; move the cases from `SharedResourcesPage.test.tsx` into them.

**Interfaces:**
- Consumes: Task 6's `SettingsLayout` keys, and the hooks in `web/src/api/hooks/useShared*Definitions.ts`, unchanged.
- Produces: `FormPage`, which Tasks 8 and 9 may reuse.

- [ ] **Step 1: Write the failing tests.**
  - The list pages:
    - a row is a link to `…/$id`;
    - the row menu has Open and Delete…;
    - Delete confirms, then calls the delete hook.
  - The form pages:
    - `new` posts and navigates back to the list;
    - `$id` loads, puts and navigates back;
    - leaving with unsaved changes asks for confirmation;
    - the template page still renders the `SchemaBuilder` for extra vars.
  - `form-page.test`: Save is disabled while submitting; the footer is in the document.
  - `router.test`: `/settings/shared` → `/settings/shared/repos`.
- [ ] **Step 2: Run** `npx vitest run src/features/instance src/ui/form-page.test.tsx src/router.test.tsx`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `npx vitest run`, `npx eslint .`, `npx tsc -b`. Expected: all pass.
- [ ] **Step 5: Commit** `feat(web): give shared resources list and form pages`.

### Task 8: Users page, account page, member page

**Files:**
- Create:
  - `web/src/api/hooks/useAdminUsers.ts`: `useAdminUsers(q)` (infinite, cursor), `useAdminUser(id)`, `useSetTemporaryPassword`, `useResetSecondFactor`, `useEndSessions`, `useDeactivateUser`, `useReactivateUser`, `useSendResetLink`. Each mutation invalidates `['admin-user', id]` and `['admin-users']`.
  - `web/src/features/users/`:
    - `UsersPage.tsx`: search plus `ListRow`s with the state pill;
    - `AccountPage.tsx`: `DetailLayout`; the sidebar is `SidebarFacts`; the sections are Organizations, Sign-in and Actions;
    - `AccountSignIn.tsx`: the Sign-in section;
    - `AccountActions.tsx`: the Actions section with its confirm dialogs, the one-time password reveal with a copy button, and the notice toast;
    - `accountState.ts`: state → pill tone and label.
  - `web/src/features/members/MemberPage.tsx`: role (change and remove, the existing hooks and rules) plus, for instance admins, `AccountSignIn` and `AccountActions`.
- Modify:
  - `web/src/features/members/MembersPage.tsx`: rows become `ListRow`s to the member page, and the role dialog is removed.
  - `web/src/router.tsx`: `/settings/users`, `/settings/users/$userId`, `/settings/members/$userId`.
  - `SettingsLayout.tsx`: the Users item.
  - i18n: `members.ts` and a new `users.ts`, en and de, registered in both `index.ts` files.
- Test: `UsersPage.test.tsx`, `AccountPage.test.tsx`, `AccountActions.test.tsx`, `MemberPage.test.tsx`, an updated `MembersPage.test.tsx`

**Interfaces:**
- Consumes: Task 3's and Task 4's routes; `useStepUp` / `isStepUpRequired` from `features/account/stepUp.tsx` (retry after a step-up, as Account security does); Task 6's layout.
- Produces: `AccountSignIn({ user })` and `AccountActions({ user, selfId })` as props-driven components, used by both pages.

- [ ] **Step 1: Write the failing tests.**
  - `AccountActions.test`:
    - every action opens a confirm dialog first;
    - the reset-factor dialog contains the warning that anyone with the password can enrol a factor and the advice to combine it with a temporary password;
    - the temporary password is shown once with a copy button and is gone after closing (not in the DOM, not in the query cache: `queryClient.getQueryCache().findAll()` holds no entry containing it);
    - "Send reset link" is disabled with the reason when `/auth/password/reset-available` says false;
    - `notice: 'failed'` shows a warning toast;
    - a 403 `step_up_required` opens `StepUpDialog` and retries after proof;
    - on one's own account (`selfId === user.id`) no actions are rendered;
    - for `invite_pending` only Deactivate is rendered;
    - a deactivated account shows Reactivate instead of Deactivate.
  - `UsersPage.test`:
    - a search term goes into the query;
    - the five state pills render their labels;
    - a row links to `/settings/users/$id`.
  - `MemberPage.test`:
    - an org admin who is not an instance admin sees the role controls and no account sections;
    - an instance admin sees both.
  - `MembersPage.test`: a row links to the member page, and no role dialog exists.
- [ ] **Step 2: Run** `npx vitest run src/features/users src/features/members`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `npx vitest run`, `npx eslint .`, `npx tsc -b`. Expected: all pass.
- [ ] **Step 5: Commit** `feat(web): add the users, account and member pages`.

### Task 9: Forgot, reset and change password in the SPA

**Files:**
- Create:
  - `web/src/features/auth/ForgotPasswordPage.tsx`;
  - `web/src/features/auth/ResetPasswordPage.tsx`;
  - `web/src/features/account/ChangePasswordPage.tsx`: the forced screen without the app shell, like `EnrolmentLayout`;
  - `web/src/features/account/PasswordCard.tsx`: the card on Account security, reusing one `PasswordChangeForm` component with the forced page;
  - `web/src/api/hooks/usePassword.ts`: `useResetAvailable`, `useRequestReset`, `useResetPassword`, `useChangePassword`.
- Modify:
  - `web/src/features/auth/LoginPage.tsx`: the "Forgot password?" link.
  - `web/src/router.tsx`: `/forgot-password`, `/reset-password` (public), `/account/password`.
  - `web/src/routes/RootRedirect.tsx` and `PendingSessionRedirect.tsx`: `me.must_change_password` → `/account/password`.
  - The API client's error handling: a 403 `password_change_required` anywhere → navigate to `/account/password`.
  - `web/src/features/account/AccountSecurityPage.tsx`.
  - i18n `auth.ts` and `account.ts`.
- Test: one test per new page, plus `LoginPage.test.tsx`, `RootRedirect.test.tsx`, `AccountSecurityPage.test.tsx`

**Interfaces:**
- Consumes: Task 2's `POST /auth/password` and `/auth/me` `must_change_password`; Task 4's public routes.

- [ ] **Step 1: Write the failing tests.**
  - `ForgotPasswordPage.test`:
    - with `available: false`, it shows "ask an instance administrator" and no form;
    - with `available: true`, submitting shows the neutral confirmation ("If an account exists for this address, we sent a link") whatever the answer was.
  - `ResetPasswordPage.test`:
    - it reads the token from `window.location.hash` (`#token=abc`) and sends it in the body;
    - the token never appears in a request URL (assert on the mocked fetch URL);
    - a mismatch of the two passwords or fewer than 12 characters gives a client-side error;
    - 400 shows "link invalid or expired" with a link to `/forgot-password`;
    - success shows "Sign in with your new password and your second factor" with a link to `/login`.
  - `ChangePasswordPage.test`:
    - it posts current and new;
    - new == current gives a client-side error;
    - success navigates to `/`.
  - `RootRedirect.test`: `must_change_password: true` → `/account/password`.
  - `LoginPage.test`: the link to `/forgot-password` exists.
  - `AccountSecurityPage.test`:
    - the Password card renders;
    - `step_up_required` opens the step-up dialog.
- [ ] **Step 2: Run** `npx vitest run src/features/auth src/features/account src/routes`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `npx vitest run`, `npx eslint .`, `npx tsc -b`. Expected: all pass.
- [ ] **Step 5: Commit** `feat(web): add forgot, reset and change password`.

### Task 10: Create organizations and the Organizations page

**Files:**
- Create:
  - `web/src/features/orgs/CreateOrgDialog.tsx`: Name, and Slug via `ui/SlugField`;
  - `web/src/features/instance/OrganizationsPage.tsx`;
  - `useCreateOrg` and `useAdminOrgs` in `web/src/api/hooks/useOrgs.ts`.
- Modify:
  - `web/src/ui/shell.tsx`: an optional `onCreateOrg?: () => void` prop. When set, the org switcher shows "+ New organization" as its last entry.
  - `web/src/routes/OrgLayout.tsx` and `AccountSecurityLayout.tsx`: pass `onCreateOrg` only when `me.instance_admin`.
  - `web/src/router.tsx`: `/settings/organizations`.
  - `SettingsLayout.tsx`: the Organizations item.
  - i18n `shell.ts` and `instance.ts`.
- Test: `CreateOrgDialog.test.tsx`, `OrganizationsPage.test.tsx`, `shell.test.tsx`

**Interfaces:**
- Consumes: Task 5's routes.

- [ ] **Step 1: Write the failing tests.**
  - `shell.test`: there is no create entry without `onCreateOrg`; choosing the entry calls it.
  - `CreateOrgDialog.test`:
    - the slug is derived from the name until it is edited;
    - submit posts and navigates to `/o/$slug/hosts`;
    - a 409 (slug taken) shows the error at the field.
  - `OrganizationsPage.test`:
    - the list renders the member counts;
    - an org the admin is a member of links to its hosts page;
    - another org is plain text.
- [ ] **Step 2: Run** `npx vitest run src/features/orgs src/features/instance/OrganizationsPage.test.tsx src/ui/shell.test.tsx`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `npx vitest run`, `npx eslint .`, `npx tsc -b`. Expected: all pass.
- [ ] **Step 5: Commit** `feat(web): create organizations from the switcher and list them for instance admins`.

### Task 11: E2E, user docs, backlog

**Files:**
- Modify:
  - every `web/e2e/*.spec.ts` that used the settings tabs, the shared-resources dialogs, the member role dialog, or created an org as a non-admin (`grep -rln 'settings/\|role\b\|/orgs' web/e2e`);
  - `docs/BACKLOG.md`: 7.74 → done, with the commits.
- noix-docs:
  - Work in a worktree of `~/_dev/noix-docs` on branch `pulsix`, never in that checkout itself.
  - Update `src/content/docs/pulsix/`:
    - `user/account-and-security.md`: change password, forgot password, "must change password", what a reset never skips;
    - `user/team-and-roles.md`: the member page, and that account actions are for instance admins;
    - `admin/shared-resources.md`: the pages;
    - `admin/security.md`: the account actions, the temporary password, the factor reset and its caveat, and that only instance admins create organizations;
    - `reference/changelog.md`.
  - Add a page `admin/users-and-organizations.md` (Users page, account page, Organizations page, creating an organization).
  - Describe the settings navigation wherever a page today says "the X tab".

- [ ] **Step 1:** Adapt the e2e specs and run `cd web && npx playwright test` against the dev stack the specs expect (see `web/playwright.config.ts`). Expected: PASS. If the stack cannot be started in this environment, say so in the report and list the specs changed.
- [ ] **Step 2:** Write the docs, with no tech-stack names and no internal file paths.
- [ ] **Step 3: Commit** in pulsix: `docs: close backlog 7.74` and `test(e2e): follow the settings navigation and the account pages`. In noix-docs: `docs(pulsix): document settings navigation, account administration and organizations`.
