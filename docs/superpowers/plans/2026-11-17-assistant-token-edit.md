# Editing tokens, more assistant areas and an assistant e2e suite — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Owners and admins edit an existing token's name, areas and expiry. Assistant tokens gain monitoring, alert handling, schedule, activity, host tag and connection-test tools. One e2e suite drives `/api/mcp` through all of it.

**Architecture:** Tokens get a `PATCH /api/v1/orgs/{org}/tokens/{id}` route backed by `auth.Tokens.Update`, which uses a new store `Get` and `Update` (sqlc). New areas live in `internal/tenancy/areas.go`. Alert handling gets its own permission, `alerts.handle`. The new MCP tools dispatch to existing REST routes through `internal/mcp`'s `caller`. The SPA adds an edit dialog and new area rows. `internal/e2e/assistant_test.go` drives everything over real HTTP.

**Tech Stack:** Go (huma v2, sqlc, pgx, the Go MCP SDK `github.com/modelcontextprotocol/go-sdk/mcp`), Postgres testcontainers, React + TanStack Query + react-hook-form + zod, Vitest.

**Spec:** `docs/superpowers/specs/2026-11-17-assistant-token-edit-design.md` (it builds on `docs/superpowers/specs/2026-11-16-assistant-access-design.md`).

## Global Constraints

- Layers: handlers decode, call a service and encode. They never touch `gen.Queries`. Services depend on interfaces and get the clock injected.
- No migration: the existing `api_tokens` columns and the CHECK `api_tokens_kind_areas` cover Part 1.
- Editing a token requires `requireInteractiveSession` and `PermTokensManage` (owner or admin). Any bearer request gets 403.
- A token's role and kind are never editable. An expired token gets 409, and a revoked or unknown one gets 404.
- Areas must stay within `tenancy.Lower(editorRole, token.Role)`. An assistant token keeps at least one area. An `api` token has none.
- The audit summary names every changed field as `old → new`.
- The new permission `alerts.handle` has minimum role operator, the same bar `checks.write` sets today.
- The assistant never starts a playbook run, never sees a secret value, and deletes nothing new.
- Code, comments, commits and docs in English. GUI strings go through `web/src/i18n/{en,de}`. Public docs name no tech stack (MCP and Claude Code are allowed).
- Use Conventional Commits, signed, ending with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. Never push.
- Before every commit, run `make test` (or the affected groups from `scripts/test-groups.sh` with their timeouts) and `golangci-lint run`. For SPA changes, also run `cd web && npm run lint && npm test`.

## Review Focus

1. **An admin edits an owner's token.** It succeeds, the areas are capped by the lower of the two roles, and the audit names the editor. Pinned in Task 3: `TestPatchTokenAdminEditsOwnersToken`.
2. **An expiry between the read and the write.** The token expires after `Get` but before `Update`. The store's predicate refuses the write, so the result is 404, never a revived token. Pinned in Task 2: `TestSQLTokenStoreUpdateRefusesExpired`.
3. **`expires_at` with a non-UTC offset.** The value must be stored as the same instant, and the audit must print UTC. Pinned in Task 3: a case in `TestPatchTokenAuditSummary` with `+02:00`.
4. **The SPA dialog sends only changed fields.** Saving an untouched form sends `{}` and shows the "nothing to change" error, not a silent success. Pinned in Task 7: `sends only changed fields` and `shows the server's nothing-to-change error`.
5. **A narrowed token on an open MCP session.** The removed tool fails on the very next `tools/call`, before any reconnect. Pinned in Task 4: `TestRemovedAreaRefusedOnNextCall`, and again in Task 8.

---

### Task 1: Areas and the `alerts.handle` permission

**Files:**
- Modify: `internal/tenancy/roles.go` (add `PermAlertsHandle`, matrix entry `RoleOperator`)
- Modify: `internal/tenancy/areas.go` (three areas, order, perms)
- Modify: `internal/tenancy/roles_test.go`, `internal/tenancy/areas_test.go`
- Modify: `internal/api/alert_handlers.go` (the four routes `POST`/`DELETE /alerts/{id}/acknowledge` and `POST`/`DELETE /alerts/{id}/snooze`: `PermChecksWrite` → `PermAlertsHandle`)
- Modify: `internal/api/token_handlers.go` (the `Areas` doc tag lists the new areas)
- Test: `internal/api/alert_handlers_test.go`

**Interfaces:**
- Produces: `tenancy.PermAlertsHandle Permission = "alerts.handle"`; `tenancy.AreaMonitoringRead Area = "monitoring.read"`, `tenancy.AreaAlertsHandle Area = "alerts.handle"`, `tenancy.AreaActivityRead Area = "activity.read"`.
- `areaPerms`: `monitoring.read` → `{PermAgentsRead, PermChecksRead, PermMaintenanceRead}`. `alerts.handle` → the same three plus `PermAlertsHandle`. `activity.read` → `{PermAuditRead}`.
- `areaOrder` appends `AreaMonitoringRead, AreaAlertsHandle, AreaActivityRead` after `AreaJobsRequest`.

