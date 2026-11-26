# Webhook triggers wait for a quiet moment before they run (backlog 2.137) — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`) syntax.

**Goal:** backlog 2.137 (`grep -n "^| 2.137 " docs/BACKLOG.md` on the other branch; quoted here): "a tool that sends the same hook 10 times in 5 minutes should start one run, not ten."

**Owner decisions (2026-11-26, binding):**
1. The run gets the mapped values (extra vars, limit) of the **last** delivery.
2. **Quiet window + maximum wait:** every delivery extends the window; the run starts when no delivery arrived for *settle* seconds, at the latest *max wait* after the first delivery. Both per trigger; max wait defaults to 10 × settle.
3. **Run already active:** when the window ends while a run of the same trigger is active, the collected run waits until that run is done and then starts exactly once with the newest values; deliveries meanwhile join the same waiting run.

**Architecture:** `internal/triggers` (trigger config, receive path, delivery outcomes, a pending-run store), a manager ticker that starts due pending runs (`internal/app` wiring, like the schedule ticker), `internal/api` trigger DTOs/handlers, the SPA trigger form and delivery log, the trigger request picture (part C) and MCP trigger tools.

## Global Constraints
- Go layers per `CLAUDE.md`; targeted `go test -race -timeout 900s ./internal/<pkg>/...`, `./internal/api/...` with `-timeout 1800s`, `internal/e2e` only on its own invocation, never `go test ./...`; `golangci-lint run` clean; sqlc via `PATH=$PATH:~/go/bin make sqlc`; SPA `cd web && npm run lint && npx tsc -b && npx vitest run <files>`; `npm run api:gen` when the OpenAPI changes.
- Migrations additive (next free number on main; another branch may add one in parallel — if numbers collide at merge time the controller renumbers); a new table needs a backup round-trip fixture row.
- Webhook values stay untrusted/inert exactly as today; the pending store keeps them sealed or as today's delivery keeps them (never decrypted into logs).
- Several managers: the ticker claims due pending rows with `FOR UPDATE SKIP LOCKED`; a delivery and the ticker never both start a run for one window.
- TDD: failing test first; deterministic (fake clock, ticker step hook; no sleeps).
- `web/node_modules` here is a symlink — do not run `npm install`. Do NOT run `make test` (another worktree runs it). No push/tag/stash/reset/rebase/amend/branch switch, no `git checkout -p`; `git add <paths>` only (never `internal/web/dist`, `.superpowers`, untracked plan files, `docs/BACKLOG.md`); no subagents; no background processes left running.
- Conventional Commits, footer exactly `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.

## Rulings
- **Config:** `job_triggers.settle_seconds int NOT NULL DEFAULT 0` (0 = off, today's behaviour unchanged) and `max_wait_seconds int NULL` (NULL = 10 × settle); bounds settle 0 or 5…3600, max wait settle…86400. Applies to both kinds (template run and repository sync). API/MCP/SPA expose both; the part-C trigger request picture shows them.
- **Pending store:** `trigger_pending_runs(trigger_id PK, org_id, first_at, last_at, due_at, deadline_at, deliveries int, last_delivery_id, overrides (as today's enqueue input: untrusted mapped values of the last delivery, sealed if today's path seals))`. A delivery on a settling trigger (after auth, IP, rate limit and mapping succeed) upserts it: first_at kept, last_at = now, due_at = min(now + settle, deadline_at), deliveries + 1, overrides replaced; outcome `collected` (new) with the pending id in the delivery log.
- **Ticker:** every few seconds (configurable, injectable clock), claims due rows (`due_at <= now`): if a run started by this trigger is still active, leaves the row (it fires once that run ends; due stays past); otherwise enqueues one run with the stored overrides exactly as the receive path does today (same untrusted handling, same `on_active` semantics no longer apply to collected runs — they are owner decision 3), deletes the row in the same transaction, and records the run id on the collected deliveries ("collapsed into run X"). A disabled or deleted trigger drops its pending row. A trigger's settle changed to 0 fires its pending row at the next tick.
- **Delivery log & SPA:** outcome `collected` shown as "Collected — runs at <due> unless more arrive" while pending, then "Collapsed into run <link>"; the trigger page shows a pending run (deliveries count, due, deadline) with "Run now" (starts it immediately; needs the same permission as the trigger's test route) and "Discard".
- **Rate limit** still counts deliveries; collected deliveries are cheap, so no change.

---

## Task 1: Config, pending store, receive path, ticker
- [ ] Commit `feat(triggers): a trigger can wait for a quiet moment and start one run`.

## Task 2: API, MCP, SPA, request picture
- [ ] Commit `feat(web): set a trigger's settle delay and see collected deliveries`.

## Task 3: Final
Whole-branch review, `make test` (when no other run is active), lint, SPA, `docs/upgrades.md`, user docs (webhook triggers page: settle delay), backlog 2.137 done, merge, redeploy.
