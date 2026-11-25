# Assistant gaps part C: schedule and trigger requests, pausing after an assistant's change — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`) syntax.

**Goal:** parts C and C2 of the design (`docs/superpowers/specs/2026-11-25-assistant-api-gaps-design.md`, owner decisions D1 and D5), backlog 2.132.

**Architecture:** the run-request machinery (`internal/runrequests`: store, service, picture, approve, expiry, cap, approvals page) is generalised to request kinds `schedule` and `trigger`; approval executes the schedule/trigger service call exactly as pictured (`internal/schedules`, `internal/triggers`); new MCP tools in `internal/mcp` mapping to REST routes; a new token area `automation.request` (`internal/tenancy/areas.go`); the change hook for D5 in the template and repository update paths (`internal/templates`, `internal/repos`) when the actor is an assistant token.

**Tech Stack:** Go, sqlc, Postgres, React/TypeScript.

**Spec:** the design document above (owner-approved), sections C, C2, constraints. Parts A and B are merged: reuse their conventions (pictures stored at approval and compared, omitempty picture fields, untrusted values from assistants, `pulsix_` and Ansible names refused, `agent_enrollment` templates need `agents.write`).

## Global Constraints
- Go layers per `CLAUDE.md`; targeted `go test -race -timeout 900s ./internal/<pkg>/...`, `./internal/api/...` with `-timeout 1800s`, `internal/e2e` only on its own invocation, never whole-module `go test ./...`; `golangci-lint run` clean; sqlc via `PATH=$PATH:~/go/bin make sqlc`; SPA `cd web && npm run lint && npx vitest run <files> && npx tsc -b`; `npm run api:gen` when the OpenAPI changes.
- Migrations additive only (widening a CHECK by drop/re-add allowed), next free number; new columns/tables: run `./internal/backup/...` (a new table needs a backup fixture row).
- A trigger secret never reaches an MCP response, a picture, a log or the activity log; it is shown once to the approving person.
- Timestamps compared in tests are truncated to microseconds. TDD: the failing test first. Deterministic tests.
- No Playwright, no Docker beyond the testdb harness; no push/tag/stash/reset/rebase/amend/branch switch, no `git checkout -p`; `git add <paths>` only (never `internal/web/dist`, never `.superpowers`, never untracked plan files); no subagents; no background processes left running.
- Conventional Commits, footer exactly `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.

## Rulings (controller's calls, in addition to the design)
- **One request table.** `run_requests.kind` widens to `run`, `retry_failed`, `schedule`, `trigger` (plus `schedule_review`/`trigger_review` for C2, or a `review` flag — pick one and say why); a nullable `payload jsonb` holds what the request would create or change (the schedule body, or the trigger body without any secret) and the target id for updates; `template_id` stays the template the schedule/trigger runs (a repository-sync trigger has none: make `template_id` nullable for that kind only, with a CHECK). Expiry, the per-token cap, the approvals list, badge and pending count include every kind. The picture is kind-specific (schedule: template, name, mode/cron or simple, time zone, guards, limit, extra vars; trigger: kind, target, preset, timestamp header, allow list, on_active, mapping, rate limit, enabled; for an update: the current values next to the new ones) and fingerprinted like run pictures; approval stores what it approved and executes exactly that (refused as "changed, review again" when the target changed since the picture).
- **Schedule requests**: `create_schedule`, `update_schedule`, `resume_schedule` (MCP) → `POST …/schedule-requests` (or a kind field on the run-request route — your call, one REST route per tool as MCP requires). Extra vars in them are untrusted like run-request overrides (part A's checks: schema required, names refused, never a secret's name, inert). On approval the schedule is created/updated/resumed with the approver as actor. `pause_schedule` and `delete_schedule` act directly (existing routes; MCP tools added).
- **Trigger requests**: `create_trigger`, `update_trigger`, `rotate_trigger_secret` → trigger requests. On approval the trigger is created/updated/rotated and the approval response carries the new secret once (only to that session; the SPA shows it in the approval dialog with copy, like the existing trigger secret dialog); the request record keeps no secret. `list_triggers`, `get_trigger` (URL and settings, never a secret), `delete_trigger` and `update_trigger` with only `enabled: false` act directly. Webhook mapping targets follow the existing execution-level name rule.
- **Permissions**: area `automation.request` grants the request tools and the direct pause/delete/disable tools; reads need `templates.read`/`repos.read` as today. Approving needs an interactive session plus the permission the direct REST route needs (`templates.write`, or `repos.write` for repo-sync triggers), plus `jobs.run` for anything that will run a template. A flagged (`agent_enrollment`) template's schedules/triggers never mint (part B), so no `agents.write` is needed.
- **C2 pause after an assistant's change.** When a request made with an assistant token changes a run-relevant field — template: playbook, scope, limit, extra vars (values and schema), credentials, vault identities, `agent_enrollment`, follow-ups; repository: URL, branch, subpath, credentials, vault identities — every active schedule of that template (or of templates whose playbook comes from that repository) is paused with `paused_reason = 'assistant_change'` (additive column) and every enabled trigger that runs that template or syncs that repository is disabled with the same reason; one review request per paused item (kind review) shows the item, the change (old → new, secrets masked) and the actor; approving resumes/re-enables, rejecting leaves it paused. A person's change does nothing of the kind. The activity log records each pause. Inventory changes are excluded (design). Tests through the real store for each field class.
- **SPA**: approvals page lists all kinds with kind-specific pictures; the schedule and trigger pages show "Paused: changed by an assistant, needs review" with a link to the review request; en/de.

---

## Task 1: Requests of kind schedule (and the generalisation)
**Files:** migration, `internal/runrequests/*`, `internal/schedules/*`, `internal/api/*request*`, `internal/mcp/tools_ops.go` (or new file), `internal/tenancy/areas.go`, SPA approvals page, tests.
- [ ] Commit `feat(runrequests): an assistant asks for a schedule and a person approves it`.

## Task 2: Requests of kind trigger
**Files:** `internal/runrequests/*`, `internal/triggers/*`, `internal/api/*`, `internal/mcp/*`, SPA approval dialog with the one-time secret, tests.
- [ ] Commit `feat(runrequests): an assistant asks for a webhook trigger and only the approver sees its secret`.

## Task 3: Pausing after an assistant's change (C2)
**Files:** migration, `internal/templates/*`, `internal/repos/*`, `internal/schedules/*`, `internal/triggers/*`, `internal/runrequests/*` (review kind), SPA schedule/trigger pages, tests.
- [ ] Commit `feat(schedules): an assistant's change pauses what runs unattended until a person reviews it`.

## Task 4: Final
Whole-branch review, `make test`, lint, SPA checks, `docs/upgrades.md`, user docs in the docs clone (assistant page: schedule/trigger requests and the secret; schedules and webhook triggers pages: paused for review; changelog), backlog 2.132 done, merge to local main, redeploy.
