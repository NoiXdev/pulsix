# Session lock, expiry warning and new-version notice — design

Backlog 3.93 (session lock with a PIN), 3.94 (session expiry warning with
in-place renewal), 3.95 (new-version notice). Owner's request, 2026-11-17.

## Intent

Three things a person at the GUI should not be surprised by:

1. **Lock.** The owner's scenario: their own screen in the office, they step
   away for a few minutes (to the restroom) and do not want to sign out and
   back in. A colleague walking past must not be able to change anything —
   or read along. One click locks the session; a short PIN unlocks it.
2. **Expiry.** A session lives a fixed 12 hours from sign-in
   (`auth.SessionTTL`), however active it is. Today it simply ends: the next
   request answers 401 and whatever was half-typed is lost. Instead the GUI
   warns ahead of time and lets the person carry on in place by proving
   themselves again (password plus second factor), without leaving the page.
3. **New version.** When the manager was upgraded while a tab was open, the
   tab still runs the old GUI against the new server. The GUI notices and
   offers a reload.

## Goals

- The lock is enforced **by the server**, per session: reloading, a second
  tab, or the browser's developer tools do not get past it.
- Guessing the PIN is pointless: a handful of wrong attempts ends the
  session, and getting a new one needs the full sign-in.
- Renewing a session needs the same proof as signing in. It never extends
  the existing session's lifetime; it replaces the session.
- The version notice never interrupts work it could destroy: "later" is
  always available.

## Non-goals

- **No automatic lock** after inactivity — only by click (owner's
  confirmation of the playback).
- **No idle timeout.** The 12-hour absolute lifetime stays as it is.
- **No "newer release on GitHub" notice.** 3.95 is only about a version
  already running on the instance's own server.
- No lock for API tokens: the lock is a cookie-session state.
- No organization- or instance-wide PIN policy (length rules, mandatory
  PIN). Possible later; not now.

## Anti-goals

- A lock that is only a client-side overlay. It would vanish on reload and
  is exactly what the owner does not want.
- A renewal that can be done from a locked screen — that would let someone
  at a stranger's desk keep the stranger's session alive.
- A PIN that becomes a second password: it unlocks a session that already
  proved password and second factor; it never signs anyone in.

## 3.93 Session lock

### Behaviour the owner decided

- A lock icon in the top bar locks the session in one click. While locked,
  only the lock screen is shown: a PIN field and "Sign out". Data behind it
  is not visible.
- The PIN is set under **Account security**.
- If no PIN exists when the lock icon is clicked, the GUI asks for one
  right there (enter twice), stores it, then locks.
- On **first sign-in**, the PIN is offered as a step in the same flow as
  the second-factor enrolment.
- 5 wrong PINs end the session; the person then signs in normally.
- "Sign out" is available on the lock screen.
- The session's 12-hour lifetime keeps running while locked.

### Calls made in the playback (owner did not object)

- PIN: digits only, 4 to 8 of them.
- The first-sign-in PIN step can be **skipped** (unlike the second factor).
  An account that already exists is offered it **once**, at its next
  sign-in; skipping is remembered, so the prompt does not return.
- The lock icon is always shown; without a PIN, clicking it starts the
  "set a PIN, then lock" flow above.

### Server

**Storage (additive migration).**

- `users.lock_pin_hash` (bytea or text, nullable): the PIN hashed with the
  same password hasher `internal/auth/password.go` uses for passwords.
  Never stored or logged in clear.
- `users.lock_pin_prompted_at` (timestamptz, nullable): when the sign-in
  flow last offered the PIN step. Set when the PIN is set *or* the step is
  skipped. The sign-in flow offers the step while both this and the hash
  are null.
- `sessions.locked_at` (timestamptz, nullable): set while the session is
  locked.
- `sessions.unlock_failures` (int, not null, default 0): wrong PINs since
  the session was last locked; reset on lock and on a successful unlock.

**Principal.** `Sessions.Lookup` reads `locked_at` alongside the existing
fields; `auth.Principal` gains `Locked bool`. `StillValid` treats a locked
session as no longer serving a long-running request (the job log stream
ends, as on sign-out), so a stream left open behind the lock screen does not
keep printing.

**Gate.** The lock joins the session gates in `internal/api/session_gates.go`
the way `must_change_password` does: a locked cookie session is refused with
**403 `session_locked`** by `requireEnrolmentSession`, `requireMFAComplete`,
`requireInteractiveSession`, `requireInstanceAdmin`, `requireOrg`, and every
other gate built on them, and also by `requirePasswordChangeSession`
(`POST /auth/password`), which sits on `requireSession` directly. The lock
takes precedence over `password_change_required`: an administrator can raise
that flag on an account whose session is locked, and the lock screen must
still win. The detail string is contract (the SPA matches on it, the way
`web/src/api/client.ts` already listens for `password_change_required`).

