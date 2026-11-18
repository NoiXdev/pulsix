# Four-digit PIN boxes and one settings layout — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Lock PINs become exactly four digits entered in four boxes (older PINs keep working), and every settings page gets one navigation and one card layout.

**Architecture:** Server: the screenlock service validates 4 digits on set and records the length in an additive column; `/auth/me` says whether the PIN is legacy. SPA: a presentational `PinInput` in `web/src/ui` drives the lock screen and the PIN form; new presentational settings primitives (`PageHeader`, `SettingsCard`, `SettingsRow`, `CardSaveBar`) replace the hand-written cards page by page; the shell's settings navigation grows to cover account, organization, alerting and instance, and `SettingsLayout`'s inner nav goes away.

**Tech Stack:** Go (huma, sqlc, goose), React + TanStack Router/Query, Tailwind, Vitest, Playwright.

**Spec:** `docs/superpowers/specs/2026-11-18-pin-boxes-and-settings-layout-design.md` (mockup: https://claude.ai/artifact/8DGeKhsJQH1yNom6MPezKL).

## Global Constraints

- Everything in the repo is English; UI strings go through i18n (`web/src/i18n/en/*`, `web/src/i18n/de/*`), German copy uses "du".
- Never run a whole-module `go test ./...`. Go packages: `go test -race -timeout 900s ./internal/<pkg>/...`; `internal/api` with `-timeout 1800s`. `golangci-lint run` must pass.
- SPA: `cd web && npx vitest run <paths>`, `npx tsc -b`, `npx eslint <files>`. `src/ui` never value-imports from `src/api` (ESLint enforces).
- Migrations additive only. A changed table: also run `go test -race -timeout 900s ./internal/backup/...` (round-trip fixture).
- Tests never dial default URLs. Never touch Docker containers this work did not start.
- Commits: Conventional Commits, footer `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. No push, no tags, no stash/reset/rebase.
- Keep `data-card` on every settings card (e2e `expectNoClippedCards` reads it).
- Reduced motion: no shake/caret animation under `prefers-reduced-motion: reduce`.
- Copy (en / de) fixed by the spec and mockup:
  - PIN hint: "4 digits. Avoid 1234 or 0000." / "4 Ziffern. Nimm nicht 1234 oder 0000."
  - Mismatch: "The PINs do not match. Repeat it once more." / "Die PINs stimmen nicht überein. Bitte noch einmal wiederholen."
  - Legacy note on the lock screen: "Your PIN is from an older version and longer than 4 digits. After unlocking, set a new 4-digit PIN under Security." / "Deine PIN stammt aus einer älteren Version und ist länger als 4 Ziffern. Lege nach dem Entsperren unter Sicherheit eine neue 4-stellige PIN fest."
  - Save bar: "Unsaved changes" / "Ungespeicherte Änderungen", "Discard" / "Verwerfen", "Save" / "Speichern".
  - Settings nav groups: "My account" / "Mein Konto", "Organization · {name}" / "Organisation · {name}", "Alerting" / "Alarmierung", "Instance" / "Instanz".

## Review Focus

1. A person with a 6-digit PIN from rc.4 locks the session: the lock screen must show the single field and unlock with that PIN, never four boxes that auto-submit after 4 digits (that would burn attempts and end the session).
2. Typing fast or pasting "12345" into the boxes: only the first 4 digits count; pasting "12a4" keeps "124"; a paste of 4 digits on the lock screen submits exactly once.
3. A card with unsaved edits, then a click on another settings nav item: the leave question appears; after Save or Discard it does not.
4. Backups: editing both cards, then saving only the destination card must not send the schedule draft.
5. A viewer (no alerting rights, not instance admin) on the settings nav sees only My account and Organization groups; an instance admin on `/account/*` sees the Instance group linking to the back-organization's routes.

---

### Task 1: Server — four-digit PINs, recorded length, legacy flag

**Files:**
- Create: `internal/db/migrations/00112_lock_pin_digits.sql`
- Modify: `internal/db/queries/auth.sql` (GetLockPIN, SetLockPIN, RemoveLockPIN and the reset query near lines 238-252), regenerate `internal/db/gen` (`make sqlc`)
- Modify: `internal/screenlock/service.go`, `internal/screenlock/store_sql.go`, fakes in tests
- Modify: `internal/api/auth_handlers.go` (`/auth/me` output), wherever `/auth/me` is assembled
- Modify: `web/src/api/schema.d.ts` (`cd web && npm run api:gen` against the manager's `/api/openapi.json`, or the repo's documented way; `cmd/manager/openapi.go` prints the document without a database)
- Modify: `docs/upgrades.md` (release note: new PINs are 4 digits; older PINs keep working)
- Test: `internal/screenlock/*_test.go`, the `/auth/me` API test, `internal/backup` fixture if it lists user columns

**Interfaces:**
- Produces: `/auth/me` field `lock_pin_legacy: boolean` (true iff a PIN is set and its length was not recorded).
- Produces: `screenlock.Status{PINSet, Prompt, Legacy bool}`; `Store.PINState` returns the digits (nullable) as well.
- Produces: `ErrInvalidPIN` message "screenlock: PIN must be 4 digits".

- [ ] **Step 1:** Failing tests: `SetPIN` accepts "0427", refuses "042", "04271", "12a4" with `ErrInvalidPIN`; after `SetPIN` the status is `{PINSet:true, Legacy:false}`; a PIN stored without digits (simulate the rc.4 row) reports `Legacy:true`; `Unlock` with a stored 6-digit legacy PIN and the right 6 digits unlocks; `RemovePIN` clears the digits; `/auth/me` returns `lock_pin_legacy`.
- [ ] **Step 2:** Migration `ALTER TABLE users ADD COLUMN lock_pin_digits smallint CHECK (lock_pin_digits BETWEEN 4 AND 8);` (+ goose Down dropping it). Queries: SetLockPIN writes `lock_pin_digits = @lock_pin_digits`; every query that nulls `lock_pin_hash` nulls `lock_pin_digits` too; GetLockPIN returns it.
- [ ] **Step 3:** Service: set pattern `^[0-9]{4}$`; unlock keeps accepting `^[0-9]{4,8}$` for the comparison; store 4 on set.
- [ ] **Step 4:** Run `go test -race -timeout 900s ./internal/screenlock/... ./internal/auth/... ./internal/backup/... ./internal/db/...` and `go test -race -timeout 1800s ./internal/api/...`; `golangci-lint run`. Regenerate `schema.d.ts`; `cd web && npx tsc -b`.
- [ ] **Step 5:** Commit `feat(screenlock): four-digit PINs, record the length, flag older PINs`.

### Task 2: SPA — `PinInput`

**Files:**
- Create: `web/src/ui/pin-input.tsx`, `web/src/ui/pin-input.test.tsx`

**Interfaces:**
- Produces: `PinInput({ id, value, onChange, onComplete?, length = 4, autoFocus?, disabled?, invalid?, shake?, autoComplete = 'off', 'aria-describedby'?, inputRef? }: {...; value: string; onChange: (digits: string) => void; onComplete?: (digits: string) => void; inputRef?: Ref<HTMLInputElement>})`. Renders one transparent `<input type="password" inputMode="numeric" maxLength={length}>` over `length` boxes (`data-pin-box`, filled ones contain a dot, the active one has the accent ring and caret); `aria-invalid` from `invalid`.

- [ ] **Step 1:** Failing tests: typing "4" then "2" calls `onChange("4")`, `onChange("42")`; pasting "12a45" yields `onChange("1245")` (digits only, truncated to 4); `onComplete` fires once with the 4 digits when the value reaches 4; non-digit input is dropped; the label (`<label htmlFor=id>`) names the input; four `data-pin-box` elements, `n` of them filled for `n` digits; `invalid` sets `aria-invalid="true"`.
- [ ] **Step 2:** Implement (presentational; no API import). Tailwind classes; shake via a class with `motion-safe:` prefix.
- [ ] **Step 3:** vitest, tsc, eslint. Commit `feat(web): four-box PIN input`.

### Task 3: SPA — PIN form and lock screen on `PinInput`

**Files:**
- Modify: `web/src/features/lock/PinForm.tsx`, `LockScreen.tsx`, `SetPinDialog.tsx`, `LockPinPromptPage.tsx`, `web/src/features/account/LockPinCard.tsx` (only as far as PinForm's props change), i18n `lock` catalogue (en, de)
- Test: `PinForm` (new test file if none), `LockScreen.test.tsx`, `SetPinDialog.test.tsx`, `LockPinPromptPage.test.tsx`

**Interfaces:**
- Consumes: `PinInput` (Task 2); `me.lock_pin_legacy` (Task 1).
- Produces: `PinForm({ onSubmit, busy?, extra?, idPrefix? })` — `submitLabel` is dropped (no submit button). Same `onSubmit(pin)` contract (throws to refuse).

- [ ] **Step 1:** Failing tests:
  - PinForm: filling the first row moves focus to the second; a matching second row calls `onSubmit("0427")` once; a mismatching one shows the mismatch copy, clears the second row, keeps the first, does not call `onSubmit`; when `onSubmit` throws, its message shows and both rows clear with focus on the first; the hint copy is shown.
  - LockScreen: with `lock_pin_legacy` false, four boxes; the fourth digit posts the unlock once; a 422 clears the boxes, shows wrong-PIN + attempts left, focus back in the input; no Unlock button. With `lock_pin_legacy` true: the single field (maxLength 8), the Unlock button and the legacy note; four boxes absent.
- [ ] **Step 2:** Implement. The lock screen reads `lock_pin_legacy` from the cached `['me']` query (it is rendered inside RootLayout, which holds `useMe`).
- [ ] **Step 3:** vitest for `src/features/lock src/features/account src/routes`, tsc, eslint. Commit `feat(web): enter and set the lock PIN in four boxes`.

### Task 4: SPA — settings primitives

**Files:**
- Create: `web/src/ui/settings.tsx`, `web/src/ui/settings.test.tsx`

**Interfaces (all presentational, no API import):**
- `PageHeader({ title, description?, badge?, actions? }: { title: ReactNode; description?: ReactNode; badge?: ReactNode; actions?: ReactNode })` — `<h1>` + description; badge beside the title; actions on the right; wraps on narrow screens.
- `SettingsPage({ title, description?, badge?, actions?, children })` — `PageHeader` + a `max-w-[880px]` column with `gap-5`.
- `SettingsCard({ title, description?, badge?, children, footer?, headingId? })` — `<section aria-labelledby>` with `data-card`; header row; children; `footer` rendered last.
- `SettingsRow({ label, htmlFor?, help?, children, indent?, stack? })` — grid `minmax(0,1fr) auto` (label/help left, control right), single column below `sm`; `label` is a `<label htmlFor>` when `htmlFor` is given, else text with an id usable as `aria-labelledby` (`labelId` prop).
- `CardSaveBar({ dirty, saving, onDiscard, saveLabel?, formId? })` — footer bar: when `dirty`, "Unsaved changes" + Discard; Save (`type="submit"`, `form={formId}` when given) disabled unless `dirty && !saving`.
- `useCardForm<T>(saved: T)` in `web/src/ui/useCardForm.ts`: `{ draft, setDraft, dirty, discard, markSaved }` with structural equality, and it registers `useUnsavedGuard(dirty)`.

- [ ] **Step 1:** Failing tests for each: header renders title as `h1`, description, badge; card has `data-card` and is labelled by its title; row associates label and control and shows help; save bar disables Save when clean, shows Discard + "Unsaved changes" when dirty and calls `onDiscard`; `useCardForm` flips `dirty` on change and back on discard / `markSaved`.
- [ ] **Step 2:** Implement with the existing `Button`, `Pill` and tokens (`bg-card`, `border-border`, `text-muted-foreground`).
- [ ] **Step 3:** vitest, tsc, eslint. Commit `feat(web): settings page, card, row and save bar`.

### Task 5: SPA — one settings navigation

**Files:**
- Modify: `web/src/ui/shell-nav.tsx` (`SettingsNav`), `web/src/ui/shell.tsx` (`ShellSettingsNav` type), `web/src/routes/AccountLayout.tsx`, `web/src/routes/OrgLayout.tsx`, `web/src/features/settings/SettingsLayout.tsx`, `web/src/lib/breadcrumbs.ts`, i18n (`account.nav`, `settings` catalogues)
- Test: `ui/shell.test.tsx`, `routes/AccountLayout.test.tsx`, `features/settings/SettingsLayout.test.tsx`, `routes/OrgLayout.test.tsx` if present, `lib/breadcrumbs.test.ts`, `router.test.tsx`; e2e `web/e2e/settings-area.spec.ts`

**Interfaces:**
- Produces: `ShellSettingsNav` = `{ backOrg: {slug, name}; personName; personRole; showAlerting: boolean; showInstance: boolean }` (built by the route layouts from `may(role,'channels.read')` and `me.instance_admin`; `src/ui` gets plain booleans).
- `SettingsNav` renders Back, person, then groups My account / Organization · {name} / Alerting / Instance (via `NavGroup`), links with `NAV_LINK_PROPS`, org links with `params={{ org: backOrg.slug }}`.
- `OrgLayout` passes `settingsNav` while the location is under `/o/$org/settings`; `SettingsLayout` renders only its Outlet (no inner `SideNav`, no grid).

- [ ] **Step 1:** Failing tests: on `/o/demo/settings/general` the sidebar shows Back to Demo and all four groups for an instance-admin owner, the org switcher is gone, and no second nav is rendered; a viewer sees only My account + Organization; on `/account/security` the Organization links point at the back-org; breadcrumbs still read "Settings / …".
- [ ] **Step 2:** Implement. Keep the user-footer "Settings" link and the OrgNav "Settings" entry as the ways in.
- [ ] **Step 3:** vitest (`src/ui src/routes src/features/settings src/lib src/router.test.tsx`), tsc, eslint. Update `web/e2e/settings-area.spec.ts` selectors. Commit `feat(web): one settings navigation for account, organization and instance`.

### Task 6: SPA — account pages on the card pattern

**Files:**
- Modify: `web/src/features/account/AccountSecurityPage.tsx`, `PasswordCard.tsx`, `LockPinCard.tsx`, `AppearancePage.tsx`, `LanguageCard.tsx`, `ProfilePage.tsx`, i18n `account`
- Test: their `*.test.tsx`, `routes/EnrolmentLayout.test.tsx`, `ChangePasswordPage.test.tsx`, `PasswordChangeForm.test.tsx`

**Interfaces:**
- Consumes: Task 4 primitives; Task 3 `PinForm`; `me.lock_pin_legacy`.

- [ ] **Step 1:** Failing tests: Security shows two cards, "Sign-in" (badge second factor on/off; rows Password, Authenticator app, Recovery codes, Passkeys with the passkey list under it) and "Session" (rows Lock PIN, This session's expiry + "Lock now" when a PIN is set); "Change password …" opens a dialog with the password form; "Set up …" for the authenticator, "Regenerate …" for recovery codes, "Add passkey …" and "Set PIN …"/"Change PIN …" each open a dialog; a legacy PIN shows the warn badge; step-up behaviour unchanged. Enrolment (`onEnrolled`) shows only the Sign-in card and completes as before. Appearance: cards Density (picker body), Lists (row Page size, immediate), Language (row, immediate), Saved columns (rows with Reset). Profile: rows Name, Email, then an Organizations card.
- [ ] **Step 2:** Implement with `SettingsPage`/`SettingsCard`/`SettingsRow`; flows move into `Dialog`s unchanged in logic.
- [ ] **Step 3:** vitest `src/features/account src/routes src/features/lock`, tsc, eslint. Commit `feat(web): account settings on the card layout`.

### Task 7: SPA — organization General and the digest card

**Files:**
- Modify: `web/src/features/settings/OrgGeneralPage.tsx`, `OrgMonitoringCard.tsx`, `AssistantCard.tsx`, `PersonalTokensCard.tsx`, `web/src/features/notifications/DigestCard.tsx`, i18n
- Test: their tests

- [ ] **Step 1:** Failing tests: General is one column with cards Organization (rows Name, Slug read-only; `CardSaveBar`: Save disabled until the name changes, Discard restores), Monitoring (rows with unit "days" and retention select; save bar), "Assistant and token access" (switch rows; the follow-up switch indented and disabled while assistant access is off; each switch applies immediately with a toast). Digest: form card with `CardSaveBar`. Permission gating (read-only for members without `org.manage`/`org.settings.write`) unchanged: no save bar and disabled controls.
- [ ] **Step 2:** Implement with `useCardForm`.
- [ ] **Step 3:** vitest, tsc, eslint. Commit `feat(web): organization settings on the card layout`.

### Task 8: SPA — instance Email, Language, Backups

**Files:**
- Modify: `web/src/features/instance/SmtpSettingsPage.tsx`, `LanguageSettingsPage.tsx`, `BackupsPage.tsx`, `BackupSettingsForm.tsx` (split per card), i18n
- Test: `SmtpSettingsPage.test.tsx`, `LanguageSettingsPage.test.tsx`, `BackupsPage.test.tsx`, `BackupsPage.de.test.tsx`

- [ ] **Step 1:** Failing tests: Email: badge configured/not configured in the page header; card "SMTP server" (rows; STARTTLS switch is a form field; save bar gated on dirty; the password field's "leave empty to keep" rule unchanged); card "Test email" (row recipient + "Send test email"). Language: one card, row with the select, save bar. Backups: header actions "Back up now"; a status card (last / next / destination as rows or facts); cards Destination and Schedule each with its own save bar; saving Destination sends the destination fields on top of the last saved schedule (Review Focus 4); the backup list below as today.
- [ ] **Step 2:** Implement.
- [ ] **Step 3:** vitest, tsc, eslint. Commit `feat(web): instance settings on the card layout`.

### Task 9: SPA — member page and instance account page

**Files:**
- Modify: `web/src/features/members/MemberPage.tsx` (+ `MemberRole`), `web/src/features/users/AccountPage.tsx`, `AccountSignIn.tsx`, `AccountActions.tsx`, i18n
- Test: `MemberPage.test.tsx`, `AccountPage.test.tsx` and the component tests

- [ ] **Step 1:** Failing tests: both pages render `SettingsPage` (no `DetailLayout` sidebar); the facts that were in the sidebar become rows of a first card; role change is a row with the select and its own save bar ("Change role" stays the save label); destructive actions sit in a last card titled "Danger zone" / "Gefahrenbereich" with ghost-destructive buttons opening the existing dialogs.
- [ ] **Step 2:** Implement. `DetailLayout` stays for other pages.
- [ ] **Step 3:** vitest, tsc, eslint. Commit `feat(web): member and account pages on the card layout`.

### Task 10: SPA — list pages use the shared header

**Files:**
- Modify: `account/PersonalTokensPage.tsx`, `account/MyActivityPage.tsx`, `members/MembersPage.tsx`, `activity/ActivityPage.tsx`, `tokens/TokensPage.tsx`, `secrets/SecretsPage.tsx`, `notifications/ChannelsPage.tsx`, `notifications/AlertRoutesPage.tsx`, `notifications/RotationsPage.tsx`, `users/UsersPage.tsx`, `instance/OrganizationsPage.tsx`, `instance/shared/SharedListFrame.tsx`

- [ ] **Step 1:** Each page's hand-written `h1`/hint/action row becomes `PageHeader` (title, description, actions). Existing tests are the safety net; add none unless one breaks for a reason other than markup.
- [ ] **Step 2:** vitest for the touched features, tsc, eslint. Commit `refactor(web): one page header on the settings list pages`.

### Task 11: e2e, docs, backlog

**Files:**
- Modify: `web/e2e/*.spec.ts` touched by the changes (settings-area, unsaved-guard, members, accounts, notifications, personal-tokens, passkey, setup, session lock), add PIN-box coverage (set a PIN in boxes, lock, wrong PIN, unlock).
- Modify: `docs/BACKLOG.md` rows 3.106 (PIN), 3.107 (settings layout), 3.108 (the signed-out root loop fixed in b8d4f99c, found 2026-11-18, in rc.4).
- User docs in the scratch clone `/private/tmp/claude-501/-Users-noidee--dev-pulsix/76b69f36-5040-49f9-a80e-08a1ef63bcdb/scratchpad/noix-docs-merge` on a new branch `docs/pulsix-pin-settings-layout` from `docs/pulsix-lists-and-tokens`: `user/account-and-security.md` (4-digit PIN, boxes, older PINs), settings navigation and save model where the docs describe settings pages. No tech-stack names.

- [ ] **Step 1:** Run the full Playwright suite the repo's way (it builds its own stack; never touch other containers); fix fallout.
- [ ] **Step 2:** Backlog rows, docs commit.
- [ ] **Step 3:** `make test` (repo root), `golangci-lint run`, `cd web && npx vitest run && npx tsc -b && npx eslint .`. Commit `docs: backlog 3.106-3.108` / `test(e2e): …`.
