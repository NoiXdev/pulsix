# Group page redesign and list filters — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`) syntax.

**Goal:** the group page gets the host page's header + tabs, a Manage-fields dialog, an Inheritance view and a coloured JSON viewer; every list gets the same search/filter bar with filters in the URL and real sortable columns.

**Spec:** `docs/superpowers/specs/2026-11-18-groups-and-list-filters-design.md` (mockup https://claude.ai/artifact/6sVWUhYPCzwFEK5YRsTCb3).

## Global Constraints
- SPA only unless a server filter already exists; src/ui presentational (no value import from src/api).
- `cd web && npx vitest run && npx tsc -b && npx eslint .` must pass; single quotes in TS, double in JSX attributes; files under ~400 lines.
- en + de copy (German "du"). Never run Playwright except in the last task; never touch Docker.
- Commits: Conventional Commits, footer `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`; no push/tags/stash/reset/rebase/amend/branch switching.

## Task 1: Shared pieces
- `web/src/ui/json-viewer.tsx` (+ tests): `JsonViewer({ value, label, maskedValue?: (v) => boolean, initiallyCollapsed?: string[] })` — colours, line numbers, collapse per node, Expand all / Collapse all, Copy (pretty JSON).
- `web/src/ui/list-toolbar.tsx` (+ tests): `ListToolbar`, `SearchInput` (debounce 250 ms, clear), `FilterChip` (aria-pressed), `FilterMenu` (+ Filter, items with optional hint), `ActiveFilter` (removable pill), `ResetFilters`, `ResultCount`.
- `web/src/features/lists/filterSearch.ts` (+ tests): typed filter param schemas per list (string, enum, boolean, multi-enum), `validate`, `useListFilters(route)` returning `{ filters, setFilter(key, value, { replace? }), reset }` that drops `page` on change.
- Migrate hosts (`hostsSearch.ts`/`HostsListPage`), runs, alerts toolbars onto these pieces (behaviour unchanged except alerts filters move into the URL).

## Task 2: Group page header, tabs, variables, members, subgroups, inventory
Header + tabs per spec 1–3, 6, 7; inventory tab uses JsonViewer; breadcrumb with ancestors; GroupSidebar deleted when unused.

## Task 3: Manage fields dialog
Spec 4; SchemaSection logic moves into the dialog; page no longer shows the schema editor.

## Task 4: Inheritance tab
Spec 5, with the cross-check test against the inventory preview.

## Task 5: List filters — infrastructure and monitoring
Groups (search, has jump host), jump hosts (search, zone, reachable), credentials (search, kind, expiring/expired, unused), agents (search, status, outdated, zone if present) + agent tokens (expired), workers (zone, status), fleet (zone, monitoring group, tag, state; into URL), alerts (already moved in Task 1), checks (search, plugin, state), item decisions (verdict, check), maintenance (status), monitoring groups (search), rules (search, severity, state).

## Task 6: List filters — automation, settings, account
Repos (search, sync status, credential), playbooks (search, source, repository), templates (search, playbook, scope), schedules (status, template select; keep ?template), runs (+ template, zone), run requests (status, kind, template), host runs (status, kind), members (search, role, status), pending invites (expired), activity (actor, object types, date range if the server supports it; into URL), org tokens (search, owner, role, expired), secrets (search, unused), channels (kind, status), alert routes (enabled), users (search, state; into URL), organizations (search), backups (kind, state), shared repos/templates/checks (search; plugin for checks), personal tokens (kind, org, expired), my activity (organization, object types; into URL).

## Task 7: Sortable columns sweep, e2e, docs, backlog
Columns that look sortable but are not: make them sortable where the data has an order (hosts os/agent version/tags-count?, runs template/zone, fleet state, users state, secrets used_by count, …) or drop the affordance. Full e2e run; public docs; backlog 3.115/3.116.
