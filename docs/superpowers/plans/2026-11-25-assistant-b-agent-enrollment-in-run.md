# Assistant gaps part B: agent enrollment inside an approved run — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`) syntax.

**Goal:** part B of the design (`docs/superpowers/specs/2026-11-25-assistant-api-gaps-design.md`, section "B. Agent enrollment inside an approved run", owner decision D2), backlog 2.131.

**Architecture:** a job-template flag (`internal/templates`, API, MCP whitelist, SPA template form); the run-request picture and fingerprint (`internal/runrequests`); permission checks where such a run is approved or started (`internal/api`, `internal/runrequests`); minting, handing over and revoking tokens at claim and at the job's end (`internal/jobs` claim/`buildPayload`, `internal/agents` token service, the job terminal path); the inventory render's per-host vars and secret list (`internal/inventory` render, `jobs/queue_sql.go`).

**Tech Stack:** Go, sqlc, Postgres, React/TypeScript.

**Spec:** the design document above (owner-approved). Part A is merged (run-request overrides, pinned commit) — its conventions apply (picture fields omitempty so older pictures keep their fingerprint; overrides untrusted).

## Global Constraints
- Go layers per `CLAUDE.md`; targeted `go test -race -timeout 900s ./internal/<pkg>/...`, `./internal/api/...` with `-timeout 1800s`, `internal/e2e` only on its own invocation, never whole-module `go test ./...`; `golangci-lint run` clean; sqlc via `PATH=$PATH:~/go/bin make sqlc`; SPA `cd web && npm run lint && npx vitest run <files> && npx tsc -b`; `npm run api:gen` when the OpenAPI changes.
- Migrations additive only, next free number; a new column or table: run `./internal/backup/...` (a new table needs a backup round-trip fixture row).
- A token never reaches a log, an event, the job record, a picture, an MCP response or the activity log in clear. Tokens are stored only as their hash (as `agents.CreateToken` does today).
- Timestamps compared in tests are truncated to microseconds. TDD: the failing test first. Deterministic tests.
- No Playwright, no Docker beyond the testdb harness; no push/tag/stash/reset/rebase/amend/branch switch, no `git checkout -p`; `git add <paths>` only (never `internal/web/dist`, never `.superpowers`, never untracked plan files); no subagents; no background processes left running.
- Conventional Commits, footer exactly `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.

## Rulings (controller's calls, in addition to the design)
- **What Pulsix sets.** The customer's role (`noixdev.infra.pulsix_agent`) reads `pulsix_agent_enroll_tokens[inventory_hostname]` first and falls back to `pulsix_agent_enroll_token`; its manager address (`pulsix_agent_manager_addr`) and server name (`pulsix_agent_server_name`) are the operator's own inventory values. Pulsix therefore sets, in the rendered inventory, for each host that gets a token: host variable `pulsix_agent_enroll_token` = the token, and host variable `pulsix_agent_enroll_tokens` = `{}` (so a stale per-host dict in group vars cannot win). Pulsix sets nothing else; the design's `pulsix_agent_manager_url`/`pulsix_agent_server_name` are dropped (the role does not use the first, and the second is the operator's). A docs page states the contract.
- **Shadowing.** Inventory host variables beat group variables, so the render's host entry wins over group/membership values of the same name (the render replaces them for that host). Template extra vars would beat host vars: a template with `agent_enrollment` on refuses (422 on save, and a blocking picture reason) extra vars — the template's own and any run-request override — named `pulsix_agent_enroll_token` or `pulsix_agent_enroll_tokens` (run-request overrides already refuse every `pulsix_` name since part A).
- **Template flag.** `job_templates.agent_enrollment boolean NOT NULL DEFAULT false` (additive). Changing it needs `agents.write` in addition to `templates.write` (so an assistant, which has no area granting `agents.write`, cannot switch it on; `create_job_template`/`update_job_template` accept the key but the REST layer refuses it without `agents.write` — keep authorization in REST). It is part of the template revision, so pending requests' fingerprints change when it changes.
- **Picture.** `agent_enrollment` (omitempty): `{hosts: [names], none_reason?}` — the targets without an agent at picture time; for a dry run `none_reason: "dry_run"` and no hosts. The list is in the fingerprint and, at approval, stored on the request and the job (`jobs.enroll_host_ids uuid[]`, nullable) — the claim compares stored values, like part A's commit.
- **Who may.** Approving a run request of such a template, starting such a template by hand (`POST …/run`, retry, the playbook page's run), and creating or resuming a schedule of it (by a person today; part C's requests later) require `agents.write` in addition to today's permissions; otherwise 403 with a message naming the enrollment. A webhook-triggered run of such a template mints nothing (the role then finds no token and its own check stops the play for those hosts with its clear message) — no refusal at trigger time.
- **Minting at claim.** In the claim transaction (where the inventory is rendered — `buildPayload`): for each host in the job's stored enroll list (or, for a manual run without a stored list, the targets without an agent) that still has no agent and is among the job's hosts, mint a host-bound single-use token in the job's zone, lifetime 24 h, name hint the host name, linked to the job (`agent_enrollments.job_id` or a join table — additive). Put it into that host's hostvars and into `inv.Secrets` (redact list). A dry run, a webhook run and a job of a template with the flag off mint nothing. A re-claim (reaper requeue) revokes the earlier claim's unused tokens of that job and mints fresh ones. If minting fails, the claim fails the job with a clear error (no partial hand-out).
- **Revoking.** When a job reaches any terminal state (succeeded, failed, cancelled, errored, withdrawn), every unused token minted for it is revoked in the same transaction as the terminal write (find the terminal paths — the job finish, the reaper's give-up, cancel). A used token is already consumed by its claim (`ClaimToken`).
- **Activity log.** At mint: "enrollment tokens minted for <n> hosts by job <id>" (host names, no token); at revoke: "revoked <n> unused enrollment tokens of job <id>".
- **Retries.** A retry of such a run mints again for its hosts that still have no agent (it is a run of the template; the same permission rule applies to whoever starts or approves it).
- **SPA.** Template form: an "Enrol agents" switch with help text (en/de) saying what Pulsix sets and that it needs the Agents permission; run-request page and the manual run confirmation show "Will enrol agents on: …"; the job page shows "Enrolment tokens: <n> minted, <m> used" (counts only).

---

## Task 1: The template flag, the picture and the permission
**Files:** migration, `internal/templates/*`, `internal/api/template_*`, `internal/mcp/tools_write.go` (whitelist), `internal/runrequests/*` (picture, fingerprint, stored list at approval), `internal/api/run_request_handlers.go` and the manual-run/retry/schedule routes (permission), SPA template form and run-request page, tests.
- [ ] Commit `feat(templates): a job template can enrol agents on the hosts it runs on`.

## Task 2: Minting, handing over and revoking tokens
**Files:** `internal/jobs/*` (claim, payload, terminal paths, reaper), `internal/agents/*` (mint for a job, revoke a job's unused tokens), `internal/inventory` render hook if needed, migration (job link, `jobs.enroll_host_ids`), activity log, job DTO/SPA counts, tests (incl. that no token appears in the job record, events, picture, activity text; the worker's redaction of the token through an existing runner redaction test).
- [ ] Commit `feat(jobs): an enrolling run hands each new host its own token and revokes the rest`.

## Task 3: Final
Whole-branch review, `make test`, lint, SPA checks, `docs/upgrades.md` (migrations, permission change for templates with the flag), user docs in the docs clone (template option, approval picture, the role contract page: which variables Pulsix sets, that the operator's inventory must provide `pulsix_agent_manager_addr`; changelog), backlog 2.131 done, merge to local main, redeploy.
