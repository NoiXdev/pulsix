# Assistant access (MCP endpoint) — design

Date: 2026-11-16. Status: approved by the owner on 2026-11-16.

## Intent

The owner set up Pulsix for their own infrastructure by hand: a repository with a
subpath, inventory groups and hosts that mirror an existing Ansible inventory, job
templates with a vault identity. They want an AI assistant (Claude, through Claude
Code) to do this kind of setup and to read job logs, so that the next setup is a
conversation instead of an afternoon of forms.

The assistant must never be able to do more than the owner granted it, and must
never run a playbook against real hosts without a person approving that one run in
Pulsix. Secrets (SSH keys, vault passwords, tokens) never pass through the
assistant. That has to hold on the server, not by the assistant's good behaviour:
whoever holds an assistant token, with whatever client, gets exactly the same limits.

## Goals

- An MCP endpoint built into the manager (`/api/mcp`, Streamable HTTP), so an MCP
  client such as Claude Code connects with nothing to install.
- A new kind of API token, the **assistant token**, that works **only** at that
  endpoint, carries a chosen set of **areas** (subsets of the existing
  permissions), and is refused by the REST API.
- A per-organization switch, **assistant access**, off by default, that an admin
  turns on; while it is off the endpoint refuses every assistant token of that
  organization.
- Tools to read and set up the inventory, repositories, playbooks and job
  templates, and to read jobs and their logs.
- **Run requests**: the assistant can only *request* a run of a job template (or a
  retry of a job's failed hosts). A person approves or rejects the request in the
  GUI with an interactive session; only an approval enqueues the job. Requests
  expire after one hour.
- End-user documentation in `noix-docs`: creating an assistant token, turning the
  switch on, approving runs, registering the endpoint in Claude Code.

## Non-goals

- A separate MCP binary (rejected in brainstorming, see Approaches).
- Deleting anything through the assistant. Deletes stay in the GUI.
- Anything that touches hosts directly besides an approved run: ad hoc commands,
  bootstrap, connection tests, host-key pinning.
- Reading or writing credential contents, org secrets, tokens, memberships,
  invites, worker or agent enrollment tokens, org settings, admin settings,
  monitoring, alerting, schedules and webhook triggers.
- Notifications (mail, channels) for pending run requests. The tool returns a link
  the assistant hands to the person; a notification can follow later.
- OAuth for the MCP endpoint. A bearer token is enough for a self-hosted,
  single-person setup; OAuth can follow if a client needs it.
- Making approval mandatory for ordinary API tokens, schedules or webhooks.

## Anti-goals (a failure even if it works)

- An assistant token that reaches anything through the REST API, or a route of the
  MCP layer that skips the REST layer's authorization, validation or audit.
- A run that reaches a worker without a person's approval, or an approval that
  runs something other than what the person saw.
- A tool result that contains a secret value: a private key, a password, a vault
  password, a token, an org secret, a `$secret` value of a variable.
- Logging the bearer token or tool arguments that could carry one.

## Constraints

- Layers as `CLAUDE.md` prescribes: `internal/api` stays the HTTP transport, the
  MCP endpoint is a second transport that calls the REST routes in-process (see
  Approaches), new business rules (run requests) live in a service with a `Store`
  interface, injected `Clock`.
- Migrations additive only (`docs/upgrades.md`). New org-owned tables get the
  standard row-level-security pair and are covered by backup and restore.
- Every dependency on its newest stable release: the official Go SDK
  `github.com/modelcontextprotocol/go-sdk` (v1.8.0 at the time of writing).
- Tests for every tool and every refusal; coverage gates (auth and tenancy ≥ 90 %).
- Public texts (docs site) do not name the tech stack. Naming the protocol (MCP)
  and the client (Claude Code) is the feature itself and allowed; implementation
  details (Go, huma, Postgres) are not.
- GUI strings in English and German (`web/src/i18n/*`).

## Approaches

1. **Separate binary** (`cmd/pulsix-mcp`, stdio) that calls the REST API with a
   normal token. No server change, but every limit it applies is advisory: whoever
   has the token can call the REST API directly, and the server cannot tell the
   assistant from any other client, so neither per-token areas nor a per-org
   switch could be enforced for the assistant specifically. It also has to be
   installed and kept in step with the server.
2. **Endpoint in the manager whose tools call the domain services directly.** No
   HTTP inside the process, but every tool would re-implement what the REST
   handlers do around the service call: authorization (`requireOrg`), validation,
   If-Match revisions, DTO mapping that hides secrets, audit. Two copies that drift.
3. **Endpoint in the manager whose tools dispatch to the REST routes in-process**
   (picked). A tool builds an `http.Request` for one fixed REST route, marks its
   context as coming from the assistant endpoint, and serves it through the same
   router into a response recorder. Authorization, validation, revisions, secret
   masking and audit are the REST layer's own; the MCP layer adds only the tool
   schema, an allow-list of routes, a result filter where a tool must show less
   than the route returns, and text formatting.

The owner picked an endpoint in the manager over the binary (2026-11-16) because
it lets the server enforce the switch and the areas. Approach 3 over 2 is the
builder's call: one authorization path is the safer one.

## Decisions taken with the owner (2026-11-16)

- Endpoint built into the manager, no binary.
- Assistant tokens work only at the endpoint; areas per token; a per-org switch.
- Job starts go through a request that a person approves in Pulsix (option B).
- Tool scope as listed under Tools, deletes excluded.
- An approval binds to a fingerprint of everything the approval page showed; any
  change in between refuses the approval (option 1b).
- The assistant may create and extend variable schemas (groups, template extra
  vars) and mark new fields secret, but never sends a value to a secret-typed
  field and never removes the secret type from a field.

## Builder's calls (after the builder check, for the owner to strike)

- Audit entries name the token: a nullable `actor_token_id` on `audit_events`,
  shown as the token's name in the activity views.
- `get_organization` belongs to `inventory.read`, because `GET /orgs/{org}` needs
  `hosts.read` (`internal/api/org_handlers.go`).
- Repositories have no revision, so `update_repo` sends no If-Match; the approval
  fingerprint covers a repository change between request and approval.
- The endpoint sets its own request body limit (1 MiB), since there is no shared
  body-size middleware outside huma.
- The token's areas ride in `api.Scope` (not only in `requireOrg`'s check), so the
  handlers that re-check `scope.Role` after `requireOrg` see them too.

