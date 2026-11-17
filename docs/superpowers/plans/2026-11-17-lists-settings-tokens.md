# One list component, appearance settings, personal settings area, personal tokens — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Every list on one component with sorting, one row menu (also on right-click), chosen columns, pages and three densities, remembered per user on the server; a personal settings area with its own navigation; personal API and assistant tokens valid for chosen organizations (backlog 3.97, 3.98, 3.99, 3.100).

**Architecture:** The server gains a per-user preferences document (`internal/preferences`), offset paging with whitelisted sorting and a `total` beside the existing cursor paging of six list endpoints (shared types in `internal/paging`), an Active-runs count for hosts and a `status=active` run filter. The SPA gains a presentational `web/src/ui/data-list` component, glue in `web/src/features/lists` (URL sort/page, saved columns, density), an `AccountLayout` that switches the shell's navigation, and then migrates every list. Personal tokens live in their own tables (`personal_tokens`, `personal_token_grants` with RLS by organization, `org_token_settings`); the bearer middleware only authenticates them, `requireOrg` looks up the grant for the route's organization, and the assistant endpoint gains `/api/mcp/o/{org}`.

**Tech Stack:** Go (huma, sqlc, pgx, goose), PostgreSQL, MCP go-sdk, React + TanStack Router/Query, Vitest, Playwright, bash (connect script).

**Spec:** `docs/superpowers/specs/2026-11-17-lists-and-appearance-design.md` (3.97–3.99) and `docs/superpowers/specs/2026-11-17-personal-tokens-design.md` (3.100) — read both first; they are the authority on behaviour. This plan fixes names, files, values and tests.

**Worktree / branch:** `/Users/noidee/_dev/.worktrees/pulsix-session-lock`, branch `feat/list-component`.

## Global Constraints

- TDD: every task writes its failing test first, runs it red, then the minimal implementation.
- Layers per `CLAUDE.md`: handlers decode/call/encode; rules in services behind interfaces declared next to them; stores are sqlc (`internal/db/gen`). Clock injected. `web/src/ui` never value-imports `src/api` (ESLint enforces).
- Go tests: the race suite is `make test` — never one whole-module `go test ./...`. Single packages: `go test -race -timeout 900s ./internal/<pkg>/...`; `internal/api` with `-timeout 1800s`. `golangci-lint run` → 0 issues before every commit.
- SPA, from `web/`: `npm test`, `npm run lint`, `npx tsc -b` all green before every SPA commit.
- Regenerate after query/API changes: `PATH=$PATH:$HOME/go/bin make sqlc`; `cd web && npm run api:gen`. Commit generated files with the change.
- Migrations additive (`docs/upgrades.md`), next free number. `00107` is the latest: this plan uses `00108` (preferences), `00109` (live-jobs index), `00110` (personal tokens). If `main` gained migrations meanwhile, rebase first and take the next free numbers then.
- i18n: every user-visible string in `web/src/i18n/en` and `web/src/i18n/de` (German in the catalogue's du-form).
- Timestamps that Go stores and also returns are truncated to `time.Microsecond` (Postgres precision) before they are stored and returned.
- No secrets in logs, audit summaries or error texts (token plaintext, secret hashes). Preference values are not put into audit summaries either.
- Spec values (verbatim): densities `comfortable` / `standard` / `compact`, default `standard`; page sizes `10, 25, 50, 100`, default `25`; preference ids `^[a-z][a-z0-9_.-]{0,63}$`, at most 64 lists, 40 columns each, `lists` document ≤ 16 KiB; at most 50 personal tokens per user, 50 grants per token; live run statuses `queued, claimed, running, cancelling`.
- Moving an inline button or switch into a row menu keeps its label, permission and confirmation; the task that moves it also updates every Vitest test and `web/e2e` spec that used it (grep the label).
- Docker: about 50 foreign containers run on this machine — touch only what the task starts (testcontainers, the e2e compose project).
- Conventional Commits, footer `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. No push, no tag.
- Public docs (noix-docs) never name the tech stack.

## Review Focus

1. **A page past the end.** A stale `?page=9`, deleting the last row of the last page, or raising `per_page` leaves the page beyond the last. The server answers 200 with no rows and the right `total` (Task 2 `TestHostsPageBeyondEnd`); the SPA moves to the last page instead of showing the empty state (Task 7 `clamps a page past the end`).
2. **Changing a filter on page 3.** A new search, chip or the ⌘K box must start again at page 1, or the person sees an empty page for a filter that has matches (Task 7 `resets the page when resetKey changes`; Task 9 hosts test).
3. **Sorting by a column with NULLs and ties.** Hosts never seen, users who never signed in, alerts never acknowledged: NULLs sort last in both directions, and the id tie-break keeps pages disjoint — walking every page yields every row exactly once (Task 2 `TestHostsSortEveryKeyStable`, Task 5 users test).
4. **Stale saved columns.** A saved column list that names columns which no longer exist, repeats one, or leaves out the required name column must render the name column first and drop the rest silently; the server refuses duplicates (Task 1 `TestSetValidates`, Task 7 `mergeColumns` tests).
5. **Personal token timestamps.** `created_at` and `expires_at` from the create answer must equal what the list answers a moment later (microsecond precision), or the SPA shows the token as changed (Task 17 `TestCreatePersonalTokenAnswersStoredPrecision`).

---

## Part A — Server: preferences and paged lists

### Task 1: User preferences (store, service, routes)

**Files:**
- Create: `internal/db/migrations/00108_user_preferences.sql`, `internal/db/queries/preferences.sql`
- Create: `internal/preferences/service.go`, `internal/preferences/store_sql.go`, tests `service_test.go` (fake store), `store_sql_test.go` (`internal/db/testdb`)
- Create: `internal/api/preferences_handlers.go`, `internal/api/preferences_handlers_test.go`
- Modify: `internal/api/api.go` (`Deps.Preferences`, `validate`, `registerRoutes`), `internal/api/apitest/apitest.go`, `internal/app/app.go` (wiring)

**Interfaces:**
- Produces table (spec, verbatim): `user_preferences (user_id uuid primary key references users on delete cascade, density text not null default 'standard' check (density in ('comfortable','standard','compact')), page_size int not null default 25 check (page_size in (10,25,50,100)), lists jsonb not null default '{}', updated_at timestamptz not null default now())`.
- Produces queries: `GetUserPreferences(user_id) :one`, `UpsertUserPreferences(user_id, density, page_size, lists) :one` (`ON CONFLICT (user_id) DO UPDATE`, `updated_at = now()`, returns density, page_size, lists).
- Produces:
  ```go
  package preferences
  type Density string // "comfortable" | "standard" | "compact"
  type List struct{ Columns []string `json:"columns"` }
  type Prefs struct { Density Density; PageSize int; Lists map[string]List }
  var ErrInvalid = errors.New("invalid preferences") // wrapped with the reason
  func Defaults() Prefs // standard, 25, empty map
  type Store interface {
      Get(ctx context.Context, userID uuid.UUID) (Prefs, error) // ErrNotFound when no row
      Put(ctx context.Context, userID uuid.UUID, p Prefs) (Prefs, error)
  }
  func NewService(store Store) *Service
  func (s *Service) Get(ctx context.Context, userID uuid.UUID) (Prefs, error) // Defaults() when no row
  func (s *Service) Set(ctx context.Context, userID uuid.UUID, p Prefs) (Prefs, error)
  ```
- Routes: `GET /api/v1/auth/me/preferences` and `PUT /api/v1/auth/me/preferences`, body `{density, page_size, lists: {"<list id>": {columns: [...]}}}` (whole document; `lists` never null in the answer). Gate `requireInteractiveSession` on both. PUT: `withAudit("setPreferences", "user")`, `setAuditScope(ctx, nil, p.UserID)`, summary `"changed their appearance settings"` (no values). `ErrInvalid` → 422 with the reason.

- [ ] **Step 1: Failing tests**
  - `TestGetReturnsDefaultsWithoutRow` — `Density == "standard"`, `PageSize == 25`, `len(Lists) == 0`.
  - `TestSetValidates` (table): density `"dense"`; page_size `20`; list id `"Hosts"`; list id of 65 chars; column id `"1col"`; 65 lists; 41 columns in one list; a column repeated in one list; serialized `lists` over 16384 bytes → all `errors.Is(err, ErrInvalid)`. Valid: `{"hosts": {"columns": ["name","zone","active_runs"]}}`, `"agent.version"`, `"last_seen"`.
  - `TestStoreUpsertRoundTrip` (testdb): put twice, get returns the second; deleting the user deletes the row.
  - API: `TestPreferencesDefaults` (GET → standard/25/{}), `TestPutPreferencesRoundTrip`, `TestPutPreferencesInvalidIs422`, `TestPreferencesRefuseBearerToken` (403), `TestPreferencesRefuseLockedSession` (403 `session_locked`), `TestPutPreferencesIsAudited` (one event `setPreferences`, no org, summary does not contain any column id).
- [ ] **Step 2: Run** `go test -race -timeout 900s ./internal/preferences/...` and `go test -race -timeout 1800s -run Preferences ./internal/api/` → FAIL.
- [ ] **Step 3: Implement** migration, queries, `make sqlc`, service, store, handlers, wiring.
- [ ] **Step 4: Run** the same → PASS; `TestEveryMutatingRouteIsAuditedOrAllowlisted` still passes; `golangci-lint run` clean.
- [ ] **Step 5: Commit** `feat(preferences): store density, page size and list columns per user`

### Task 2: Offset paging foundation, hosts page mode, Active runs

**Files:**
- Create: `internal/paging/paging.go`, `internal/paging/paging_test.go`
- Create: `internal/api/list_page.go`, `internal/api/list_page_internal_test.go`
- Create: `internal/db/migrations/00109_jobs_live_idx.sql`
- Modify: `internal/db/queries/hosts.sql` (`ListHosts` gains `active_only`; new queries below), `internal/hosts/service.go` (`ListFilter`, new methods), `internal/hosts/store_sql.go`
- Modify: `internal/api/host_handlers.go` (`hostListInput`, `hostListOutput`, list item DTO), `internal/api/tag_handlers.go` (`POST /hosts/query` answer)
- Test: `internal/hosts/store_sql_test.go`, `internal/api/host_page_test.go`

**Interfaces:**
- Produces:
  ```go
  package paging
  var Sizes = []int{10, 25, 50, 100}
  const DefaultSize = 25
  type Page struct { Number, Size int; Sort string; Desc bool } // Number is 1-based
  func (p Page) Offset() int
  ```
  ```go
  // internal/api/list_page.go — embedded in each paged list input
  type pageQuery struct {
      Page    int    `query:"page" minimum:"1" doc:"Page number; when set the list is offset-paged and answers total"`
      PerPage int    `query:"per_page" enum:"10,25,50,100"`
      Order   string `query:"order" enum:"asc,desc"`
  }
  // pageOf returns nil when q.Page is 0 (cursor mode). per_page 0 → 25.
  // sort "" → defSort with defDesc (unless order is given); sort set and
  // order "" → ascending. cursor and page together → 422.
  func pageOf(q pageQuery, sort, cursor, defSort string, defDesc bool) (*paging.Page, error)
  ```
  The sort parameter is declared per endpoint (`Sort string query:"sort" enum:"..."`) so the OpenAPI document lists each whitelist. The answer gains `Total *int json:"total,omitempty"` — set only in page mode (then `next_cursor` is `""`).
- Hosts: `ListFilter.ActiveRuns *bool` (true → only hosts with at least one live run; both modes; query `active_runs=true|false`). `Sort` enum `name,zone,managed,monitored,last_seen,active_runs`, default `name asc`. `last_seen` NULLs last in both directions; ties: then `name`, then `id`.
  ```go
  func (s *Service) ListHostsPage(ctx context.Context, orgID uuid.UUID, f ListFilter, p paging.Page) ([]Host, int, error)
  // ActiveRuns counts each host's live runs (job_hosts joined to jobs in
  // queued, claimed, running, cancelling); hosts without one are absent.
  func (s *Service) ActiveRuns(ctx context.Context, orgID uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]int, error)
  ```
  sqlc queries: `ListHostsPage` (the default order `name ASC, id ASC` with `OFFSET`/`LIMIT` — index-friendly), `ListHostsPageSorted` (whitelisted `CASE` ordering for every other key/direction), `CountHosts` (same filters), `ActiveRunsForHosts(org_id, ids uuid[])`. `00109`: `CREATE INDEX jobs_live_idx ON jobs (org_id) WHERE status IN ('queued','claimed','running','cancelling')` (a comment in `docs/upgrades.md` style at the top of the file).
- Every hosts list answer (cursor mode, page mode, `POST /hosts/query`) carries `active_runs` (int, 0 when none) on each host through `hostListItemDTO` (embeds `hostDTO`, adds `ActiveRuns int json:"active_runs"`); `GET /hosts/{id}` is unchanged.

- [ ] **Step 1: Failing tests**
  - `paging_test.go`: `Page{Number: 3, Size: 25}.Offset() == 50`.
  - `list_page_internal_test.go` (table): page 0 → nil; `{Page:2}` → size 25, default sort; sort `"zone"` without order → asc; order `desc` without sort → default key desc; cursor and page → 422.
  - `TestHostsPageModeReturnsTotalAndPage` — 30 hosts, `page=2&per_page=10` → 10 hosts (the 11th–20th by name), `total == 30`, `next_cursor == ""`.
  - `TestHostsWithoutPageKeepsCursorMode` — no `total` key in the JSON, `next_cursor` set as today.
  - `TestHostsSortEveryKeyStable` — table over every key × asc/desc with ties (several hosts sharing zone and flags): concatenating all pages of size 10 equals one size-100 page and holds every host once.
  - `TestHostsSortLastSeenNullsLast` — never-seen hosts last for asc and for desc.
  - `TestHostsPageBeyondEnd` — `page=99` → 200, `hosts == []`, `total == 30`.
  - `TestHostsPageRespectsFilters` — `q`, `zone`, `managed` narrow both rows and `total`.
  - `TestHostsPageRejectsBadParams` — `per_page=20`, `sort=address`, `order=up`, `page=0` → 422.
  - `TestHostsActiveRuns` — host A with one queued, one running and one succeeded run, host B with none: A `active_runs == 2`, B `0`; `active_runs=true` returns only A (`total == 1`); `sort=active_runs&order=desc` puts A first; `POST /hosts/query` carries `active_runs == 2` for A.
- [ ] **Step 2: Run** `go test -race -timeout 900s ./internal/paging/... ./internal/hosts/...` and `go test -race -timeout 1800s -run 'Hosts(Page|Sort|Without|Active)|ListPage' ./internal/api/` → FAIL.
- [ ] **Step 3: Implement** migration, queries, `make sqlc`, service/store, handlers.
- [ ] **Step 4: Run** the same plus the whole `internal/api` package → PASS; lint clean.
- [ ] **Step 5: Commit** `feat(api): page, sort and count the hosts list; count each host's active runs`

