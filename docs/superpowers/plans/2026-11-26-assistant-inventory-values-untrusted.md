# Inventory values an assistant writes: plain text, and a review before unattended runs use them (backlog 2.136) — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`) syntax.

**Goal:** owner decision D6 in `docs/superpowers/specs/2026-11-25-assistant-api-gaps-design.md`, backlog 2.136.

**Architecture:** the inventory store (`inventory_groups.vars/schema`, `inventory_memberships.vars`, `internal/inventory`), the renderer that builds a run's inventory (`internal/inventory/render`, claim-time `buildPayload` in `internal/jobs/queue_sql.go`), the REST write paths for group vars, schemas and memberships (`internal/api/inventory_*`), and part C2's change-review machinery (`internal/assistantchange`, the schedule/trigger pause and review requests).

**Tech Stack:** Go, sqlc, Postgres, React/TypeScript.

**Spec:** the design (owner-approved) — principles, D5, D6, section C2. Parts A–D/G are merged; reuse their untrusted-value handling (`ansiblevars` inert form, `CheckUntrusted`, the execution-level name rule) and C2's pause/review flow (one open review per item, person-paused items, follow-up chains, item lock order).

## Global Constraints
- Go layers per `CLAUDE.md`; targeted `go test -race -timeout 900s ./internal/<pkg>/...`, `./internal/api/...` with `-timeout 1800s`, `internal/e2e` only on its own invocation, never whole-module `go test ./...`; `golangci-lint run` clean; sqlc via `PATH=$PATH:~/go/bin make sqlc`; SPA `cd web && npm run lint && npx vitest run <files> && npx tsc -b`; `npm run api:gen` when the OpenAPI changes.
- Migrations additive only, next free number; new columns: run `./internal/backup/...`.
- Secrets: an assistant never sets a secret (unchanged); masks unchanged.
- TDD: the failing test first. Deterministic tests.
- No Playwright, no Docker beyond the testdb harness; no push/tag/stash/reset/rebase/amend/branch switch, no `git checkout -p`; `git add <paths>` only (never `internal/web/dist`, never `.superpowers`, never untracked plan files); no subagents; no background processes left running.
- Conventional Commits, footer exactly `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.

## Rulings (controller's calls, in addition to the design)
- **Where the mark lives.** Per top-level key: `inventory_groups.assistant_keys text[]` (keys of `vars` an assistant wrote), `inventory_groups.assistant_default_keys text[]` (schema fields whose `default` an assistant set or changed), `inventory_memberships.assistant_keys text[]` — additive, default `'{}'`. A write through an assistant token adds the keys it set (or changed) to the set; a write by a person (any non-assistant caller) of the same object clears the marks of every key in its body (a group-vars or membership save replaces the whole map, so it clears all marks of that object; a schema save clears the default marks of the fields it carries). Removing a key removes its mark.
- **Plain text in the run.** The renderer emits every marked value (and a marked schema default where it fills in) in the inert form the claim already uses for untrusted run overrides (`ansiblevars` — Ansible never templates, looks up or vault-decrypts it; nested structures inert throughout). The worker needs no change. Values from people are rendered as today. The inventory preview routes show which values are assistant-written (a flag per value or a `assistant_written` list) so a person can see what is plain text.
- **Write-time checks.** An assistant's inventory write also refuses keys that Ansible or Pulsix act on (the execution-level name rule from part A: `ansible_…`, `pulsix_…`, magic names) — values only; a person may still set such names.
- **Pause and review (C2 extended).** An assistant's change to group vars, a schema default, or membership vars pauses every active schedule and disables every enabled trigger whose run would receive a changed value: the templates whose resolved hosts include an affected host (group change → every member of the group and of its descendant groups; membership change → that host), plus templates reaching those through follow-ups (C2's reverse closure), with C2's review requests (picture: group/host, key, old → new, secrets masked, actor). A person's change pauses nothing. Hosts joining or leaving groups stay excluded. Resolve "templates whose hosts include H" with the existing scope resolution (org-wide, group, host list, limit); a template whose limit cannot be decided counts as affected (fail closed).
- **SPA.** Group and host variable views mark assistant-written values ("written by an assistant — used as plain text until a person saves it", en/de); the schedule/trigger review notice from C2 covers inventory changes (picture renders them).

---

## Task 1: Marks and plain-text rendering
**Files:** migration, `internal/inventory/*` (store, service, render), `internal/api/inventory_*` (actor-aware writes, preview flag), `internal/mcp` (descriptions), SPA variable views, tests (incl. a claim-time payload test that a marked `{{ lookup('pipe','id') }}` arrives inert and a person's save makes it live).
- [ ] Commit `feat(inventory): values an assistant writes reach a run as plain text`.

## Task 2: Pause and review on an assistant's inventory change
**Files:** `internal/inventory/*`, `internal/assistantchange/*`, `internal/runrequests/*` (review picture for inventory changes), `internal/schedules`/`internal/triggers` hooks, SPA review picture, tests (group change pauses a schedule of a template targeting a member; membership change pauses only templates targeting that host; descendant groups; follow-up chain; person change pauses nothing; membership add/remove pauses nothing).
- [ ] Commit `feat(inventory): an assistant's inventory change pauses what runs unattended until reviewed`.

## Task 3: Final
Whole-branch review, `make test`, lint, SPA checks, `docs/upgrades.md`, user docs in the docs clone (assistant page, inventory page, schedules page), backlog 2.136 done, merge to local main, redeploy.