- [ ] **Step 1: Write the failing tests.** In `roles_test.go`, add rows `{RoleOperator, PermAlertsHandle, true}`, `{RoleViewer, PermAlertsHandle, false}` and `{RoleAdmin, PermAlertsHandle, true}`. In `areas_test.go`, add `TestNewAreas`, which asserts:
  - `RoleAllowsArea(RoleViewer, AreaMonitoringRead)` is true;
  - `RoleAllowsArea(RoleViewer, AreaAlertsHandle)` is false;
  - `RoleAllowsArea(RoleOperator, AreaAlertsHandle)` is true;
  - `RoleAllowsArea(RoleViewer, AreaActivityRead)` is false and `RoleAllowsArea(RoleOperator, AreaActivityRead)` is true;
  - `AreasAllow([]Area{AreaAlertsHandle}, PermChecksRead)` is true and `AreasAllow([]Area{AreaAlertsHandle}, PermChecksWrite)` is false;
  - `Areas()` ends with the three new areas in that order.

  In `alert_handlers_test.go`, add `TestAlertHandlingNeedsAlertsHandle`: a viewer gets 403 on acknowledge, and an operator gets 200 on acknowledge, unacknowledge, snooze and unsnooze. Seed the alert with `SeedOpenAlertWithDeferredNotification`.
- [ ] **Step 2: Run them and see them fail.** Run `go test ./internal/tenancy/ -run 'TestNewAreas|TestAllows'`. Expected: FAIL (undefined identifiers).
- [ ] **Step 3: Implement** the constants, the matrix entry and the area map. Switch the four alert routes to `PermAlertsHandle`.
- [ ] **Step 4: Run them and see them pass.** Run `go test ./internal/tenancy/` and `go test -race -timeout 1800s ./internal/api/ -run 'TestAlert'`. Expected: PASS.
- [ ] **Step 5: Commit.** `feat(tenancy): add monitoring, alert-handling and activity areas`

### Task 2: Token store and service `Update`

**Files:**
- Modify: `internal/db/queries/tokens.sql`, then regenerate `internal/db/gen` with `make sqlc`
- Modify: `internal/auth/token.go` (types, errors, `TokenStore`, `Get`, `Update`)
- Modify: `internal/auth/token_store_sql.go`
- Modify: every `TokenStore` fake. `grep -rn "TouchLastUsed(ctx" internal` finds them: `internal/auth/token_test.go`, `internal/auth/middleware_test.go` and `internal/api/authz_test.go`.
- Test: `internal/auth/token_test.go`, `internal/auth/token_store_sql_test.go`

**Interfaces:**
- Produces:
  ```go
  type ExpiryChange int
  const ( ExpiryKeep ExpiryChange = iota; ExpirySet; ExpiryClear )
  type TokenChange struct {
      Name      *string
      Areas     *[]string
      Expiry    ExpiryChange
      ExpiresAt *time.Time // set when Expiry == ExpirySet
  }
  var (
      ErrTokenExpired    = errors.New("an expired token cannot be changed; create a new one")
      ErrNoChange        = fmt.Errorf("%w: nothing to change", ErrInvalidInput)
      ErrAreasOnAPIToken = fmt.Errorf("%w: only assistant tokens have areas", ErrInvalidInput)
      ErrNoAreas         = fmt.Errorf("%w: an assistant token needs at least one area", ErrInvalidInput)
      ErrExpiryInPast    = fmt.Errorf("%w: the expiry must be in the future", ErrInvalidInput)
  )
  // TokenStore additions
  Get(ctx context.Context, orgID, id uuid.UUID) (Token, error)        // non-revoked, any expiry; ErrNotFound otherwise
  Update(ctx context.Context, orgID, id uuid.UUID, name string, areas []string, expiresAt *time.Time, now time.Time) (Token, error)
  // Tokens
  func (t *Tokens) Get(ctx context.Context, orgID, id uuid.UUID) (Token, error)
  func (t *Tokens) Update(ctx context.Context, orgID, id uuid.UUID, ch TokenChange) (before, after Token, err error)
  ```
