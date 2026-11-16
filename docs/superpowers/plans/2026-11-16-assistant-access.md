# Assistant access (MCP endpoint) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** An MCP endpoint in the manager that an assistant token (areas, per-org switch, endpoint-only) can use to set up the inventory, repositories, playbooks and job templates, read jobs and logs, and *request* runs that a person approves in the GUI against a fingerprint of what they saw.

**Architecture:** Assistant tokens are `api_tokens` rows of kind `assistant` with areas; `requireOrg` narrows by areas and `bearerAuthMiddleware` refuses them unless the request carries the endpoint's context marker. `internal/mcp` serves stateless Streamable HTTP at `/api/mcp` and dispatches each tool to one fixed REST route in-process through the same router, so authorization, validation, masking and audit stay the REST layer's. Run requests are a new service (`internal/runrequests`) whose approval recomputes a run picture through an app port and enqueues through the existing jobs paths.

**Tech Stack:** Go (huma, chi, sqlc, pgx), `github.com/modelcontextprotocol/go-sdk` v1.8.0, PostgreSQL, React/TypeScript SPA (openapi-fetch, Vitest).

**Spec:** `docs/superpowers/specs/2026-11-16-assistant-access-design.md`

## Global Constraints

- Assistant tokens are refused by every REST route (403 "assistant tokens work only at the assistant endpoint"); only a request carrying the endpoint's context marker may use one.
- The assistant access switch is off by default (absent row = off); writing it needs an interactive session and `org.settings.write`.
- Areas never widen the role: effective role = `tenancy.Lower(membership, token role)`, then the permission must also be in one of the token's areas. `jobs.run` is in no area.
- Run requests expire 1 hour after creation; approve/reject need an interactive session and `jobs.run`; approve requires the fingerprint the detail returned.
- Never a secret value in a tool result, a log line or an error; the bearer token and tool arguments are never logged.
- Endpoint: stateless, JSON responses, request body ≤ 1 MiB, a request with an `Origin` header refused.
- Migrations additive only; take the next free numbers at implementation time (other branches add migrations; renumber on rebase if they collide). New org-owned tables get the RLS pair (`org_isolation`, `org_isolation_bypass`) like `00033_org_job_settings.sql`, a row in `internal/backup/fixture_test.go`, and sqlc regenerated with `make sqlc`.
- New dependency on its newest stable release (`go-sdk` v1.8.0 checked 2026-11-16; re-check with `go list -m -versions` when adding it).
- Repo English; GUI strings in `web/src/i18n/en` and `de`; public docs name no tech stack (MCP and Claude Code are allowed, Go/huma/Postgres are not).
- Never `go test ./...`. Per package `go test -race -timeout 900s ./internal/<pkg>/...`, `internal/api` with `-timeout 1800s`; `make test` before the final commit; `golangci-lint run` clean before every commit; SPA `npm run lint`, `npm test`, `npx tsc -b` in `web/` when it changes; regenerate the SPA client with `npm run api:gen` after API changes.
- Conventional Commits; footer `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`; commit on `feat/assistant-access` only; no push, tag, stash, branch switch; touch no Docker container you did not start (testcontainers are fine).

## Review Focus

1. A tool that returns more than its route shows a person: the credential list (only id, name, kind), host SSH settings (credential reference, never key material), template and group reads (`$secret` markers only). A sweep test seeds known secret strings and asserts no tool output contains them (Task 9).
2. A secret-typed field reached by a value through any write path — group vars, membership vars, template extra vars — including the schema arriving in the same request, and nested objects/arrays inside a vars document (Task 8).
3. Approve racing an edit: the fingerprint must change for every input of the run picture, including a repository change (no revision) and group/membership vars that the page does not list (Task 5).
4. A token whose org switch is turned off, or which is revoked or expired, between `tools/list` and `tools/call`: every call re-resolves the token and re-checks the switch, no state kept between requests (Task 6).
5. A long-running job's log read in pages: `after` must never skip or repeat an event across attempts, and the next `after` stays stable when no new events arrived (Task 9).

---

### Task 1: Token kind, areas and the REST refusal

