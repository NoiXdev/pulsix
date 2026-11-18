# Host view: header, tabs and an SSH dialog — design

Backlog 3.112. Owner's request, 2026-11-18: the host page is "still very
cluttered; SSH access is very prominent — could be a dialog or a tab; and
there the IP / host is missing in the form". Approved on the mockup
https://claude.ai/artifact/WUC9wyiDa3jVj8FxmMUxcr ("ja das ist besser").

## Today

`web/src/features/hosts/HostDetailPage.tsx` uses `DetailLayout`: a sticky
left sidebar (`HostSidebar`: facts, capability switches, tags, actions,
section nav) and one long column — State, Alerts (+ alert routes), Checks
(every metric of every check listed, plus effective rules), Runs, Connection
& SSH (agent card, the full SSH form with key snippets, connection test),
Inventory, Facts. A monitored host page is over 4000 px tall. The SSH form
has no address (the address is only editable in the host's edit dialog). The
breadcrumb shows the host's id, not its name.

## Decided (mockup)

1. **Header instead of the sidebar** (full width):
   - line 1: state dot, name (mono), the state pill (worst open alert or ok),
     capability pills "managed" / "monitored" (only those that are on);
   - line 2: address with a copy button, zone, operating system (from facts,
     when known), the SSH route ("via <jump host>", when there is one),
     agent version (when there is an agent);
   - line 3: tags with the existing add/remove behaviour (compact);
   - right: primary "Run command", "Run template …", and a "⋯" menu with
     Edit …, SSH access …, Start connection test, Start maintenance … (if
     that action exists today), and Delete host … (destructive). Every
     action keeps today's permission gating (`useHostActions`).
   - The capability switches move into the Edit dialog (HostDialog already
     has managed/monitored) — no switches in the header.
2. **Tabs** below the header, the active tab in the URL (`?tab=`), default
   `overview`: Overview · Checks (count) · Alerts (open count, highlighted
   when > 0) · Runs · Inventory & facts · Connection. Tabs that depend on a
   capability that is off show the existing "Monitoring is off" / "Management
   is off" hint inside the tab instead of disappearing.
3. **Overview** (monitored host): key metrics (whatever HostStateSection
   already shows — do not invent metrics), open alerts (with Acknowledge as
   today), last 3 runs with "All runs" switching to the Runs tab, a
   Connection summary card (SSH user@address:port, route, last connection
   test result, "Change …" opens the SSH dialog), a Checks summary (counts
   per state, "All N" switching to the Checks tab). An unmonitored host's
   overview shows the monitoring-off hint plus runs and connection.
4. **Checks tab**: one row per check — name, source (this host / group /
   organization), interval, status, key value — expandable to its metrics
   (and, for http, today's assertions table). Rows with a warning/critical
   status first, expanded state not persisted. Item decisions (Ignore,
   Rules, Chart) stay available in the expanded rows. Effective rules
   ("Rules for removed items") below the table as today.
5. **Alerts tab**: open alerts and history link as today, alert routes in a
   collapsed "Who is notified?" section.
6. **Runs tab**: today's runs section. **Inventory & facts tab**: inventory
   card, then facts.
7. **Connection tab**: SSH summary card (address, port, user, credential,
   route as a chain "worker (zone) → jump host → host", where it comes from)
   with "Edit …"; connection test card (last result, "Test now"); agent card
   (as today); a short card pointing at the key snippets in the dialog.
8. **SSH dialog** ("SSH access · <host>"): address (hostname or IP), port,
   user, credential, jump host as three choices (inherit — shows what it
   resolves to now; direct; specific → select), a live route preview, the
   key snippets (authorized_keys / cloud-init / Terraform) in a collapsed
   section, a connection test row, and Save (enabled only when something
   changed) / Cancel. Saving writes the address through the host update and
   the SSH settings through the SSH route, as today, in one action; a
   failure keeps the dialog open with the error.
9. **Connection test in the dialog**: if the existing connection-test route
   can test the drafted values without saving (or can be extended without a
   migration), test the draft ("checks the values above without saving");
   otherwise the button reads "Save and test" and tests after saving.
10. **Breadcrumb** shows the host name.

## Non-goals

New metrics, new API data, changes to other pages, the host list.

## Constraints

- Keep every permission rule; unmonitored/unmanaged behaviour as described.
- Existing tests keep their intent; e2e specs that drive the host page are
  updated (hosts, agent, connection test, checks, alerts specs).
- en + de copy (German "du"); public docs updated (no tech stack names).