## Design

### Assistant tokens

Later changes to tokens (editing, and the areas added since) are in `2026-11-17-assistant-token-edit-design.md`.

`api_tokens` gains `kind text NOT NULL DEFAULT 'api' CHECK (kind IN ('api',
'assistant'))` and `areas text[]` (NULL for `api` tokens, non-empty for
`assistant` tokens, enforced by a CHECK). `auth.Token` and `auth.TokenScope` carry
`Kind` and `Areas`.

**Areas** are named sets of existing permissions, defined in
`internal/tenancy/areas.go`, so the mapping lives in one place and the token stores
the owner's choice rather than a permission list that would silently grow:

| Area | Permissions |
|---|---|
| `inventory.read` | `hosts.read`, `inventory.read` |
| `inventory.write` | the above plus `hosts.write`, `inventory.write` |
| `repos.read` | `repos.read`, `playbooks.read` |
| `repos.write` | the above plus `repos.write`, `playbooks.write` |
| `templates.read` | `templates.read` |
| `templates.write` | the above plus `templates.write` |
| `credentials.names` | `credentials.read` (the endpoint only ever shows id, name, kind) |
| `jobs.read` | `jobs.read` |
| `jobs.request` | `jobs.read`, `jobs.request` (new, operator) |

An area never widens the token's role: `requireOrg` still takes the lower of the
token's role and the owner's membership, and **then** refuses a permission that
none of the token's areas contains (403, "the token's areas do not include …").
`jobs.run` is in no area, so an assistant token can never call the run, retry or
approve routes, whatever its role. The areas travel in `api.Scope`, so handlers
that check `scope.Role` again after `requireOrg` (e.g. the jump-host check in
`host_handlers.go`) can consult them as well.

**Creating** an assistant token uses the existing `POST /orgs/{org}/tokens` with
`kind` and `areas` in the body (interactive session only, as today). Areas whose
permissions the token's capped role does not allow are refused (422) rather than
kept as dead weight. The list shows kind and areas. The GUI token page gets a kind
choice and the area checkboxes (read and write per row; write implies read).

**The REST API refuses assistant tokens.** `BearerMiddleware` resolves the token
as today; `requireOrg` (and every other place that accepts a bearer principal)
refuses a token of kind `assistant` unless the request context carries the
endpoint's marker (403, "assistant tokens work only at the assistant endpoint").
The marker is a context value set by the MCP layer only; nothing in a request from
outside can set it.

### Assistant access switch

A new table `org_assistant_settings (org_id PK, enabled boolean NOT NULL DEFAULT
false, updated_at)` mirrors `org_job_settings` (absent row = off), with
`GET/PUT /orgs/{org}/settings/assistant` (read: `org.settings.read`; write:
`org.settings.write`, interactive session only, so a token cannot turn it on). The
GUI shows it on the organization settings page with a line on what it allows.