### Task 3: Runs — `status=active` and page mode

**Files:**
- Modify: `internal/jobs/service.go` (`Filter.Active bool`, `ListPage`), `internal/jobs/store_sql.go`, `internal/db/queries/jobs.sql`
- Modify: `internal/api/job_handlers.go` (`jobListInput` embeds `pageQuery`, `Sort`; `parseJobListFilter` accepts `active`; `jobListOutput.Total`)
- Test: `internal/jobs/store_sql_test.go`, `internal/api/job_page_test.go`

**Interfaces:**
- Consumes: Task 2 `paging.Page`, `pageQuery`, `pageOf`.
- `status=active` sets `Filter.Active` (= `queued, claimed, running, cancelling`), in cursor mode and page mode, with and without `host_id`. The 400 message for a bad status lists `active` too.
- `Sort` enum `created,status,kind,trigger`, default `created desc`. `status` orders by the rank queued, claimed, running, cancelling, succeeded, failed, cancelled, error; `kind`, `trigger` by stored value; ties `created_at DESC, id DESC`.
- `func (s *Service) ListPage(ctx context.Context, orgID uuid.UUID, f Filter, p paging.Page) ([]Job, int, error)`
- sqlc: `ListJobs`/`ListJobsForHost` gain `active_only`; new `ListJobsPage`, `ListJobsPageSorted`, `CountJobs`, and `ListJobsForHostPage`, `ListJobsForHostPageSorted`, `CountJobsForHost` (the host path reads `job_hosts` like today; defaults index-friendly).

- [ ] **Step 1: Failing tests**
  - `TestJobsStatusActive` — runs in all eight statuses; `status=active` returns exactly the four live ones, cursor and page mode, and with `host_id`.
  - `TestJobsPageMode` — 30 runs, `page=3&per_page=10` → the oldest 10, `total == 30`.
  - `TestJobsPageWithHostFilter` — total counts only that host's runs.
  - `TestJobsSortStatusRank` — `sort=status&order=asc` → queued before running before succeeded before error.
  - `TestJobsSortStable` — all pages concatenated hold every run once, for every key × direction.
- [ ] **Step 2: Run** `go test -race -timeout 900s ./internal/jobs/...`, `go test -race -timeout 1800s -run Jobs ./internal/api/` → FAIL.
- [ ] **Step 3: Implement**, `make sqlc`.
- [ ] **Step 4: Run** → PASS; lint clean.
- [ ] **Step 5: Commit** `feat(api): page and sort the runs list, filter live runs`

### Task 4: Alerts page mode with the muted filter in SQL

**Files:**
- Modify: `internal/alerts/service.go` (`ListPage`, `mutedHostIDs`), `internal/alerts/store_sql.go`, `internal/db/queries/alerts.sql`
- Modify: `internal/api/alert_handlers.go` (`alertListInput` embeds `pageQuery`, `Sort`; `Total`)
- Test: `internal/alerts/service_test.go` (fake store), `internal/alerts/store_sql_test.go`, `internal/api/alert_page_test.go`

**Interfaces:**
- `func (s *Service) ListPage(ctx context.Context, orgID uuid.UUID, f Filter, p paging.Page) ([]View, int, error)` — pins `now` once; when `f.Muted != nil` it first computes `mutedHostIDs(ctx, orgID, now) ([]uuid.UUID, error)`: for each distinct host with an open alert, `WindowsForHost` then `Mute(Alert{}, windows, now).Muted` (reuses the existing window rules); then the page query filters `muted = (closed_at IS NULL AND (snoozed_until > now OR snooze_until_change OR host_id = ANY(muted_host_ids)))`. Rows still get their `Suppression` from `Mute` as today. Cursor mode (`List`) is unchanged.
- `Sort` enum `opened,host,peak,tracked,acknowledged`, default `opened desc`. `host` = host name; `peak` = `peak_severity`, `tracked` = `severity`, both ranked crit > warn > unknown; `acknowledged` = `acknowledged_at` NULLs last. Ties `opened_at DESC, id DESC`.
- sqlc: `ListAlertsPage`, `ListAlertsPageSorted`, `CountAlerts` (each takes `now` and `muted_host_ids uuid[]` plus `muted` nullable bool), `OpenAlertHostIDs(org_id)`.

- [ ] **Step 1: Failing tests**
  - `TestAlertsPageMutedFilterTotal` — four alerts: open+snoozed, open on a host inside an active maintenance window, open plain, closed on the maintenance host. `muted=true` → 2 rows, `total == 2`; `muted=false` → 2 rows (the plain open and the closed one), `total == 2`.
  - `TestAlertsPageMutedIgnoresNonMutingWindow` — a window whose reason does not mute leaves its host's alert unmuted (same verdict as `Mute`).
  - `TestAlertsSortTrackedSeverity` — `sort=tracked&order=desc` → crit, warn, unknown.
  - `TestAlertsSortHostName` and `TestAlertsSortAcknowledgedNullsLast`.
  - `TestAlertsCursorModeUnchanged` — no `total`, cursor as before.
