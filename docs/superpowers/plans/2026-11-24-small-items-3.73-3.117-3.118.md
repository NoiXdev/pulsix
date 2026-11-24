# Small items: testcontainers reaper race, probe rows after a forget, vanished metrics during a replay (backlog 3.73, 3.117, 3.118) — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`) syntax.

**Goal:** close backlog rows 3.73, 3.117 and 3.118 (`grep -nE "^\| (3\.73|3\.117|3\.118) " docs/BACKLOG.md`).

**Architecture:** `internal/db/testdb` retries one known transient container error; the item and metric forgets in `internal/checks` also delete `probe_current` rows; the alert evaluation does not judge a metric vanished while its host's results are still being replayed.

**Tech Stack:** Go (sqlc, testcontainers).

**Spec:** the backlog rows; monitoring design docs under `docs/superpowers/specs/` for 3.88 (vantage probes) and the rule engine (vanished/stale) as background. The owner asked to continue with the backlog on 2026-11-24. Rulings below are the controller's calls.

## Global Constraints
- Go layers per `CLAUDE.md`; targeted `go test -race -timeout 900s ./internal/<pkg>/...`, `./internal/api/...` with `-timeout 1800s`, never whole-module `go test ./...`; `golangci-lint run` clean; sqlc via `PATH=$PATH:~/go/bin make sqlc`. Migrations additive only; no proto change unless the ruling allows it.
- Timestamps compared in tests are truncated to microseconds (Postgres precision).
- TDD. No Playwright, no Docker beyond the testdb harness; no push/tag/stash/reset/rebase/amend/branch switch; `git add <paths>` only (never `internal/web/dist`, never `.superpowers`); no subagents; no background processes left running.
- Conventional Commits, footer exactly `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.

## Rulings (controller's calls)
- **3.73 reaper race.** In `internal/db/testdb`, retry the container start (or whatever call surfaces it) when the error is the reaper's transient state (`wait for reaper` … `unexpected container status "removing"`), at most 3 attempts with a short backoff, logging each retry; any other error fails at once as today. Make the start call injectable so a unit test drives the retry with fake errors (no real Docker needed for that test). Do not change the reaper's global configuration.
- **3.117 forget and probe rows.** The item forget and the metric forget delete the matching `probe_current` rows (same org, host, item/metric tuple — check the exact key) inside the same transaction as the rows they already delete; a real-DB test shows the vantage breakdown (`probes.SQLStore.VantageCurrent`) no longer lists the forgotten metric, and that other metrics' probe rows survive.
- **3.118 vanished during a replay.** Investigate how a reconnecting agent replays (chunks of 200), what the server knows about it, and where "vanished" is judged. Prefer the option that needs no protocol change: do not judge a metric vanished on a host whose newest readings arrived in a replay that may not be complete yet (define "in a replay" from data the server already has — e.g. readings whose observed time lies well before their ingest time, or a per-host replay marker the ingest path can set — and pick the simplest sound signal). If only a protocol or migration change can tell a replay apart reliably, stop and report the options with their cost instead of building it. Tests: a split replay with an evaluation between the chunks closes no alert as stale; an ordinary vanished metric (no replay) still closes as before.

---

## Task 1: 3.73 testdb retry
**Files:** `internal/db/testdb/*` (+ test).
- [ ] Commit `test(testdb): retry a start that meets the reaper being removed`.

## Task 2: 3.117 forget deletes probe rows
**Files:** `internal/checks/forget*.go`, `internal/db/queries/*.sql` (+ sqlc), tests.
- [ ] Commit `fix(checks): forgetting an item or metric removes its vantage rows`.

## Task 3: 3.118 vanished during a replay
**Files:** as the investigation finds (rule evaluation and ingest paths).
- [ ] Commit `fix(rules): do not judge metrics vanished while a replay is in progress` (or stop and report per the ruling).

## Task 4: Final
Whole-branch review, `make test`, lint, backlog rows done, `docs/upgrades.md` only if behaviour visible to operators changed (3.118: fewer stale notifications after a long gap), changelog line in the docs clone if user-visible, merge to local main, redeploy.