- `Update` rules, in this order:
  1. `Get`; a `before` whose expiry is not after `t.now()` returns `ErrTokenExpired`.
  2. A change with `Name == nil`, `Areas == nil` and `Expiry == ExpiryKeep` returns `ErrNoChange`.
  3. `Areas != nil` on an api token returns `ErrAreasOnAPIToken`.
  4. An empty `*Areas` on an assistant token returns `ErrNoAreas`.
  5. `ExpirySet` with `ExpiresAt` not after now returns `ErrExpiryInPast`.
  6. Otherwise, compute the final name, areas and expiry, then call `store.Update` with `t.now()`.

  Area validity against the role stays in the handler (Task 3); `tenancy` imports `auth`, so `auth` cannot import `tenancy`.
- SQL:
  - `GetAPITokenForOrg :one` selects the explicit column list of `ListAPITokensForOrg`, `WHERE id = $1 AND org_id = $2 AND revoked_at IS NULL`.
  - `UpdateAPIToken :one` is `UPDATE api_tokens SET name = $3, areas = sqlc.narg(areas)::text[], expires_at = $4 WHERE id = $1 AND org_id = $2 AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at > sqlc.arg(now)::timestamptz) RETURNING` the same column list. Never return `secret_hash`.
  - Map `pgx.ErrNoRows` to `ErrNotFound`.

- [ ] **Step 1: Write the failing unit tests.** Add `TestTokensUpdate`, a table over the fake store. Cases:
  - rename;
  - replace areas;
  - set expiry;
  - clear expiry;
  - expiry in the past → `ErrExpiryInPast`;
  - areas on an api token → `ErrAreasOnAPIToken`;
  - empty areas → `ErrNoAreas`;
  - empty change → `ErrNoChange`;
  - expired token → `ErrTokenExpired`;
  - revoked or unknown token → `ErrNotFound`;
  - `before` and `after` returned with the old and new values;
  - an unchanged value still writes and returns equal `before` and `after`.
- [ ] **Step 2: Write the failing store tests.** In `token_store_sql_test.go`:
  - `TestSQLTokenStoreUpdateRoundTrip`: name, areas and expiry persist, and `ListForOrg` shows them.
  - `TestSQLTokenStoreUpdateScopedToOrg`: another org's id gives `ErrNotFound`.
  - `TestSQLTokenStoreUpdateRefusesRevoked`.
  - `TestSQLTokenStoreUpdateRefusesExpired`: insert with an expiry, then call `Update` with a `now` after it; expect `ErrNotFound` (Review Focus 2).
  - `TestSQLTokenStoreUpdateCheckHoldsShape`: nil areas on an assistant token is rejected by the CHECK.
- [ ] **Step 3: Run them and see them fail.** Run `go test -race -timeout 900s ./internal/auth/ -run 'TestTokensUpdate|TestSQLTokenStoreUpdate'`. Expected: FAIL to compile.
- [ ] **Step 4: Implement** the queries, run `make sqlc`, then write the store methods, the service methods and the fake methods.
- [ ] **Step 5: Run them and see them pass.** Run the same command. Expected: PASS.
- [ ] **Step 6: Commit.** `feat(auth): update a token's name, areas and expiry`

### Task 3: `PATCH /api/v1/orgs/{org}/tokens/{id}`

**Files:**
- Modify: `internal/api/token_handlers.go` (input type, route, summary builder, `mapTokenErr` adds `ErrTokenExpired` → 409)
- Create: `internal/api/token_update_test.go`
- Modify: `web/src/api/schema.d.ts`, regenerated with `cd web && npm run api:gen`

**Interfaces:**
- Consumes: `auth.Tokens.Get` and `auth.Tokens.Update` (Task 2), and `validateTokenAreas(kind, role, areas)` (existing).
- Produces: route `PATCH /api/v1/orgs/{org}/tokens/{id}`. Its body is `orgTokenUpdateInput.Body`:
  - `name *string` with `minLength:"1"`;
  - `areas []string` (nil means unchanged; the handler treats a present key as a change, so decode it into `*[]string`);
  - `expires_at *time.Time`;
  - `never_expires bool`.

  The response is `{ token: tokenDTO }`, and the operation name is `updateToken`. Audit: `withAudit("updateToken", "token")`.
- Handler order:
  1. `requireInteractiveSession`.
  2. `requireOrg(PermTokensManage)`.
  3. Parse the id; a bad id gets 400.
  4. `expires_at` together with `never_expires` gets 422 "expires_at and never_expires exclude each other".
  5. `Tokens.Get`, mapping its error.
  6. When areas are given, `validateTokenAreas(tok.Kind, tenancy.Lower(scope.Role, tenancy.Role(tok.Role)), areas)`.
  7. `Tokens.Update`.
  8. `setAuditObject(id, summary)`.
- Summary, built by `tokenChangeSummary(before, after auth.Token) string`:
  - Start with `changed API token <before.Name>: ` or `changed assistant token <before.Name>: `.
  - Then join with `; ` the parts that differ, in this order: `name A → B`, `areas a, b → a, b, c`, `expiry X → Y`.
  - A missing expiry prints as `never`. An expiry prints as `t.UTC().Format(time.RFC3339)`.
  - When nothing differs, the text after the colon is `no change`.