What stays reachable while locked: `/auth/me`, `/auth/logout` and the unlock
route — nothing else. `/auth/totp/verify` is **refused** for a locked
session too: it is not only the pending-session upgrade but also the TOTP
step-up of a complete session (it refreshes `mfa_at` and rotates the cookie,
`CompleteVerifiedSession`), and a locked session must not be able to make
its factor fresh. (A pending session cannot be locked — locking needs an
MFA-complete session — so this never blocks a sign-in.)

The non-huma routes — the backup download (`requireInstanceAdmin`), the
explorer, `/api/openapi.json` and `/api/schemas` (`requireMFAForExplorer` →
`requireMFAComplete`) — and the job log stream (a huma route behind
`requireOrg`, which also polls `StillValid`) are all covered by putting the
check into the shared gates; they need tests that pin the refusal, not new
code. `/api/mcp` takes only assistant bearer tokens, so no cookie session
reaches it. A bearer token is never locked.

**Routes** (all under `/api/v1/auth/lock`, cookie sessions only):

| Route | Gate | Does |
|---|---|---|
| `PUT /auth/lock/pin` `{pin}` | MFA-complete, unlocked; **step-up** (`FactorFresh`) when a PIN already exists, none when it does not | sets or replaces the PIN; sets `lock_pin_prompted_at` |
| `DELETE /auth/lock/pin` | MFA-complete, unlocked, step-up | removes the PIN |
| `POST /auth/lock/prompt-skipped` | MFA-complete, unlocked | sets `lock_pin_prompted_at` without a PIN |
| `POST /auth/lock` | MFA-complete, unlocked; 409 `lock_pin_missing` without a PIN | sets `locked_at`, resets `unlock_failures` |
| `POST /auth/lock/unlock` `{pin}` | session (locked or not) | right PIN: clears `locked_at` and `unlock_failures`. Wrong PIN: increments `unlock_failures`; at 5 the session is revoked and the cookie cleared (401). Answers 422 with the attempts left otherwise |

Why no step-up for the *first* PIN: setting it is needed in the middle of
"lock now", on a session that may be hours old; someone at an unlocked
screen already holds the whole session, and the worst a PIN they choose can
do is make the owner sign in again. Replacing or removing an existing PIN
does need step-up, like every other account-security change (backlog 1.28).
This is the designer's call.

`/auth/me` gains `locked` (bool), `lock_pin_set` (bool) and
`lock_pin_prompt` (bool: the sign-in flow should offer the PIN step).

Audit, in the house convention (`withAudit("camelCaseAction",
"object_type")`, recorded on success): `lockSession`, `unlockSession`,
`setLockPin`, `removeLockPin`, `skipLockPinPrompt`. The two failures that
matter — a wrong PIN (with the count) and the fifth wrong PIN ending the
session — answer 422 and 401, which `withAudit` does not record, and the
existing `recordAuditFailure` needs an organization the lock does not have.
The plan therefore adds a small instance-level (no organization) failure
audit path and records both through it. Never the PIN.

**Administrator actions.** Resetting a user's second factor also clears
`lock_pin_hash` and `lock_pin_prompted_at` (it is the "start this account's
security over" action; the user is offered the PIN step again at the next
sign-in). Setting a temporary password does not touch the PIN.
Deactivating already revokes every session. Designer's call.

Unlock attempts are also charged to the existing auth rate limiter
(`withAuthRateLimit`) so a script cannot hammer the route; the per-session
counter is the real bound.

### SPA

- **Lock icon** in the top bar (`web/src/ui/shell.tsx`), labelled
  "Lock session", next to the account menu.
- **Lock screen**: a full-viewport layer rendered *instead of* the routed
  page (not over it), so the page's data is not in the DOM. PIN field,
  "Unlock", "Sign out", and the attempts left after a wrong PIN.
- Any API answer of 403 `session_locked` switches the app to the lock
  screen — that is how another tab of the same browser, which shares the
  cookie, follows a lock. A `BroadcastChannel` makes the other tabs follow
  at once instead of on their next request; the server stays the authority.
- On load, `/auth/me`'s `locked` decides whether the lock screen shows.
- "Set a PIN, then lock" dialog when there is none; PIN entered twice.
- Account security page: a "Lock PIN" section — set, change, remove (the
  last two through the existing step-up dialog,
  `web/src/features/account/StepUpDialog.tsx`).
- Sign-in flow: after the second factor (enrolled or verified), when
  `lock_pin_prompt` is true, a "Set a lock PIN" step with "Skip".
- Everything in en and de.

## 3.94 Expiry warning and renewal

### Behaviour

- **5 minutes** before the session's expiry, a dialog shows a countdown.
- In it: password, and the account's second factor — TOTP code or passkey;
  a recovery code as the fallback, as at sign-in. Success gives a **new
  session** with a fresh 12 hours; the old one is revoked. The page stays
  as it is: open forms keep their input.
