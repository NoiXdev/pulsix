# One list component, appearance settings and a personal settings area — design

Backlog 3.97 (shared list component: sort, row menu, context menu, columns,
pages), 3.98 (appearance settings: density, page size, saved columns) and
3.99 (personal settings area with its own navigation). Owner's feedback,
2026-11-17; the owner approved a clickable mockup
(https://claude.ai/artifact/3PYEVUfYGnvZ5fmdTTQrDH, private) and the
assumptions below.

## Intent

Pulsix has about thirty lists. They look alike but behave differently:
some have a "…" row menu, many have inline buttons, none can be sorted,
none lets a person choose columns, and the long ones load more rows with a
"Load more" button instead of pages. The owner wants every list to work the
same way and to fit the way a person likes to work:

- **Sort** by clicking a column's name.
- **Row actions always in the "…" menu**, and the same menu on a
  **right-click** on the row.
- **Choose columns** per list, remembered **per user**.
- **Pages** with a choosable **page size**.
- **Three densities** — comfortable, standard, compact.
- Hosts get an **Active runs** column.
- A **personal settings area**: when a person is in their own settings,
  the main navigation turns into the settings menu, with a back link at
  the top.

## Decided with the owner (mockup and chat)

- One component for all lists; everything below applies to every list.
- Sorting: click a column name → ascending, again → descending, again →
  off (the list's default order). Only columns that make sense to sort are
  sortable.
- The "…" menu is the only place for row actions; inline action buttons in
  rows go away. Right-click (and the keyboard's context-menu key or
  Shift+F10) on a row opens the same menu at the pointer; when the row
  opens a page, the context menu also offers "Open in new tab" and "Copy
  link".
- "Columns" button above the list: show and hide columns, reorder them
  (drag, or up/down buttons), "Restore defaults". The name column is always
  shown and stays first.
- **Pages instead of "Load more"**, with a footer "1–25 of 137 hosts",
  a page-size select (10 / 25 / 50 / 100) and page numbers. Long lists sort
  and page **on the server**, so sorting covers every row, not just the
  loaded ones.
- Densities: **Comfortable** (more air, a host's address under its name),
  **Standard** (today's look), **Compact** (tight rows, smaller type).
- Column choice, density and page size are stored **per user on the
  server**, so they follow the person to every browser.
- Hosts: new column **Active runs** ("2 running", linking to that host's
  live runs) and a filter chip **With active runs**. Agent version and
  operating system become optional columns where the data is already there.
- Settings: the left navigation switches to the settings menu with
  "← Back to <organization>" at the top, the person's name and role under
  it, then the settings pages. No second sidebar inside the page.
- The **Appearance** page holds density (three cards with a small
  preview), page size, language (moved here from the security page) and a
  list of saved column choices with a reset per list.

## Designer's calls (not discussed; check when reading)

1. **Settings pages**: Security (`/account/security`, today's page without
   the language card), **Appearance** (`/account/appearance`, new),
   **Profile** (`/account/profile`, new, read-only: name, email, the
   organizations and roles — there is no API to edit one's own name or
   email today, and email changes need verification; editing stays out of
   scope), **My activity** (`/account/activity`, new page on the existing
   `GET /api/v1/me/activity`), and **API tokens** (`/account/tokens`) for
   the user's personal tokens — specified separately in
   `2026-11-17-personal-tokens-design.md` (3.100). Organization tokens
   stay in the organization's settings.
2. The account menu's "Account security" item becomes **"Settings"** and
   opens `/account/security`, the page people already know. The back link
   returns to the organization the person came from (else their first).
3. **Sort and page live in the URL** (`?sort=name&order=asc&page=2`), not
   in the preferences: links and the back button keep working, and a sort
   is a momentary view, not a setting. Page size is one setting for all
   lists; changing it in a list's footer changes that setting.
4. **"Active runs"** counts the host's live runs (queued, claimed,
   running, cancelling). Its link opens the runs list filtered to that
   host and to live runs; the runs list gains a `status=active` filter
   value for that.
5. **Server paging** is offset-based (`page`, `per_page`, `sort`,
   `order`; the answer carries `total`), added beside the existing cursor
   parameters, which stay for the assistant tools (internal/mcp) and the
   pickers. When `page` is absent an endpoint behaves exactly as today.
6. **Whole lists** (every list fetched in one request) sort and page in
   the browser with the same component; nothing changes on the server for
   them.
7. Density applies to every list built on the component, not to other
   tables (detail-page key/value tables keep their spacing).
8. The default for every new setting is today's behaviour: standard
   density, 25 per page, every list's current columns.

## Goals

- Every list on one component, with identical sorting, menus, columns,
  paging and density.
- Preferences per user on the server; the GUI reads them once per load and
  writes them when changed.
- No list loses a capability it has today (filters, search, links, the
  run-requests badge, the shell's ⌘K search into the hosts list).

## Non-goals

- Editing one's own name or email.
- A cross-organization API-token page.
- Remembering sort per list; bulk selection; inline editing; column
  resizing.
- Changing the cursor API that the assistant tools use.

## Anti-goals

- Sorting only the rows that happen to be loaded on a server-paged list.
- A context menu with actions the "…" menu does not have, or the reverse
  (except the two link entries).
- A preference write per keystroke or drag step — debounce or write on
  release.
- Breaking keyboard use: every menu is reachable and operable by keyboard,
  with focus returning to its origin.

## Server

### Preferences

- Migration (next free number; 00107 is the latest as of writing):
  `user_preferences (user_id uuid primary key references users on delete
  cascade, density text not null default 'standard' check (density in
  ('comfortable','standard','compact')), page_size int not null default 25
  check (page_size in (10,25,50,100)), lists jsonb not null default '{}',
  updated_at timestamptz not null default now())`. Additive; a missing row
  reads as the defaults.
- `lists` is `{ "<list id>": { "columns": ["<column id>", …] } }` — the
  ordered visible columns. The server validates the shape only (list ids
  and column ids `^[a-z][a-z0-9_.-]{0,63}$`, at most 64 lists, 40 columns
  each, document ≤ 16 KiB); the SPA owns which ids exist and ignores
  unknown ones. A list absent from the map uses its defaults.
- `GET /api/v1/auth/me/preferences` and `PUT /api/v1/auth/me/preferences`
  (whole document). Gate: an MFA-complete, unlocked cookie session
  (`requireInteractiveSession`). Audited (`setPreferences`, object type
  `user`) — cheap, and the activity feed shows that something changed, not
  the values. Service in its own package (`internal/preferences`) behind a
  store interface, per CLAUDE.md.

### Server-paged lists

For hosts, runs, alerts, the admin users list, the organization activity
and `/me/activity`:

- New optional query parameters: `page` (≥ 1), `per_page` (10, 25, 50,
  100), `sort` (a per-endpoint whitelist), `order` (`asc`/`desc`). The
  response adds `total` (rows matching the filters). With `page` absent the
  endpoint answers exactly as today (cursor mode).
- Sortable keys (each belongs to a visible column; columns not listed are
  not sortable):
  - hosts: `name`, `zone`, `managed`, `monitored`, `last_seen`,
    `active_runs`; default `name asc`.
  - runs: `created` (the column today labelled "Started" shows
    `created_at`; it is relabelled "Created"), `status`, `kind`,
    `trigger`; default `created desc`. Template is not sortable (the list
    has only its id).
  - alerts: `opened` (the "Open" column), `host` (host name), `peak`,
    `tracked` (severities, ordered crit > warn > unknown), `acknowledged`;
    default `opened desc`. Mute is not sortable.
  - users: `email`, `name`, `last_sign_in`; default `email asc`. The
    derived account state is not sortable.
  - activity: `at` only; default `at desc`.
- **Alerts' `muted` filter in page mode**: a mute is decided in Go
  (snoozes, one-off and cron maintenance windows, `alerts.Mute`), so the
  service first works out the hosts muted right now and passes their ids
  (and the snooze condition) into the page query. That keeps `total`
  right. The cursor mode keeps today's behaviour.
- Every sort ends with the row id as tie-breaker, so pages are stable.
- Hosts gain `active_runs` (int) in every list answer (a count over
  `job_hosts` joined to `jobs` in a live status) and a filter
  `active_runs=true`. Runs accept `status=active` (= queued, claimed,
  running, cancelling).

## SPA

### Component

`web/src/ui/data-list/` — presentational (no `src/api` value imports):

- Column definition: `id`, `label`, `width` (grid track), `sortKey?`,
  `required?` (the name column), `defaultVisible`, `render(row)`,
  `align?`.
- Props: rows, row key, row link (optional, as `ListRow` today), row
  actions (a function returning menu items — one source for the "…" menu
  and the context menu), sort state + change handler, paging state + change
  handler (`total`, `page`, `perPage`), visible-columns state + change
  handler, density, empty and loading states, toolbar slot (search,
  filters) to the left of the "Columns" button.
- Keyboard and screen readers: sortable headers are buttons inside
  `columnheader`s with `aria-sort`; the menus are `role="menu"` with arrow
  navigation and Escape; the context-menu key and Shift+F10 open the row
  menu; focus returns to the row or button.
- Builds on today's `ListRow`, `RowMenu`, `Card` and grid styles; those
  stay as the building blocks.

### Glue

- `usePreferences()` (query + mutation on the preferences routes) and
  `useListView(listId, columns, { mode: 'client' | 'server' })` in
  `web/src/features/lists/`: merges saved columns with the list's defaults,
  reads/writes `sort`/`order`/`page` in the URL, applies page size and
  density, and for client mode sorts and slices the rows.
- Density is set once on the document (`data-density`) from the
  preferences; the component's row and cell spacing reads CSS variables.

### Migration of the lists

Every list in the inventory (hosts, runs, alerts, users, activity, fleet,
tokens, members and pending invites, repos, playbooks, templates,
schedules, run requests, groups and their member/children sections,
credentials, jump hosts, secrets, agents and agent tokens, workers and
enrollments, checks, monitoring groups, rules, item decisions,
maintenance, channels, alert routes, rotations, organizations, backups,
shared repos/templates/checks, the host page's runs section). Inline action
buttons — and inline switches such as an alert route's enable toggle —
move into the row menu with the same labels, permissions and
confirmations; a switch becomes an Enable/Disable entry and the state is
shown read-only in its column. Pickers and dialogs that show short lists
are not lists in this sense and stay as they are.

- **Two lists on one page** (members and pending invites, agents and
  agent tokens, workers and enrollments, a group's sections): only the
  page's main list keeps sort and page in the URL; the secondary list keeps
  them in component state.
- **The host page's runs section** stays a teaser of the latest runs with
  a link to the full list: it uses the component without paging.
- **The hosts tag-selector mode** (one answer with every matching host)
  sorts and pages in the browser; the selector answer also carries
  `active_runs`.

### Settings area

- `AppShell` gets a navigation variant: the organization navigation
  (today) or the settings navigation (back link, name and role, then
  Security, Appearance, Profile, API tokens, My activity). The `/account/*` pages use
  the settings variant through one `AccountLayout` (replacing
  `AccountSecurityLayout`).
- The forced pages (`/account/enrol`, `/account/password`,
  `/account/lock-pin`) stay as they are, without the shell.

## Constraints

- CLAUDE.md layering and testing rules; migrations additive; TDD.
- i18n en and de for every string (German in the catalogue's du-form).
- The OpenAPI document and the generated client are regenerated.
- User docs in the docs repo (lists, appearance settings, settings area);
  no tech-stack names.
- Work stays on the owner's local `main`; nothing is pushed until the owner
  asks.

## Left to the builder

- Exact component file split, CSS variable names, debounce timing.
- SQL shape of the per-endpoint sorting (whitelisted `CASE` ordering or one
  query per key) as long as it is index-friendly for the defaults.
- Which optional extra columns a list offers beyond today's, where the
  data is already in the list answer.