- [ ] **Step 1: Write the failing tests.** In `token_update_test.go`:
  - `TestPatchTokenOwnerChangesAreas`: an assistant token goes from `inventory.read` to `inventory.write, repos.read`, and the response and the list show the new areas.
  - `TestPatchTokenAPITokenNameAndExpiry`.
  - `TestPatchTokenAdminEditsOwnersToken` (Review Focus 1).
  - `TestPatchTokenOperatorForbidden`: 403.
  - `TestPatchTokenBearerForbidden`: an api token with `tokens.manage` (via `OrgWithTokenPerms`) gets 403, and so does an assistant token through `AssistantDo`.
  - `TestPatchTokenOtherOrg404`.
  - `TestPatchTokenRevoked404`.
  - `TestPatchTokenExpired409`.
  - `TestPatchTokenValidation`, a table expecting 422 with the message:
    - an empty body → `nothing to change`;
    - both expiry fields → `exclude each other`;
    - an expiry in the past → `in the future`;
    - areas on an api token → `only assistant tokens have areas`;
    - empty areas → `at least one area`;
    - an unknown area;
    - a duplicate area;
    - an area above a viewer-role token's role.
  - `TestPatchTokenAuditSummary`: read the org activity and assert the exact summaries:
    - `changed assistant token t: areas inventory.read → inventory.write, repos.read; expiry never → 2030-01-01T00:00:00Z`;
    - the same with input `2030-01-01T02:00:00+02:00` printing `2030-01-01T00:00:00Z` (Review Focus 3);
    - `changed API token ci: name ci → ci-2`;
    - `changed API token ci-2: no change`.
  - `TestPatchTokenTakesEffectImmediately`: through `AssistantDo`, `GET /repos` with `inventory.read` gets 403; after a PATCH adding `repos.read` the same request gets 200; after a PATCH back it gets 403 again.
- [ ] **Step 2: Run them and see them fail.** Run `go test -race -timeout 1800s ./internal/api/ -run TestPatchToken`. Expected: FAIL (405 or 404 route).
- [ ] **Step 3: Implement** the route, the summary builder and the `mapTokenErr` override. Then run `cd web && npm run api:gen`.
- [ ] **Step 4: Run them and see them pass.** Run the same command, plus `go test ./internal/api/ -run 'TestOrgTokenRoutes|TestCreateAssistantToken'`. Expected: PASS.
- [ ] **Step 5: Commit.** `feat(api): edit a token's name, areas and expiry`

### Task 4: MCP monitoring and alert-handling tools

**Files:**
- Create: `internal/mcp/tools_monitoring.go` (`monitoringTools() []toolDef`)
- Modify: `internal/mcp/registry.go` (`registry()` appends `monitoringTools()` after `jobTools()`)
- Create: `internal/mcp/tools_monitoring_test.go`
- Modify: `internal/mcp/fixture_test.go` if the fixture needs an agent, a check, an alert or a maintenance window. Reuse apitest seeds such as `SeedOpenAlertWithDeferredNotification`.

**Interfaces:**
- Consumes: `define`, `passGet`, `send`, `readTool`/`writeTool`, `hostInput`, and `pageInput.query()` from `internal/mcp`; `tenancy.AreaMonitoringRead` and `tenancy.AreaAlertsHandle` (Task 1).
- Produces the following tools (name → route). All `monitoring.read` tools are read tools.

  | Tool | Route |
  |---|---|
  | `list_agents` | `GET /agents` |
  | `get_host_agent` | `GET /hosts/{id}/agent` |
  | `list_checks` | `GET /checks` |
  | `get_check` | `GET /checks/{id}` |
  | `list_host_checks` | `GET /hosts/{id}/checks` |
  | `get_host_metrics` | `GET /hosts/{id}/metrics`, passing the route's query parameters through unchanged |
  | `list_monitoring_groups` | `GET /monitoring-groups` |
  | `get_monitoring_group` | `GET /monitoring-groups/{id}` |
  | `list_alert_rules` | `GET /rules` |
  | `get_alert_rule` | `GET /rules/{id}` |
  | `get_host_effective_rules` | `GET /hosts/{id}/effective-rules` |
  | `list_alerts` | `GET /alerts`, with the route's filters and cursor |
  | `get_alert` | `GET /alerts/{id}` |
  | `list_maintenance_windows` | `GET /maintenance-windows` |
  | `get_maintenance_window` | `GET /maintenance-windows/{id}` |

  The `alerts.handle` write tools:

  | Tool | Route |
  |---|---|
  | `acknowledge_alert` | `POST /alerts/{id}/acknowledge`, body `{note}` |
  | `unacknowledge_alert` | `DELETE /alerts/{id}/acknowledge` |
  | `snooze_alert` | `POST /alerts/{id}/snooze`, body `{until, until_change}` as the route takes them |
  | `unsnooze_alert` | `DELETE /alerts/{id}/snooze` |

  Argument names follow the existing style (`host_id`, `check_id`, `alert_id`, …). Read each route's input struct for its query parameters. No tool for `GET /agents/tokens`.

