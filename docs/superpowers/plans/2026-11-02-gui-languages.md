# GUI Languages (English, German) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The SPA speaks English and German; the instance default is chosen at setup and changeable by an instance admin; each user can override it, stored on the server.

**Architecture:** A tiny shared Go package (`internal/locale`) validates the two codes. The user's choice is a nullable column on `users` (auth domain); the instance default is an `instance_settings` key (setup domain). The SPA gets a typed, dependency-free catalogue under `web/src/i18n/` (one module per feature, English as the source type, German typed against it) plus `useT()` / `useFormat()` hooks; feature directories are then swept one batch at a time.

**Tech Stack:** Go (huma, sqlc, goose), React 19 + TypeScript, Vitest, `Intl`.

**Spec:** `docs/superpowers/specs/2026-11-02-gui-languages-design.md`

## Global Constraints

- Supported locales: exactly `en`, `de`; anything else is 422 at the API.
- Resolution order: user's `locale` → instance `default_locale` → `en`.
- Server-produced text (problem `detail`, validation messages) stays English; only SPA-authored text is translated.
- No new npm or Go dependency.
- A key missing from or extra in the German catalogue must fail `npx tsc -b`.
- Tests render in English by default; existing English assertions stay valid.
- Migrations additive only. Conventional Commits, footer `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.
- Before every commit: `golangci-lint run`, `gofmt -s -l .` (Go tasks); `npx eslint .` and `npx tsc -b` in `web/` (SPA tasks; Node at `$HOME/.nvm/versions/node/v24.14.0/bin`). No Prettier. Never `go test ./...`; use `go test -count=1 ./internal/<pkg>/...` per package and `make test` once at the end.
- After API changes regenerate `web/src/api/schema.d.ts` with `cd web && PATH=$HOME/.nvm/versions/node/v24.14.0/bin:$PWD/node_modules/.bin:$PATH bash scripts/api-gen.sh`.
- Generators: `PATH=/private/tmp/claude-501/-Users-noidee--dev-pulsix/76b69f36-5040-49f9-a80e-08a1ef63bcdb/scratchpad/bin:$PATH sqlc generate`.
- German glossary (spec §German style), fixed once as a comment at the top of `web/src/i18n/de/index.ts`: Run → "Lauf", Template → "Vorlage", Inventory → "Inventar", Credential → "Zugangsdaten", Jump host → "Sprunghost", Schedule → "Zeitplan", Channel → "Kanal", Alert → "Alarm", Maintenance window → "Wartungsfenster", Settings → "Einstellungen"; unchanged: Playbook, Host, Worker, Agent, Check, Token, Zone. Neutral imperatives, no "du"/"Sie" where avoidable.

## Review Focus

1. A user whose stored `locale` is cleared (`null`) must fall back to the instance default, not to English — Task 2 pins it.
2. The login page before any session must use the instance default (`/setup/status`), and the setup wizard before setup must start in German for a German browser — Task 3 pins both.
3. Switching language on the account page must re-render the whole shell without a reload, including `<html lang>` — Task 3 pins it.
4. Dates with an explicit time zone (schedules, calendar, digests) must keep that zone while switching locale — Task 3's `useFormat` test pins it; Tasks 7/8 keep the zone argument.
5. Zod validation messages must follow the language — Task 3 pins `schemas(t)` with one German assertion.

---

### Task 1: Instance default language (backend)

**Files:**
- Create: `internal/locale/locale.go`, `internal/locale/locale_test.go`
- Modify: `internal/setup/service.go` (Input, Status, new methods), `internal/setup/service_test.go`, `internal/setup/store_sql_test.go`
- Modify: `internal/api/setup_handlers.go`, test `internal/api/locale_settings_test.go` (new)

**Interfaces:**
- Produces: `package locale`: `const EN = "en"; const DE = "de"`; `func Valid(s string) bool`; `func Resolve(user *string, instanceDefault string) string` (user if valid, else default if valid, else EN).
- Produces: `setup.Service.DefaultLocale(ctx) (string, error)` (absent key → `"en"`), `setup.Service.SetDefaultLocale(ctx, l string) error` (`ErrInvalidInput` unless `locale.Valid`); `setup.Input.DefaultLocale string` (empty = `en`, invalid = `ErrInvalidInput`); `setup.Status.DefaultLocale string`. Stored as instance_settings key `default_locale`, JSON string value.
- Produces API: `GET /api/v1/setup/status` body adds `default_locale`; `POST /api/v1/setup/complete` body accepts optional `default_locale` (`enum:"en,de"`); `GET` / `PUT /api/v1/admin/settings/locale` body `{default_locale}` (instance admin, same guard as `/admin/settings/smtp`; PUT audited as `updateDefaultLocale` with object `default_locale`).

- [ ] **Step 1:** Tests for `locale.Valid` (`en`, `de` true; `""`, `EN`, `fr`, `de-DE` false) and `locale.Resolve` (`(&"de","en")→de`, `(nil,"de")→de`, `(&"xx","de")→de`, `(nil,"")→en`). Run, see them fail.
- [ ] **Step 2:** Implement `internal/locale`. Run, pass.
- [ ] **Step 3:** Setup service tests (fake store): `DefaultLocale` is `en` before any write; `SetDefaultLocale("de")` then `DefaultLocale` = `de`; `SetDefaultLocale("fr")` = `ErrInvalidInput`; `Complete` with `DefaultLocale: "de"` stores it inside the same transaction; `Complete` with `"fr"` = `ErrInvalidInput` before anything is written; `Status` carries `DefaultLocale`. Run, fail; implement; pass.
- [ ] **Step 4:** API test `TestDefaultLocaleSettings`: status is public and returns `default_locale: "en"` on a fresh instance; admin PUT `de` then GET returns `de` and status returns `de`; PUT `fr` → 422; a non-admin → 403; setup complete with `default_locale: "de"` makes status return `de`. Run, fail; implement handlers; pass. Regenerate `schema.d.ts`.
- [ ] **Step 5:** `golangci-lint run ./internal/locale/... ./internal/setup/... ./internal/api/...`; commit `feat(setup,api): an instance default GUI language`.

### Task 2: A user's own language (backend)

**Files:**
- Create: `internal/db/migrations/00074_users_locale.sql`
- Modify: `internal/db/queries/auth.sql` (or the file holding user queries), regenerate `internal/db/gen`
- Modify: `internal/auth` (User gains `Locale *string`; `Service.SetLocale(ctx, userID uuid.UUID, l *string) error`; store method), tests beside them
- Modify: `internal/api/auth_handlers.go` (`/auth/me`), new route file `internal/api/locale_handlers.go`, test `internal/api/locale_me_test.go`

**Interfaces:**
- Consumes: `locale.Valid`, `locale.Resolve`, `setup.Service.DefaultLocale` (Task 1).
- Produces: migration `ALTER TABLE users ADD COLUMN locale text CHECK (locale IN ('en','de'))` (nullable, comment citing backlog 1.41).
- Produces API: `GET /api/v1/auth/me` body adds `locale` (string or null) and `effective_locale` (`locale.Resolve(user.Locale, default)`); `PUT /api/v1/auth/me/locale` `{locale: "en"|"de"|null}` → 200 `{locale, effective_locale}`, `requireInteractiveSession`, audited as `setLocale`, object the user id; invalid → 422; a bearer token → 403.

- [ ] **Step 1:** Store/service tests: `SetLocale(&"de")` then `Me` carries `de`; `SetLocale(nil)` clears it; `SetLocale(&"fr")` = `ErrInvalidInput` (auth's sentinel or a new one mapped to 422). Run, fail.
- [ ] **Step 2:** Migration, query, sqlc, service, store. Run, pass. `go test -count=1 ./internal/db/...` (migration round-trip).
- [ ] **Step 3:** API test `TestUserLocale`: `/auth/me` before any choice → `locale: null`, `effective_locale: "en"`; with the instance default set to `de` (setup service) → `effective_locale: "de"`; PUT `en` → `effective_locale: "en"` though default is `de`; PUT `null` → back to `de` (Review Focus 1); PUT `fr` → 422; bearer → 403. Run, fail; implement; pass. Regenerate `schema.d.ts`.
- [ ] **Step 4:** Lint; commit `feat(auth,api): each user can choose their GUI language`.

### Task 3: SPA i18n core, language UI, schemas

**Files:**
- Create: `web/src/i18n/types.ts`, `web/src/i18n/I18nProvider.tsx`, `web/src/i18n/useT.ts`, `web/src/i18n/format.ts`, `web/src/i18n/en/index.ts`, `web/src/i18n/de/index.ts`, `web/src/i18n/en/common.ts`, `web/src/i18n/de/common.ts`, tests `web/src/i18n/*.test.ts(x)`
- Create: `web/src/features/instance/LanguageSettingsPage.tsx` (+ test), route `/settings/language` beside the mail route in `web/src/router.tsx`, nav entry wherever the Mail entry is gated for instance admins
- Modify: `web/src/main.tsx` (wrap in provider), `web/src/test/*` render helpers (provider with `en`), `web/src/lib/schemas.ts` → `schemas(t)` and its callers, `web/src/features/account/AccountSecurityPage.tsx` (Language card), `web/src/features/setup/SetupWizardPage.tsx` (language field first), `web/src/api/hooks/*` (hooks for the three new endpoints)

**Interfaces:**
- Consumes: the API fields and routes of Tasks 1–2.
- Produces: `type Locale = 'en' | 'de'`; `type Messages = typeof en` (en is a plain object assembled from feature modules: `export const en = { common, ... }`); each feature module exports `en<Feature>` / `de<Feature>`, the German one declared as `const deX: Messages['x'] = {...}` so excess and missing keys fail.
- Produces: `useT(): Messages`; `useLocale(): { locale: Locale; setLocale(l: Locale | null): Promise<void> }`; `useFormat(): { date(d: Date|string, opts?: Intl.DateTimeFormatOptions & {timeZone?: string}): string; dateTime(...): string; time(...): string; number(n: number, opts?: Intl.NumberFormatOptions): string }`.
- Produces: `schemas(t: Messages)` returning the same schema set `lib/schemas.ts` exports today, with messages from `t.validation`.
- Rule for later tasks: add strings only to your own feature module(s), `en/<feature>.ts` and `de/<feature>.ts`, and register them in both `index.ts` files.

- [ ] **Step 1:** Tests: provider resolves `effective_locale` from `/auth/me` when signed in and `default_locale` from `/setup/status` otherwise (Review Focus 2, first half); before setup (`completed: false`) a `navigator.language` of `de-DE` gives `de`, `fr-FR` gives `en` (second half); `setLocale('de')` calls `PUT /auth/me/locale` and re-renders a consumer in German without a reload, and `document.documentElement.lang` becomes `de` (Review Focus 3); `useFormat().dateTime(…, {timeZone:'Asia/Tokyo'})` keeps the Tokyo time under both locales (Review Focus 4); a runtime walk asserts no German leaf is an empty string; `schemas(deMessages)` reports the German "required" text (Review Focus 5). Run, fail.
- [ ] **Step 2:** Implement the i18n module, provider wiring, test-helper default, `schemas(t)` (move every existing message into `en/validation.ts` + German), the three hooks, the account Language card (choices: "Instance default (<language>)", "English", "Deutsch"), the wizard's language field (sends `default_locale`), and `LanguageSettingsPage`. Their own texts go through the catalogue. Pass. `npx vitest run`, `npx eslint .`, `npx tsc -b`.
- [ ] **Step 3:** Commit `feat(web): a typed English and German catalogue, and where to choose the language`.

### Tasks 4–9: Translate the feature directories

Same shape for each; the batch lists the directories. For every `.tsx`/`.ts` (not tests) in the batch: move each user-visible string — JSX text, `aria-label`, `title`, `placeholder`, `toast.show` texts, `confirm()` texts, dialog titles, option labels, empty states, error framing — into the feature's catalogue module, English verbatim (existing tests keep passing unchanged), German per the glossary; replace `toLocale*String(...)` with `useFormat()` helpers keeping any `timeZone`. Server-provided strings (`err.message` from the API, data values) are not keys. Add one test per batch that renders one page of the batch under `de` and asserts two German strings.

- [ ] **Task 4:** `ui/`, `routes/`, `features/settings`, `features/activity`, `features/tokens`, remaining `lib/` texts. Commit `feat(web): German for the shell and shared components`.
- [ ] **Task 5:** `features/auth`, `features/setup`, `features/account`, `features/members`, `features/instance`. Commit `feat(web): German for sign-in, setup, account and members`.
- [ ] **Task 6:** `features/hosts`, `features/fleet`, `features/agents`, `features/workers`. Commit `feat(web): German for hosts, fleet, agents and workers`.
- [ ] **Task 7:** `features/checks`, `features/alerts`, `features/calendar`, `features/maintenance`. Commit `feat(web): German for checks, alerts and maintenance`.
- [ ] **Task 8:** `features/notifications`, `features/triggers`, `features/schedules`. Commit `feat(web): German for notifications, triggers and schedules`.
- [ ] **Task 9:** `features/inventory`, `features/credentials`, `features/runs`, `features/templates`, `features/playbooks`, `features/repos`. Commit `feat(web): German for inventory, credentials and runs`.

Each: `npx vitest run`, `npx eslint .`, `npx tsc -b` green; then a grep over the batch's non-test files for JSX text nodes of three or more letters outside `{t.…}` must come back empty except data and server strings (note any intentional exceptions in the report).

### Task 10: Close out

- [ ] **Step 1:** `make test`, `golangci-lint run`, full SPA checks.
- [ ] **Step 2:** End-user docs in NoiXdev/noix-docs, branch `pulsix` only, in a worktree under the scratchpad: the language card on *Account*, the wizard's language field and the instance default under the administrator settings; state that server messages and these docs are English. Commit there `docs(pulsix): choose the interface language`.
- [ ] **Step 3:** Backlog 1.41 → done with what shipped and what was left out (server messages, docs, e-mails). Commit `docs: close backlog 1.41`.
