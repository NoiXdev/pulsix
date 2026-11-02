# GUI languages: English and German (backlog 1.41)

Status: approved for implementation 2026-11-02. The user skipped the design
questions ("los gehts"), so every decision below that is not quoted from the
backlog row is the implementer's call, marked as such where it matters.

## Intent

Pulsix's web interface speaks English only. Teams that run it in German want
the interface in German. An installation picks a default language once, at
setup, and each person can choose their own afterwards.

## Goals

- Every user-visible string of the SPA exists in English and German.
- The instance has a **default language**, chosen in the first-run wizard
  and changeable later by an instance administrator.
- Each user can override it on their account page. The override follows the
  user to every browser and device, because it is stored on the server.
- Dates, times and numbers are formatted for the active language.
- A string missing from the German catalogue is a **build error**, not a
  silently English sentence on a German page.

## Non-goals

- **Server messages stay English** (implementer's call, the first of the
  backlog row's three questions). Errors and texts produced by the Go API
  — validation details, conflict messages, problem `detail`s — reach the GUI
  in English. The SPA's own framing around them ("Saving failed: …") is
  translated. Translating server text would mean the server learning the
  caller's language and every error site gaining a key: a separate project,
  to be weighed once the GUI half has shipped.
- **The end-user documentation stays English** (the row's second question).
  The language setting is documented in English like everything else.
- No third language, no right-to-left support, no translation workflow
  tooling, no machine translation at run time.
- Data stays as entered: host names, template names, check keys, job output,
  e-mails and notification bodies are not translated.
- Notification e-mails and chat messages stay English (they are rendered by
  the server).

## Anti-goals

- A mixed page: German labels around English buttons because a key was
  forgotten. The type system must catch that.
- A language choice that resets per browser or is lost on sign-out.
- A new runtime dependency where a small typed module does the job.

## Decisions

### Where the language comes from

The active language is resolved in this order:

1. The signed-in user's own choice (`users.locale`, NULL = none).
2. The instance default (`instance_settings` key `default_locale`).
3. `en`.

Pages shown without a session (login, invitation acceptance, setup wizard,
password and second-factor steps) use the instance default, which the public
`GET /api/v1/setup/status` returns as `default_locale`. Before setup has run
there is no default yet; the wizard starts in the browser's language when it
is German (`navigator.language` starting with `de`), otherwise English, and
its first field is the language choice itself.

Supported values: `en`, `de`. Anything else is refused with 422.

### Server side

- Migration (additive): `users.locale text NULL` with a CHECK for
  `('en','de')`. The instance default lives in the existing
  `instance_settings` table as key `default_locale`; absent means `en`.
- `GET /api/v1/auth/me` adds `locale` (the user's own, or null) and
  `effective_locale` (the resolved value), so the SPA needs one call.
- `PUT /api/v1/auth/me/locale` `{locale: "de" | null}` sets or clears the
  user's choice. Any signed-in session may call it, an API token may not
  (`requireInteractiveSession`): it is a personal preference of a person.
  Audited like other account changes.
- `GET /api/v1/setup/status` adds `default_locale` (public, like
  `completed`).
- `POST /api/v1/setup/complete` accepts an optional `default_locale`; absent
  means `en`.
- `GET`/`PUT /api/v1/admin/settings/locale` `{default_locale}` for instance
  administrators, audited, beside the SMTP settings.

### SPA

- **No library** (implementer's call). `web/src/i18n/` holds the catalogue
  and the runtime:
  - `en.ts` is the source catalogue: a nested object of strings and message
    functions (for interpolation and plurals, e.g.
    `membersCount: (n: number) => n === 1 ? '1 member' : `${n} members``).
  - `de.ts` is typed as `Messages` (the type of `en.ts`), so a missing,
    extra or wrongly-shaped key fails `tsc -b`.
  - `I18nProvider` holds the active locale; `useT()` returns the active
    catalogue; `useFormat()` returns `date`, `dateTime`, `time`, `number`,
    `relative` helpers bound to the active locale (`Intl` under the hood;
    explicit time zones keep working where pages pass one today).
- The provider resolves the language from `/auth/me` when signed in, from
  `/setup/status` otherwise, and updates without a reload when the user
  changes it.
- `<html lang>` follows the active language.
- Every `toLocale*String(undefined, …)` and bare `toLocale*String()` call
  moves to the format helpers.
- Zod schema messages (`src/lib/schemas.ts`) become functions of the
  catalogue (`schemas(t)`), so form validation speaks the page's language.
- **Tests keep running in English**: the test render helpers and the default
  provider use `en`, so the existing assertions on English text stay valid.
  New tests cover the provider, the resolution order, the German catalogue's
  completeness (type-level, plus a runtime check that no German value is
  empty), and one page rendered in German end to end.
- UI to change it: a *Language* card on the account page (choices: the
  instance default, English, Deutsch); a language field in the setup
  wizard; a *Default language* setting for instance administrators on the
  instance settings page.

### German style (implementer's call)

Informal "du" is avoided by using neutral imperatives and nouns, as German
software commonly does ("Speichern", "Host hinzufügen", "Passwort ändern").
Product terms that are names in the domain stay English where German admins
use them that way: Playbook, Inventory (as "Inventar"), Job ("Lauf" for a
run), Template ("Vorlage"), Worker, Agent, Check, Token. A glossary at the
top of `de.ts` fixes each term once.

## Constraints

- Conventional commits, additive migrations, `make test`, `golangci-lint`,
  ESLint and `tsc -b` before every commit (repo CLAUDE.md).
- The string sweep touches about 100 files; it is done feature directory by
  feature directory so each step is reviewable and green on its own.
- User-facing text of public pages names no tech stack.

## Left to the builder

- Key naming inside the catalogue (grouped by feature directory is
  expected).
- How message functions are shaped for each plural or interpolated string.
- Exact German wording, within the glossary.
- Whether the account page's language card and the instance setting share a
  component.