- Ignored, the session runs out; the GUI goes to sign-in with the notice
  "Your session has expired".
- **All tabs** show the dialog; renewing in one closes it in the others
  (`BroadcastChannel`; the new cookie is shared anyway).
- **Not while locked**: a locked session shows no expiry dialog and cannot
  be renewed. It simply runs out.

### Server

- `/auth/me` gains `session_expires_at` (RFC 3339). `Lookup` reads
  `expires_at`; `Principal` gains `ExpiresAt`.
- **`POST /auth/session/renew`** `{password, code? , recovery_code?}`: gate
  MFA-complete, unlocked cookie session. Checks the password and the second
  factor of the session's own user (no email in the request — it can only
  renew itself), creates a new MFA-complete session with `mfa_at = now`,
  revokes the old one, sets the new cookie. Rate limited like
  `/auth/login`; failures charge it. A TOTP code is consumed as at sign-in.
- **Passkey renewal**: `POST /auth/session/renew/passkey/begin` and
  `.../finish`, the same ceremony as passkey sign-in but bound to the
  session's user (an assertion of another account's passkey is refused).
  Whether a passkey alone suffices, as it does at passkey sign-in, follows
  the sign-in rule: yes.
- Audit event `session.renewed` (old and new session id).
- Both must reuse the existing login, TOTP and passkey verification code
  paths in `internal/auth` rather than re-implement them.

### SPA

- A timer from `session_expires_at` (re-read after every renewal and on tab
  focus; clock skew handled by comparing against the server's `Date` header
  or by the server also returning `seconds_left` — builder's choice).
- The dialog reuses the step-up dialog's factor inputs where possible.
- **The existing step-up passkey path is fixed in the same work.**
  `web/src/features/account/StepUpDialog.tsx` proves a passkey through the
  unauthenticated, discoverable sign-in ceremony (`/auth/passkeys/login/*`):
  `Passkeys.FinishLogin` is not bound to the current user and opens a
  brand-new 12-hour session without revoking the old one. So a passkey
  step-up today silently "renews" while the old session lives on, and
  another account's passkey would switch accounts mid-page. Step-up moves
  onto user-bound passkey routes that refresh the *current* session's
  `mfa_at` (no new session), and renewal uses the user-bound renewal
  ceremony above. Designer's call: the renewal needs user-bound passkey
  verification anyway, and leaving the step-up on the old path would carry
  the flaw into the renewal dialog.
- The timer also re-reads `/auth/me` after every step-up, since the session
  id (and, until the fix above, the expiry) can change there.
- On expiry: clear client state, go to sign-in with the notice.

## 3.95 New-version notice

### Behaviour

- The GUI remembers the version it loaded with (`/setup/status`'s
  `version`, already read for the 3.83 badge).
- It re-reads `/setup/status` every **5 minutes** and whenever the tab
  becomes visible again.
- When the version differs: a dialog "Pulsix was updated to vX" with
  **Reload** and **Later**, and a link to that version's release notes
  (`https://github.com/NoiXdev/pulsix/releases/tag/v<version>`; no link for
  a version that is not a release, such as `dev` or `local-…`).
- **Later** closes it; a slim banner "A new version is available — Reload"
  stays at the top until reload. The dialog does not return for the same
  version.
- The lock screen and sign-in pages do not show the dialog; the check
  resumes after unlock.

### Server

No change.

## Constraints

- Layers as `CLAUDE.md` says: handlers decode/encode only; lock and renewal
  rules live in `internal/auth` services behind interfaces, with clock
  injected.
- Migration additive, next free number (main is at 00106 as of writing —
  check at build time; another session works on main in parallel).
- Secrets: the PIN, password and codes never appear in logs, audit
  details or error texts.
- TDD; Go coverage floors (auth ≥ 90 %); Vitest for the SPA; one Playwright
  spec per feature (lock and unlock, expiry dialog with an injected short
  TTL or clock, version change).
- The OpenAPI document and the generated SPA client are regenerated.
- End-user docs in `NoiXdev/noix-docs` (Pulsix section): account security
  (lock PIN), a short "Locking your session" section, the expiry dialog, the
  update notice. No tech-stack names.

## Left to the builder

- Exact column types and names within the above, query shapes, index needs.
- How the e2e drives expiry. An injected clock does not reach it:
  `Sessions.Create` uses the clock for `expires_at`, but `GetSession`
  filters on the database's `now()`, and Playwright runs against a real
  manager. So either a manager config value for the session lifetime with a
  safe floor, or a Playwright test that intercepts `/auth/me` to show the
  dialog and drives the renewal for real. The Go API tests can use the
  injected clock. No test-only switch in a production build.
- Polling details of 3.95 (jitter, back-off when the server is unreachable).
- Visual design within the existing shell and dialog components.