- [ ] **Step 1: Write the failing tests.**
  - `TestMonitoringToolsListedByArea`: `monitoring.read` lists all 15 read tools and none of the 4 write tools. `alerts.handle` lists all 19. `inventory.read` lists none of them.
  - `TestMonitoringReadTools`: each read tool returns the seeded object; for example `list_alerts` contains the seeded alert's id.
  - `TestAlertHandlingTools`: acknowledge, then `get_alert` shows it acknowledged, then unacknowledge; the same for snooze and unsnooze. The org activity holds the entries with the token's name.
  - `TestRemovedAreaRefusedOnNextCall` (Review Focus 5): open a session with `alerts.handle`, PATCH the token to `monitoring.read` with the owner's session client, then `call(acknowledge_alert)` on the same session returns an error.
- [ ] **Step 2: Run them and see them fail.** Run `go test -race -timeout 900s ./internal/mcp/ -run 'TestMonitoring|TestAlertHandling|TestRemovedArea'`. Expected: FAIL.
- [ ] **Step 3: Implement** `tools_monitoring.go`. If it grows past ~400 lines, split the alert write tools into `tools_alerts.go`.
- [ ] **Step 4: Run them and see them pass.** Run the same command, plus `TestEveryToolRegisters`. Expected: PASS.
- [ ] **Step 5: Commit.** `feat(mcp): add monitoring and alert-handling tools`

### Task 5: MCP schedule, activity, host-tag and connection-test tools; instructions; secret sweep

**Files:**
- Create: `internal/mcp/tools_ops.go` (`opsTools() []toolDef`), appended in `registry()`
- Modify: `internal/mcp/server.go` (`instructions`)
- Modify: `internal/mcp/sweep_test.go`
- Create: `internal/mcp/tools_ops_test.go`

**Interfaces:**
- Produces the following tools:

  | Tool | Area | Kind | Route |
  |---|---|---|---|
  | `list_schedules` | `templates.read` | read | `GET /schedules` |
  | `get_schedule` | `templates.read` | read | `GET /schedules/{id}` |
  | `list_schedule_runs` | `templates.read` | read | `GET /schedules/{id}/runs` |
  | `get_calendar` | `templates.read` | read | `GET /calendar` with the route's range parameters |
  | `list_activity` | `activity.read` | read | `GET /activity` with `actor`, `object_type`, `since` and `cursor` |
  | `set_host_tags` | `inventory.write` | write | `PUT /hosts/{id}/tags`, which replaces the manual tags; the body shape follows the route's input |
  | `start_connection_test` | `inventory.write` | write | `POST /hosts/{id}/connection-test`, returns `job_id` |
  | `get_connection_test` | `inventory.read` | read | `GET /hosts/{id}/connection-test` |

  `get_calendar` deletes the `maintenance` key from the decoded body unless `tenancy.AreasAllow(c.areas, tenancy.PermMaintenanceRead)`. If `caller` does not yet carry the token's areas, add `areas []tenancy.Area` to it in `server.go`.
- `instructions` becomes the existing text with this sentence after the first: "Playbook runs need a person's approval in Pulsix: request_run and request_retry create a request and return a link; give that link to the person and wait for their approval, then read the run with get_job_log. A connection test (start_connection_test) runs no playbook: it logs in, probes the host and pins nothing, so it needs no approval." It replaces the current "Runs need a person's approval…" sentence. Keep the secrets sentence.