**Files:** new `internal/tenancy/areas.go` (+ `areas_test.go`); `internal/tenancy/roles.go` (`PermJobsRequest`); migration `internal/db/migrations/NNNNN_assistant_tokens.sql` (`api_tokens.kind`, `api_tokens.areas`, CHECKs); `internal/db/queries/tokens.sql` + sqlc output; `internal/auth/token.go`, `token_store_sql.go` (+ tests), `middleware.go` (marker); `internal/api/token_handlers.go`, `authz.go`, `org_handlers.go` (`bearerAuthMiddleware`); all `Tokens.Create` callers (handlers, `apitest`); tests `internal/api/assistant_token_test.go`.

**Interfaces — Produces:**
- `tenancy.Area` (string) with constants `AreaInventoryRead = "inventory.read"`, `AreaInventoryWrite`, `AreaReposRead`, `AreaReposWrite`, `AreaTemplatesRead`, `AreaTemplatesWrite`, `AreaCredentialsNames = "credentials.names"`, `AreaJobsRead`, `AreaJobsRequest = "jobs.request"`; `tenancy.AreaPermissions(a Area) ([]Permission, bool)` (the spec's table, write includes read); `tenancy.AreasAllow(areas []Area, perm Permission) bool` (unknown area → false); `tenancy.ValidArea(a Area) bool`.
- `tenancy.PermJobsRequest Permission = "jobs.request"`, matrix minimum `RoleOperator`.
- `auth.TokenKind` with `TokenKindAPI = "api"`, `TokenKindAssistant = "assistant"`; `auth.Token` and `auth.TokenScope` gain `Kind TokenKind` and `Areas []string`.
- `auth.NewToken struct { Name, Role string; Kind TokenKind; Areas []string; ExpiresAt *time.Time }`; `(*auth.Tokens).Create(ctx, userID, orgID uuid.UUID, in NewToken) (string, Token, error)` replaces the positional form.
- `auth.WithAssistantEndpoint(ctx) context.Context` and `auth.ViaAssistantEndpoint(ctx) bool` (unexported key type; the only setter).
- `api.Scope` gains `TokenID *uuid.UUID`, `TokenKind auth.TokenKind`, `Areas []tenancy.Area` (nil for sessions and `api` tokens).

Rules: in `bearerAuthMiddleware`, after resolution, a `TokenKindAssistant` scope without `auth.ViaAssistantEndpoint` → 403 with the exact detail "assistant tokens work only at the assistant endpoint". In `requireOrg`, after the role check, an assistant scope whose areas do not allow `perm` → 403 "the token's areas do not include <perm>". Token create body gains `kind` (enum `api,assistant`, default `api`) and `areas` (required non-empty for `assistant`, forbidden for `api` → 422); an area whose permissions the capped role does not allow → 422 naming the area; list returns `kind` and `areas`; the audit summary names kind and areas.

- [ ] Failing tests: `TestAreaPermissions` (table from the spec, write implies read, `jobs.run` in no area), `TestAreasAllowUnknownAreaFailsClosed`; token store round trip of kind and areas; API `TestAssistantTokenRefusedByREST` (GET `/hosts` with an assistant token → 403 with the detail above), `TestAssistantTokenAreasNarrowPermissions` (marked request through a test-only route helper or a direct `requireOrg` internal test: `inventory.read` token → `hosts.read` ok, `repos.read` 403), `TestCreateAssistantTokenValidation` (no areas 422, areas on `api` 422, `inventory.write` on a viewer-capped token 422, created via bearer 403 as today), `TestTokenListShowsKindAndAreas`.
- [ ] Implement; PASS `internal/tenancy`, `internal/auth`, `internal/db/...`, WHOLE `internal/api`.
- [ ] Commit `feat(auth): assistant tokens with areas`.

### Task 2: Assistant access switch

**Files:** new `internal/assistant/` (`service.go`, `store_sql.go` + tests); migration `NNNNN_org_assistant_settings.sql`; `internal/db/queries/assistant.sql`; new `internal/api/assistant_settings_handlers.go` (+ test); `internal/api/api.go` (`Deps.Assistant`, `validate`, `stubDeps` in `openapi_doc.go`); `internal/api/authz.go`; `internal/app/app.go` wiring; `apitest` wiring; backup fixture row.

**Interfaces — Produces:** `assistant.Store interface { Enabled(ctx, orgID uuid.UUID) (bool, error); SetEnabled(ctx, orgID uuid.UUID, enabled bool) error }`; `assistant.NewService(store Store) *Service` with the same two methods; `api.AssistantSettings` interface (the two methods) as `Deps.Assistant`. Routes `GET /api/v1/orgs/{org}/settings/assistant` (`org.settings.read`) and `PUT` (interactive session + `org.settings.write`, audited `updateAssistantSettings`), body/DTO `{"assistant_enabled": bool}`.

Rule: `requireOrg` on a request with `auth.ViaAssistantEndpoint` checks `deps.Assistant.Enabled` and refuses 403 "assistant access is turned off for this organization" when false.

- [ ] Failing tests: store default false and round trip, RLS isolation between two orgs; API GET default, PUT by admin session, PUT by token 403, PUT by operator 403; `requireOrg` refusal for a marked request while off.
- [ ] Implement; PASS `internal/assistant`, `internal/db/...`, `internal/backup`, WHOLE `internal/api`, `internal/app`.
- [ ] Commit `feat(api): per-organization assistant access switch`.

### Task 3: Audit entries name the token

**Files:** migration `NNNNN_audit_actor_token.sql` (`audit_events.actor_token_id uuid NULL`, no FK so a revoked or deleted token's history stays); `internal/db/queries/audit.sql` + sqlc; `internal/audit/audit.go`, `store_sql.go` (+ tests); `internal/api/audit_middleware.go` (fill from `auth.Principal.TokenID`); `internal/api/audit_handlers.go` (DTO `token_name` resolved by a join on `api_tokens`, empty when none); SPA activity rows (`web/src/features/activity/`) and `web/src/i18n/{en,de}/activity.ts`.

**Interfaces — Produces:** `audit.Event.TokenID *uuid.UUID`; activity DTO field `token_name string \`json:"token_name,omitempty"\``.

- [ ] Failing tests: `TestAuditRecordsTokenID` (a bearer PATCH records the token id, a session call records nil), activity list returns `token_name`; SPA shows "via token <name>" / "über Token <name>".
- [ ] Implement; PASS `internal/audit`, WHOLE `internal/api`; SPA lint, test, `tsc -b`.
- [ ] Commit `feat(audit): record which API token made a change`.

### Task 4: Run request service and storage

**Files:** new `internal/runrequests/` (`service.go`, `picture.go`, `store_sql.go`, tests); migration `NNNNN_run_requests.sql` (columns per the spec; no fingerprint column — it is always recomputed); `internal/db/queries/run_requests.sql` + sqlc; backup fixture row; restore output names pending run requests (`internal/app/restorecmd.go` where it names enrollment tokens).

**Interfaces — Produces** (package `runrequests`):
- `type Kind string` (`KindRun = "run"`, `KindRetryFailed = "retry_failed"`); `type Status string` (`StatusPending`, `StatusApproved`, `StatusRejected`, and the computed `StatusExpired`).
- `type Request struct { ID, OrgID uuid.UUID; Kind Kind; TemplateID uuid.UUID; TemplateRevision int64; HostID, JobID *uuid.UUID; Reason string; RequestedByUser uuid.UUID; RequestedByToken *uuid.UUID; Status Status; DecidedBy *uuid.UUID; DecidedAt *time.Time; RunGroupID *uuid.UUID; ExpiresAt, CreatedAt time.Time }`.
- `type Picture struct` with exported, JSON-tagged fields for everything the spec's run picture lists, plus `VarsDigest string` (SHA-256 hex over the canonical vars of every group the targets belong to and their membership vars; secrets as their sealed ciphertext, never decrypted).
- `func Fingerprint(requestID uuid.UUID, p Picture) string` — SHA-256 hex over `requestID` and the canonical JSON of `p` (sorted keys; `encoding/json` of a struct with sorted map keys is canonical enough; hosts sorted by id).
- `type Store interface { Insert(ctx, Request) error; Get(ctx, orgID, id uuid.UUID) (Request, error); List(ctx, orgID uuid.UUID, status *Status) ([]Request, error); Decide(ctx, orgID, id uuid.UUID, from Status, to Status, by uuid.UUID, at time.Time) (bool, error); SetRunGroup(ctx, orgID, id, group uuid.UUID) error; RevertToPending(ctx, orgID, id uuid.UUID) error }`.
- Ports: `type Pictures interface { Picture(ctx, orgID uuid.UUID, r Request) (Picture, error) }`, `type Enqueuer interface { Run(ctx, orgID, templateID uuid.UUID, onlyHost *uuid.UUID, actor uuid.UUID, idempotencyKey string) (runGroup uuid.UUID, err error); RetryFailed(ctx, orgID, jobID, actor uuid.UUID) (runGroup uuid.UUID, err error) }`, `type Validator interface { ValidateRun(ctx, orgID, templateID uuid.UUID, onlyHost *uuid.UUID) (revision int64, err error); ValidateRetry(ctx, orgID, jobID uuid.UUID) (templateID uuid.UUID, revision int64, err error) }`.
- `NewService(store Store, pictures Pictures, enqueuer Enqueuer, validator Validator, clock func() time.Time, newID func() uuid.UUID) *Service` with `Create(ctx, orgID uuid.UUID, in CreateInput) (Request, error)`, `Get(ctx, orgID, id) (Request, Picture, string /*fingerprint*/, error)`, `List(ctx, orgID, status *Status) ([]Request, error)`, `Approve(ctx, orgID, id, by uuid.UUID, fingerprint string) (Request, error)`, `Reject(ctx, orgID, id, by uuid.UUID) (Request, error)`, `CreatedByToken(ctx, orgID, runGroup, tokenID uuid.UUID) (bool, error)` (for cancel).
- Sentinels: `ErrNotFound`, `ErrExpired`, `ErrDecided`, `ErrChanged`, `ErrInvalidInput`.

Rules: expiry 1 h (`const TTL = time.Hour`), computed on read (`Status()` returns `StatusExpired` for pending past `ExpiresAt`). `Approve`: refuse expired/decided; recompute picture and fingerprint, mismatch → `ErrChanged`; `Decide(pending→approved)` (false → `ErrDecided`); enqueue with idempotency key `"run-request:" + id` (retry has no key: on its error `RevertToPending`); on enqueue error `RevertToPending` and return the error; then `SetRunGroup`. (This replaces the spec's "same transaction" with an idempotent two-step; a crash between the steps leaves an approved request whose repeat approval is refused, which the person sees as approved-without-run — acceptable, and logged.)

- [ ] Failing unit tests with fakes and a fixed clock: create validates through `Validator`; expiry at exactly 1 h; approve success; approve with a stale fingerprint `ErrChanged`; double approve `ErrDecided`; enqueue failure reverts; reject; `Fingerprint` changes for each `Picture` field and is stable under host order. Store tests through `testdb` incl. RLS isolation.
- [ ] Implement; PASS `internal/runrequests`, `internal/db/...`, `internal/backup`, `internal/app`.
- [ ] Commit `feat(runrequests): run requests that a person approves`.

### Task 5: Run request routes, the picture port and cancel

**Files:** new `internal/app/runrequests_ports.go` (+ test) implementing `Pictures`, `Enqueuer`, `Validator` over the jobs, templates, playbooks, repos, hosts, sshconf, credentials and inventory services (`jobs.Service.RunTargets`, `EnqueueRunWith` with `IdempotencyKey`, `RetryFailed`); new `internal/api/run_request_handlers.go` (+ `run_request_handlers_test.go`); `internal/api/job_handlers.go` (cancel); `Deps.RunRequests`; app and `apitest` wiring.

**Interfaces — Consumes:** Task 4's service. **Produces** routes:
- `POST /api/v1/orgs/{org}/job-templates/{id}/run-requests` body `{host_id?, reason}` (`reason` 1–300 chars), `jobs.request`, 201, audited `requestRun`.
- `POST /api/v1/orgs/{org}/jobs/{id}/retry-requests` body `{reason}`, `jobs.request`, 201, audited `requestRetry`.
- `GET /api/v1/orgs/{org}/run-requests?status=` and `GET …/run-requests/{id}`, `jobs.read`; the detail body is `{request, picture, fingerprint, approval_path}` with `approval_path = "/o/{org}/runs/requests/{id}"`.
- `POST …/run-requests/{id}/approve` body `{fingerprint}` and `POST …/reject`, interactive session + `jobs.run`, audited `approveRunRequest` / `rejectRunRequest`; errors: expired/decided/changed → 409 with details "the request expired", "the request was already decided", "changed since you looked at it; review it again".
- Cancel: `POST /jobs/{id}/cancel` keeps `jobs.run`, and additionally accepts a scope with `jobs.request` when `RunRequests.CreatedByToken(org, job.RunGroupID, *scope.TokenID)` is true; otherwise 403 as today.

- [ ] Failing API tests: create (template not found 404, host not among targets 422, reason missing 422), list/detail with picture and fingerprint, approve by session ok and job enqueued with the approver as actor, approve by token 403, approve by viewer 403, approve after expiry 409 (inject the clock through an `apitest` option; add `WithRunRequestClock(func() time.Time)` if none fits), and **one sub-test per picture input** changing between detail and approve → 409: template edit, playbook path, repository URL/branch/subpath, a host's SSH settings, a target added to the group, credential reference, vault identity, extra vars, group vars, membership vars (Review Focus 3); reject; cancel own vs foreign run group; audit coverage test lists the new routes.
- [ ] Implement; PASS `internal/app`, WHOLE `internal/api`; regenerate the OpenAPI client (`npm run api:gen`).
- [ ] Commit `feat(api): run request routes with fingerprinted approval`.

### Task 6: MCP endpoint skeleton

**Files:** `go.mod`/`go.sum` (`github.com/modelcontextprotocol/go-sdk@v1.8.0`); new `internal/mcp/` (`server.go`, `auth.go`, `dispatch.go`, `registry.go`, `result.go`, tests); `internal/api/api.go` (mount `/api/mcp` on the chi router after the huma API is built, before `NotFound`; add the path to the session-skip set if the cookie lookup would otherwise run — check `session_skip.go`); `internal/api/request_log.go` if it logs headers or bodies (it must not log `Authorization` for this path).

**Interfaces — Produces:**
- `mcp.New(router http.Handler, tokens TokenResolver, assistant SwitchReader, orgs OrgSlugs, version string) http.Handler` with `TokenResolver interface { Resolve(ctx context.Context, plain string) (auth.Principal, auth.TokenScope, error) }` (satisfied by `*auth.Tokens`), `SwitchReader` (Task 2's `Enabled`), `OrgSlugs interface { SlugOf(ctx context.Context, orgID uuid.UUID) (string, error) }`.
- `type call struct { Method, Path string; PathParams map[string]string; Query url.Values; Body any; IfMatch string }` and `func (d *dispatcher) do(ctx context.Context, c call) (status int, header http.Header, body []byte, err error)`; only paths registered in the registry are accepted (`errRouteNotAllowed`).
- `type tool struct { Name, Description string; Area tenancy.Area; ReadOnly bool; Input any /*schema struct*/; Handle func(ctx context.Context, tc toolContext, args json.RawMessage) (*sdk.CallToolResult, error) }` and `toolContext{ OrgSlug string; Scope auth.TokenScope; dispatch func(call) (...)}`; `register(tools ...tool)`.
- Problem mapping: 4xx → tool error result (`IsError: true`) with "<title>: <detail>"; 5xx → "Pulsix could not complete the request"; never echo the request.

Rules: per HTTP request, resolve the bearer (missing/invalid 401; kind `api` 403 "this endpoint needs an assistant token"; switch off 403), refuse an `Origin` header (403), cap the body at 1 MiB (`http.MaxBytesReader`, 413), stateless JSON mode of the SDK's Streamable HTTP handler, `tools/list` filtered by `tenancy.AreasAllow` of each tool's area, `initialize` name `pulsix`, version = manager version, instructions text: "Runs need a person's approval in Pulsix: request_run returns a link; give it to the person." The dispatched request carries `Authorization` from the original request, `auth.WithAssistantEndpoint`, and goes through the full router (so `bearerAuthMiddleware`, org scope, audit all run). First tool: `get_organization` (area `inventory.read`, `GET /api/v1/orgs/{org}`).

- [ ] Failing tests (`internal/mcp` with an `apitest` server, an MCP client from the SDK over `httptest`): no token 401; `api` token 403; switch off 403; `Origin` header 403; body > 1 MiB 413; `tools/list` with an `inventory.read` token contains `get_organization`, with only `jobs.read` it does not; calling a hidden tool → error; `get_organization` returns name and slug; a captured log buffer never contains the token after a call (Review Focus 4 also: revoke the token between `tools/list` and `tools/call` → 401, turn the switch off between them → 403).
- [ ] Implement; PASS `internal/mcp`, WHOLE `internal/api`; `go mod tidy` leaves no diff; add the SDK to `docs/dependency-exceptions.md` only if it cannot be on its newest release.
- [ ] Commit `feat(mcp): assistant endpoint with token, switch and area checks`.

### Task 7: Read tools

**Files:** `internal/mcp/tools_inventory.go`, `tools_repos.go`, `tools_templates.go`, `tools_credentials.go` (+ tests per file).

Tools and routes exactly as the spec's Tools table (read rows): `list_hosts`, `get_host` (host + ssh + groups as sections), `list_groups`, `get_group` (group + members), `preview_inventory`, `list_repos`, `get_repo`, `list_repo_files`, `list_playbooks`, `get_playbook`, `list_job_templates`, `get_job_template`, `list_credentials`. Results are JSON text of the route's body; `list_credentials` maps each item to `{id, name, kind}` only. List tools pass through the routes' pagination params (`cursor`, `limit`) and return the next cursor.

- [ ] Failing tests: one per tool (seeded data, expected fields present) and one refusal per tool for a token lacking its area; `list_credentials` output has exactly the keys `id`, `name`, `kind`; `get_host` contains no `public_key`/`fingerprint` keys.
- [ ] Implement; PASS `internal/mcp`.
- [ ] Commit `feat(mcp): read tools for inventory, repositories, playbooks and templates`.

### Task 8: Write tools and the secret rules

**Files:** `internal/mcp/tools_inventory_write.go`, `tools_repos_write.go`, `tools_templates_write.go`, `secrets.go` (+ `secrets_test.go`), tests per file.

Tools: `create_host`, `update_host`, `set_host_ssh`, `create_group`, `update_group`, `set_group_schema`, `set_group_vars`, `add_group_member` (with `vars`), `remove_group_member`, `create_repo`, `update_repo`, `sync_repo`, `create_playbook` (repository path only; `inline` refused), `create_job_template`, `update_job_template`. Inputs mirror the route bodies; `revision` optional (read first and send If-Match when absent; 412 → "changed meanwhile; read it again"); `update_repo` has no revision.

**Interfaces — Produces** (`secrets.go`):
- `func secretPaths(schema json.RawMessage) ([]string, error)` — dotted paths of every secret-typed field, recursing into objects and array items (use `internal/inventory/schema` for parsing; reuse its secret detection rather than re-implementing it).
- `func checkValues(paths []string, values map[string]any, keep keepForm) (map[string]any, error)` — a value at a secret path must be the tool's keep token `{"$secret":"keep"}` (translated to `{"$secret":"keep","from":"<path>"}` for groups/memberships, `{"$secret":"set"}` for templates) or absent; anything else → error "secrets are set by a person in Pulsix (<path>)".
- `func checkSchemaChange(old, new json.RawMessage) error` — refuses removing the secret type from an existing field and removing a secret field ("secret fields keep their type; change them in Pulsix (<path>)"); adding fields, including new secret fields, is allowed.

Rules: vars writes read the current schema (group: `GET` group; template: current `extra_vars_schema`) and, when the same request carries a schema, use the new one (union of both for the check is the safe choice: a path secret in either is secret). `remove_group_member` is the only removal and needs no extra confirmation (it deletes no data besides the membership vars — the tool description says so).

- [ ] Failing tests: `secrets_test.go` table (top-level, nested object, array item, schema in same request, keep translation per form, union of old and new schema, schema change refusals); one happy path per tool through the router; one area refusal per tool; If-Match conflict path; Review Focus 2 cases end to end (`set_group_vars` with a plain string under a secret field → error and nothing stored).
- [ ] Implement; PASS `internal/mcp`, `internal/inventory/schema` if a helper is exported there.
- [ ] Commit `feat(mcp): write tools that never set secrets`.

### Task 9: Job tools and the secret sweep

**Files:** `internal/mcp/tools_jobs.go`, `joblog.go` (+ tests), `sweep_test.go`.

Tools: `list_jobs`, `get_job`, `get_job_log` (`after` int64 default 0, `limit` default 200 max 1000, `host`, `failed_only`), `request_run` (`template_id`, `host_id?`, `reason`), `request_retry` (`job_id`, `reason`), `list_run_requests`, `get_run_request`, `cancel_job`. `request_*` results: request id, status, expiry, and the absolute approval link (`<manager public URL><approval_path>`; the public URL comes from the manager config already used for links in mails — find it in `internal/config`).

**Interfaces — Produces:** `func renderEvents(events []jobEvent, host string, failedOnly bool, limit int) (text string, next int64)` — one line per event `"<seq> <host> <task> <state>: <text or msg>"`, `next` = the last returned seq, or the input `after` when none.

- [ ] Failing tests: log paging over two attempts never skips or repeats (Review Focus 5), `failed_only` and `host` filters, limit cap 1000; `request_run` returns the link and creates a pending request; `cancel_job` on its own request's job ok, on another job error; area refusals; **sweep**: seed a credential private key, a vault password, a group secret field and a template secret extra var with distinct marker strings, call every registered tool with valid arguments, assert no output contains any marker (Review Focus 1).
- [ ] Implement; PASS `internal/mcp`, WHOLE `internal/api`.
- [ ] Commit `feat(mcp): job, log and run request tools`.

### Task 10: SPA

**Files:** `web/src/features/tokens/TokensPage.tsx` (+ tests, `.de` test), `web/src/features/settings/OrgGeneralPage.tsx` or a new `AssistantCard.tsx` beside `OrgMonitoringCard.tsx`, new `web/src/features/runs/RunRequestsPage.tsx`, `RunRequestDetailPage.tsx` (+ tests), router entries (`/o/:org/runs/requests`, `/o/:org/runs/requests/:id`), navigation badge with the pending count, `web/src/i18n/{en,de}/{tokens,settings,runs}.ts`.

Behaviour: token dialog gets a kind choice; for `assistant` an area grid (rows Inventory, Repositories & playbooks, Job templates, Jobs; columns read / write — Jobs: read / request runs; Credentials: names only), write ticks read; areas above the token's role are disabled with a hint. Settings card "Assistant access" with the switch and one line on what it allows (admin only, read-only for others). Run requests list (pending first, expiry countdown, reason, requested by user and token). Detail shows the whole picture (sections: template, playbook and repository, targets table, credentials and vault identities by name, extra vars with secrets masked), Approve/Reject buttons for users with `jobs.run`; Approve sends the fingerprint from the loaded detail; a 409 "changed" reloads the detail and shows "This request changed since you opened it. Review it again." / "Diese Anfrage hat sich geändert, seit du sie geöffnet hast. Bitte prüfe sie erneut."; after approve, navigate to the run.

- [ ] Failing Vitest tests for each behaviour above, en and de.
- [ ] Implement; SPA lint, test, `tsc -b`, coverage ≥ 70 %.
- [ ] Commit `feat(web): assistant tokens, assistant access switch and run requests`.

### Task 11: Documentation

`docs/upgrades.md` (the new columns and tables; stop every manager instance before the new version starts, because an older manager ignores `kind` and would accept an assistant token at the REST API); `docs/BACKLOG.md` (new row, done with a summary; follow-ups as planned rows: notifications for pending run requests, OAuth for the endpoint, approval for ordinary tokens if wanted; a note for the next audit run naming `/api/mcp` and the run request routes as new units).

noix-docs: worktree `~/_dev/.worktrees/noix-docs-assistant-access`, new branch `docs/pulsix-assistant-access` from `origin/main` (the main checkout is on another branch; do not touch it). New page `src/content/docs/pulsix/user/assistant-access.md`: what assistant access is, turning the switch on, creating an assistant token and choosing areas, approving or rejecting a run request, what the assistant can never do; registering in Claude Code. Before writing the registration section, verify with the Claude Code docs which config scopes expand `${VAR}` inside `headers` and whether `claude mcp add --transport http … --header` keeps a `${VAR}` unexpanded; document only what holds, with the token in an environment variable. Update `reference/api.md` (token kinds and areas, the run request routes), `user/team-and-roles.md` if it lists token behaviour, the changelog. Build the site and compare the page count.

- [ ] Write; build passes.
- [ ] Commit in both repos (`docs: assistant access` / `docs(pulsix): assistant access`); push nothing.

### Task 12: End-to-end check against a local Pulsix

Build the manager from the branch (`make build`), start it with a Postgres container per `deploy/compose/docker-compose.yml` (or the repo's documented dev path; use a container you start yourself and remove afterwards), complete first setup, create an org, turn assistant access on, create an assistant token with every area — the owner creates the token in the GUI and exports it as `PULSIX_ASSISTANT_TOKEN` in the shell that starts Claude Code, so it never passes through the assistant. Register the endpoint in a scratch Claude Code project config per the docs from Task 11, then: create a group with a schema and vars, a host, a repository (a public test repository), a playbook, a template; `request_run`; approve in the GUI; read the log with `get_job_log`. Record what was run and seen in the PR description; nothing in this step goes against real hosts (use a local SSH test container or the embedded worker against `localhost` only, with the owner's OK).

- [ ] Run `make test` and `golangci-lint run` on the finished branch; both pass.
- [ ] Walk through the scenario; note results.
- [ ] Final commit if anything changed (`fix(...)` per finding).
