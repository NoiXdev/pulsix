# Vanished metrics (3.88) and assertion row actions (3.89) — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`) syntax.

**Goal:** metrics a check stops reporting stop keeping hosts unknown and get forgotten; assertion items get their actions in the assertion table instead of duplicate generic rows.

**Spec:** `docs/superpowers/specs/2026-11-18-vanished-metrics-and-assertion-actions-design.md` (binding).

## Global Constraints
- Layers per `CLAUDE.md`; sqlc via `PATH=$PATH:~/go/bin make sqlc`, never hand-edit `internal/db/gen`; OpenAPI/SPA schema via the repo's generator (`cd web && npm run api:gen` after the Go side).
- No migration expected (additive only if one appears; a new table needs a backup fixture row).
- TDD, failing test first. Go vs Postgres timestamps: truncate to microseconds.
- Targeted runs only: `go test -race -timeout 900s ./internal/<pkg>/...`, `./internal/api/...` with `-timeout 1800s`; never whole-module `go test ./...`. `golangci-lint run` clean. SPA: `cd web && npx vitest run <files> && npx tsc -b && npx eslint .`; single quotes in TS, double in JSX attributes; en + de copy (German "du"); `src/ui` stays presentational.
- No Docker, push, tag, stash, reset, rebase, amend, branch switch; `git add <paths>` only; no background processes left running; no subagents.
- Conventional Commits, footer `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. Mark backlog rows done (Notes cell, "Done 2026-11-18: …") in the commit that finishes them (3.88 after Task 2, 3.89 after Task 3).

## Task 1: Vanished rule in evaluation and API, MetricAsked hook
Spec 3.88 items 1–4 (Go side): pure `vanished` function (in the package both callers can import without a cycle — e.g. `internal/checks`), applied in rule loading (`internal/rules/load_sql.go`, reuse the 3.55 "not asked" path so vanished tuples go to unmeasured and their alerts close as stale) and in the host-check API (`internal/api/host_check_handlers.go`: additive `vanished` field on value rows); descriptor hook `MetricAsked` with the http `body_match` case, applied wherever `ItemAsked` is (host page `stillAsked`, rule `Asks`, effective-rules `toTuples`). Tests: table tests of the rule (just inside / just past 3 intervals, fresh error never vanished, whole item stale stays unknown, not vanished when it is the item's newest), a test pinning both callers to the same function, rules test that a vanished tuple is unmeasured and leaves the host roll-up, API DTO test (`vanished` true for a stale `dns_ms` next to fresh siblings; `body_match` dropped when `body_contains` unset), regenerate OpenAPI + `web/src/api/schema.d.ts`.

## Task 2: Metric expiry
Spec 3.88 item 5: `ListExpiredMetrics` and `ForgetMetric` next to the item expiry (`internal/db/queries/checks.sql`, `internal/checks/forget.go`, `store_sql_forget.go`), run in the same hourly loop. Store integration tests: selects only a dead metric of a live item (not a wholly dead item, not a fresh one); ForgetMetric keeps siblings and the `check_items` row, deletes the metric's states/state changes, closes only that metric's alerts as forgotten; a SKIP LOCKED race test modelled on the existing forget race test; loop test that one round forgets both items and metrics. Mark 3.88 done.

## Task 3: SPA — vanished rows and assertion table actions
Spec 3.88 item 4 (SPA) and 3.89. Vanished row: muted, "No longer reported" marker (en/de), Forget offered as for stale rows. 3.89 per spec. Tests: `httpCheck.test.ts` filter; `AssertionTable` renders the actions column only when actions exist; host checks http test: no generic rows for assertions, the table offers Ignore/Rules/Forget to a writer and no write actions to a viewer; vanished row rendering. Mark 3.89 done.

## Task 4: Final
Whole-branch review, `make test`, lint, SPA suite (`npx vitest run`), affected e2e spec(s) only if host page selectors changed; public docs (vanished metrics, assertion table actions) on the docs scratch branch.
