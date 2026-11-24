# Editing API tokens, more assistant areas, and an assistant end-to-end suite — design

Date: 2026-11-17. Status: approved by the owner on 2026-11-17. Builds on
`docs/superpowers/specs/2026-11-16-assistant-access-design.md` (backlog 2.116).

## Intent

The owner connected Claude Code to their Pulsix instance with an assistant token
and found an area missing. Today the areas, name and expiry of a token are fixed
at creation, so the only fix was a new token, a keychain swap and a reconnect.
They want to change an existing token instead.

While at it, the owner wants the assistant to reach more of Pulsix where that is
sensible (monitoring, alert handling, schedules, host tags, connection tests,
the activity log), and one end-to-end suite that drives the assistant endpoint
through every tool and every refusal, so they know the whole thing works.

## Goals

1. **Edit a token.** Owners and admins change the name, the areas (assistant
   tokens) and the expiry of an existing token, ordinary `api` tokens included
   (name and expiry). Changes apply to the very next request made with the
   token.
2. **More assistant areas and tools** (the owner picked candidates 1, 2, 4, 7, 9
   and 10 on 2026-11-17):
   - `monitoring.read` — agents, checks and their current values, alerts,
     maintenance windows, monitoring groups, alert rules (read only);
   - `alerts.handle` — acknowledge and snooze alerts, and undo either;
   - schedules, their runs and the calendar, read only, under the existing
     `templates.read`;
   - `activity.read` — the organization's activity log;
   - host tags, set through the existing `inventory.write`;
   - connection tests, started through `inventory.write`, read through
     `inventory.read`.
3. **One end-to-end suite** in `internal/e2e` that drives `/api/mcp` with real
   assistant tokens against a fully wired manager, through every tool and area,
   the run request → approval → run path with follow-ups, every refusal, and a
   token edit.

## Non-goals

- Changing a token's **role** or **kind**. A higher role is a new trust decision
  (a new token); a lower role would silently make areas dead weight. Both stay
  "revoke and create".
- Reviving an expired or revoked token.
- Editing a token with a token (any bearer credential): editing needs a signed-in
  browser session, like creating.
- Schedule writes for the assistant: a schedule starts runs without a person's
  approval, which breaks "the assistant requests runs, it never starts one".
- Maintenance window writes, notification channels, on-call and alert routes,
  credential creation, host-key pinning and bootstrap for the assistant (the
  owner declined candidates 3, 5, 6 and 8; pinning and bootstrap change trust or
  the host itself).
- A `tools/list_changed` notification. The endpoint is stateless (one MCP server
  per HTTP request), so it has no session to notify.
- A Playwright spec for the edit dialog (the owner chose the Go suite plus
  Vitest).

## Anti-goals

- An edit that makes a token more powerful than creating it fresh would have
  allowed: every rule creation applies (areas within the token's role, known and
  distinct, an assistant token keeps at least one) applies to an edit.
- A silent change: every edit leaves an audit entry naming each changed field's
  old and new value.
- An area that hands the assistant a secret, starts a run, or deletes anything
  that it could not delete before.

## Constraints

- The layering of `CLAUDE.md`: handlers decode, call `auth.Tokens`, encode; the
  rules live in the service; the store is sqlc behind `TokenStore`.
- Migrations additive only (`docs/upgrades.md`). Part 1 needs no schema change:
  `api_tokens.name`, `areas` and `expires_at` exist, and the CHECK
  `api_tokens_kind_areas` already keeps the kind/areas shape.
- The MCP endpoint resolves the token on every HTTP request and registers only
  the tools its areas unlock (`internal/mcp/server.go`), and `requireOrg` checks
  the areas again for every dispatched REST call. Both read the stored areas,
  so an edit needs no cache invalidation.
- The repository is public; the user docs name no tech stack (MCP and Claude
  Code may be named).

## Approaches (chosen by the owner on 2026-11-17)

**Edit route.** (a) one `PATCH /api/v1/orgs/{org}/tokens/{id}` with optional
fields — **chosen**: one validation, one audit entry with every change; (b) one
route per field — finer audit entries but three routes and dialogs for no gain.