The MCP endpoint checks the switch after resolving the token and before any tool
runs; `requireOrg` checks it again for marked requests, so a tool can never run on
a switch that was turned off mid-session. Turning the switch off does not touch
pending run requests: they still need a person, who can approve or reject them.

### MCP endpoint

- `POST /api/mcp` (and whatever else the SDK's Streamable HTTP handler needs),
  mounted outside the huma API but behind the same request logging and security
  headers, with its own request body limit of 1 MiB (the router has no shared
  body-size middleware; huma limits per operation). **Stateless** mode with JSON responses: no
  server-side session, no SSE stream, so it works with several managers and with
  restarts. Tools that would stream (a job's live log) are polled instead.
- Authentication: `Authorization: Bearer pxt_…` only, an assistant token only (an
  `api` token gets 403 with a message that says which kind is needed). The token
  must resolve, its organization's switch must be on. A request with an `Origin`
  header is refused (no browser has a reason to call this endpoint; protects
  against DNS rebinding as the MCP spec asks).
- Every tool is pinned to the token's organization: tools take no `org` argument;
  the endpoint uses the token's org slug when it builds the REST path.
- The token and the tool arguments are never logged. Tool results pass through the
  REST layer's masking; the MCP layer adds a filter where it shows less (credential
  list) and refuses any variable value that tries to set a secret (see below).
- `initialize` reports the server name `pulsix`, the manager's version, and
  instructions that say runs need a person's approval and where to send them.

### Dispatch

`internal/mcp` (new package): the server setup, tool definitions and a small
`dispatcher` that takes a method, a path template, path parameters, optional
query and body, builds the request with the caller's `Authorization` header and
the marker, serves it through the manager's handler into an
`httptest.ResponseRecorder`-like writer, and returns status, headers (ETag) and
body. A REST problem (4xx) becomes a tool error with the problem's title and
detail, so the assistant can correct itself; a 5xx becomes a generic tool error.
The dispatcher only accepts routes from the tools' fixed allow-list.

### Run requests

New package `internal/runrequests` (service + `Store` + SQL store), table
`run_requests`:

`id, org_id, kind ('run' | 'retry_failed'), template_id, template_revision,
host_id (nullable, the optional one-host narrowing), job_id (for retry_failed),
reason text (the assistant's one-line why, shown to the approver), requested_by_user,
requested_by_token, status ('pending' | 'approved' | 'rejected'), decided_by,
decided_at, run_group_id (set on approval), expires_at, created_at`, with the RLS
pair. "Expired" is computed (`pending` and `now > expires_at`), so no ticker is
needed.

Routes:

- `POST /orgs/{org}/job-templates/{id}/run-requests` and
  `POST /orgs/{org}/jobs/{id}/retry-requests` — `jobs.request`. They validate as
  much as the run and retry routes would (template exists, host among its targets,
  job has failed hosts), record the template's current revision, and return the
  request with its approval link (`/runs/requests/{id}` in the SPA).
- `GET /orgs/{org}/run-requests` (filter by status) and `GET …/run-requests/{id}`
  — `jobs.read`. The detail resolves what the run would use **now**, the
  *run picture*:
  - template name and revision, limit, the one-host narrowing if any;
  - playbook (repository path or editor), repository name, URL, branch, subpath;
  - the target hosts as the run would resolve them (name, address, SSH user and
    port, jump host);
  - names and kinds of the SSH credential and the vault identities (label and
    credential name), never their contents;
  - extra vars, with secret values masked as the template read does;
  - for a retry: the job and its failed hosts.

  The detail also returns a **fingerprint**: a SHA-256 over a canonical encoding
  of exactly that run picture (and the request id). The SPA shows the picture and
  keeps the fingerprint.
- `POST …/run-requests/{id}/approve` (body: `fingerprint`) and `…/reject` —
  interactive session and `jobs.run`. Approve refuses (409) an expired or decided
  request, and recomputes the run picture inside the approval transaction: a
  fingerprint that differs from the one sent is refused ("changed since you looked
  at it; review it again"), and the SPA reloads the detail. So the person never
  approves anything other than what the page showed, whatever was edited between
  the request, the page load and the click. Group vars and membership vars are
  not part of the picture (too large to review on one page); a change to them
  still changes the fingerprint, which covers a hash of the vars of every group
  the targets belong to. On a match, approve calls the existing enqueue path
  (`EnqueueRunWith` / `RetryFailed`) with the approver as actor, in the same
  transaction that marks the request approved, and stores the run group.
  The template revision is still recorded on the request, for the audit summary.
  What a repository sync brings in after the approval (a new commit on the
  branch) is not covered, exactly as for a manual run.

Cancelling: `POST /jobs/{id}/cancel` additionally accepts `jobs.request` when the
job belongs to a run group that a run request of the **same token** created; all
other cancels still need `jobs.run`.

Audit: request created (actor: the token's user, via the token), approved or
rejected (actor: the person), plus the existing run and cancel records. Today an
audit entry only says the actor kind was `token`
(`internal/api/audit_middleware.go`), so `audit_events` gains a nullable
`actor_token_id` that the middleware fills for every bearer call (assistant and
ordinary tokens alike), and the activity views show the token's name next to the
user. Every step the assistant takes is therefore traceable to one token.

GUI: a "Run requests" view under runs (pending first, with expiry), a badge with
the pending count in the navigation, and the detail page with Approve and Reject.

### Tools

Names are snake_case. Read tools are annotated read-only; write tools are not
destructive (no deletes); `request_run` and `request_retry` say in their
description that a person must approve and that the result carries the link.

| Tool | Route(s) | Area |
|---|---|---|
| `get_organization` | `GET /orgs/{org}` (needs `hosts.read`) | `inventory.read` |
| `list_hosts`, `get_host` | `GET /hosts`, `GET /hosts/{id}`, `GET /hosts/{id}/ssh`, `GET /hosts/{id}/inventory/groups` | `inventory.read` |
| `list_groups`, `get_group` | `GET /inventory/groups`, `GET …/{id}`, `GET …/{id}/members` | `inventory.read` |
| `preview_inventory` | `GET /inventory/preview` | `inventory.read` |
| `create_host`, `update_host`, `set_host_ssh` | `POST /hosts`, `PATCH /hosts/{id}`, `PUT /hosts/{id}/ssh` | `inventory.write` |
| `create_group`, `update_group`, `set_group_schema`, `set_group_vars` | `POST /inventory/groups`, `PATCH …/{id}`, `PUT …/{id}/schema`, `PUT …/{id}/vars` | `inventory.write` |
| `add_group_member` (with membership vars), `remove_group_member` | `PUT`/`DELETE …/{id}/members/{host}` | `inventory.write` |
| `list_repos`, `get_repo`, `list_repo_files` | `GET /repos`, `GET /repos/{id}`, `GET /repos/{id}/playbooks` | `repos.read` |
| `create_repo`, `update_repo`, `sync_repo` | `POST /repos`, `PUT /repos/{id}`, `POST /repos/{id}/sync` | `repos.write` |
| `list_playbooks`, `get_playbook` | `GET /playbooks`, `GET /playbooks/{id}` | `repos.read` |
| `create_playbook` | `POST /playbooks` (from a repository path) | `repos.write` |
| `list_job_templates`, `get_job_template` | `GET /job-templates`, `GET …/{id}` | `templates.read` |
| `create_job_template`, `update_job_template` | `POST /job-templates`, `PUT …/{id}` | `templates.write` |
| `list_credentials` | `GET /credentials`, filtered to id, name, kind | `credentials.names` |
| `list_jobs`, `get_job`, `get_job_log` | `GET /jobs`, `GET /jobs/{id}`, `GET /jobs/{id}/events` | `jobs.read` |
| `request_run`, `request_retry`, `list_run_requests`, `get_run_request` | the run request routes | `jobs.request` (list/get: `jobs.read`) |
| `cancel_job` | `POST /jobs/{id}/cancel` | `jobs.request` (own requests only) |

The tool list the endpoint advertises is filtered by the token's areas, so the
assistant only sees what it may use; a call to a hidden tool is refused anyway by
`requireOrg`.

Rules inside the tools:

- **Revisions.** `update_*` and `set_*` tools read the object first when the
  caller passes no revision and send it back as If-Match; a 412 becomes "changed
  meanwhile, read it again", never an overwrite. Repositories have no revision
  (`PUT /repos/{id}` takes no If-Match), so `update_repo` overwrites; the approval
  fingerprint covers a repository change between request and approval.
- **Secrets never go in.** In the vars documents a plain value under a
  secret-typed field *sets* the secret (`internal/inventory/schema/secrets.go`),
  so the rule is about fields, not about value shapes:
  - Before every write that carries values (`set_group_vars`, `add_group_member`,
    `create/update_job_template` extra vars) the tool determines the secret-typed
    fields from the schema that will apply after the write (the stored one, or
    the one in the same request). For each such field it accepts only "keep the
    stored secret" and translates it into the route's own keep form (groups and
    memberships: `{"$secret": "keep", "from": "<path>"}`; template variables:
    `{"$secret": "set"}`), or leaves the field absent where the schema allows
    that. Any other value is refused with "secrets are set by a person in
    Pulsix".
  - Schemas (`set_group_schema`, `extra_vars_schema` on templates): the assistant
    may add fields and mark a new field secret (it stays empty; a person fills it
    in the GUI). It may not remove the secret type from an existing field or
    remove a secret field that holds a value, because either could make a stored
    secret readable or lose it; the tool compares the old and new schema and
    refuses. The REST layer's own schema validation still applies.
  - Group vars only accept keys the group's schema declares
    (`internal/inventory/schema/validate.go`), so mirroring an Ansible inventory
    means `set_group_schema` first, then `set_group_vars`; the tool descriptions
    say so.
- **Credential references.** Repositories, host SSH settings and template vault
  identities reference credentials by id; the assistant finds the id with
  `list_credentials`.
- **Job log.** `get_job_log` takes `after` (sequence) and `limit` (default 200, max
  1000) and an optional `host` and `failed_only`; it renders one line per event
  (`seq host task state` plus the stdout text or the failure message) and says the
  next `after` value. A job that is still running is read again by calling it with
  that value.
- **Errors** carry the REST problem's title and detail; refusals name the missing
  area or the switch.

### Registering in Claude Code

The docs show the user-scope registration with the token kept in an environment
variable, so it never appears in Claude Code's config or in the conversation:

```json
{ "mcpServers": { "pulsix": { "type": "http",
  "url": "https://pulsix.example.org/api/mcp",
  "headers": { "Authorization": "Bearer ${PULSIX_ASSISTANT_TOKEN}" } } } }
```

The builder verifies which config locations expand `${…}` in headers and writes
the docs accordingly (and the `claude mcp add --transport http … --header` form if
it keeps the variable unexpanded).

### Upgrades, backup, docs

- `docs/upgrades.md` gets the version's notes: the new columns and tables; stop
  every manager instance before the new version starts (an older manager ignores
  `kind` and would accept an assistant token at the REST API).
- Backup and restore cover `run_requests` and `org_assistant_settings`; restore
  output names pending run requests (they become approvable again until they
  expire).
- `docs/BACKLOG.md` gets a row for this feature; the next security audit run lists
  the endpoint and the run request routes as new units.
- `noix-docs` (branch until the feature ships): a Pulsix page "Assistant access"
  covering the switch, token creation with areas, approving run requests, and
  registering the endpoint in Claude Code.

## Testing

- `tenancy`: area → permission table, write implies read, unknown area fails
  closed.
- `auth`: token create/resolve with kind and areas; store round trip.
- `api` (through `apitest`): assistant token refused on REST routes; refused by
  `requireOrg` for a permission outside its areas; areas wider than the role
  refused at creation; token creation still interactive-only; switch routes;
  run request routes (create, list, detail, approve, reject, expiry, approve needs
  an interactive session and `jobs.run`, cancel of own versus foreign jobs); the
  fingerprint refuses an approval after each kind of change (template, playbook,
  repository URL/branch/subpath, target hosts, host SSH settings, credential and
  vault identity references, extra vars, group and membership vars); audit
  coverage for every new mutating route and `actor_token_id` filled for bearer
  calls; RLS for the new tables.
- `runrequests`: service rules with a fake store and clock; SQL store against
  `testdb`.
- `mcp`: per tool, a happy path against a real router with seeded data and the
  refusal when its area is missing; endpoint refusals (no token, `api` token,
  switch off, `Origin` header, unknown tool, oversized body); tool list filtered by
  areas; a value for a secret-typed field refused (stored schema and schema in the
  same request), keep translated per route; removing a secret type or a filled
  secret field refused; credential list shows only id, name, kind; nothing secret in
  any result (a sweep over every tool's output for seeded secret values); token
  never in logs.
- SPA (Vitest): token page kind and areas, switch, run request list, detail,
  approve and reject, de and en.
- Manual: against the local dev setup, register the endpoint in Claude Code, set up
  a group, a host, a repository and a template, request a run, approve it in the
  GUI, read the log.

## Left to the builder

- Exact Go and SPA file layout inside the packages above; the SDK's handler wiring.
- Problem detail wording, the tool descriptions and input schemas.
- Whether `get_host` and `get_group` combine their routes into one result or return
  them as sections.
- Migration numbers at merge time (other branches add migrations in parallel).
