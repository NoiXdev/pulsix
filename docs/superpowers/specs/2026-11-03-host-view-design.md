# Host view overhaul — design

Date: 2026-11-03. Decisions taken with the owner in chat the same day, from mockups (`.superpowers/brainstorm/…/host-detail-layout.html`, `host-list-dialog.html`; git-ignored).

## Intent

The owner tried the hosts pages on a fresh local stack and found them hard to use: detail cards are cut off, the row menu in the list does nothing, a host cannot be edited after it is created, the add dialog has no jump host, checkboxes feel dated, and a host with monitoring switched off still shows monitoring data. The host view is to be rebuilt first, and the patterns it establishes become the template for every other list and detail view ("build the host view and learn from it how we make all the others").

Success: opening a host shows everything about it at a glance without scrolling through clipped boxes; every action a person looks for (edit, run a play, switch a capability, delete) is where they expect it; nothing on the page suggests data that is not being collected.

## Goals

1. **Host detail page, layout B** (chosen over a tabbed header layout):
   - A **left sidebar** (about 300 px, sticky while the right side scrolls) holding the host's identity and the things one changes:
     - status dot and name;
     - an overall state pill, e.g. "2 warnings" or "OK";
     - a key–value block: address, zone, operating system (from facts), agent (status and version), and the effective jump host;
     - capability switches (Managed, Monitored);
     - tags;
     - the actions: **Run play** (primary), **Edit**, and **⋯** (Bootstrap, Delete);
     - a section navigation of anchor links. The current section is highlighted while scrolling, and the alert and check counts sit beside their links.
   - A **right column** of sections, one long page in this order:
     1. **State:** metric tiles with a sparkline for CPU, RAM, the fullest disk and load. Show only the tiles the host has data for; colour a tile by its check state.
     2. **Alerts:** open alerts with snooze, plus the alert routes that cover the host.
     3. **Checks:** the existing checks table with values, interval and source, the effective rules, and the rules for items the host no longer reports.
     4. **Runs:** the most recent runs that touched this host, with a link to all of them.
     5. **Connection & SSH:** SSH user and credential, jump host, host-key pin, connection test, and the agent card.
     6. **Inventory:** the host's inventory groups.
     7. **Facts.**
   - Every card and section grows with its content. Nothing is clipped (today's bug: `Card` is `overflow-hidden` inside a flex column, so the cards shrink).
2. **Capability off means its sections are gone (option A).**
   - When **Monitored** is off, the State, Alerts and Checks sections are replaced by one dashed hint card: "Monitoring is off — no checks are measured and no alerts raised", with an **Enable** button (shown only if the role may change it).
   - The preview of the checks that *would* apply is dropped.
   - *(Controller's call, by analogy.)* When **Managed** is off, the Connection & SSH and Inventory sections get the same treatment ("Management is off …" plus Enable). The agent card stays, because the agent is part of monitoring.
3. **Host list:**
   - The **whole row is a link** to the host page: hover highlight, keyboard-focusable, opens in a new tab with middle-click or Cmd-click.
   - The **⋯** at the row's end opens a real menu: Open, Edit, Run play, Switch monitoring on/off, Switch management on/off, Delete… (with confirmation). Items the role may not use are hidden.
   - In the list, capabilities are **read-only pills** ("Managed" / "not managed" and so on), not switches, so a click on the row never toggles anything by accident.
   - Clicking the menu never also navigates.
4. **One dialog for adding and editing a host.**
   - Fields: Name, Address, Zone, **Jump host**, Tags (key=value chips), and Managed and Monitored as switches with a one-line explanation each.
   - **Edit** (in the sidebar and in the row menu) opens the same dialog, pre-filled. It saves through the existing `PATCH /api/v1/orgs/{org}/hosts/{id}`, which is a full replace of name, zone, address and capabilities, plus the jump-host and tag endpoints.
   - **Jump host** offers three choices:
     - **Inherited** (the default). The dialog shows what it currently resolves to; the chain is host → inventory group → zone → none.
     - **Direct, no jump host.**
     - **A specific jump host** from the org's list, plus "+ Create jump host…".
   - These map onto the existing per-host `bastion_mode` (`inherit` / `none` / `explicit`).
   - SSH user and credential stay in the host page's Connection & SSH section, not in the dialog.
5. **Switches instead of checkboxes, everywhere.**
   - A shared `Switch` component in `web/src/ui` replaces every boolean checkbox in the SPA (17 forms today; list them with `grep -rl 'type="checkbox"' web/src | grep -v test`). It also replaces the host page's hand-rolled toggles.
   - It must be accessible: `role="switch"`, `aria-checked`, a label, Space and Enter toggle it, and a visible focus ring.
   - Checkboxes that select items from a list (multi-select) are not booleans and stay checkboxes. Whoever builds this decides, form by form, which kind a box is.
6. **Reusable building blocks extracted from the host view**, so other views can follow:
   - `DetailLayout` (sticky sidebar plus a section column with anchor navigation);
   - `SidebarFacts` (key–value block);
   - `Section`;
   - `DisabledHint` (the dashed "X is off" card);
   - a clickable `ListRow` with a `RowMenu`;
   - `Switch`.
   - Name and place them as the builder sees fit under `web/src/ui` (presentational only, no data fetching, per CLAUDE.md).

## Non-goals

- Migrating the other detail views (groups, workers, agents, runs, checks, …) to the new layout. They follow in separate items, using the building blocks above. A backlog row lists them.
- New metrics or checks. The State tiles use only data the host's checks already report.
- Changing what the capabilities mean, or how the jump host is resolved.
- SSH user and credential in the add dialog.

## Anti-goals

- A click anywhere on a list row that changes state.
- Monitoring numbers or check lists on a host that is not monitored.
- Clipped or fixed-height cards anywhere on the page.

## Constraints

- The layering rules of `CLAUDE.md`: `src/api` hooks → `src/features/hosts` → `src/ui` (presentational). Tests in Vitest. Coverage gates as in `CLAUDE.md`.
- English in code, en and de i18n for every new string.
- Backend changes stay additive. Known gaps the builder must close:
  - **Runs per host:** the runs list (`GET /api/v1/orgs/{org}/jobs`) has no host filter today. Add a `host_id` filter, or a host-scoped endpoint, so the Runs section can list the runs that touched the host. How "touched" is defined (a target host of the run) is the builder's call; document it.
  - **Create with jump host and tags:** a create that half-succeeds is worse than none. Either `POST /hosts` accepts the jump-host mode and the tags in the same request and transaction, or the dialog creates the host first and then sets the jump host and tags, and a failure there stays visible on the newly created host with a clear message. The builder picks one; the first is preferred.
  - **SPA hook for `PATCH /hosts/{id}`:** it does not exist yet.
- Changing a host's zone moves its future jobs to that zone's workers. The edit dialog says so, in one line, when the zone changes.
- End-user documentation (noix-docs, branch `pulsix`): update the hosts page, covering the list, the detail page, add and edit, jump host and switches, in the same work item.

## Controller's calls (from the builder check; the owner may overturn them)

1. **One backend write for create and edit.**
   - A single service method sets the host fields, the jump-host mode and the tags in one transaction. `POST /hosts` and `PATCH /hosts/{id}` both accept the optional `bastion_mode` and `bastion_id` and the `tags` (additive request fields).
   - The jump-host write touches only `bastion_mode` and `bastion_id`. It never resets the SSH user, port or credential the way a full `PUT /hosts/{id}/ssh` would.
   - Omitting the fields on PATCH leaves them unchanged.
2. **Inherited jump host, shown live only for a saved host.**
   - In the sidebar and in the edit dialog with the zone unchanged, it comes from the existing resolver (`connection` in `GET /hosts/{id}/inventory/preview`).
   - On create, or once the zone is changed in the dialog, the dialog shows the zone's default jump host plus the static hint "an inventory group can override it".
   - No new resolver endpoint.
3. **Roles follow the existing rules.**
   - Fields and actions the role may not use are hidden or disabled, as `HostSshCard` does today: the jump host needs `inventory.write`, and "+ Create jump host" needs `credentials.write`.
   - The row-menu capability switches use the permission of the capabilities route.
   - A role without `credentials.write` can still use the dialog; only the create entry is missing.
4. **Open alerts on an unmonitored host are not hidden silently.**
   - If the backend leaves alerts open after monitoring is switched off, the "Monitoring is off" card adds one line: "N alerts are still open from before", linking to the alerts page filtered to the host.
   - The sidebar pill then reads "Monitoring off", and the nav shows no counts.
   - Alert routes move with the Alerts section, so they are hidden too.
5. **State tiles use the 24 h range.** There is one metrics request per tile, at most four. "Fullest disk" is picked from the current check values. Load appears only if a check reports it.
6. **Order of work:** the `Switch` component and the migration of every boolean checkbox come first, as their own reviewed task on the same branch. The host view builds on it.

## Left to the builder

- Exact component names and APIs; the sidebar width and breakpoints. Below about 1100 px the sidebar stacks above the sections.
- Which existing cards are reused inside sections and which are rewritten.
- How the "current section" highlight is computed (an IntersectionObserver is fine).
- Whether "Run play" opens the existing ad-hoc run dialog pre-filled with the host (preferred) or a new one.