- [ ] **Step 1: Write the failing tests.**
  - `TestOpsToolsListedByArea`: `templates.read` lists the 4 schedule tools; `activity.read` lists `list_activity`; `inventory.write` lists `set_host_tags`, `start_connection_test` and `get_connection_test`; `inventory.read` lists `get_connection_test` only.
  - `TestCalendarHidesMaintenanceWithoutMonitoring`: with a seeded maintenance window, `get_calendar` has no `maintenance` key with `templates.read` alone, and has the window with `templates.read` + `monitoring.read`.
  - `TestSetHostTags`: tags set, then `get_host` shows them.
  - `TestStartConnectionTest`: returns a job id; the job's kind is `connection_test`; no host key gets pinned (the pin state stays unchanged).
  - `TestListActivity`: contains an entry made earlier by the token.
  - `TestInstructionsNameTheConnectionTestException`: the initialize result's instructions contain `start_connection_test` and `Playbook runs need a person's approval`.
- [ ] **Step 2: Extend the sweep.** In `TestNoToolOutputContainsASecret`, add these markers:
  - an org secret value `sweep-org-secret-c4d2`, referenced by an HTTP check's secret parameter on a host (seed it through `Deps.OrgSecrets` and the checks service);
  - a secret membership value `sweep-member-secret-a9e7` on the fixture group's host.

  Call every new tool with arguments that reach that data.
- [ ] **Step 3: Run them and see them fail.** Run `go test -race -timeout 900s ./internal/mcp/`. Expected: FAIL.
- [ ] **Step 4: Implement** `tools_ops.go` and the instructions text.
- [ ] **Step 5: Run them and see them pass.** Run the same command. Expected: PASS.
- [ ] **Step 6: Commit.** `feat(mcp): add schedule, activity, host tag and connection test tools`

### Task 6: SPA — the `alerts.handle` permission and the new area rows

**Files:**
- Modify: `web/src/lib/roles.ts` (`UiPermission` gains `'alerts.handle'`, minimum `'operator'`), `web/src/lib/roles.test.ts`
- Modify: `web/src/features/alerts/AlertsPage.tsx`: the acknowledge and snooze controls gate on `may(role, 'alerts.handle')`; other writes on the page keep `checks.write`.
- Modify: `web/src/features/tokens/areas.ts`, `AreaGrid.tsx`, `areas.test.ts`
- Modify: `web/src/i18n/en/tokens.ts`, `web/src/i18n/de/tokens.ts`

**Interfaces:**
- `Area` gains `'monitoring.read' | 'alerts.handle' | 'activity.read'`.
- `AreaRow.key` gains `'monitoring'`. `AREA_ROWS` appends `{ key: 'monitoring', read: 'monitoring.read', write: 'alerts.handle' }`.
- `ACTIVITY_AREA: Area = 'activity.read'` renders as a single checkbox after `CREDENTIALS_AREA`.
- `MINIMUM_ROLE` maps `monitoring.read` to viewer, `alerts.handle` to operator and `activity.read` to operator.
- `t.tokens.areas` gains:
  - `rows.monitoring`: en "Monitoring & alerts", de "Monitoring & Alarme";
  - `handle`: en "handle alerts", de "Alarme bearbeiten" — the write column label for this row only;
  - `activity`: en "Activity log: read", de "Aktivitätsprotokoll: lesen";
  - `labels` for the three areas (en "Monitoring & alerts: read", "Monitoring & alerts: handle alerts", "Activity log: read", with matching de);
  - `writeHint`, extended with en: "Handling alerts means acknowledging and snoozing them." and its de translation.

- [ ] **Step 1: Write the failing tests.**
  - In `roles.test.ts`: `may('operator','alerts.handle')` is true and `may('viewer','alerts.handle')` is false.
  - In `areas.test.ts`: `areaAllowed('viewer','monitoring.read')` is true; `areaAllowed('viewer','alerts.handle')` is false; ticking `alerts.handle` also ticks `monitoring.read` (whatever helper implements write-includes-read today).
  - In `TokensPage.test.tsx`: the create dialog shows the Monitoring row and the activity checkbox; for a viewer role, `alerts.handle` and `activity.read` are disabled.
  - In `TokensPage.de.test.tsx`: the de labels.
- [ ] **Step 2: Run them and see them fail.** Run `cd web && npx vitest run src/lib/roles.test.ts src/features/tokens`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run them and see them pass.** Run the same command, plus `npm run lint`. Expected: PASS.
- [ ] **Step 5: Commit.** `feat(web): show the monitoring, alert-handling and activity areas`

### Task 7: SPA — the edit dialog

**Files:**
- Modify: `web/src/api/hooks/useTokens.ts` (`useUpdateToken`)
- Create: `web/src/features/tokens/EditTokenDialog.tsx`
- Modify: `web/src/features/tokens/TokensPage.tsx` (an "edit" action per row; disabled with a tooltip when the token has expired)
- Modify: `web/src/i18n/en/tokens.ts`, `web/src/i18n/de/tokens.ts`
- Create: `web/src/features/tokens/EditTokenDialog.test.tsx`
- Modify: `web/src/features/tokens/TokensPage.de.test.tsx`

**Interfaces:**
- Consumes: the regenerated `schema.d.ts` (Task 3) and `AreaGrid` (Task 6).
- Produces:
  - `export type UpdateTokenInput = Omit<components['schemas']['OrgTokenUpdateInputBody'], '$schema'>;`
  - `useUpdateToken(org: string)`, a mutation `({ id, body }: { id: string; body: UpdateTokenInput })` that PATCHes and invalidates `['tokens', org]`.
  - `EditTokenDialog({ org, token, open, onClose })`, where `token` is the list row type.
- Form fields:
  - name;
  - expiry: a date input plus a "never expires" checkbox;
  - areas (assistant only), via `AreaGrid` with `role={token.role}`.

  Kind and role are shown as read-only text.
- On submit, compare with the token and send only the changed keys:
  - a cleared expiry sends `never_expires: true`;
  - a new date sends `expires_at` at the end of that day in local time, converted to ISO.
- On success, for an assistant token whose areas changed, the toast is `t.tokens.edit.savedReconnect(name)`; otherwise it is `t.tokens.edit.saved(name)`. On error, show the server's `detail`.
- `t.tokens.edit` strings (en / de):
  - `action`: "edit" / "bearbeiten";
  - `title(name)`: "Edit token {name}" / "Token {name} bearbeiten";
  - `neverExpires`: "Never expires" / "Läuft nie ab";
  - `expiresLabel`: "Expires" / "Läuft ab";
  - `kindRoleFixed`: "Kind and role cannot be changed; create a new token for that." / "Art und Rolle lassen sich nicht ändern; lege dafür einen neuen Token an.";
  - `save`: "Save" / "Speichern";
  - `saved(name)`: "Token {name} saved" / "Token {name} gespeichert";
  - `savedReconnect(name)`: "Token {name} saved. Reconnect the assistant to see new tools." / "Token {name} gespeichert. Verbinde den Assistenten neu, um neue Tools zu sehen.";
  - `expiredHint`: "Expired tokens cannot be changed." / "Abgelaufene Tokens lassen sich nicht ändern.";
  - `failed`: "failed to save token" / "Token konnte nicht gespeichert werden".

- [ ] **Step 1: Write the failing tests** in `EditTokenDialog.test.tsx`, with the fetch mock the existing token tests use:
  - `sends only changed fields`: rename only gives the body `{name}`. Saving an untouched form sends `{}` (Review Focus 4).
  - `shows the server's nothing-to-change error`: a 422 with detail `nothing to change` is shown.
  - `clears the expiry`: the body is `{never_expires: true}`.
  - `edits areas of an assistant token`: the AreaGrid is preset with the token's areas; ticking `repos.read` sends `areas` with the full new list; the reconnect toast appears.
  - `api token has no area grid`.
  - `expired token: action disabled` (in `TokensPage.test.tsx`).
  - The de title and reconnect toast (in `TokensPage.de.test.tsx`).