- [ ] **Step 2: Run** `go test -race -timeout 900s ./internal/alerts/...`, `go test -race -timeout 1800s -run Alerts ./internal/api/` → FAIL.
- [ ] **Step 3: Implement**, `make sqlc`.
- [ ] **Step 4: Run** → PASS; lint clean.
- [ ] **Step 5: Commit** `feat(api): page and sort the alerts list, muted filter included`

### Task 5: Admin users and activity feeds page mode

**Files:**
- Modify: `internal/accounts/admin.go` (`ListUsersPage`), `internal/accounts/store_sql.go`, `internal/db/queries/accounts.sql`
- Modify: `internal/audit/audit.go` (`Store`/`Recorder`: `ListPage`, `ListForActorPage`), `internal/audit/store_sql.go`, `internal/db/queries/audit.sql`
- Modify: `internal/api/admin_user_handlers.go`, `internal/api/audit_handlers.go`
- Test: `internal/accounts/*_test.go`, `internal/audit/store_sql_test.go`, `internal/api/users_activity_page_test.go`

**Interfaces:**
- Users: `func (s *Service) ListUsersPage(ctx context.Context, actorIsAdmin bool, q string, p paging.Page) ([]UserSummary, int, error)`; `Sort` enum `email,name,last_sign_in`, default `email asc`; `name` = `display_name`; `last_sign_in` NULLs last; ties `email` (unique). sqlc `ListAccountsPage`, `ListAccountsPageSorted`, `CountAccounts`.
- Activity (`GET /orgs/{org}/activity` and `GET /me/activity`): `Sort` enum `at`, default `at desc`; `asc` allowed. sqlc `ListAuditEventsForOrgPage` (`at DESC, id DESC`), `ListAuditEventsForOrgPageAsc`, `CountAuditEventsForOrg`, and the same three `…ForActor…`. Filters (`actor`, `object_type`, `since`) apply to rows and `total`. `func (r *Recorder) ListPage(ctx, orgID uuid.UUID, f Filter, p paging.Page) ([]Event, int, error)`, `ListForActorPage(ctx, actorID uuid.UUID, f Filter, p paging.Page) ([]Event, int, error)`.

- [ ] **Step 1: Failing tests**
  - `TestAdminUsersPageMode` — 30 accounts, `page=2&per_page=10`, `total == 30`, `q` narrows total.
  - `TestAdminUsersSortLastSignInNullsLast` both directions; `TestAdminUsersSortName`.
  - `TestOrgActivityPageMode` — `total` matches the filter; `order=asc` returns the oldest first.
  - `TestMyActivityPageMode` — only the caller's events across organizations; `total` right.
  - Each endpoint: no `page` → cursor mode unchanged.
- [ ] **Step 2: Run** `go test -race -timeout 900s ./internal/accounts/... ./internal/audit/...`, `go test -race -timeout 1800s -run 'AdminUsers|Activity' ./internal/api/` → FAIL.
- [ ] **Step 3: Implement**, `make sqlc`, then `cd web && npm run api:gen` (first SPA-visible change of Part A; commit `schema.d.ts`).
- [ ] **Step 4: Run** → PASS; `make test` once for Part A; lint clean.
- [ ] **Step 5: Commit** `feat(api): page and sort the users list and the activity feeds`

---

## Part B — SPA: the list component and its glue

### Task 6: `web/src/ui/data-list`

**Files:**
- Create: `web/src/ui/data-list/types.ts`, `DataList.tsx`, `SortHeader.tsx`, `RowActionsMenu.tsx`, `ColumnsMenu.tsx`, `Pager.tsx`, `pageItems.ts`, `index.ts`, tests `DataList.test.tsx`, `ColumnsMenu.test.tsx`, `Pager.test.tsx`, `pageItems.test.ts`
- Create: `web/src/ui/menu-popover.tsx` (the portal/positioning/keyboard core extracted from `RowMenu`); Modify `web/src/ui/list-row.tsx` so `RowMenu` uses it (behaviour and API unchanged; `list-row.test.tsx` stays green)
- Modify: `web/src/styles/globals.css` (density variables), `web/src/i18n/{en,de}/ui.ts` (`ui.dataList.*`)

**Interfaces:**
- Produces (exported from `web/src/ui/data-list/index.ts`):
  ```ts
  export type Density = 'comfortable' | 'standard' | 'compact';
  export type SortOrder = 'asc' | 'desc';
  export interface SortState { key: string; order: SortOrder }
  export interface DataListColumn<Row> {
    id: string;
    label: string;            // header text; '' for an icon column (menuLabel then names it)
    menuLabel?: string;
    width: string;            // grid track
    sortKey?: string;         // absent → not sortable
    sortValue?: (row: Row) => string | number | boolean | null | undefined; // client mode
    required?: boolean;       // always shown, always first
    defaultVisible: boolean;
    align?: 'start' | 'end';
    render: (row: Row, ctx: { density: Density }) => ReactNode;
  }
  export type RowAction =
    | { kind?: 'action'; id: string; label: string; onSelect: () => void; destructive?: boolean; disabled?: boolean }
    | { kind: 'link'; id: string; label: string; href: string; download?: string };
  export interface RowLink { to: LinkProps['to']; params: LinkProps['params']; search?: LinkProps['search']; hash?: LinkProps['hash']; label: string }
  export interface DataListProps<Row> {
    label: string;                                  // accessible name of the table
    columns: DataListColumn<Row>[];
    rows: readonly Row[];
    rowKey: (row: Row) => string;
    rowLink?: (row: Row) => RowLink | undefined;
    rowActions?: (row: Row) => RowAction[];         // one source for "…" and right-click
    sort?: SortState | null;
    onSortChange?: (next: SortState | null) => void;
    paging?: { page: number; perPage: number; total: number; countLabel: (n: number) => string;
               onPageChange: (page: number) => void; onPerPageChange: (n: number) => void };
    visibleColumns: string[];
    onVisibleColumnsChange: (ids: string[]) => void;
    defaultColumns: string[];                       // for "Restore defaults"
    density: Density;
    loading?: boolean;
    empty: ReactNode;
    toolbar?: ReactNode;                            // left of the "Columns" button
  }
  export function DataList<Row>(props: DataListProps<Row>): JSX.Element
  export function pageItems(page: number, pageCount: number): (number | 'gap')[]
  ```
