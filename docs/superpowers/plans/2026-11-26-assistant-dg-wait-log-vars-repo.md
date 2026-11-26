# Assistant gaps parts D–G: waiting, job log search, membership variables, repository details — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`) syntax.

**Goal:** parts D, E, F and G of the design (`docs/superpowers/specs/2026-11-25-assistant-api-gaps-design.md`), backlog 2.133 (G's sync dedupe is already done in 2.130).

**Architecture:** REST handlers in `internal/api` with their services (`internal/jobs`, `internal/runrequests`, `internal/inventory`, `internal/repos`), the MCP tools in `internal/mcp` (each tool maps to one REST route through `caller.go`), the SPA host form for G's hint.

**Tech Stack:** Go, sqlc, Postgres (TimescaleDB hypertable `job_events`), React/TypeScript.

**Spec:** the design document above (owner-approved), sections D, E, F, G. Research notes: `/private/tmp/claude-501/-Users-noidee--dev-pulsix/76b69f36-5040-49f9-a80e-08a1ef63bcdb/scratchpad/api-mcp-gaps.md` sections 5–8.

## Global Constraints
- Go layers per `CLAUDE.md`; targeted `go test -race -timeout 900s ./internal/<pkg>/...`, `./internal/api/...` with `-timeout 1800s`, `internal/e2e` only on its own invocation, never whole-module `go test ./...`; `golangci-lint run` clean; sqlc via `PATH=$PATH:~/go/bin make sqlc`; SPA `cd web && npm run lint && npx vitest run <files> && npx tsc -b`; `npm run api:gen` when the OpenAPI changes.
- Migrations additive only (none expected; an index on `job_events` only if a measured need says so — the table is a hypertable, see `docs/upgrades.md` for how indexes on it are added).
- Secrets stay masked in every read (vars as `{"$secret":"set"}`); an assistant never sets a secret (`{"$secret":"keep"}` only).
- Timestamps compared in tests are truncated to microseconds. TDD: the failing test first. Deterministic tests (waits driven by a fake clock/poll hook, no sleeps as synchronisation).
- No Playwright, no Docker beyond the testdb harness; no push/tag/stash/reset/rebase/amend/branch switch, no `git checkout -p`; `git add <paths>` only (never `internal/web/dist`, never `.superpowers`, never untracked plan files); no subagents; no background processes left running.
- Conventional Commits, footer exactly `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.

## Rulings (controller's calls, in addition to the design)
- **D waiting.** REST `GET …/jobs/{id}` and `GET …/run-requests/{id}` accept `wait` (seconds, 0–25, default 0) and `unless` (the status/state the caller already knows). With `wait > 0` the handler polls the database every 500 ms (injectable interval/clock for tests) until the status differs from `unless` (or, with no `unless`, until it differs from the status at the first read), the request context ends, or the time is up, then answers as today. Authorization is checked once at the start; the loop reads through the same service call. MCP `get_job` and `get_run_request` gain `wait_seconds` and `unless` and pass them through. A job also counts as changed when its newest event sequence moves only if the caller passes `after_seq` — skip this unless trivial. Document the cap.
- **E job log.** REST `GET …/jobs/{id}/events` gains `limit` (1–1000, default 1000), `host`, `task` (case-insensitive substring), `role` (matches the `"<role> : "` prefix of `task`, case-insensitive), `state` (repeatable or comma list of `ok, changed, skipped, failed, unreachable, task, stdout, recap`), all in SQL within the job's rows, bounded below by the job's creation time for chunk pruning; `after` stays. The response carries `next_after` (the last returned seq) so a client can page. MCP `get_job_log` passes them through (keeping `failed_only` as `state=failed,unreachable` plus today's extra failed states), keeps its own rendering, and adds `changed_only: true` → a per-host summary `{host: [task names]}` of `changed` events (computed from a filtered fetch, bounded by `limit`). Escape `%`/`_` in substring matches.
- **F membership variables.** MCP `set_member_vars(group_id, host_id, vars, revision)` → the existing `PUT …/inventory/groups/{id}/members/{host}` with `If-Match: <revision>` (required in the tool), same secret rules as `set_group_vars` (`checkValues`: only declared host-scope/overridable keys; secret fields only `{"$secret":"keep"}`). MCP `get_host_vars(host_id)` → `GET …/hosts/{id}/inventory/preview` (masked); if that route does not say which group each value comes from, add a `sources` map (variable → group name or "host") to its response — additive. Area: inventory.write / inventory.read.
- **G repository details.** `get_repo` (the REST repo DTO) gains `last_synced_commit` and `last_synced_at` from `repo_var_sources` (absent when unknown); the host form (create and rename) says under the name field: "The name is the host's inventory name; playbooks and roles may derive names from it." (en/de).

---

## Task 1: Waiting (D)
**Files:** `internal/api/job_handlers.go`, `internal/api/run_request_handlers.go` (+ a small shared wait helper), `internal/mcp/tools_jobs.go`, tests.
- [ ] Commit `feat(api): wait for a job or a request to change instead of polling`.

## Task 2: Job log search (E)
**Files:** `internal/db/queries/jobs.sql` (+ sqlc), `internal/jobs/*`, `internal/api/job_handlers.go`, `internal/mcp/tools_jobs.go`, tests.
- [ ] Commit `feat(jobs): search a job's log by host, task, role and state`.

## Task 3: Membership variables and repository details (F, G)
**Files:** `internal/mcp/tools_write.go`/`tools_read.go`, `internal/api/inventory_*`/`host_*` (preview sources), `internal/api/repo_handlers.go` + repos service, SPA host form, tests.
- [ ] Commit `feat(mcp): per-host variables and repository sync details for assistants`.

## Task 4: Final
Whole-branch review, `make test`, lint, SPA checks, `docs/upgrades.md` (API additions), user docs in the docs clone (assistant page: waiting, log search, per-host variables; API reference if it lists params; changelog), backlog 2.133 done, merge to local main, redeploy.
