# Detail Views on the Host-View Building Blocks (7.67–7.72) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The inventory group page, the run page and the worker, agent, run, check, rule and notification-channel lists follow the host view's patterns. Detail pages get a sticky sidebar and anchored sections. Rows that open something are links. Row actions sit in a ⋯ menu that never navigates. Write actions are hidden from roles that may not use them. Nothing is clipped.

**Architecture:** SPA only. One preparatory task makes four small additive changes to `web/src/ui/detail-layout.tsx` and `web/src/ui/list-row.tsx`, which several pages need, and moves the host page's clipped-card guard into a shared test helper. Each page family is then migrated in its own task, the way `HostDetailPage`/`HostSidebar`/`HostRow` do it. Existing cards and forms are reused inside `Section`s. Row components move into their own files so no page crosses the ~400-line signal. A last task updates the Playwright specs that drive these pages.

**Tech Stack:** React/TypeScript SPA (TanStack Router/Query, Vitest + Testing Library), Playwright E2E.

**Spec:** `docs/superpowers/specs/2026-11-03-host-view-design.md` (goals 1, 3 and 6; anti-goals), backlog rows 7.67–7.72 (and 3.42) in `docs/BACKLOG.md`, and the Rulings below.

## Global Constraints

- Everything in the repository is English; every new user-visible SPA string exists in English and German (`web/src/i18n/en|de/*.ts`; `catalogue.test.ts` checks parity).
- Conventional Commits; every commit ends with exactly `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. Commit only the task's own files.
- TDD: failing test first, watched failing, then the minimal change.
- SPA: `PATH=$HOME/.nvm/versions/node/v24.14.0/bin:$PATH`; in `web/`: `npx vitest run <files>`, `npx eslint <files>`, `npx tsc -b`. **No prettier** (no config in this repo; it reformats everything — see backlog 3.42). ESLint plus `tsc -b` are the formatting gate.
- Layering: `src/ui` stays presentational (no `src/api` value imports — ESLint enforces it); data hooks stay in `src/features/*`.
- No backend change, no migration, no `npm run api:gen`. Every page here gets its data from hooks that exist today (Rulings 1, 4, 7).
- A `ListRow` is an `<a>`: its cells hold **no** link or button (nested interactive content is invalid HTML). A row that needs inline links (a host or group link in a cell) is a `StaticListRow`.
- Row menus: label `t.common.rowActions(name)` ("Actions for {name}" / "Aktionen für {name}"); an item that asks for confirmation ends in "…" (as the host list's "Delete…"); a menu whose every item is hidden is not rendered, and its 40px column is not reserved.
- The "nothing clipped" check in every page task uses `clippedElements(container)` from `web/src/test/clipped.ts` (Task 1) and expects `[]`. It flags a hidden overflow at a fixed height or max-height. A scrolling box (`overflow-auto max-h-…`) is not clipped, the same reading the host page's guard has today.
- File splits are pure moves: behaviour changes go in separate steps.
- The controller updates the backlog rows and the end-user docs in `NoiXdev/noix-docs` (branch `pulsix`). Each task's last step lists the doc lines that change. Public docs never name the tech stack.

## Review Focus

1. **A ⋯ menu never navigates, and no `ListRow` holds a nested link or button.** This matters most on the agent rows, whose host name used to be a link, and on the run rows. Each list task asserts that `within(rowLink).queryAllByRole('link')` and `queryAllByRole('button')` are empty and that selecting a menu item leaves the location unchanged.
2. **A save in one group section never throws away another section's unsaved draft.** The sections are no longer behind tabs, so saving the values must not reset a schema being edited, and renaming the group must not reset unsaved values (Task 2 tests).
3. **An old group link with `?tab=members`, and the host page's Inventory card link, land on the Members section after the group has loaded.** The section only exists once the async read is done, so a plain hash would miss it (Task 1 hash-scroll test, Task 2 redirect test).
4. **Write actions are hidden by role.** A viewer sees no Edit, Delete, Assign host, Save schema or Revoke anywhere on these pages, and on the group page a viewer's schema builder and values form cannot be edited (Task 2, 3, 4, 6, 7 viewer tests).
5. **The run page stays live.** The sidebar's status pill follows the SSE status frame ahead of the detail poll, and Cancel disappears once the run is terminal (Task 5 test).

## Rulings

Binding decisions of the controller for this package:

1. **Shared extensions (Task 1), only what several pages use.**
   - `ListRow` gains `hash` (agent rows → host page `#connection`).
   - `StaticListRow` gains `menu` and its `title` becomes optional. Worker, agent-token, check, rule, monitoring-group, item-decision and channel rows open nothing but have actions.
   - Both row wrappers carry `data-list-row` (a stable hook for E2E row helpers instead of `xpath=../..` chains).
   - `DetailLayout` scrolls to the section named by the URL hash once that section is in its nav, a single time per hash. This is because detail pages render their sections only after an async read.
   - `SidebarHeading` (status dot, title, pill, optional sub-line) is the host sidebar's heading made reusable; the host, group and run sidebars use it.
   - No other building block changes.
2. **7.67 — group page becomes a detail page; tabs become sections.**
   - **Sidebar:** `SidebarHeading` (mono name; pill "Complete" / "Incomplete" from the group summary), the description, and `SidebarFacts` with Parent (link, or "—"), Members ("N, M to fill"), Child groups, Fields ("G group · H host") and Jump host. Actions: **Edit** and **⋯ → Delete group…**.
   - **Sections, in this order:** Members (count) · Group values · Variable schema · Child groups (count) · Preview inventory.
   - **Section-scoped actions sit in their section's header:** "Assign host" in Members, "Add child group" in Child groups.
   - **The Settings tab becomes `GroupSettingsDialog`** (title "Edit group": name, description, parent, jump host). Delete moves to the sidebar menu and navigates to `/groups`.
   - **Member rows are `ListRow`s** to the host page, with ⋯ Open host / Edit values / Remove from group…. Child-group rows are `ListRow`s without a menu. The groups list (`GroupsPage`) rows become `ListRow`s without a menu (there are no list actions today; none are invented).
   - **Group values without any group-scoped field:** a `DisabledHint` stands in for the form ("No group fields — the schema declares none"), with a "Go to schema" link to `#schema` for writers.
   - **Unsaved drafts across sections:** the Schema section is keyed by `JSON.stringify(group.schema)`, the Values section by `JSON.stringify([group.schema, group.vars])`. A save elsewhere (which bumps `revision`) no longer remounts them. A schema change still restarts the values form, because its fields changed.
   - **Old links:** `?tab=` links redirect to `#<section>` (`settings` → no hash). Links inside the app pass `hash` instead of `tab`.
   - **Roles** (`inventory.write`; the jump-host create row keeps `credentials.write`): without it, no Edit, Delete, Assign host, Add child group or row-menu write items. Schema and Values render without their save bars, the builder inside `<fieldset disabled>` and the form with `disabled`. A dedicated read-only rendering is out of scope.
   - **Preview** keeps its scrolling `<pre>`: scrolling, not clipped (Global Constraints).
3. **7.68 — no worker detail page; only the list migrates.**
   - **Why no page:** the API has no single-worker read and no per-worker history (the runs list has no worker filter). A page would restate the row's eight columns.
   - **Worker rows** are `StaticListRow`s whose cells keep the current-job link. The ⋯ menu offers "Revoke…" for a live worker and "Delete…" for a revoked one (`workers.write`).
   - **Pending enrolments:** `StaticListRow` with ⋯ Withdraw.
   - **Columns:** the Actions column header and its 150px track go; a 40px menu track is reserved only for `workers.write`.
4. **7.69 — no agent detail page. The host page is the agent's detail page and keeps its agent card permanently.**
   - **Why no page:** an agent belongs to exactly one host. The API reads it per host (`GET /hosts/{id}/agent`) and has nothing beyond what `HostAgentCard` and the host sidebar's Agent fact already show.
   - **Agent rows** are `ListRow`s to `/o/$org/hosts/$id#connection`, the section that holds the agent card. The menu offers Open host, plus "Revoke…" (`agents.write`). The host name in the row is plain text now.
   - **Pending tokens:** `StaticListRow` with ⋯ Withdraw (`agents.write`).
   - **The host page does not link to the Agents page.** The backlog's "until this lands" is resolved: there is nothing to link to.
5. **7.70 — run page on `DetailLayout`; run rows are `ListRow`s.**
   - **Sidebar:** `SidebarHeading` (short id, dot and pill from the status that is fresher of SSE and poll), kind · template as sub-line. `SidebarFacts`: Zone (slug from `useZones`, short id as fallback), Worker, Triggered by (schedule link kept), Started, Finished, Run group. Actions (`jobs.run`): Cancel run (live) and Retry failed (terminal; disabled without failed hosts; the override note stays linked by `aria-describedby`).
   - **Sections:** Result (error banner, per-host table, cancelled-run breakdown) · Run group (only with siblings; sibling `ListRow`s with status, short id and zone) · Vault identities (only when there are any; recorded/computed note in the section header) · Event log (last, since it grows; the polling marker in the section header).
   - **The per-host table stays a table.** Ansible keys the stats by inventory name, not host id, so there is nothing to link a row to.
   - **No `DisabledHint` on this page.** No capability switches a section off; absent data omits the section or shows an empty line.
   - **The short-id back-link to `/runs` is dropped.** The shell nav has Runs.
   - **`RunsPage` rows** become `ListRow`s with ⋯ Open, plus "Cancel run" for a queued/claimed/running run (`jobs.run`). There is no confirmation, the same as the run page's own button. Retry stays on the run page, because the list's `JobDTO` has no stats.
   - **The host page's `HostRunsSection` rows** become `ListRow`s without a menu.
6. **7.71 — no check or rule detail pages; all four Checks-tab lists migrate.**
   - **Why no pages:** a definition, rule, monitoring group or item decision is fully shown by its row and edited in its dialog. The API has nothing more per item.
   - **Scope:** Checks, Rules, Monitoring groups and Items all migrate. They share one tab strip and one row shape, and leaving two of the four on inline buttons would be the inconsistency this work removes.
   - **Rows** are `StaticListRow`s, because their cells hold host and group links. Menu items, behind `checks.write`:
     - checks: Edit · Copy (seed/shared) · Delete… (not shared);
     - rules: Edit · Delete…;
     - monitoring groups: Edit · Delete…;
     - item decisions: Edit · Delete….
   - **Takes 3.42 into account:** `RuleDialog.tsx`, `HostChecksCard.tsx` and `HostCheckValueRow.tsx` are not touched. The host page's check rows carry inline graph and rule disclosures, which a link row cannot hold, and they already sit in the host page's Checks section. Each list's row goes into its own file, so the pages shrink.
   - **When nothing reports a metric,** the disabled "New rule" and "New decision" buttons get a `DisabledHint` above the list, shown to writers only, explaining why.
7. **7.72 — no channel detail page; only the list migrates.**
   - **Why no page:**
     - a channel has four fields and a delivery log;
     - the log is a read-only list that already opens on demand;
     - a page would put a 300px detail sidebar inside the Settings side nav (220px), leaving the log about 450px at common widths.
   - **Rows** are `StaticListRow`s with ⋯ Deliveries (opens the existing `DeliveryLog` dialog; everyone with `channels.read`), plus Send test · Edit · Delete… (`channels.write`).
   - **The "no outgoing mail" note** becomes a `DisabledHint` ("E-mail is not set up on this installation").
   - **`DigestCard`** is unchanged.
8. **Docs.** The noix-docs pages that change are `user/inventory-and-variables.md`, `admin/configuration/workers-and-zones.md`, `user/agents.md`, `user/playbooks-and-jobs.md`, `user/monitoring.md`, `user/rules-and-states.md`, `user/items.md` and `user/notifications.md`. Each task lists its lines.

---

### Task 1: Building-block extensions shared by the pages

**Files:** Modify `web/src/ui/list-row.tsx`, `web/src/ui/detail-layout.tsx`, `web/src/features/hosts/HostSidebar.tsx` (`HostHeading` renders through `SidebarHeading`; the `DOT` map moves into `detail-layout.tsx`), `web/src/features/hosts/HostDetailPage.test.tsx` (imports the helper), `web/src/i18n/en|de/common.ts`. Create `web/src/test/clipped.ts`. Tests: `web/src/ui/list-row.test.tsx`, `web/src/ui/detail-layout.test.tsx`.

**Interfaces (Produces):**
- `ListRow` props gain `hash?: LinkProps['hash']`, passed to its `<Link>`. The wrapper `div` gets `data-list-row=""`.
- `StaticListRow({ columns, title, children, menu }: { columns: string; title?: string; children: ReactNode; menu?: ReactNode })`: the wrapper becomes `relative` and carries `data-list-row=""`; `menu` renders exactly as `ListRow`'s (`absolute top-1/2 right-3 -translate-y-1/2`); the caller reserves the last 40px column and leaves its cell empty.
- `SidebarHeading({ title, mono, dot, pill, children }: { title: string; mono?: boolean; dot?: { tone: Tone; label: string }; pill?: ReactNode; children?: ReactNode })` in `detail-layout.tsx`:
  - an `h1` (`font-mono` when `mono`, `break-all`);
  - the dot, `title={dot.label}`, coloured from the moved `DOT` map;
  - the pill below it, then `children` as a muted sub-line.
  - `Tone` is imported from `./badge.tsx` (ui → ui).
- `DetailLayout`: on mount and whenever its nav ids change, if `window.location.hash.slice(1)` is one of the ids and that hash has not been scrolled to yet (a ref holds the last hash scrolled), it calls `document.getElementById(id)?.scrollIntoView({ block: 'start' })` and seeds the current section with it. Later nav changes never scroll again for the same hash.
- `web/src/test/clipped.ts`: `export function isClipped(classes: string[]): boolean` (moved verbatim from `HostDetailPage.test.tsx`) and `export function clippedElements(root: HTMLElement): HTMLElement[]`.
- i18n `common.rowActions: (name: string) => string` ("Actions for ${name}" / "Aktionen für ${name}") and `common.open` ("Open" / "Öffnen").

- [ ] **Step 1: Failing tests.**
  - `list-row.test.tsx`:
    - `ListRow` › `carries a hash into its href`: `hash="connection"` → `getByRole('link')` has `href` ending `#connection`.
    - `StaticListRow` › `renders a menu outside its cells that does not need a title`: no `title`, `menu={<RowMenu label="Actions for a"><RowMenuItem onSelect={spy}>Go</RowMenuItem></RowMenu>}`:
      - no `title` attribute;
      - the button "Actions for a" is not inside the grid element that holds the cells;
      - selecting "Go" calls `spy` once.
    - `both row kinds carry data-list-row`.
  - `detail-layout.test.tsx`:
    - `scrolls once to the section named by the hash when it appears`:
      - `window.history.replaceState(null, '', '/#b')`, `Element.prototype.scrollIntoView = vi.fn()`;
      - render with nav `[a]` and only section `a` → not called;
      - rerender with nav `[a, b]` and both sections → called once, `this` is `#b`, and the nav link "b" has `aria-current="location"`;
      - rerender with nav `[a, b, c]` → still once.
    - `SidebarHeading` › `renders the title as the page heading with dot, pill and sub-line` (`getByRole('heading', { level: 1, name: 'web-04' })`, dot `title` "2 warnings", pill text, sub-line text).
  - Existing `HostSidebar.test.tsx` and `HostDetailPage.test.tsx` stay unchanged, apart from the helper import.
- [ ] **Step 2:** `npx vitest run src/ui/list-row.test.tsx src/ui/detail-layout.test.tsx` — the new cases fail.
- [ ] **Step 3:** Implement as above; `HostHeading` passes its existing tone/label into `SidebarHeading`.
- [ ] **Step 4:** Run the two ui test files plus `src/features/hosts/` — all pass; eslint on touched files; `npx tsc -b`.
- [ ] **Step 5: Commit** `feat(web): let list rows carry a hash and a menu, scroll detail pages to their hash, share the sidebar heading`.
- Docs: none.

### Task 2: Inventory group page on the building blocks (7.67)

**Files:**
- `git mv` `web/src/features/inventory/tabs/` → `sections/`, renaming:
  - `MembersTab` → `MembersSection`;
  - `ChildrenTab` → `ChildrenSection`;
  - `PreviewTab` → `PreviewSection`;
  - `SchemaTab` → `SchemaSection`;
  - `ValuesTab` → `ValuesSection`;
  - `AssignHostDialog`, `ManagedHostPicker` (and its test) and `StaleNotice` move unchanged.
- `SettingsTab.tsx` becomes `web/src/features/inventory/GroupSettingsDialog.tsx`.
- Create `web/src/features/inventory/GroupSidebar.tsx` and `web/src/features/inventory/sections/MemberRow.tsx`.
- Modify:
  - `GroupDetailPage.tsx` and `GroupsPage.tsx`;
  - `CreateGroupDialog` callers in `GroupsPage.tsx` and `ChildrenSection.tsx` (`hash: 'schema'`);
  - `web/src/router.tsx` (`groupDetailRoute`);
  - `web/src/features/hosts/HostInventoryCard.tsx` (`search={{ tab: undefined }} hash="members"`);
  - `web/src/i18n/en|de/inventory.ts`.
- Tests:
  - `GroupDetailPage.test.tsx` (the tab test is rewritten; the rest keep their assertions);
  - create `GroupDetailPage.layout.test.tsx` (the existing file is 384 lines);
  - `GroupsPage.test.tsx`, `web/src/router.test.tsx`, `HostInventoryCard.test.tsx`.

**Interfaces:**
- Consumes Task 1's `SidebarHeading`, `ListRow`, `RowMenu`, `DisabledHint`, `clippedElements`, `common.rowActions`, `common.open`.
- `GroupDetailPage` guards the org query, then the group query (the `HostDetailPage` order), and renders `DetailLayout` with:
  - nav `members` (count `own.members`), `values`, `schema`, `children` (count), `preview`;
  - labels from `t.inventory.detail.section.{members,values,schema,children,preview}`, which replaces `detail.tabs` and drops `settings`.
- `GroupSidebar({ org, group, groups, role }: { org: string; group: InventoryGroup; groups: readonly InventoryGroupSummary[]; role: string })`:
  - owns the Edit dialog state and the delete flow (confirm `t.inventory.settings.confirmDelete`; on success toast plus navigate to `/o/$org/groups`; on failure a destructive toast and stay);
  - reads `useBastions` for the jump-host name.
- `GroupSettingsDialog({ org, group, groups, canCreateCredential, onClose }: { …; onClose: () => void })`:
  - `Dialog` titled `t.inventory.settings.editTitle` ("Edit group");
  - the former form without the Delete button: save → toast `saved` plus `onClose()`, 412 → `StaleNotice`;
  - `BastionDialog` stays a sibling of the `<form>`.
- Section components each render their own `<Section id=… title=…>`, `canWrite` gating as in Ruling 2:
  - `MembersSection({ org, group, canWrite })`;
  - `ValuesSection({ org, group, canWrite })`;
  - `SchemaSection({ org, group, canWrite })`;
  - `ChildrenSection({ org, group, groups, canWrite })`;
  - `PreviewSection({ org, group })`.
- `MemberRow({ org, group, member, columns, canWrite, onEdit }: { …; member: InventoryMember; onEdit: (m: InventoryMember) => void })`:
  - `ListRow` to `/o/$org/hosts/$id`, label `member.host_name`; cells are host name and completeness pill;
  - menu: Open host · (writer) Edit values · Remove from group… (the existing confirm and toasts);
  - columns `'minmax(0, 1fr) 200px 40px'`.
- Router: `groupDetailRoute` keeps `validateSearch` for `tab` and gains `beforeLoad: ({ search, params }) => { if (search.tab) throw redirect({ to: '/o/$org/groups/$id', params, search: { tab: undefined }, hash: TAB_SECTION[search.tab], replace: true }) }`, where `TAB_SECTION` is `{ schema: 'schema', values: 'values', members: 'members', children: 'children', preview: 'preview' }` (`settings` and unknown values → no hash).
- New i18n:
  - `inventory.detail.section.*`, `inventory.detail.fact.{parent,members,children,fields,jumpHost}`, `inventory.detail.complete`, `inventory.detail.incomplete`, `inventory.detail.edit`, `inventory.detail.more(name)`;
  - `inventory.members.{openHost,editValues,removeItem}`;
  - `inventory.valuesTab.noFieldsTitle`, `noFieldsBody` and `toSchema` (the hint);
  - `inventory.settings.editTitle`.

- [ ] **Step 1: Pure move.** `git mv` and rename the components; update imports. `npx vitest run src/features/inventory` passes unchanged. **Commit** `refactor(web): move the group page tabs to sections`.
- [ ] **Step 2: Failing tests** (`GroupDetailPage.layout.test.tsx` unless named otherwise):
  - `shows the group in the sidebar and every section in one column`:
    - `heading level 1 'web'`, pill "Incomplete" for the fixture;
    - the facts Parent/Members/Child groups/Fields/Jump host;
    - nav links Members, Group values, Variable schema, Child groups, Preview inventory;
    - `getByRole('region', { name: … })` for each; no `role="tab"` on the page.
  - `member rows open the host and their menu does not navigate`:
    - `getByRole('link', { name: 'web-01' })` has `href` `/o/acme/hosts/h1`, and holds no link or button;
    - opening "Actions for web-01" and choosing "Edit values" opens the assign dialog prefilled, while the router location stays `/o/acme/groups/g1`.
  - `edits name, parent and jump host in a dialog and deletes from the sidebar menu`:
    - this is the old settings test, moved: Edit → dialog "Edit group" → PUT body and `If-Match` as before;
    - ⋯ "Delete group…" with confirm → DELETE, then location `/o/acme/groups`;
    - a refused delete shows the toast and stays.
  - `hides every write action from a viewer`:
    - role `viewer`;
    - no "Edit", "Assign host", "Add child group", "Save schema" or "Save values" buttons;
    - the member row menu holds only "Open host";
    - the schema "Add field" button is disabled, and the `env` input is disabled.
  - `stands a hint in for group values when the schema has no group field`: a group whose schema has only host fields → "No group fields" and a link with `href` ending `#schema` for an admin.
  - `keeps an unsaved schema draft when the group values are saved`:
    - add a field in Schema (not saved), save values;
    - after the refetched group (revision +1) the new field editor is still there.
  - `keeps unsaved values when the group is renamed`: type into `env`, rename via the Edit dialog → `env` still holds the typed text.
  - `no clipped cards`: `clippedElements(container)` is `[]`.
  - `GroupsPage.test.tsx` › `each group row is a link to its page`: `getByRole('link', { name: 'web' })` `href` `/o/acme/groups/g1`, and no `button` inside.
  - `router.test.tsx` › `an old group link with ?tab= lands on that section`: navigate `/o/acme/groups/g1?tab=members` → `pathname` `/o/acme/groups/g1`, `hash` `members`, `search.tab` undefined.
  - `HostInventoryCard.test.tsx`: the group link `href` ends `/groups/g1#members`.
- [ ] **Step 3:** `npx vitest run src/features/inventory src/router.test.tsx src/features/hosts/HostInventoryCard.test.tsx` — the new cases fail.
- [ ] **Step 4:** Implement per the Interfaces and Ruling 2. The old tab-switch test in `GroupDetailPage.test.tsx` becomes `renders the schema builder and the group values together` (both `list 'Fields'` and `label 'env'` present without clicks). The settings tests there move to the dialog flow.
- [ ] **Step 5:** All inventory, router and hosts tests pass; eslint; `npx tsc -b`. No file under `features/inventory` over ~400 lines (`wc -l`).
- [ ] **Step 6: Commit** `feat(web): put the inventory group page on the detail layout`.
- [ ] **Step 7: Docs (controller), `user/inventory-and-variables.md`:**
  - "Open a group and the *Variable schema* tab" → the group page shows Members, Group values, Variable schema, Child groups and Preview inventory as sections of one page, reachable from the left-hand navigation;
  - "*Group values* tab" / "*Members* tab" → section;
  - "Use *Preview inventory*" → the Preview inventory section;
  - a new line: rename, parent and jump host via **Edit** in the sidebar; **⋯ → Delete group**; a member's ⋯ menu offers Edit values and Remove; viewers see the page without these actions.

### Task 3: Worker list on the building blocks (7.68)

**Files:** Create `web/src/features/workers/WorkerRow.tsx`. Modify `web/src/features/workers/WorkersPage.tsx` and `web/src/i18n/en|de/workers.ts`. Test `web/src/features/workers/WorkersPage.test.tsx`.

**Interfaces:**
- `WorkerRow({ org, worker, columns, now, canWrite, onRevoke, onDelete }: { org: string; worker: Worker; columns: string; now: Date; canWrite: boolean; onRevoke: (w: Worker) => void; onDelete: (w: Worker) => void })`:
  - a `StaticListRow` with today's seven cells;
  - when `canWrite`, a menu "Actions for {name}" with "Revoke…" (status ≠ revoked) or "Delete…" (revoked) plus an empty last cell.
- `EnrollmentRow({ enrollment, canWrite, onWithdraw }: { …; onWithdraw: (e: WorkerEnrollment) => void })`: a `StaticListRow`, menu "Withdraw" when `canWrite`.
- Columns: `COLUMNS_BASE` and `` `${COLUMNS_BASE} 40px` `` for writers; enrolments `'minmax(0, 1fr) 160px'`, plus `' 40px'` for writers.
- The Actions header column goes.
- New i18n: `workers.revokeItem` ("Revoke…"), `workers.deleteItem` ("Delete…"); the menu item "Withdraw" reuses `workers.withdraw`.

- [ ] **Step 1: Failing tests:**
  - `revokes after confirmation and withdraws a pending token`, rewritten: open "Actions for w1" → menuitem "Revoke…" → confirm → POST revoke; open "Actions for tok-name" → "Withdraw" → DELETE.
  - `deletes a revoked worker after confirmation`: its menu offers "Delete…" and no "Revoke…".
  - `rows are not links and keep the current-job link`: no `[data-list-row] a[aria-labelledby]`, and `getByRole('link', { name: 'abcd1234' })` points at `/o/acme/runs/…`.
  - `workers.read` and `workers.write` are both admin (`lib/roles.ts`), so there is no read-only role to test. `tells non-admins who manages workers` (operator) stays as it is and additionally asserts there are no "Actions for" buttons; `gives an admin a menu on every row` asserts one "Actions for …" per worker and per enrolment.
  - `no clipped cards`.
  - The three error-toast tests switch to the menu path.
- [ ] **Step 2:** `npx vitest run src/features/workers` — expect failure.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** The tests pass; eslint; tsc.
- [ ] **Step 5: Commit** `feat(web): move worker actions into row menus`.
- [ ] **Step 6: Docs (controller), `admin/configuration/workers-and-zones.md`:** "revoke and delete the worker on the Workers page" → "… with **Revoke** and then **Delete** in the worker's ⋯ menu on the Workers page" (the three places, lines ~112, ~198, ~213–221); pending tokens: **Withdraw** is in the token's ⋯ menu.

### Task 4: Agent list on the building blocks (7.69)

**Files:** Create `web/src/features/agents/AgentRow.tsx`. Modify `web/src/features/agents/AgentsPage.tsx` and `web/src/i18n/en|de/agents.ts`. Tests `web/src/features/agents/AgentsPage.test.tsx` and `AgentsPage.de.test.tsx` (selectors only). `HostAgentCard.tsx` is unchanged (Ruling 4).

**Interfaces:**
- `AgentRow({ org, agent, columns, now, canWrite, onRevoke }: { …; agent: Agent; onRevoke: (a: Agent) => void })`:
  - `ListRow` with `to="/o/$org/hosts/$id"`, `params={{ org, id: agent.host_id }}`, `hash="connection"`, `label={agent.host_name}`;
  - cells: host name (plain `span`), agent name, status pill, last seen, version + `VersionSkewBadge`, certificate;
  - menu: "Open host" · (writer) "Revoke…".
- `AgentTokenRow({ token, canWrite, onWithdraw })`: a `StaticListRow`, menu "Withdraw" for writers.
- Columns: `` `${COLUMNS_BASE} 40px` `` always (Open is always there); tokens `TOKEN_COLUMNS` without the trailing 110px, plus `' 40px'` for writers.
- New i18n: `agents.openHost` ("Open host" / "Host öffnen"), `agents.revokeItem` ("Revoke…" / "Widerrufen…").

- [ ] **Step 1: Failing tests:**
  - `each agent row opens its host's connection section`:
    - `getByRole('link', { name: 'web-01' })` has `href` `/o/acme/hosts/h1#connection`;
    - `within(link)` has no `link` and no `button`.
  - `revokes from the row menu without navigating`: "Actions for web-01" → "Revoke…" → confirm → POST `/agents/h1/revoke`; the location is still `/o/acme/agents`.
  - `shows a viewer the fleet but no write actions`, extended: the menu holds only "Open host"; the token rows have no menu.
  - `no clipped cards`.
- [ ] **Step 2:** `npx vitest run src/features/agents` — expect failure.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** The tests pass, plus `src/features/hosts/HostAgentCard.test.tsx` unchanged; eslint; tsc.
- [ ] **Step 5: Commit** `feat(web): make agent rows open their host and move agent actions into row menus`.
- [ ] **Step 6: Docs (controller), `user/agents.md`:**
  - "**Revoke** on the agents list or the host's Agent card" → "**Revoke…** in the agent's ⋯ menu on the Agents page, or on the host's Agent card";
  - a line: a row on the Agents page opens the host's page at its Connection section, where the Agent card is;
  - pending tokens: **Withdraw** is in the token's ⋯ menu.

### Task 5: Run page, runs list and the host page's run rows (7.70)

**Files:**
- Create `web/src/features/runs/RunSidebar.tsx`, `RunResultSection.tsx`, `RunGroupSection.tsx`, `RunVaultSection.tsx` and `RunRow.tsx`.
- Modify `web/src/features/runs/RunDetailPage.tsx` (composes; ~100 lines after), `RunsPage.tsx`, `web/src/features/hosts/HostRunsSection.tsx` and `web/src/i18n/en|de/runs.ts`.
- Tests:
  - `RunDetailPage.test.tsx` (505 lines: existing tests keep their assertions; selectors adapt only where the markup moved);
  - create `RunDetailPage.layout.test.tsx`;
  - `RunsPage.test.tsx`, `web/src/features/hosts/HostRunsSection.test.tsx`.

**Interfaces:**
- `RunSidebar({ org, job, status, canRun }: { org: string; job: JobDetail; status: string; canRun: boolean })`:
  - the cancel and retry hooks, handlers, toasts and navigation move here from the page, unchanged;
  - `SidebarHeading` `title={shortId(job.id)} mono dot={{ tone: statusTone(status), label: statusLabel(status, t) }} pill={<Pill …/>}`, sub-line `kindLabel · template name`;
  - `SidebarFacts` as in Ruling 5; `RUNNING` stays the cancellable set.
- `RunResultSection({ job, status, events }: { job: JobDetail; status: string; events: readonly JobEvent[] })`: `Section id="result"`; the error alert, per-host table (`aria-label` unchanged), cancelled breakdown and warnings move as they are; with none of these it shows `t.runs.detail.noResults`.
- `RunGroupSection({ org, siblings }: { org: string; siblings: NonNullable<JobDetail['group']> })`: `Section id="group"`; each sibling a `ListRow` (columns `'110px 90px minmax(0, 1fr)'`: status pill, short id, zone slug-or-short-id), no menu.
- `RunVaultSection({ identities, recorded }: { identities: JobVaultIdentity[]; recorded: boolean })`: `Section id="vault"` titled `t.credentials.vault.heading`, recorded/computed in the header with its `title`.
- `RunDetailPage` nav:
  - `result`;
  - `group` (count siblings + 1, only with siblings);
  - `vault` (only with identities);
  - `log` (title `t.runs.detail.eventLog`, header action the polling marker).
- `RunRow({ org, job, columns, canRun }: { org: string; job: Job; columns: string; canRun: boolean })`:
  - `ListRow` to `/o/$org/runs/$id`, label `shortId(job.id)`, today's seven cells;
  - menu: Open · "Cancel run" (status in queued/claimed/running and `canRun`) via `useCancelJob(org, job.id)`, with the run page's toasts;
  - `RunsPage` columns `` `${COLUMNS} 40px` ``.
- `HostRunsSection` rows: `ListRow` (columns `'90px 80px minmax(0, 1fr) 110px'`: pill, short id, kind · trigger, relative time), no menu.
- New i18n: `runs.detail.section.{result,group,log}`, `runs.detail.noResults` ("No per-host results yet." / "Noch keine Ergebnisse je Host."), `runs.list.cancelItem` ("Cancel run").

- [ ] **Step 1: Pure split.** Move the result block, sibling list, vault card and header actions into the new files, still rendered in today's places. `npx vitest run src/features/runs` passes unchanged. **Commit** `refactor(web): split the run page into its parts`.
- [ ] **Step 2: Failing tests** (`RunDetailPage.layout.test.tsx`):
  - `puts the run in the sidebar and its parts in sections`: a finished failed run with siblings and vault identities →
    - heading level 1 = short id;
    - facts Zone (`prod`, from the zones fixture), Worker, Triggered by, Started, Finished, Run group;
    - nav Result, Run group (count 2), Vault identities, Event log, in this order;
    - Event log is the last `region`.
  - `omits Run group and Vault identities when the run has none`.
  - `follows the stream's status ahead of the poll and drops Cancel when it ends`:
    - a running job → "Cancel run" visible;
    - emit `status` `succeeded` on the fake EventSource → the sidebar pill reads "succeeded" before the next poll, and "Cancel run" is gone.
  - `sibling runs are rows that link to their run`: `getByRole('link', { name: shortId(sibling) })` `href` `/o/acme/runs/<id>`.
  - `hides Cancel and Retry from a viewer`.
  - `no clipped cards`.
  - `RunsPage.test.tsx`:
    - `each run row is a link to its run`: `href`, no nested link/button;
    - `cancels a live run from its row menu without navigating`: "Actions for 0190abcd" → "Cancel run" → POST `/jobs/<id>/cancel`; location still `/o/acme/runs`;
    - `offers no Cancel for a finished run or to a viewer`: the menu holds only "Open".
  - `HostRunsSection.test.tsx`: `each recent run is a link row`, `href` `/o/acme/runs/<id>`.
- [ ] **Step 3:** `npx vitest run src/features/runs src/features/hosts/HostRunsSection.test.tsx` — expect failure.
- [ ] **Step 4:** Implement per Ruling 5 and the Interfaces.
- [ ] **Step 5:** The runs and hosts tests pass; eslint; tsc; `wc -l src/features/runs/*.tsx` shows none over ~400.
- [ ] **Step 6: Commit** `feat(web): put the run page on the detail layout and make run rows links`.
- [ ] **Step 7: Docs (controller), `user/playbooks-and-jobs.md` § "Running and following a job":** the run page shows the run's status, zone, worker, trigger and times in the left column, with *Cancel* and *Retry failed hosts* there. The sections are Result (each host's state, and on a cancelled run what each host was doing), Run group, Vault identities (the existing paragraph; "card" → "section") and the live Event log. A line for the runs list: a row opens its run; a running run can be cancelled from the row's ⋯ menu.

### Task 6: Checks, rules, monitoring groups and item decisions (7.71)

**Files:**
- Create in `web/src/features/checks/`: `CheckRow.tsx`, `RuleRow.tsx`, `MonitoringGroupRow.tsx` and `DecisionRow.tsx`.
- Modify `ChecksPage.tsx`, `RulesPage.tsx`, `MonitoringGroupsPage.tsx`, `ItemsPage.tsx` and `web/src/i18n/en|de/checks.ts`.
- Tests: `ChecksPage.test.tsx`, `RulesPage.test.tsx`, `MonitoringGroupsPage.test.tsx` and `ItemsPage.test.tsx` (new cases in `<Page>.rows.test.tsx` where the file would pass ~400 lines).
- **Not touched:** `RuleDialog.tsx`, `HostChecksCard.tsx`, `HostCheckValueRow.tsx` (Ruling 6).

**Interfaces:**
- Each row is a `StaticListRow` with the page's existing cells; `Binding` and `RuleTarget` move into their row files. A writer gets a menu labelled `t.common.rowActions(<key|name>)`, or `t.checks.items.decisionActions(pattern)` for decisions.
- `CheckRow({ org, check, columns, groups, canWrite, onEdit, onCopy, onDelete })`: items Edit · Copy (origin seed/shared) · Delete… (origin ≠ shared).
- `RuleRow({ org, rule, columns, groups, canWrite, onEdit, onDelete })`: Edit · Delete….
- `MonitoringGroupRow({ group, columns, canWrite, onEdit, onDelete })`: Edit · Delete….
- `DecisionRow({ org, decision, columns, canWrite, onEdit, onDelete })`: Edit · Delete….
- Each page's last column becomes `40px` for writers and is dropped for others; the existing confirm texts are kept.
- Rules and Items: when `canWrite` and the metric list is empty, a `DisabledHint` (`t.checks.rules.noMetricsTitle` and `noMetricsBody`; `t.checks.items.noMetricsTitle` and `noMetricsBody`) sits above the list; the New button stays disabled.
- New i18n: `checks.deleteItem` ("Delete…"), `checks.items.decisionActions(pattern)` ("Actions for decision {pattern}"), and the four no-metrics strings.

- [ ] **Step 1: Failing tests:**
  - `ChecksPage`:
    - `moves edit, copy and delete into the row menu`: a seeded check's menu has Edit, Copy, Delete…; a shared check's has Edit and Copy and no Delete…; a custom one's has Edit and Delete….
    - `the binding link still opens the host`, inside the static row.
    - `offers a viewer no row menu`.
    - Existing edit/copy/delete flows go through the menu.
    - `no clipped cards`.
  - `RulesPage`:
    - `deletes from the row menu after confirmation`;
    - `edits from the row menu`;
    - `explains a disabled New rule when nothing reports a metric` (writer: the hint text; viewer: no hint);
    - `offers a viewer no row menu`.
  - `MonitoringGroupsPage`: `edits and deletes from the row menu`, `offers a viewer no row menu`.
  - `ItemsPage`: `edits and deletes a decision from its row menu` ("Actions for decision /srv") and the no-metrics hint.
  - In each file: `rows are not links` (no `[data-list-row] > a`).
- [ ] **Step 2:** `npx vitest run src/features/checks` — expect failure.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** The checks tests pass, plus `src/features/hosts` unchanged; eslint; tsc. Check that `wc -l` of the four pages dropped and that `RuleDialog.tsx` and `HostChecksCard.tsx` have no diff (`git diff --stat`).
- [ ] **Step 5: Commit** `feat(web): move check, rule, group and item-decision actions into row menus`.
- [ ] **Step 6: Docs (controller):**
  - `user/monitoring.md`: "the *Edit* …" and "**Copy** beside a seeded check" → in the check's ⋯ menu; *Delete* likewise; the same for monitoring groups.
  - `user/rules-and-states.md`: edit, turn off or delete via the rule's ⋯ menu; a line that *New rule* stays unavailable until an agent reports a metric.
  - `user/items.md`: the Items tab's Edit and Delete are in the decision's ⋯ menu.

### Task 7: Notification channel list (7.72)

**Files:** Create `web/src/features/notifications/ChannelRow.tsx`. Modify `web/src/features/notifications/ChannelsPage.tsx` and `web/src/i18n/en|de/notifications.ts`. Tests `ChannelsPage.test.tsx` and `ChannelsPage.de.test.tsx` (selectors). `DeliveryLog.tsx`, `ChannelDialog.tsx` and `DigestCard.tsx` are unchanged.

**Interfaces:**
- `ChannelRow({ channel, columns, canWrite, testing, onLog, onTest, onEdit, onDelete }: { channel: NotifyChannel; columns: string; canWrite: boolean; testing: boolean; onLog: (c: NotifyChannel) => void; onTest: (c: NotifyChannel) => void; onEdit: (c: NotifyChannel) => void; onDelete: (c: NotifyChannel) => void })`:
  - a `StaticListRow` with today's four cells;
  - menu "Actions for {name}": "Deliveries" (always) · (writer) "Send test" · "Edit" · "Delete…".
- `COLUMNS = 'minmax(0, 0.8fr) 90px minmax(0, 1.2fr) 90px 40px'`.
- `!mailAvailable` → `DisabledHint title={c8.mailOffTitle} body={c8.mailNote}` in place of the `role="note"` paragraph.
- New i18n: `notifications.channels.deliveriesItem` ("Deliveries"), `testItem` ("Send test"), `deleteItem` ("Delete…"), `mailOffTitle` ("E-mail is not set up on this installation" / "E-Mail ist auf dieser Installation nicht eingerichtet").

- [ ] **Step 1: Failing tests:**
  - `opens a channel's deliveries from its row menu`: dialog "Deliveries · ops-mail".
  - `sends a test, edits and deletes from the row menu`: the existing flows go through the menu; the delete confirm is kept.
  - `gives an operator only Deliveries`: role operator → the menu holds "Deliveries" and nothing else; no "Add channel".
  - `says when e-mail is not set up`: `mail_available: false` → the hint title is visible.
  - `rows are not links`.
  - `no clipped cards`.
- [ ] **Step 2:** `npx vitest run src/features/notifications` — expect failure.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** The tests pass; eslint; tsc.
- [ ] **Step 5: Commit** `feat(web): move notification channel actions into row menus`.
- [ ] **Step 6: Docs (controller), `user/notifications.md`:** "*Log* on a channel shows …" → "*Deliveries* in a channel's ⋯ menu shows …"; *Send test*, *Edit* and *Delete* are in the same menu (the test-message paragraph near line 142); the roles table is unchanged.

### Task 8: E2E specs that drive these pages

**Files:** Modify:
- `web/e2e/inventory.spec.ts`;
- `web/e2e/workers.spec.ts`, `web/e2e/agent.spec.ts`;
- `web/e2e/alerts.spec.ts`, `web/e2e/fleet.spec.ts`, `web/e2e/items.spec.ts` (agent row helpers, rule/decision buttons);
- `web/e2e/rules.spec.ts`, `web/e2e/notifications.spec.ts`;
- `web/e2e/ansible.spec.ts` (only if a run-page selector broke).

Run `grep -n "getByRole('tab'\|Revoke \|Withdraw \|Edit \|Delete \|Deliveries of\|Send test through\|xpath=\.\./\.\." web/e2e/*.ts` first, and update every hit that belongs to a migrated page.

**Interfaces (Consumes):** the menu labels "Actions for {name}" and "Actions for decision {pattern}"; menuitems "Revoke…", "Delete…", "Withdraw", "Edit", "Copy", "Deliveries", "Send test", "Cancel run", "Edit values", "Remove from group…", "Delete group…"; the dialog "Edit group"; `[data-list-row]`.

- [ ] **Step 1: Update the selectors:**
  - **inventory:**
    - drop every `getByRole('tab', …)` click; the sections are on the page, and Playwright scrolls;
    - Settings → `getByRole('button', { name: 'Edit', exact: true })` → dialog "Edit group" → "Save";
    - assert `getByRole('navigation').getByRole('link', { name: 'Members' })` is present once.
  - **workers:** "Withdraw {name}" → "Actions for {name}" + menuitem "Withdraw"; the "Revoke {name}" count assertion → `getByRole('button', { name: 'Actions for e2e-worker-1' })` has count 0 (no worker yet).
  - **Agent row helpers** (`agentRow` in alerts/fleet/items): `link.locator('xpath=..')` (the `ListRow` wrapper). In `openHost`, clicking the row link lands on `/hosts/<id>#connection`; the URL regex already stops at `#`.
  - **agent.spec:** "Revoke agent on X" → "Actions for X" + "Revoke…".
  - **rules/alerts/fleet:** `Edit {RULE}` / `Delete {RULE}` → "Actions for {RULE}" + menuitem; the listing proof becomes `getByRole('button', { name: 'Actions for {RULE}' })` visible, and count 0 after delete.
  - **items:** "Actions for decision {mount}" visible.
  - **notifications:**
    - `channelRow` = `page.locator('[data-list-row]', { hasText: CHANNEL })`;
    - Send test and Deliveries via the row menu;
    - Delete via "Delete…";
    - absence check on "Actions for {CHANNEL}".
- [ ] **Step 2:** In `web/`: `npx playwright test e2e/inventory.spec.ts e2e/workers.spec.ts e2e/agent.spec.ts e2e/rules.spec.ts e2e/notifications.spec.ts e2e/items.spec.ts e2e/alerts.spec.ts e2e/fleet.spec.ts e2e/ansible.spec.ts e2e/hosts.spec.ts`. All pass, or a spec that self-skips says why (`ansible.spec.ts` without the worker image). Record the output in the task report.
- [ ] **Step 3:** The full suite `npx playwright test` passes. Record it.
- [ ] **Step 4: Commit** `test(e2e): follow the detail-view and row-menu migration`.
- Docs: none.