- Semantics (decided here): `role="table"` with `aria-label`; header `role="row"` of `role="columnheader"` cells (`aria-sort` `ascending`/`descending`/`none` on sortable ones, a `<button>` inside); each body row `role="row"` with `role="cell"` cells. A linked row keeps today's look (`ListRow`'s frame, grid, hover accent) but its link is one `<a>` in the required (name) cell, stretched over the row (`after:absolute after:inset-0`), so middle-click, copy-link and screen readers see one link with the row's `label`; the "…" button and in-cell links (e.g. Active runs) sit above it (`relative z-10`). `ListRow`/`StaticListRow`/`RowMenu`/`Card` stay for pickers and dialogs.
- Sorting cycle: none → asc → desc → none (`onSortChange(null)`).
- Context menu: right-click, the ContextMenu key and Shift+F10 on a focused row open the row's menu at the pointer (keyboard: at the row's top-left); same entries as "…", plus "Open in new tab" and "Copy link" (`copyText` from `web/src/lib/clipboard.ts`) when the row has a link; href from `useRouter().buildLocation(...)`. Escape closes; focus returns to the row (context menu) or the "…" button. A row without actions gets neither menu.
- Columns menu: one checkbox per column in display order (required one checked and disabled), up/down buttons, drag to reorder (HTML drag and drop; `onVisibleColumnsChange` fires on drop, not on drag-over), "Restore defaults" → `defaultColumns`.
- Footer: `t.ui.dataList.range(from, to, countLabel(total))` — en `"1–25 of 137 hosts"`, de `"1–25 von 137 Hosts"`; per-page select 10/25/50/100; page buttons from `pageItems`, previous/next.
- `pageItems` rule: always first and last, current ±1; a gap that would hide exactly one page shows that page instead.
- Density CSS (globals.css): `--px-row-py` comfortable `0.875rem`, standard `0.625rem` (today's `py-2.5`), compact `0.375rem`; compact also `--px-row-font: 12px`; selectors `:root[data-density="…"]`; only `DataList` rows read them.

- [ ] **Step 1: Failing tests**
  - `pageItems.test.ts`: `(1,1) → [1]`; `(2,4) → [1,2,3,4]`; `(5,10) → [1,'gap',4,5,6,'gap',10]`; `(3,10) → [1,2,3,4,'gap',10]`; `(10,10) → [1,'gap',9,10]`.
  - `DataList.test.tsx`: renders only `visibleColumns`, in order, required column first even when listed later; header click on a sortable column calls `onSortChange({key,'asc'})`, then with `asc` set `{key,'desc'}`, then with `desc` set `null`; `aria-sort` follows; a column without `sortKey` has no button; the "…" menu and the right-click menu list identical action labels; on a linked row the context menu adds "Open in new tab" and "Copy link"; Shift+F10 on a focused row opens the menu and Escape returns focus to the row; a destructive action is coloured; `empty` shows with no rows, a loading state while `loading`; the row link's accessible name is `label`.
  - `Pager.test.tsx`: `"1–25 of 137 hosts"`, `"126–137 of 137 hosts"` on page 6; choosing 50 calls `onPerPageChange(50)`; page 2 button calls `onPageChange(2)`; previous disabled on page 1.
  - `ColumnsMenu.test.tsx`: unchecking `zone` → `onVisibleColumnsChange` without it; required checkbox disabled; "Move down" swaps neighbours; "Restore defaults" → `defaultColumns`.
- [ ] **Step 2: Run** `cd web && npx vitest run src/ui/data-list src/ui/list-row.test.tsx` → FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `npm test`, `npm run lint`, `npx tsc -b` → PASS.
- [ ] **Step 5: Commit** `feat(web): one list component with sort, row menu, columns and pages`

### Task 7: Glue — preferences, list view, URL state, density

**Files:**
- Create: `web/src/api/hooks/usePreferences.ts`, `web/src/api/hooks/paging.ts`
- Create: `web/src/features/lists/useListView.ts`, `listSearch.ts`, `columns.ts`, `clientRows.ts`, `registry.ts`, `DensityEffect.tsx`, tests beside each
- Modify: `web/src/routes/RootLayout.tsx` (mount `DensityEffect` for an MFA-complete, unlocked session), i18n `lists.*`

**Interfaces:**
- Consumes: Task 1 routes; Task 6 types.
- Produces:
  ```ts
  // api/hooks/usePreferences.ts — the data hooks live in src/api per CLAUDE.md
  export const DEFAULT_PREFERENCES: Preferences; // { density: 'standard', page_size: 25, lists: {} }
  export function usePreferences(): { prefs: Preferences; loaded: boolean } // staleTime Infinity, defaults until loaded
  export function useSavePreferences(): (next: Preferences) => void         // PUT, optimistic ['preferences'] update
  // api/hooks/paging.ts
  export interface ServerPageQuery { page: number; per_page: number; sort?: string; order?: 'asc' | 'desc' }
  // features/lists/listSearch.ts
  export interface ListSearch { sort?: string; order?: 'asc' | 'desc'; page?: number } // optional keys
  export function validateListSearch(search: Record<string, unknown>): ListSearch // page < 2 → undefined
  // features/lists/columns.ts
  export function mergeColumns<Row>(saved: string[] | undefined, columns: DataListColumn<Row>[]): string[]
  // features/lists/clientRows.ts
  export function sortRows<Row>(rows: readonly Row[], columns: DataListColumn<Row>[], sort: SortState | null, rowKey: (r: Row) => string, locale: string): Row[]
  export function pageRows<Row>(rows: readonly Row[], page: number, perPage: number): Row[]
  // features/lists/registry.ts
  export type ListId = 'hosts' | 'runs' | …;  // grows with each migration task
  export function listName(id: string, t: Messages): string // unknown ids → the id
  // features/lists/useListView.ts
  export interface ListViewOptions { mode: 'client' | 'server'; urlState?: boolean /* default true */; resetKey?: string }
  export interface ListView<Row> {
    sort: SortState | null; page: number; perPage: number; visibleColumns: string[]; density: Density;
    serverQuery: ServerPageQuery;                                  // server mode
    view(rows: readonly Row[]): { rows: Row[]; total: number };   // client mode: sort + slice
    listProps(total: number): Pick<DataListProps<Row>, 'sort' | 'onSortChange' | 'paging' | 'visibleColumns' | 'onVisibleColumnsChange' | 'defaultColumns' | 'density'>;
  }
  export function useListView<Row>(listId: ListId, columns: DataListColumn<Row>[], opts: ListViewOptions): ListView<Row>
  ```
- Rules: `mergeColumns` drops unknown and repeated ids, puts the required column first, and returns the `defaultVisible` ids when `saved` is undefined. `sortRows` compares with `Intl.Collator(locale, { numeric: true, sensitivity: 'base' })` for strings, NULL/undefined/'' last in both directions, ties by `rowKey`. URL: `sort`, `order`, `page` via `useSearch({ strict: false })` and `navigate({ to: '.', search: (prev) => ({ ...prev, … }) })`; the default (no sort, page 1) leaves the keys out; with `urlState: false` they live in component state. A sort key not offered by the columns is ignored. Changing sort, page size or `resetKey` resets the page to 1. A page past the last page (known `total`) is replaced by the last page (`replace: true`). Column changes are saved debounced 400 ms (one PUT for a burst); page size changes save at once and apply to every list. `DensityEffect` sets `document.documentElement.dataset.density` from the preferences and removes it on unmount.
- List route `validateSearch`s spread `validateListSearch(search)` from Task 9 on; optional keys keep existing `search={{ q: undefined }}` call sites type-correct.

- [ ] **Step 1: Failing tests**
  - `columns.test.ts`: saved `['zone','name','gone','zone']` → `['name','zone']`; undefined → defaults; saved without the required column → it is first.
  - `clientRows.test.ts`: sorts `'host10'` after `'host9'`; NULL last for asc and desc; equal values keep `rowKey` order; `pageRows` page 2 of 25 → items 26–50.
  - `listSearch.test.ts`: `{page:'1'}` → no page; `{page:'3', order:'up'}` → `{page:3}`.
  - `useListView.test.tsx`: reads `?sort=name&order=desc&page=2`; header change writes the URL and drops `page`; `resets the page when resetKey changes`; `clamps a page past the end` (total 30, per page 25, `page=9` → 2, replace); two column changes within 400 ms → one PUT with the second list; page-size change → one PUT with `page_size: 50`; `urlState: false` leaves the URL alone; client mode `view` sorts then slices.
  - `usePreferences.test.tsx`: defaults before the GET resolves; save updates the cache before the PUT resolves; a failed PUT rolls back.
  - `DensityEffect.test.tsx`: `compact` from the API → `data-density="compact"` on `<html>`.
- [ ] **Step 2: Run** `cd web && npx vitest run src/features/lists src/api/hooks/usePreferences.test.tsx` → FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `npm test`, `npm run lint`, `npx tsc -b` → PASS.
- [ ] **Step 5: Commit** `feat(web): list views remember columns, sort and page, and apply the density`

### Task 8: Personal settings area

**Files:**
- Create: `web/src/routes/AccountLayout.tsx` (+ test; replaces `AccountSecurityLayout.tsx` and its test, which are deleted), `web/src/ui/shell-nav.tsx` (`OrgNav` moved out of `shell.tsx`, new `SettingsNav`), `web/src/lib/lastOrg.ts` (+ test)
- Create: `web/src/features/account/AppearancePage.tsx`, `ProfilePage.tsx`, `MyActivityPage.tsx`, tests; `web/src/api/hooks/useActivity.ts` gains `useMyActivityPage`
- Modify: `web/src/ui/shell.tsx` (`settingsNav` prop; account menu item), `web/src/router.tsx` (account route tree), `web/src/routes/OrgLayout.tsx` (`rememberOrg`), `web/src/features/account/AccountSecurityPage.tsx` (language card removed), i18n `shell.*`, `account.nav.*`, `appearance.*`, `profile.*`, `lists.names.*`
- Modify: `web/e2e/passkey.spec.ts`, `web/e2e/setup.spec.ts` only if a selector changes

**Interfaces:**
- Consumes: Tasks 6, 7; `GET /api/v1/me/activity` page mode (Task 5).
- Produces:
  ```ts
  // ui/shell.tsx
  export interface ShellSettingsNav { backOrg: ShellOrg; personName: string; personRole: string }
  // AppShellProps gains `settingsNav?: ShellSettingsNav`: present → the sidebar shows
  // "← Back to <org>" (to /o/$org/hosts), the name and role, then Security, Appearance,
  // Profile, My activity (Task 19 inserts API tokens before My activity); the org
  // switcher and the org navigation are not shown. No second sidebar in the page.
  // lib/lastOrg.ts — sessionStorage 'pulsix.lastOrg', every access in try/catch
  export function rememberOrg(slug: string): void
  export function lastOrg(): string | undefined
  ```
- Routes: layout `/account` (`AccountLayout`: today's pending-session redirects and guards from `AccountSecurityLayout`, the shell with `settingsNav`, crumbs "Settings / <page>") with children `/security` (`AccountSecurityPage` without `LanguageCard`), `/appearance`, `/profile`, `/activity`, and `/` → redirect `/account/security`. `/account/enrol`, `/account/password`, `/account/lock-pin` stay root routes without the shell. Back org = `lastOrg()` when the person is still a member, else their first organization.
- Account menu: the item `t.shell.accountSecurity` becomes `t.shell.settingsMenu` (en "Settings", de "Einstellungen") linking `/account/security`. The org nav also has "Settings": tests scope to the account menu.
- Appearance: density as a radio group of three cards with a small preview (`t.appearance.density.{comfortable,standard,compact}` — de "Komfortabel", "Standard", "Kompakt"), page size select (10/25/50/100), `LanguageCard` moved here, and "Saved column choices": one row per key of `prefs.lists` (`listName`), each with "Reset" removing that key (one PUT).
- Profile (read-only): name and email from `useMe`, organizations and roles from `useOrgs` (`roleLabel`); a note that name and email are changed by an administrator.
- My activity: `DataList` in server mode, list id `my-activity`, columns At (sortKey `at`, required), Organization, Action/summary, Object; filters as `ActivityPage` has.

- [ ] **Step 1: Failing tests**
  - `AccountLayout.test.tsx`: the sidebar shows "Back to Acme", the name and role, the four entries, no org switcher; back org follows `rememberOrg('beta')` when still a member, else the first org; the pending-session redirects of the old layout test still hold.
  - `router.test.tsx`: `/account` redirects to `/account/security`; `/account/enrol` renders without the shell.
  - `shell.test.tsx`: the account menu's item is named "Settings" and links `/account/security`.
  - `AppearancePage.test.tsx`: choosing Compact PUTs `density: 'compact'`; page size 50 PUTs `page_size: 50`; "Reset" for hosts PUTs `lists` without `hosts`; the language card is here and not on Security (`AccountSecurityPage.test.tsx` updated).
  - `ProfilePage.test.tsx`, `MyActivityPage.test.tsx` (requests `page=1&per_page=25`, footer "1–25 of 40 events").
- [ ] **Step 2: Run** `cd web && npx vitest run src/routes src/features/account src/ui/shell.test.tsx src/router.test.tsx` → FAIL.
- [ ] **Step 3: Implement** (`shell.tsx` stays under ~400 lines by moving the nav into `shell-nav.tsx`).
- [ ] **Step 4: Run** `npm test`, `npm run lint`, `npx tsc -b`; `npm run e2e -- passkey setup` → PASS.
- [ ] **Step 5: Commit** `feat(web): a personal settings area with appearance, profile and activity`

---

## Part C — SPA: migrate the lists

Each migration: the page builds `DataListColumn[]` for today's columns (labels unchanged unless stated), `rowActions` from today's row menu plus every inline button or switch (a switch becomes an "Enable"/"Disable" entry and its state shows read-only in a column), the route spreads `validateListSearch`, and the list id joins `registry.ts` with en/de names. Secondary lists on a page use `urlState: false`. Client mode unless stated.

### Task 9: Hosts and runs (server-paged)

**Files:**
- Modify: `web/src/features/hosts/HostsListPage.tsx`, `HostRow.tsx` (becomes `hostColumns.tsx` + `hostActions.ts`; delete `HostRow.tsx` when unused), `web/src/api/hooks/useHosts.ts` (`useHostsPage`), `web/src/features/runs/RunsPage.tsx`, `RunRow.tsx` → `runColumns.tsx`, `web/src/api/hooks/useJobs.ts` (`useJobsPage`), `web/src/router.tsx` (hosts and runs `validateSearch`), i18n
- Test: `HostsListPage.test.tsx`, `HostsListPage.de.test.tsx`, `RunsPage.test.tsx`; e2e `hosts.spec.ts` and any spec using the moved controls

**Interfaces:**
- Consumes: Tasks 2, 3, 6, 7.
- `useHostsPage(org: string, filter: HostListFilter & { activeRuns?: boolean }, page: ServerPageQuery)` and `useJobsPage(org: string, filter: JobListFilter, page: ServerPageQuery)` — `useQuery` with `placeholderData: keepPreviousData`, keys under `['hosts', org, 'page', …]` / `['jobs', org, 'page', …]` so existing invalidations still hit. `useHosts`/`useJobs` stay for pickers.
- Hosts columns (list id `hosts`): status dot (icon column, menuLabel "Agent status"), Host (required, sortKey `name`; comfortable density shows the address under the name), Zone (`zone`), Tags, Managed (`managed`), Monitored (`monitored`), Last seen (`last_seen`), **Active runs** (`active_runs`, default visible: "2 running" linking `/o/$org/runs?host=<id>&status=active`, "—" for 0); optional, default hidden: Agent version (from `useAgents`), Operating system (from `facts`). Filter chip **With active runs** (`active_runs=true`). `resetKey` = the serialised filter, so ⌘K `?q=` and chips return to page 1. Tag-selector mode: client mode over the selector answer (which carries `active_runs`). Header count line becomes the footer total.
- Runs columns (list id `runs`): today's columns; "Started" is relabelled **Created** (sortKey `created`); `status`, `kind`, `trigger` sortable; template not sortable. The status filter offers **Active** (`status=active`).

- [ ] **Step 1: Failing tests:** hosts request `page=1&per_page=25&sort=…` only when a sort is chosen; header click on Zone → `?sort=zone&order=asc`; footer "1–25 of 30 hosts"; Active runs cell links to `/o/acme/runs?host=h1&status=active`; the chip sends `active_runs=true` and resets `?page=3` to page 1; ⌘K `?q=web` resets page; selector mode pages in the browser; right-click on a host row shows the same entries as "…" plus "Open in new tab"/"Copy link"; runs show "Created" and the Active status option sends `status=active`.
- [ ] **Step 2: Run** `cd web && npx vitest run src/features/hosts/HostsListPage src/features/runs/RunsPage` → FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `npm test`, `npm run lint`, `npx tsc -b`, `npm run e2e -- hosts` → PASS.
- [ ] **Step 5: Commit** `feat(web): hosts and runs on the list component, with active runs`

### Task 10: Alerts, users, organization activity (server-paged)

**Files:**
- Modify: `web/src/features/alerts/AlertsPage.tsx`, `AlertRow.tsx` (→ `alertColumns.tsx`, `alertActions.ts`), `AlertFilters.tsx`; `web/src/features/users/UsersPage.tsx`; `web/src/features/activity/ActivityPage.tsx`; hooks `useAlertsPage`, `useAdminUsersPage`, `useOrgActivityPage`; `router.tsx`; i18n
- Test: existing page tests and `alerts/__tests__`; e2e `alerts.spec.ts`, `accounts.spec.ts`, `activity.spec.ts` where selectors change

**Interfaces:**
- Consumes: Tasks 4, 5, 6, 7 (same hook shape as Task 9).
- Alerts (list id `alerts`): Alert (host name and check, required, sortKey `host`), Open (`opened`), Peak (`peak`), Tracked (`tracked`), Acknowledged (`acknowledged`), Mute (not sortable) — today's columns, now sortable as the spec lists. The four inline buttons in `AlertRow` (acknowledge, unacknowledge, snooze, unsnooze) move into the row menu with their dialogs.
- Users (list id `users`): Email (`email`, required), Name (`name`), Last sign-in (`last_sign_in`), state (not sortable), organizations.
- Activity (list id `activity`): At (`at`, required), actor, action/summary, object; the existing filters feed `resetKey`.

- [ ] **Step 1: Failing tests:** each page sends `page`/`per_page`, sorts through the URL, shows the footer total; the alert's "Acknowledge" is no longer a visible button but a row-menu entry opening the same dialog; muted filter still sent; "Load more" gone everywhere on these pages.
- [ ] **Step 2: Run** → FAIL. **Step 3: Implement.** **Step 4: Run** `npm test`, lint, tsc, `npm run e2e -- alerts accounts activity` → PASS.
- [ ] **Step 5: Commit** `feat(web): alerts, users and activity on the list component`

### Task 11: Whole lists — automation

**Files:** `web/src/features/repos/ReposPage.tsx`, `playbooks/PlaybooksPage.tsx`, `templates/TemplatesPage.tsx`, `schedules/SchedulesPage.tsx`, `runs/RunRequestsPage.tsx`, `hosts/HostRunsSection.tsx` (teaser: component without `paging`, no URL state, link to the full list kept); their tests; `router.tsx`; i18n; e2e specs that click the moved buttons (`ansible.spec.ts`, `triggers.spec.ts`, `chain.spec.ts` — grep).

**Interfaces:** List ids `repos`, `playbooks`, `templates`, `schedules`, `run-requests`, `host-runs`. Inline buttons move to the row menu: repos (sync, edit, delete), templates (run, run with follow-ups, edit, delete), schedules (enable/disable, edit, delete); run requests keep the pending badge.

- [ ] **Step 1: Failing tests** per page: sortable name column sorts in the browser (`'repo10'` after `'repo9'`); no inline action buttons remain in rows (`queryByRole('button', { name: 'Delete' })` null until the menu opens); the menu entries open the same confirmations; footer and page size work with 30 fixture rows.
- [ ] **Step 2: Run** → FAIL. **Step 3: Implement.** **Step 4: Run** `npm test`, lint, tsc, affected e2e → PASS.
- [ ] **Step 5: Commit** `feat(web): automation lists on the list component`

### Task 12: Whole lists — infrastructure

**Files:** `web/src/features/inventory/GroupsPage.tsx`, `inventory/sections/MembersSection.tsx`, `MemberRow.tsx`, `ChildrenSection.tsx` (secondary lists, `urlState: false`), `credentials/CredentialsPage.tsx`, `credentials/JumpHostsPage.tsx`, `agents/AgentsPage.tsx`, `AgentRow.tsx` (agents main, agent tokens secondary), `workers/WorkersPage.tsx`, `WorkerRow.tsx` (workers main, enrollments secondary); tests; e2e (`inventory.spec.ts`, `agent.spec.ts`, `workers.spec.ts`, `variables.spec.ts` — grep).

**Interfaces:** List ids `groups`, `group-members`, `group-children`, `credentials`, `jump-hosts`, `agents`, `agent-tokens`, `workers`, `worker-enrollments`. `ManagedHostPicker` is a picker and stays as is.

- [ ] **Step 1–5** as Task 11 (tests per page: sorting, no inline buttons, secondary list sort/page does not touch the URL). Commit `feat(web): infrastructure lists on the list component`

### Task 13: Whole lists — monitoring

**Files:** `web/src/features/fleet/FleetPage.tsx` (no sort = the server's worst-first order), `checks/ChecksPage.tsx`, `CheckRow.tsx`, `checks/MonitoringGroupsPage.tsx`, `MonitoringGroupRow.tsx`, `checks/RulesPage.tsx`, `RuleRow.tsx`, `checks/ItemsPage.tsx`, `DecisionRow.tsx`, `maintenance/MaintenancePage.tsx`; tests (`ChecksPage.rows.test.tsx`, `checksPageTestKit.tsx`, …); e2e (`fleet.spec.ts`, `rules.spec.ts`, `items.spec.ts`, `maintenance.spec.ts`, `calendar.spec.ts` — grep).

**Interfaces:** List ids `fleet`, `checks`, `monitoring-groups`, `rules`, `item-decisions`, `maintenance`. Fleet's own filters stay (`FleetFilters`).

- [ ] **Step 1–5** as Task 11. Commit `feat(web): monitoring lists on the list component`

### Task 14: Whole lists — organization settings

**Files:** `web/src/features/tokens/TokensPage.tsx` (organization tokens; revoke/edit into the menu), `members/MembersPage.tsx` (main) and `PendingInvites.tsx` (secondary), `secrets/SecretsPage.tsx`, `SecretRow.tsx`, `notifications/ChannelsPage.tsx`, `ChannelRow.tsx`, `notifications/AlertRoutesPage.tsx` (the enable switch becomes "Enable"/"Disable" in the menu; an "Enabled" column shows the state read-only), `notifications/RotationsPage.tsx`; tests; e2e (`members.spec.ts`, `tenancy.spec.ts`, `notifications.spec.ts` — grep).

**Interfaces:** List ids `org-tokens`, `members`, `invites`, `secrets`, `channels`, `alert-routes`, `rotations`.

- [ ] **Step 1: Failing tests** as Task 11, plus `AlertRoutesPage.test.tsx`: no switch in the row; "Disable" in the menu PATCHes `enabled: false` and the column then reads "Disabled"; the entry is absent for a role that could not toggle the switch before.
- [ ] **Step 2–4** as Task 11. **Step 5: Commit** `feat(web): organization settings lists on the list component`

### Task 15: Whole lists — instance administration

**Files:** `web/src/features/instance/OrganizationsPage.tsx`, `instance/BackupList.tsx`, `instance/shared/SharedListFrame.tsx` with `SharedReposPage.tsx`, `SharedTemplatesPage.tsx`, `SharedChecksPage.tsx`; tests; e2e (`accounts.spec.ts` if it touches organizations — grep).

**Interfaces:** List ids `organizations`, `backups`, `shared-repos`, `shared-templates`, `shared-checks`. After this task no page imports `LoadMoreButton` except pickers (`ManagedHostPicker`, `ScopePicker`, `AdhocDialog`); verify with grep.

- [ ] **Step 1–4** as Task 11.
- [ ] **Step 5: Commit** `feat(web): instance lists on the list component`

---

## Part D — Personal tokens

### Task 16: Storage and resolution

**Files:**
- Create: `internal/db/migrations/00110_personal_tokens.sql`, `internal/db/queries/personal_tokens.sql`
- Create: `internal/auth/personal_token.go`, `internal/personaltokens/service.go` (read side), `internal/personaltokens/store_sql.go`, tests
- Create: `internal/api/token_role.go` (shared effective-role helper), `internal/api/personal_token_resolution_test.go`
- Modify: `internal/auth/token.go` (`TokenScope.Personal`, `Resolve`, `StillUsable`, `TokenStore`), `internal/auth/token_store_sql.go`, `internal/auth/token_test.go` (fake), `internal/api/authz.go` (`requireOrg`), `internal/api/job_sse.go` (`streamAccessHeld`), `internal/tenancy/store_sql.go` + `internal/db/queries/tenancy_users.sql` (`RevokeTokensForUser` covers personal tokens), `internal/db/queries/audit.sql` (token name), `internal/api/api.go` (`Deps.PersonalTokens`), apitest, `internal/app/app.go`

**Interfaces:**
- Schema (`00110`, storage chosen by the builder per the spec's "Left to the builder"):
  - `personal_tokens (id uuid PK, user_id uuid NOT NULL REFERENCES users ON DELETE CASCADE, name text NOT NULL, secret_hash bytea NOT NULL UNIQUE, kind text NOT NULL DEFAULT 'api' CHECK (kind IN ('api','assistant')), areas text[], expires_at, last_used_at, revoked_at timestamptz, created_at timestamptz NOT NULL DEFAULT now(), UNIQUE (id, user_id), CHECK` kind/areas as `api_tokens_kind_areas` `)`; index on `user_id WHERE revoked_at IS NULL`. No RLS: the row belongs to a user, like `sessions`; an organization's view goes through grants (comment in the file).
  - `personal_token_grants (token_id uuid, user_id uuid, org_id uuid, role org_role NOT NULL, blocked_at timestamptz, blocked_by uuid REFERENCES users ON DELETE SET NULL, last_used_at timestamptz, created_at timestamptz NOT NULL DEFAULT now(), PRIMARY KEY (token_id, org_id), FOREIGN KEY (token_id, user_id) REFERENCES personal_tokens (id, user_id) ON DELETE CASCADE, FOREIGN KEY (org_id, user_id) REFERENCES org_memberships (org_id, user_id) ON DELETE CASCADE)`; index on `org_id`; the standard RLS pair (`org_isolation` with `NULLIF(...)::uuid`, `org_isolation_bypass`). The membership foreign key is what deletes a grant when the membership ends (spec rule 11).
  - `org_token_settings (org_id uuid PK REFERENCES organizations ON DELETE CASCADE, personal_tokens_allowed boolean NOT NULL DEFAULT true, updated_at timestamptz NOT NULL DEFAULT now())`, RLS pair; no row = allowed.
- `auth`:
  ```go
  const personalTokenPrefix = "pxp_" // a distinct prefix keeps the secret lookup one indexed read
  type PersonalToken struct { ID, UserID uuid.UUID; Name string; Kind TokenKind; Areas []string; ExpiresAt, LastUsedAt *time.Time; CreatedAt time.Time }
  func MintPersonalSecret() (plain string, hash []byte, err error) // 32 CSPRNG bytes, base64url, "pxp_"
  // TokenScope gains `Personal bool` (OrgID zero, Role "" for a personal token).
  // TokenStore gains:
  GetPersonalByHash(ctx context.Context, secretHash []byte, now time.Time) (PersonalToken, bool /*instanceAdmin*/, error) // not revoked, not expired, owner not disabled; ErrNotFound
  PersonalUsable(ctx context.Context, id uuid.UUID, now time.Time) (bool, error)
  TouchPersonalLastUsed(ctx context.Context, id uuid.UUID, at time.Time) error // 1/min throttle in SQL like TouchAPIToken
  ```
  `Resolve` dispatches on the prefix; `StillUsable` is true when either `Usable` or `PersonalUsable` is.
- `personaltokens` (read side; Task 17 adds the rest):
  ```go
  var ErrNoGrant, ErrSwitchedOff error
  type Store interface { Cap(ctx, tokenID, orgID uuid.UUID) (tenancy.Role, bool /*blocked*/, error) /* ErrNotFound */; Allowed(ctx, orgID uuid.UUID) (bool, error); TouchGrant(ctx, tokenID, orgID uuid.UUID, at time.Time) error; … }
  func NewService(store Store, members Members, now func() time.Time) *Service
  // Cap is the grant's role cap for orgID: no grant or a blocked one → ErrNoGrant; switch off → ErrSwitchedOff.
  func (s *Service) Cap(ctx context.Context, tokenID, orgID uuid.UUID) (tenancy.Role, error)
  func (s *Service) TouchGrant(ctx context.Context, tokenID, orgID uuid.UUID) // best effort, logs
  ```
- `internal/api/token_role.go`:
  ```go
  // PersonalTokenAccess is what requireOrg, the job stream and the assistant endpoint need.
  type PersonalTokenAccess interface {
      Cap(ctx context.Context, tokenID, orgID uuid.UUID) (tenancy.Role, error)
      TouchGrant(ctx context.Context, tokenID, orgID uuid.UUID)
  }
  // tokenRole narrows member (the caller's membership role in orgID) by the
  // request's bearer token: an organization token → Lower(member, its role) or
  // 403 "token is scoped to another organization"; a personal token →
  // Lower(member, Cap) or the same 403 for ErrNoGrant, 403
  // "personal tokens are turned off for this organization" for ErrSwitchedOff.
  func tokenRole(ctx context.Context, deps Deps, ts auth.TokenScope, tokenID uuid.UUID, orgID uuid.UUID, member tenancy.Role) (tenancy.Role, error)
  ```
  `requireOrg` and `streamAccessHeld` use it (the stream ends with `errStreamAccessEnded` on either refusal). `requireOrg` calls `TouchGrant` after a successful personal-token authorization.
- `RevokeTokensForUser` (tenancy store) also revokes the user's personal tokens (second query `RevokePersonalTokensForUser`). Audit token name: `COALESCE(t.name, pt.name, '')` with `LEFT JOIN personal_tokens pt ON pt.id = e.actor_token_id` in both list queries (and the page queries of Task 5).

- [ ] **Step 1: Failing tests**
  - `internal/auth`: `TestResolvePersonalToken` (`pxp_` → `Personal: true`, kind/areas, `MFAComplete`), `TestResolvePersonalTokenRevokedExpiredDisabled` → `ErrUnauthenticated`, `TestStillUsableCoversPersonalTokens`, `TestMintPersonalSecretShape` (prefix, 47 chars, distinct).
  - `internal/personaltokens` (fake store): `TestCapNoGrant`, `TestCapBlocked` → `ErrNoGrant`; `TestCapSwitchedOff` → `ErrSwitchedOff`.
  - testdb: `TestGrantRowsInvisibleToOtherOrg` (RLS: bound to org B, org A's grants are not visible); `TestGrantRequiresSameUserAsToken` (insert with another user_id fails); `TestRemovingMemberDeletesTheirGrants` (through `tenancy.Service.RemoveMember` with the app role; the blocked grant goes too).
  - `internal/api` (`personal_token_resolution_test.go`, tokens inserted through the store): `TestPersonalTokenGrantPresent` (GET hosts of A → 200), `TestPersonalTokenGrantAbsent` (org C, member, no grant → 403), `TestPersonalTokenCapVersusMembership` (cap admin, member operator → an admin route such as `PUT /settings/jobs` 403 and `POST /hosts` 201; cap viewer, member admin → `POST /hosts` 403), `TestPersonalTokenMembershipLost` (remove member → 403), `TestPersonalTokenUserDeactivated` (→ 401, token revoked), `TestPersonalTokenSwitchOff` (→ 403 with the switch detail; on again → 200), `TestPersonalAssistantTokenRefusedOnREST` (403 `assistantOnlyAtEndpoint`), `TestPersonalTokenNeverInstanceAdmin` (owner is instance admin; `/api/v1/admin/users` → 403), `TestJobStreamEndsWhenGrantRevoked` (block the grant mid-stream → stream ends within one poll), `TestActivityShowsPersonalTokenName` (an event made with the token shows its name in the org feed).
- [ ] **Step 2: Run** `go test -race -timeout 900s ./internal/auth/... ./internal/personaltokens/... ./internal/tenancy/...`, `go test -race -timeout 1800s -run 'PersonalToken|JobStream|Activity' ./internal/api/` → FAIL.
- [ ] **Step 3: Implement**, `make sqlc`.
- [ ] **Step 4: Run** → PASS; coverage `internal/auth` ≥ 90 %; lint clean.
- [ ] **Step 5: Commit** `feat(auth): resolve personal tokens per organization through their grants`

### Task 17: Personal token management, admin grant revocation, organization switch

**Files:**
- Modify: `internal/personaltokens/service.go` (split: `service.go`, `manage.go`, `org.go` if over ~400 lines), `store_sql.go`, `internal/db/queries/personal_tokens.sql`, `internal/tenancy/roles.go` (`Highest`) + `roles_test.go`
- Create: `internal/api/personal_token_handlers.go` (own tokens), `internal/api/personal_token_org_handlers.go` (organization view and switch), tests `personal_token_handlers_test.go`, `personal_token_org_handlers_test.go`
- Modify: `internal/api/audit_middleware.go` (`recordOrgEvent`; `recordAuditFailure` delegates to it), `scripts/coverage-gate.sh` (`/internal/personaltokens` joins the 90 % list), `cd web && npm run api:gen`

**Interfaces:**
- Produces:
  ```go
  const MaxTokensPerUser = 50
  const MaxGrantsPerToken = 50
  var ErrNotFound, ErrLimit, ErrBlocked, ErrAboveRole, ErrNotMember, ErrInvalid error // + ErrNoGrant, ErrSwitchedOff (Task 16)
  type GrantInput struct { OrgID uuid.UUID; Role tenancy.Role }
  type GrantState string // "active" | "blocked" | "off"
  type Grant struct { OrgID uuid.UUID; OrgSlug, OrgName string; Role tenancy.Role; State GrantState; LastUsedAt *time.Time }
  type Token struct { auth.PersonalToken; Grants []Grant }
  type NewToken struct { Name string; Kind auth.TokenKind; Areas []string; ExpiresAt *time.Time; Grants []GrantInput }
  type Change struct { Name *string; Areas *[]string; Expiry auth.ExpiryChange; ExpiresAt *time.Time; Grants *[]GrantInput } // Grants replaces the set
  type GrantChange struct { OrgID uuid.UUID; Kind string /* "added" | "changed" | "removed" */; From, To tenancy.Role }
  type OrgEntry struct { TokenID uuid.UUID; Name string; Kind auth.TokenKind; OwnerID uuid.UUID; OwnerName, OwnerEmail string; Role tenancy.Role; Blocked bool; LastUsedAt *time.Time; CreatedAt time.Time }
  type Members interface { MembershipOf(ctx context.Context, orgID, userID uuid.UUID) (tenancy.Role, error) }
  func (s *Service) Create(ctx context.Context, userID uuid.UUID, in NewToken) (plain string, tok Token, err error)
  func (s *Service) List(ctx context.Context, userID uuid.UUID) ([]Token, error)
  func (s *Service) Update(ctx context.Context, userID, id uuid.UUID, ch Change) (before, after Token, changes []GrantChange, err error)
  func (s *Service) Revoke(ctx context.Context, userID, id uuid.UUID) (Token, error) // the token as it was, for the org feeds
  func (s *Service) ListForOrg(ctx context.Context, orgID uuid.UUID) ([]OrgEntry, error)
  func (s *Service) RevokeGrant(ctx context.Context, orgID, tokenID, adminID uuid.UUID) (OrgEntry, error) // sets blocked_at/by
  func (s *Service) UnblockGrant(ctx context.Context, orgID, tokenID uuid.UUID) (OrgEntry, error)       // deletes the blocked row
  func (s *Service) Allowed(ctx context.Context, orgID uuid.UUID) (bool, error)
  func (s *Service) SetAllowed(ctx context.Context, orgID uuid.UUID, on bool) error
  func (s *Service) GrantCount(ctx context.Context, tokenID uuid.UUID) (orgID uuid.UUID, n int, err error) // live, unblocked grants; orgID set when n == 1 (Task 18)
  // tenancy/roles.go
  func Highest(roles ...Role) Role // unknown roles rank lowest; empty → ""
  ```
- Rules (spec rules 1, 4, 5, 10, 11 and the owner's decisions): at least one grant on create; each grant's org must be a current membership (`ErrNotMember`), its cap a valid role not above the membership role now (`ErrAboveRole` — refused, not silently lowered), its org's switch on for a new or raised grant (`ErrSwitchedOff`), not blocked (`ErrBlocked`; blocked rows are never changed by the owner and listing a blocked org is refused); areas validated as for organization tokens against `tenancy.Highest` of the caps; more than 50 non-revoked tokens → `ErrLimit`; more than 50 grants → `ErrInvalid`; an expired token cannot be edited (`auth.ErrTokenExpired`). `CreatedAt` and `ExpiresAt` truncated to `time.Microsecond` before insert and in the answer.
- Routes (own tokens: root API, `requireInteractiveSession`; audited with no organization, object type `personal_token`):
  | Route | Audit action | Answer |
  |---|---|---|
  | `GET /api/v1/auth/me/tokens` | — | `{tokens: [{id, name, kind, areas?, expires_at?, last_used_at?, created_at, grants: [{org_id, org_slug, org_name, role, state, last_used_at?}]}]}` |
  | `POST /api/v1/auth/me/tokens` `{name, kind?, areas?, expires_at?, grants: [{org: "<slug>", role}]}` | `createPersonalToken` | 201, the same plus `token` (plaintext, once) |
  | `PATCH /api/v1/auth/me/tokens/{id}` `{name?, areas?, expires_at?, never_expires?, grants?}` | `updatePersonalToken` | 200 `{token}` |
  | `DELETE /api/v1/auth/me/tokens/{id}` | `revokePersonalToken` | 200 |
  Each grant change is also recorded in the affected organization's feed through `recordOrgEvent(ctx, deps, orgID, userID uuid.UUID, action, objectType, objectID, summary string)`: `addPersonalTokenGrant`, `changePersonalTokenGrant`, `removePersonalTokenGrant` (also once per org when the whole token is revoked). Summaries name the token and the role, e.g. `added personal token deploy (operator)`; never a secret.
- Organization routes (bearer group; `requireOrg` with `PermTokensManage`; the switch with `PermOrgSettingsRead`/`PermOrgSettingsWrite`):
  | Route | Gate extra | Audit |
  |---|---|---|
  | `GET /api/v1/orgs/{org}/personal-tokens` → `{entries: [...]}` | — | — |
  | `DELETE /api/v1/orgs/{org}/personal-tokens/{id}` (revoke the grant here, sticky) | — | `revokePersonalTokenGrant`, object `personal_token` |
  | `POST /api/v1/orgs/{org}/personal-tokens/{id}/unblock` | `requireInteractiveSession` | `unblockPersonalTokenGrant` |
  | `GET`/`PUT /api/v1/orgs/{org}/settings/personal-tokens` `{allowed}` | PUT `requireInteractiveSession` | `updatePersonalTokenSettings`, object `org_settings` |
- Errors: `ErrNotFound` 404; `ErrLimit` 409 `personal_token_limit`; `ErrSwitchedOff` 409 `personal_tokens_off`; `ErrBlocked` 409 `grant_blocked`; `ErrAboveRole`, `ErrNotMember`, `ErrInvalid` 422; `auth.ErrTokenExpired` 409.

- [ ] **Step 1: Failing tests**
  - Service (fake store, table-driven): create needs a grant; cap above role → `ErrAboveRole`; non-member → `ErrNotMember`; switch off → `ErrSwitchedOff`; blocked org → `ErrBlocked`; 51st token → `ErrLimit`; 51 grants → `ErrInvalid`; assistant areas beyond the highest cap refused, within it accepted; `Update` reports `added`/`changed`/`removed`; blocked rows survive an edit.
  - `TestHighest` in `internal/tenancy`.
  - API: `TestViewerCanCreatePersonalToken` (a viewer, cap viewer → 201); `TestPersonalTokenRoutesRefuseBearer` (403); `TestCreatePersonalTokenAnswersStoredPrecision` (create, then list: `created_at` and `expires_at` equal); `TestCreatePersonalTokenSecretNotInAudit` (no stored audit field contains the plaintext or its `pxp_` suffix); `TestGrantChangesInOrgFeed` (add B, change A's cap, remove B → A and B feeds carry the events, the owner's feed the token events); `TestAdminRevocationSticks` (admin revokes B → token 403 on B; owner PATCH adding B → 409 `grant_blocked`; admin unblocks → owner adds B again → 200); `TestAdminCannotWidenGrant` (no route accepts a cap); `TestSwitchOffThenOn` (off: new grant 409, existing grant 403 at use; on: works again, a blocked grant stays blocked); `TestOrgViewListsOnlyOwnGrants` (org A's admin sees A's entries with owner, cap, last use; none of B's); `TestRevokeWholeTokenEndsEveryGrant`.
  - `TestEveryMutatingRouteIsAuditedOrAllowlisted` keeps passing without new allowlist entries.
- [ ] **Step 2: Run** `go test -race -timeout 900s ./internal/personaltokens/... ./internal/tenancy/...`, `go test -race -timeout 1800s -run 'PersonalToken|Grant|SwitchOff|Admin(Revocation|Cannot)|Viewer' ./internal/api/` → FAIL.
- [ ] **Step 3: Implement**, `make sqlc`, `npm run api:gen`.
- [ ] **Step 4: Run** → PASS; `internal/personaltokens` coverage ≥ 90 %; lint clean.
- [ ] **Step 5: Commit** `feat(api): manage personal tokens and their organization grants`

### Task 18: Assistant endpoint per organization

**Files:**
- Modify: `internal/mcp/server.go` (org resolution, refusals), `internal/mcp/registry.go` (`allowedFor`), `internal/mcp/caller.go` if the slug source changes
- Create: `internal/api/mcp_access.go` (adapter implementing `mcp.OrgAccess`)
- Modify: `internal/api/api.go` (mount `/api/mcp/o/{org}` beside `/api/mcp`; `mcp.Deps.Access`)
- Test: `internal/mcp/server_test.go` (fakes), `internal/api/assistant_token_test.go` (end to end)

**Interfaces:**
- Produces in `internal/mcp`:
  ```go
  // OrgAccess decides the organization of an assistant request and the token's effective role there.
  type OrgAccess interface {
      OrgBySlug(ctx context.Context, slug string) (id uuid.UUID, err error)                  // tenancy.ErrNotFound → 404
      SingleGrant(ctx context.Context, tokenID uuid.UUID) (orgID uuid.UUID, n int, err error) // personaltokens.GrantCount
      EffectiveRole(ctx context.Context, p auth.Principal, scope auth.TokenScope, orgID uuid.UUID) (tenancy.Role, error) // membership ∧ token (tokenRole); refusal → 403
  }
  // Deps gains Access OrgAccess (Orgs OrgSlugs stays for the slug of an org token's org).
  func (t toolDef) allowedFor(areas []tenancy.Area, role tenancy.Role) bool // allowed(areas) && tenancy.RoleAllowsArea(role, t.area)
  ```
- Endpoint rules (spec rule 7): an organization assistant token works at `/api/mcp` as today; at `/api/mcp/o/{org}` only for its own org (else 403 `token is scoped to another organization`). A personal assistant token at `/api/mcp/o/{org}` works when it has a live grant there; at `/api/mcp` only with exactly one live grant — no grant → 403 `the token is not valid for any organization`; several → 400 `this token is valid for several organizations; connect to /api/mcp/o/<organization>`. Unknown slug → 404. The assistant switch is checked as today. The tool list is filtered by `allowedFor(areas, effectiveRole)` per request.

- [ ] **Step 1: Failing tests**
  - `TestPersonalTokenWithSeveralGrantsNeedsOrgPath` (400, detail contains `/api/mcp/o/`), `TestPersonalTokenWithOneGrantWorksAtBareEndpoint`, `TestPersonalTokenAtOrgPathWithoutGrant` (403), `TestOrgTokenAtOtherOrgPath` (403), `TestUnknownOrgPath` (404).
  - `TestToolsListedPerEffectiveRole` — one personal assistant token, areas `jobs.read, jobs.request`, cap operator in A and viewer in B: `tools/list` at `/o/a` has `request_run`, at `/o/b` does not; `get_job_log` in both.
  - `TestOrgTokenToolsFollowLoweredMembership` — an organization assistant token whose owner was demoted to viewer no longer lists write tools.
- [ ] **Step 2: Run** `go test -race -timeout 900s ./internal/mcp/...`, `go test -race -timeout 1800s -run Assistant ./internal/api/` → FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** → PASS; lint clean.
- [ ] **Step 5: Commit** `feat(mcp): an assistant endpoint per organization for personal tokens`

### Task 19: SPA personal tokens, organization view, switch, connect script

**Files:**
- Create: `web/src/api/hooks/usePersonalTokens.ts`, `web/src/features/account/PersonalTokensPage.tsx`, `PersonalTokenDialog.tsx` (create and edit), `GrantsEditor.tsx`, tests
- Create: `web/src/features/settings/PersonalTokensCard.tsx` (+ test), mounted in `OrgGeneralPage.tsx` beside `AssistantCard`
- Modify: `web/src/features/tokens/TokensPage.tsx` (personal entries), `web/src/ui/shell-nav.tsx` (API tokens entry before My activity), `web/src/router.tsx` (`/account/tokens`), i18n `personalTokens.*`, `tokens.personal.*`
- Modify: `scripts/assistant-mcp.sh`; Create: `scripts/assistant-mcp-test.sh`; Modify: `.github/workflows/ci.yml` (run the test next to `release-agent-assets-test.sh`)

**Interfaces:**
- Consumes: Task 17 routes; Tasks 6–8.
- `/account/tokens` (list id `personal-tokens`, client mode): Name (required), Kind, Organizations (slug: role, blocked ones marked "Blocked by an administrator"), Last used, Expires, Created; menu Edit / Revoke (confirm). Dialog: name, kind, areas for assistant (existing `AreaGrid`, limited by the highest chosen cap), expiry, grants editor — one row per organization from `useOrgs`, role select offering only roles up to the person's role there, organizations whose switch is off or whose grant is blocked shown disabled with the reason. The plaintext is shown once with copy, as on the organization page.
- Organization token page: one list (`org-tokens`) of organization tokens and personal entries (union rows `{type: 'org'} | {type: 'personal'}`), personal ones tagged **Personal** with an Owner column (optional, default visible; "—" for organization tokens), cap, last use; menu for a personal entry: "Revoke for this organization" (confirm) or, when blocked, "Unblock".
- `PersonalTokensCard`: "Allow personal tokens" switch (admin only; others see the state), PUT on change.
- Script: `add <name> <pulsix-url> [--org <slug>]` → endpoint `<url>/api/mcp/o/<slug>` when `--org` is given (slug `^[a-z0-9-]+$`), else `<url>/api/mcp`; the URL may end in `/api/mcp` or `/api/mcp/o/<slug>` and is normalised; `check_stored` accepts `pxt_` and `pxp_`; `check` explains a 400 with the endpoint's detail ("use --org"). One registered server per name; a personal token used for two organizations is added twice under two names (each stores the token). Header comment and usage updated.

- [ ] **Step 1: Failing tests**
  - `PersonalTokensPage.test.tsx`: create with grants acme=operator, beta=viewer POSTs `grants: [{org:'acme',role:'operator'},{org:'beta',role:'viewer'}]` and shows the token once; the role select for an org where the person is operator offers viewer and operator only; a blocked grant shows the notice and cannot be selected; Revoke confirms then DELETEs.
  - `TokensPage.test.tsx`: personal entry tagged "Personal" with owner; "Revoke for this organization" DELETEs `/personal-tokens/{id}`; a blocked entry offers "Unblock".
  - `PersonalTokensCard.test.tsx`: an admin toggles → PUT `{allowed:false}`; a viewer sees no switch.
  - `AccountLayout.test.tsx`: the settings nav lists Security, Appearance, Profile, API tokens, My activity in that order.
  - `scripts/assistant-mcp-test.sh` (stub `CLAUDE_BIN`, `PULSIX_MCP_STORE=file`, `PULSIX_MCP_DIR` in a temp dir, token on stdin): `add prod-acme https://pulsix.example --org acme` writes `https://pulsix.example/api/mcp/o/acme` to the url file and passes it to the stub's `mcp add-json`; a `pxp_…` token is accepted, `abc` refused; `--org Bad!` refused; without `--org` the endpoint is `/api/mcp`.
- [ ] **Step 2: Run** `cd web && npx vitest run src/features/account src/features/tokens src/features/settings` and `scripts/assistant-mcp-test.sh` → FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `npm test`, `npm run lint`, `npx tsc -b`, `scripts/assistant-mcp-test.sh`, `shellcheck scripts/assistant-mcp.sh scripts/assistant-mcp-test.sh` (if installed) → PASS.
- [ ] **Step 5: Commit** `feat(web): personal tokens in the settings, personal entries on the organization's token page`

---

## Part E — End to end, backlog, docs

### Task 20: E2E, backlog, user docs, full verification

**Files:**
- Create: `web/e2e/lists.spec.ts`, `web/e2e/settings-area.spec.ts`, `web/e2e/personal-tokens.spec.ts`
- Modify: `docs/BACKLOG.md` (rows 3.97, 3.98, 3.99, 3.100 — added after 3.96, status done with what was built, files and tests named, as rows 3.93–3.96 do), `docs/upgrades.md` (migrations 00108–00110: catalog/additive, older release ignores them; 00109 builds a partial index on `jobs`)
- Docs repo: scratch clone `/private/tmp/claude-501/-Users-noidee--dev-pulsix/76b69f36-5040-49f9-a80e-08a1ef63bcdb/scratchpad/noix-docs-merge`, new branch `docs/pulsix-lists-settings-tokens` from its current branch `docs/pulsix-session-lock`. Never `~/_dev/noix-docs`. No push. Pages under `src/content/docs/pulsix/`: new `user/working-with-lists.md` (sorting, row menu and right-click, columns, pages, densities, Active runs and the filter chip; the user sidebar is autogenerated), `user/account-and-security.md` (the settings area and its pages, Appearance, Profile, My activity, the account menu's *Settings*; "API tokens" gains personal tokens and the organization switch; assistant connection with `--org`), `user/getting-started.md` if it describes the account menu, `reference/api.md` (`page`, `per_page`, `sort`, `order`, `total`; personal tokens; the per-organization assistant address), `reference/changelog.md` "Unreleased". No tech-stack names.

**E2E:**
- Lists: on Hosts (≥ 26 hosts created through the API in `beforeAll`) click the Host header twice → the first row is the last name alphabetically and the URL has `sort=name&order=desc`; choose 10 per page, reload → still 10 and the footer reads "1–10 of N hosts"; hide Zone in Columns, reload → still hidden; Appearance → Compact → `<html data-density="compact">`; right-click a host row → the menu holds the "…" entries plus "Open in new tab"; a host with a running ad-hoc command shows "1 running" in Active runs and its link opens the runs list filtered to it.
- Settings area: account menu → *Settings* → `/account/security`; the sidebar shows "Back to <org>" and the five entries; *Back* returns to the org the person came from; the language select is on Appearance.
- Personal tokens (instance admin owns org A; the spec creates org B through `POST /api/v1/orgs`): create a personal API token with A=operator, B=viewer; with `request.newContext` and the token: `GET` hosts of A and B → 200; `POST` a host in A → 201, in B → 403; on B's token page an admin revokes the grant → B 403, A still 200; the owner's edit adding B again is refused with the blocked notice.

- [ ] **Step 1: Write the specs.** **Step 2: Run** `cd web && npm run e2e -- lists settings-area personal-tokens` → PASS (ports 55432, 45025, 48025, 48444, 48090 belong to the e2e compose project; nothing else is touched).
- [ ] **Step 3: Full verification:** `make test` (read the log: exit line, `FAIL` lines, ok count), `golangci-lint run`, `gofmt -s -l .` empty, `scripts/coverage-gate.sh` if `make test` does not run it, `cd web && npm test && npm run lint && npx tsc -b && npm run e2e` (whole suite), `scripts/assistant-mcp-test.sh`, `grep -rn LoadMoreButton web/src/features` lists pickers only.
- [ ] **Step 4: Commit** `test(e2e): lists, settings area and personal tokens` and `docs: backlog 3.97–3.100 and upgrade notes`; in the docs clone `docs(pulsix): lists, settings area, personal tokens` on its branch (not pushed). Report both SHAs.
