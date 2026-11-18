# Group page redesign and one filter pattern for every list — design

Backlog 3.115 (group page) and 3.116 (list filters). Owner's request,
2026-11-18: "alle Listings mit passenden Filtern und sortierbaren Spalten",
"komplettes Redesign der Gruppen-Ansicht — Felder verwalten nur im Edit",
"JSON-Code-Viewer mit Farben", "eine Art Vererbungsansicht, wie sich
Variablen überschreiben", "alles wie aus einem Guss". Approved on the mockup
https://claude.ai/artifact/6sVWUhYPCzwFEK5YRsTCb3 ("besser, los geht's").

## Group page (`web/src/features/inventory/GroupDetailPage.tsx` and sections)

Same structure as the new host page (backlog 3.112):

1. **Header** instead of `GroupSidebar`: name (mono), completeness pill,
   description, parent link, host count (direct and via subgroups), subgroup
   count, jump host (own or inherited). Actions: primary "Assign host",
   "Subgroup …", "Manage fields …", and a "⋯" menu (Edit …, Delete …) with
   today's permission gating. Breadcrumb: Groups / <ancestors…> / <name>.
2. **Tabs** in the URL (`?tab=`, default `variables`): Variables · Members
   (count) · Subgroups (count) · Inheritance · Inventory.
3. **Variables tab**: the group's values only (today's ValuesSection
   behaviour), one row per field: key (mono), type badge, the value control,
   and a line saying where the effective value comes from ("overrides
   all_servers (staging)", "inherits ntp.example.test from all_servers",
   "schema default info"). Saving through a card save bar (Save disabled
   until dirty, Discard). Today's StaleNotice/revision behaviour stays.
4. **Manage fields dialog** (today's SchemaSection moves here, off the page):
   - left: one readable row per field — name, label, default, and three
     clickable badges: **type** (opens a menu with icon + one-line help per
     type), **scope** (group ↔ per host, toggles), **required** (toggles);
     reorder (drag handle or up/down); "+ Add field"; remove;
   - right, two tabs: **Field** (edit the selected field: name, label, type as
     six cards, scope as a segmented control, required as a switch, a default
     control matching the type — yes/no toggle for bool, option chips for
     enum with "+ Option" and click-to-make-default, masked for secret, number
     for int, comma list for list) and **Assign preview** (today's assignment
     form preview);
   - footer: unsaved hint, Cancel, Save (disabled until dirty); closing with
     changes asks first. Only the types the schema supports today are offered
     (do not invent new types; map the six cards to the existing type set).
5. **Inheritance tab**: a table with one row per variable that is set
   anywhere on the path and one column per level from weakest to strongest —
   schema default, `all`, each ancestor group, this group, and in host view
   the descendant group on the host's path and the host itself; the winning
   value highlighted, overridden values struck through, unset cells a dot; a
   result column naming the winner. A switch "View: group <name> | host <…>"
   (host picker among the group's hosts). Computed from data the SPA already
   gets (inventory preview, group tree, schemas); the precedence follows what
   the inventory preview actually produces — if the preview shows a different
   winner than the table would compute, the preview is right and the table
   must follow it (add a test that cross-checks). Legend under the table.
6. **Inventory tab**: today's preview in a new **JSON viewer** (`web/src/ui/
   json-viewer.tsx`, presentational): syntax colours (keys, strings, numbers,
   booleans, null, masked secrets), line numbers, collapsible objects/arrays
   (with "n keys"/"n items" when collapsed), Expand all / Collapse all, Copy.
   Reusable elsewhere later.
7. **Members / Subgroups tabs**: today's lists (DataList), with the shared
   filter pattern below.

## One filter pattern for every list

1. **Shared pieces** in `web/src/ui` (presentational): `ListToolbar`
   (layout), `SearchInput` (debounced, clear button, labelled), `FilterChip`
   (toggle, `aria-pressed`), `FilterMenu` ("+ Filter" with options for the
   less common filters), `ActiveFilter` (removable pill), "Reset" and a result
   count ("3 of 6 agents"). One URL helper in `web/src/features/lists`
   (validate + read + write filter params next to sort/page; any filter change
   drops `page`; typing replaces history).
2. **Every list** gets the filters its rows already support (no new API data
   unless the server already filters): see the survey table in the plan.
   Server-paged lists (hosts, runs, alerts, admin users, activity, my
   activity) pass filters to the server as today; client lists filter in the
   browser before paging.
3. **All filters in the URL**, including the ones kept in component state
   today (alerts, fleet, activity, my activity, users).
4. **Sortable columns**: every column whose content has a natural order is
   sortable; columns that cannot be sorted show no sort affordance.
5. Hosts, runs and alerts move onto the shared pieces so everything looks
   the same.

## Constraints

- Keep every permission rule and existing behaviour (revisions, stale
  notices, secrets masked, step-up).
- en + de copy; public docs updated (no tech-stack names).
- Tests: unit tests for the shared pieces and URL helper, per-list filter
  tests, group page tabs/dialog/inheritance/JSON viewer; e2e updated.
