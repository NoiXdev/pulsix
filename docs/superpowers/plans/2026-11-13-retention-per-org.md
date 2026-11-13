# Retention per Organization Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** An operator-set instance maximum for measurement retention, a per-org choice of 30/90/365 days below it, hourly deletion for shorter org settings, and 30/90-day graph ranges.

**Architecture:** `PULSIX_SAMPLE_RETENTION` in `internal/config`; a startup step syncs the TimescaleDB retention policy of `check_samples` and `check_state_changes`; `org_monitoring_settings.retention_days` (additive) through `internal/monitorsettings` and its API; a manager ticker deletes older rows of orgs with a shorter effective retention; the SPA settings card and the metric graph ranges.

**Tech Stack:** Go, PostgreSQL/TimescaleDB + sqlc (v1.31.1 binary at `/private/tmp/claude-501/-Users-noidee--dev-pulsix/76b69f36-5040-49f9-a80e-08a1ef63bcdb/scratchpad/bin/sqlc`), huma OpenAPI + `cd web && PATH=$PWD/node_modules/.bin:$PATH bash scripts/api-gen.sh`, React/TypeScript.

**Spec:** `docs/superpowers/specs/2026-11-13-retention-per-org-design.md`

## Global Constraints

- Values: `PULSIX_SAMPLE_RETENTION` whole days 7–365, default 90; `retention_days` ∈ {30, 90, 365} or NULL (= instance maximum); effective = min(choice, maximum); deletion hourly, batches of 10 000 rows, a per-run time budget (name it, e.g. 2 min); graph ranges 30 d and 90 d offered only when effective ≥ range.
- Tables affected: `check_samples`, `check_state_changes` only.
- Migrations additive, next free number after the newest in `internal/db/migrations`; follow `docs/upgrades.md`.
- Layering per CLAUDE.md; injected clock; no package-level state.
- Repo rules: TDD; never `go test ./...` or `make test` (single packages `-race -timeout 900s`; internal/api `-timeout 1800s -run …` plus ONE whole internal/api run in each task that touches it); `golangci-lint run` clean; web `npm test`, `npm run lint`, `npx tsc -b`; commit footer `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`; never touch Docker containers other than testcontainers tests start; no state-changing git besides your own commits; temp files only in the scratchpad.

## Review Focus

1. The policy sync never drops data on an unchanged value and is safe with several managers starting at once.
2. The deletion never touches another org, never deletes inside the effective retention, and cannot hold long locks or starve ingest (batching, budget).
3. With no variable and no org choice, behaviour is exactly today's (90 days, nothing extra deleted).
4. The effective value shown in the SPA equals what the ticker uses.
5. 90-day graph queries stay within today's response size and use the index.

---

### Task 1: Instance maximum and policy sync

**Files:** `internal/config` (field, parse, tests), a small package or function for the policy sync (e.g. in `internal/checks` or `internal/db` — follow where other startup DB steps live), wiring in `internal/app`/`cmd/manager`, DB-backed tests.

- [ ] Tests first: config default/bounds/invalid; sync sets both policies when different, no-op when equal (job id unchanged), concurrent calls safe. RED → GREEN → lint → tests (`./internal/config/...`, the sync package, `./cmd/manager/...`, `./internal/app/...`) → commit `feat(checks): set the measurement retention from PULSIX_SAMPLE_RETENTION`.

### Task 2: Per-org retention and the deletion ticker

**Files:** migration (`org_monitoring_settings.retention_days` + CHECK), queries + sqlc, `internal/monitorsettings` (choice, effective value with the instance maximum injected), API settings DTO (`retention_days`, `retention_max_days`, `retention_effective_days`), the ticker (new file in an appropriate package, wired in `internal/app`, leader/advisory-lock pattern of other periodic jobs), tests.

- [ ] Tests first: CHECK; service/API read-write and effective value; permissions/isolation; ticker deletes only the shorter org's older rows in both tables, in batches, respects the budget, leaves another org and newer rows. RED → GREEN → sqlc → api-gen → lint → tests (packages touched, internal/api `-run Setting` then the whole internal/api once) → commit `feat(monitorsettings): keep measurements per organization`.

### Task 3: SPA

**Files:** the Monitoring settings card (retention select, disabled options above the maximum, warning on lowering), `web/src/features/hosts/MetricGraphPanel.tsx` (ranges 30d/90d by effective retention; check how a range maps to the API's from/to and buckets), i18n en/de, Vitest.

- [ ] Tests first: options/disabling/warning; ranges offered by effective retention. RED → GREEN → `npm test`, `npm run lint`, `npx tsc -b` → commit `feat(web): choose the measurement retention and graph 30 and 90 days`.

### Task 4: Documentation

**Files:** `docs/BACKLOG.md` (3.15 done; 3.14a gains the 5 m / 1 h aggregate tiers and 365-day graphs), `docs/upgrades.md` (migration, the variable, lowering deletes, same value on every instance), the 00044/00046 retention comments stay (history); noix-docs scratch clone `/private/tmp/claude-501/-Users-noidee--dev-pulsix/76b69f36-5040-49f9-a80e-08a1ef63bcdb/scratchpad/noix-docs-merge`, new branch `docs/pulsix-retention` from `docs/pulsix-client-hosts`: configuration reference, org settings, graphs, changelog; `npm run build`. Push nothing.

- [ ] Edit, build, commit in both repos (`docs: record retention per organization (backlog 3.15)`, `docs(pulsix): measurement retention per organization`).