- [ ] **Step 2: Run them and see them fail.** Run `cd web && npx vitest run src/features/tokens`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run them and see them pass.** Run `npm test`, `npm run lint` and `npx tsc -b`. Expected: PASS.
- [ ] **Step 5: Commit.** `feat(web): edit a token's name, areas and expiry`

### Task 8: The assistant end-to-end suite

**Files:**
- Create: `internal/e2e/assistant_test.go` (`TestAssistantEndToEnd` and its phases)
- Create: `internal/e2e/assistant_steps_test.go` (MCP client helpers, seeding, the expected-tools table), split further if either file passes ~400 lines

**Interfaces:**
- Consumes: `wireScenario(t, ctx, cancel, wiring{…})`, `scenario.api` (the owner's session `apiClient` with `do` and `status`), and `pinHostKey`, `createEditorPlaybook`, `createTemplateScoped`, `awaitJob` and `runnerOutput` from the existing e2e files. MCP client: `sdk.NewClient` plus `sdk.StreamableClientTransport{Endpoint: base+"/api/mcp", HTTPClient: <bearer transport>}`, the same shape as `internal/mcp/helpers_test.go`'s `connect`.
- The skip rules match `TestChainEndToEnd`: no Docker skips, and `PULSIX_E2E_REAL_ANSIBLE=1` skips.

**Phases (subtests, in order, sharing one scenario):**

1. `seed`: turn assistant access on (`PUT` the assistant settings with `approved_runs_start_follow_ups: true`). Create a template A with follow-up B on the fixture host, plus a schedule on A. Create a group with a schema holding a secret field and its value (`e2e-asst-group-secret`) and a secret membership value (`e2e-asst-member-secret`). Create an org secret (`e2e-asst-org-secret`) used by an HTTP check on the host. Create a maintenance window and one open alert. For the alert, use the path `checks_test.go` or `alertroutes_test.go` already uses to raise one.
2. `tools_list`: create one token per area and one with every area (role owner; `activity.read` and `alerts.handle` need at least operator). Assert that `tools/list` equals the expected set for each. The table `expectedTools map[tenancy.Area][]string` lives in `assistant_steps_test.go`. The all-areas list must equal the union of the table; that is what fails a new tool without e2e coverage.
3. `every_tool`: with the all-areas token, call every listed tool with valid arguments, recording each name in a `called` set. Writes are checked through REST afterwards: a created group, an updated template, set tags, an acknowledged alert, a started connection test job. At the end, `called` must equal the all-areas list.
4. `run_request`:
   - `request_run` on A returns a link and a request id;
   - the owner's session approves it with the fingerprint from `GET` of the request;
   - A's job succeeds, B starts and succeeds;
   - `get_job_log` reads both;
   - `request_retry` on a failed run of the token's own request, using the fail playbook as in `TestChainEndToEnd`, creates a request;
   - `cancel_job` on a running job of the token's own request succeeds.
5. `refusals`, each asserted with status or tool error:
   - an assistant token on `GET /api/v1/orgs/{org}/hosts` gets 403;
   - an `api` token at `/api/mcp` gets 403;
   - with the switch off, connect fails; turning it back on works;
   - a revoked token fails to connect;
   - a token created with `expires_at` 2 s ahead fails to connect after expiry (poll up to 10 s);
   - with an `inventory.read` token, `create_group` is not listed and a direct call errors;
   - `set_group_vars` with a secret field value other than `{"$secret":"keep"}` errors.
6. `secrets`: every tool output recorded in phases 3 and 4 must contain none of these values: the fixture SSH key PEM body, the vault password, `e2e-asst-org-secret`, `e2e-asst-group-secret` and `e2e-asst-member-secret`.
7. `edit`:
   - the owner PATCHes a `monitoring.read` + `alerts.handle` token down to `monitoring.read`, and `acknowledge_alert` on the open session errors;
   - PATCH adds `templates.read`, and a fresh `ListTools` on the same session (what a reconnect issues) shows `list_schedules`, and calling it works;
   - a PATCH sent with the assistant token as bearer gets 403;
   - `GET /activity` holds `changed assistant token … areas … → …`.

- [ ] **Step 1: Write the suite** with phases 1–3 and run `go test -race -timeout 1800s ./internal/e2e/ -run TestAssistantEndToEnd -v`. Expected: PASS for phases 1–3. Fix helper gaps.
- [ ] **Step 2: Add phases 4–7** and run the same command. Expected: PASS.
- [ ] **Step 3: Run the whole e2e group** with `scripts/test-groups.sh` (the `e2e` line), using its timeout. Expected: PASS, and no other scenario slows past its budget.
- [ ] **Step 4: Commit.** `test(e2e): drive the assistant endpoint end to end`

### Task 9: Docs and backlog

**Files:**
- noix-docs, `/Users/noidee/_dev/.worktrees/noix-docs-main`: create the branch `docs/pulsix-assistant-token-edit` from the local `main` (`git switch -c docs/pulsix-assistant-token-edit`), then modify `src/content/docs/pulsix/user/assistant-access.md`. If a page on ordinary API tokens exists under `src/content/docs/pulsix/`, give it the edit too (`grep -rl "API token" src/content/docs/pulsix`).
- Modify: `docs/BACKLOG.md` (row 2.124), `docs/superpowers/specs/2026-11-16-assistant-access-design.md` (a one-line pointer under "### Assistant tokens")

**Content:**
- **Changing a token:**
  - who can change one (owners and admins, signed in);
  - what can be changed (name, areas, expiry) and what cannot (role, kind, expired tokens);
  - that changes apply to the next request;
  - that removed tools stop working at once;
  - that new tools appear after reconnecting the assistant (Claude Code: `/mcp`, then reconnect).
- **The new areas:** what each allows, in plain words. Also list what existing areas gained: schedules with "Job templates: read", host tags and connection tests with "Inventory: write".
- **The approval rule:** a connection test is the one assistant action that reaches a host without approval; it runs no playbook.
- **No tech stack named** except MCP and Claude Code.
- **Backlog row 2.124:** "Edit API tokens (name, areas, expiry); assistant areas for monitoring, alert handling and the activity log; schedule, host tag and connection test tools; an end-to-end suite over the assistant endpoint | done | Spec …, plan …, branch `feat/assistant-token-edit`. End-user docs: noix-docs branch `docs/pulsix-assistant-token-edit`, <sha>."

- [ ] **Step 1: Write the noix-docs changes,** then run the docs build (`npm run build` in the noix-docs checkout, if its README names that). Expected: build succeeds.
- [ ] **Step 2: Commit noix-docs.** `docs(pulsix): edit assistant tokens and the new areas`
- [ ] **Step 3: Write the backlog row and the spec pointer, then commit Pulsix.** `docs(backlog): add 2.124 token editing and more assistant areas`