**Permissions behind the new areas.** (a) reuse existing permissions everywhere,
so `alerts.handle` would map to `checks.write`, which also covers editing checks,
monitoring groups and rules — harmless while no tool reaches those routes, but
any later tool under `checks.write` would unlock for every `alerts.handle`
token; (b) a new narrow permission `alerts.handle` for the four alert-handling
routes, existing permissions for the rest — **chosen**.

**End-to-end suite.** (a) a Go test in `internal/e2e` over real HTTP with the
MCP client, the existing SSH and git fixtures and the fake runner, in the CI
`e2e` group, with the GUI covered by Vitest — **chosen**; (b) additionally a
Playwright spec for the edit dialog — more GUI confidence, slower, another stack.

## Part 1 — editing a token

### API

`PATCH /api/v1/orgs/{org}/tokens/{id}`, body (every field optional):

| Field | Meaning |
|---|---|
| `name` | new name, at least one character (the create route's rule) |
| `areas` | assistant tokens only: the complete new area list (replaces the old one) |
| `expires_at` | a new expiry, strictly in the future |
| `never_expires` | `true` removes the expiry |

The response is the token's metadata (`tokenDTO`, never a secret).

Rules, in this order. The handler validates the request body (the
`expires_at` / `never_expires` exclusion, the areas' shape and their fit to
the role) before it looks the token up, so an expired token with invalid areas
answers 422, not 409; the service then applies rules 2 to 5 in its own order:

1. `requireInteractiveSession`, then `requireOrg(…, PermTokensManage)` — owner or
   admin, signed-in browser session, MFA complete. A bearer request gets 403
   whatever its scope, exactly like create.
2. Unknown id, another org's token, or a revoked token: 404.
3. An expired token: 409 ("an expired token cannot be changed; create a new
   one"). The read and the conditional update use the same instant, so a token
   that merely runs out in between is still edited; a concurrent edit that moved
   its expiry into the past in between answers 409 as well: after a zero-row
   update the service re-reads the row to tell an expired token from a gone one
   (revoked: 404).
4. An empty body: 422 ("nothing to change"). An empty or whitespace-only
   `name` is 422 too; the service refuses it itself (`ErrEmptyName`), the
   handler's `minLength` is only the first line.
5. `expires_at` together with `never_expires: true`: 422. `expires_at` not after
   now: 422. `never_expires: false` on its own means nothing and is refused as
   "nothing to change" when no other field is given.
6. `areas` on an `api` token: 422 ("only assistant tokens have areas").
   `areas` on an assistant token goes through `validateTokenAreas` with the
   token's **stored** role capped by the editor's own role
   (`tenancy.Lower(scope.Role, token.Role)`): at least one area, known,
   distinct, each covered by that role. Today every area needs at most
   operator, so the cap never bites an owner or admin; it keeps the rule
   "an editor cannot grant what they could not create" true if a future area
   needs more.
7. Narrowing is the same call: removing areas, shortening the expiry or
   renaming has no extra rule.

A PATCH whose values equal the stored ones succeeds and is audited as
"no change". Two admins editing the same token at once: last write wins, and
both edits are audited (tokens carry no revision; the cost of a lost edit is
re-applying it, and both entries show what happened). *(Builder's call, see
"Calls made".)*

### Service and store

- `auth.Tokens.Update(ctx, orgID, id, TokenChange) (before, after Token, err)`.
  `TokenChange` holds `Name *string`, `Areas *[]string`, and an expiry change
  (`keep`, `set(t)` or `clear`). The service applies the shape rules that need
  no HTTP (kind vs areas, expiry in the future against the injected clock,
  nothing to change) and returns sentinel errors (`ErrNotFound`,
  `ErrTokenExpired`, `ErrNoChange`, `ErrAreasOnAPIToken`, `ErrExpiryInPast`,
  `ErrEmptyName`)
  that the handler maps. Area validity against the role stays in the handler's
  `validateTokenAreas`, since it needs the editor's scope (or moves to a
  shared helper — builder's call).
- `TokenStore` gains `Get(ctx, orgID, id)` (non-revoked, any expiry, so the
  service can tell 404 from 409) and `Update(ctx, orgID, id, change, now)`
  that writes in one statement, scoped to `org_id`, `revoked_at IS NULL` and
  `expires_at IS NULL OR expires_at > now`, and returns `ErrNotFound` when no
  row matched (a race with a revoke or an expiry). The CHECK constraint is the
  last line for the kind/areas shape.
- New sqlc queries in `internal/db/queries/tokens.sql`; the store runs on the
  org-scoped `db.DB` (row-level security), like every token query.

### Audit

`withAudit("updateToken", "token")`, object id the token id. The summary names
each changed field with its old and new value, for example:

`changed assistant token claude-laptop: areas inventory.read, repos.read → inventory.read, inventory.write, repos.read; expiry 2026-12-01 → never`

`changed API token ci-old: name ci-old → ci; expiry never → 2027-01-01T00:00:00Z`

The head names the token by its name before the edit, so a rename shows both
the old and the new name; an edit without a rename names the token once.

Expiries are written as UTC RFC 3339 dates (date only when the time is
midnight UTC — builder's call on the exact format; it must be stable for
tests).

### Effect on a connected assistant

Every request re-resolves the token, so:

- a removed area takes effect on the next tool call — the tool is no longer
  registered for that request, and `requireOrg` refuses its REST route;
- an added area's tools appear in the next `tools/list`. MCP clients usually
  list tools once per connection, so the user reconnects the client (Claude
  Code: `/mcp` → reconnect) to see them. No new token and no keychain change.

The user docs say exactly this.

### GUI

`web/src/features/tokens/TokensPage.tsx`: each row gets an "Edit" action next to
"Revoke" (owner and admin only, like the page itself). It opens a dialog with
the name, the expiry (date, or "never expires") and, for assistant tokens, the
existing `AreaGrid` preset with the stored areas and limited by the token's
role. The kind and role are shown, not editable. Saving calls the PATCH through
a new `useUpdateToken` hook in `web/src/api/hooks/useTokens.ts` and refreshes
the list. For an assistant token, a success toast reminds the user to reconnect
the assistant to see new tools. Expired tokens show the action disabled with a
tooltip. All strings through `web/src/i18n/{en,de}`.

## Part 2 — more areas and tools

### Areas and permissions

`internal/tenancy/areas.go`:

| Area | Permissions | Lowest role |
|---|---|---|
| `monitoring.read` (new) | `agents.read`, `checks.read`, `maintenance.read` | viewer |
| `alerts.handle` (new, includes `monitoring.read`) | the above + `alerts.handle` | operator |
| `activity.read` (new) | `audit.read` | operator |
| `templates.read` (existing) | unchanged: `templates.read` | viewer |
| `inventory.write` (existing) | unchanged | operator |

`internal/tenancy/roles.go` gains `PermAlertsHandle = "alerts.handle"` with
minimum role operator (the bar `checks.write` sets today, so nothing changes for
people). The four alert-handling routes (`POST`/`DELETE
/alerts/{id}/acknowledge`, `POST`/`DELETE /alerts/{id}/snooze`) check
`PermAlertsHandle` instead of `PermChecksWrite`. The role matrix test and the
SPA's role table follow.

New areas widen no existing token: none of today's areas holds their
permissions. Two widenings of existing tokens are deliberate and the owner
accepted them: tokens with `templates.read` gain the schedule read tools, and
tokens with `inventory.write` gain `set_host_tags` and `start_connection_test`
(and `inventory.read` tokens `get_connection_test`). The user docs list them.

### Tools

Each tool dispatches to the REST route named, through the existing `caller`, so
validation, masking and audit stay the REST layer's.

`monitoring.read` (read tools):

| Tool | Route |
|---|---|
| `list_agents` | `GET /agents` |
| `get_host_agent` | `GET /hosts/{id}/agent` |
| `list_checks`, `get_check` | `GET /checks`, `GET /checks/{id}` |
| `list_host_checks` | `GET /hosts/{id}/checks` (effective checks and current values) |
| `get_host_metrics` | `GET /hosts/{id}/metrics` (bounded range, the route's own limits) |
| `list_monitoring_groups`, `get_monitoring_group` | `GET /monitoring-groups`, `GET /monitoring-groups/{id}` |
| `list_alert_rules`, `get_alert_rule`, `get_host_effective_rules` | `GET /rules`, `GET /rules/{id}`, `GET /hosts/{id}/effective-rules` |
| `list_alerts`, `get_alert` | `GET /alerts`, `GET /alerts/{id}` |
| `list_maintenance_windows`, `get_maintenance_window` | `GET /maintenance-windows`, `GET /maintenance-windows/{id}` |

Not offered: `GET /agents/tokens` (enrollment tokens), the agent install
command, check plugin uploads.

`alerts.handle` (write tools): `acknowledge_alert` (optional note),
`unacknowledge_alert`, `snooze_alert` (until a time or until the value changes,
as the route takes), `unsnooze_alert`.

`templates.read` (read tools): `list_schedules`, `get_schedule`,
`list_schedule_runs`, `get_calendar`. `GET /calendar` checks only
`templates.read` but its response also carries `maintenance[]`; the tool drops
that field unless the token also holds `monitoring.read`, so maintenance
windows stay a `monitoring.read` matter. Not offered: schedule notification
rules (they configure where the organization's information goes).

Names and ids, never values: some monitoring outputs carry the names of org
secrets (`secret_names` of an agent, `missing_secret` of a host check) and
channel ids (`escalation_channel_ids` of an alert). They pass through as the
REST layer returns them; they reveal no value and are what a person reading
the same page sees.

`activity.read` (read tool): `list_activity` with the route's filters (actor,
object type, since, cursor).

`inventory.write`: `set_host_tags` (`PUT /hosts/{id}/tags`, replaces the manual
tags) and `start_connection_test` (`POST /hosts/{id}/connection-test`; it logs
in and probes, it pins nothing). `inventory.read`: `get_connection_test`
(`GET /hosts/{id}/connection-test`).

`start_connection_test` is the one new write that makes a worker act on a
host without a person's approval. It runs no playbook: it logs in over SSH,
probes and records what it saw, and pins nothing. The endpoint's
`instructions` text and the user docs keep saying that **playbook runs** need
approval and name connection tests as the exception.

Every tool's output passes the existing secret rules; the secret sweep test in
`internal/mcp` grows with seeded values the new outputs could leak: an org
secret value referenced by an HTTP check's secret parameter, a secret group
variable, and a secret value on a group membership (a host's value in a
group).

### GUI

`web/src/features/tokens/areas.ts` and `AreaGrid.tsx`: a "Monitoring" row
(read `monitoring.read`, write `alerts.handle`, labelled "Handle alerts"), and
`activity.read` as a single checkbox next to `credentials.names`. Minimum roles
as in the table. i18n en and de, including the area descriptions that say what
each area lets the assistant do.

## Part 3 — the end-to-end suite

`internal/e2e/assistant_test.go`, `TestAssistantEndToEnd`, on `wireScenario`
(real manager over HTTP, Postgres testcontainer, worker, SSH host, bastion, git
server, fake runner). It skips without Docker like the other scenarios and
runs in the CI `e2e` group.

The suite:

1. Signs in as the owner (the scenario's session client), turns assistant access
   on, seeds a repository, playbooks, groups with a schema holding a secret
   field, hosts, credentials, a job template with a follow-up, a schedule, an
   agent with checks and an alert, a maintenance window, and turns on
   `approved_runs_start_follow_ups`.
2. Creates assistant tokens through the REST API and connects to `/api/mcp`
   with the Go MCP SDK client over HTTP.
3. **`tools/list` per area set**: for each single area and a few combinations,
   the listed tools equal the expected set (a table in the test). A token with
   every area lists every tool; the test fails if that list holds a tool the
   suite does not call, so a new tool cannot land untested.
4. **Every tool**: each read tool returns the seeded objects; each write tool
   changes them and the change is visible through REST.
5. **Run request → approval → run**: `request_run` returns a link; the owner's
   session approves it with the fingerprint; the run executes on the fixture
   host and succeeds; its follow-up starts; `get_job_log` reads both.
   `request_retry` and `cancel_job` on a run of the token's own request.
6. **Refusals**: an assistant token on a REST route (403); an `api` token at
   `/api/mcp` (403); the switch off (403, and on again); a revoked token; an
   expired token (created with an expiry seconds ahead, or the scenario's fake
   clock if it drives token expiry — builder's call); a tool outside the
   token's areas (not listed, and a direct call fails); a write tool with a
   `$secret` value other than `keep` (refused).
7. **Secrets**: every tool output of the run is swept for the seeded secret
   values (the SSH key, the vault password, an org secret behind an HTTP
   check, a secret group variable, a secret membership value); none appears.
8. **Edit**: the owner PATCHes the token to drop an area — the next call of
   that area's tool fails on the open connection; then adds an area — a fresh
   `tools/list` (what a reconnect does) shows its tools, and they work. A PATCH
   with the assistant token itself is refused (403). The activity log holds the
   edit with old and new areas.

Each step is a named subtest helper in its own file if `assistant_test.go`
grows past ~400 lines.

## Tests per layer

- `tenancy`: the new areas' permissions and roles; `PermAlertsHandle` in the
  matrix.
- `auth` (unit, fake store): `Update` table — rename, areas set and replaced,
  expiry set, cleared, in the past, kind/areas mismatch, nothing to change,
  expired, revoked, not found. Store integration: the update query's scoping
  (org, revoked, expired race) and round trip.
- `api` (apitest): PATCH as owner and admin; operator 403; bearer 403
  (assistant and api tokens with `tokens.manage`); 404 for another org's token;
  409 expired; 422 cases; areas beyond the role; audit summary; the alert
  routes with the new permission (operator yes, viewer no, unchanged for
  people).
- `mcp`: each new tool's area gating and dispatch; the secret sweep; a removed
  area refused on the next call.
- SPA (Vitest): the edit dialog for both kinds, expiry "never", disabled for
  expired tokens, the PATCH body, the reconnect toast, de strings; the new area
  rows and their role limits.
- E2E: Part 3.

## Documentation

- noix-docs `src/content/docs/pulsix/user/assistant-access.md`, on a feature
  branch `docs/pulsix-assistant-token-edit` cut from the local `main` in
  `/Users/noidee/_dev/.worktrees/noix-docs-main` (so it sits on the unpushed
  `2e59bc7` and `83859a0` without rewriting them; the docs rule in
  `CLAUDE.md` wants a feature branch until the feature ships): changing a token's areas, name and
  expiry; that changes apply at once and a reconnect shows new tools; the new
  areas and what each allows; the tools that existing areas gained. If the
  ordinary API token page describes tokens, it gets the edit too.
- Backlog: a new row 2.124 in `docs/BACKLOG.md`.
- The 2026-11-16 spec gets a one-line pointer to this one under "Assistant
  tokens".

## Left to the builder

- The exact sqlc query shape for the update (a CTE returning old and new, or
  `Get` then `Update` in one transaction).
- Whether area validation moves from the handler into a shared helper.
- The audit summary's date format, as long as it is stable.
- Tool descriptions and argument schemas, following `internal/mcp`'s style.
- How the e2e suite produces an alert (agent push or a seeded row through the
  existing e2e helpers).

## Calls made (not discussed with the owner)

- Concurrent edits: last write wins, both audited, no revision.
- An unchanged PATCH succeeds and is audited as "no change"; an empty body is
  422.
- An expired token cannot be edited (409), consistent with "no reviving".
- The area cap uses the lower of the editor's role and the token's role.
- `get_connection_test` sits in `inventory.read`, not `inventory.write`.
- Schedule notification rules are not offered.
- `get_calendar` drops `maintenance[]` without `monitoring.read`.
- Secret names and channel ids in monitoring outputs pass through (no values).
- Connection tests are named as the exception to "runs need approval" in the
  endpoint's instructions and the docs.
- noix-docs work on a feature branch from the local `main`.
