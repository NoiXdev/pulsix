# Small items: agent result stranding window, nested Begin in a read transaction, escalation clock over an interval (backlog 3.80, 4.38, 4.41) — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`) syntax.

**Goal:** close backlog rows 3.80, 4.38 and 4.41 (`grep -nE "^\| (3\.80|4\.38|4\.41) " docs/BACKLOG.md`).

**Architecture:** 3.80 is in `internal/agent` (`session.go` `activateSender`/`handleResults`, `probes_wiring.go`); 4.38 is in `internal/db/scope.go` (`DB.Begin` inside `WithReadTx`); 4.41 is in `internal/alertroutes` and `internal/db/queries/alert_escalations.sql` (the maintenance anchor of `eligible_since`).

**Tech Stack:** Go, sqlc, Postgres.

**Spec:** the backlog rows; for 4.41 also `docs/superpowers/specs/2026-10-16-v0.4d-escalation-chains-design.md` (design decision 5). The owner asked to continue with the backlog on 2026-11-24. Rulings below are the controller's calls.

## Global Constraints
- Go layers per `CLAUDE.md`; targeted `go test -race -timeout 900s ./internal/<pkg>/...`, `./internal/api/...` with `-timeout 1800s`, `internal/e2e` on its own invocation, never whole-module `go test ./...`; `golangci-lint run` clean; sqlc via `PATH=$PATH:~/go/bin make sqlc`. Migrations additive only (none expected).
- Timestamps compared in tests are truncated to microseconds (Postgres precision).
- TDD: the failing test first, shown failing for the right reason. Concurrency tests deterministic (hooks/channels, no sleeps as synchronisation).
- No Playwright, no Docker beyond the testdb harness; no push/tag/stash/reset/rebase/amend/branch switch; `git add <paths>` only (never `internal/web/dist`, never `.superpowers`); no subagents; no background processes left running.
- Conventional Commits, footer exactly `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.

## Rulings (controller's calls)
- **3.80.** Close the window, do not just correct the comment: a result that `handleResults` routes to the buffer must never land in a ring after `activateSender` drained it for the sender that is becoming active. Decide whether to send or buffer under the same `routeMu` that `activateSender` holds while it drains, replays and installs the sender (or an equivalent ordering that makes "drained" and "sender active" one atomic step from `handleResults`' point of view), for both check results and probe results. Do not hold `routeMu` across a network send in `handleResults` if that would block the scheduler behind a slow stream — if the design needs a send under the lock, explain the trade-off in the report and pick the option that cannot strand results and cannot deadlock. Correct `activateSender`'s doc comment and the comment at `session.go:196`. Test: a deterministic interleaving (hook between drain and install) where a result handled in that gap reaches the new sender (or the replay), for checks and for probes; the test fails on today's code.
- **4.38.** `DB.Begin` inside `WithReadTx` returns a savepoint (`b.tx.Begin(ctx)`), exactly as the write branch already does, so a nested store's `Commit` releases only its savepoint and its `Rollback` undoes only its own statements; the outer read transaction is never ended early. Replace `TestWithReadTxBeginReturnsTheSharedTransaction` with tests that pin the new behaviour: committing and rolling back the nested transaction leaves the outer one usable for further reads. If `WithReadTx` opens a READ ONLY transaction, a write inside the savepoint fails with Postgres' read-only error — say so in the doc comments of `Begin` and of `writeAlertAction` (which can then drop its "by caller discipline" caveat).
- **4.41.** Make the maintenance anchor interval-aware as the row says: whether any maintenance occurrence covered the chain at any instant in `[previous tick, this tick]`, instead of a point sample. Find where the escalation tick knows its previous tick time (persisted on the escalation row, or the tick interval — prefer a value already stored; if none exists and only an additive column would give it, add it in a migration with the next free number and say why). An occurrence that overlaps the interval moves the anchor to that occurrence's end (or whatever the design doc's decision 5 says the anchor becomes when maintenance covered the chain — follow the doc). Tests: a window that opens and closes strictly between two ticks delays the next rung as the design intends; a window outside the interval changes nothing; the existing point-in-time cases keep passing.

---

## Task 1: 3.80 stranding window
**Files:** `internal/agent/session.go`, `internal/agent/probes_wiring.go` (+ tests).
- [ ] Commit `fix(agent): no result strands between a replay and the sender going live`.

## Task 2: 4.38 nested Begin in a read transaction
**Files:** `internal/db/scope.go`, `internal/db/scope_test.go`, `internal/alerts/store_sql_actions.go` (comment).
- [ ] Commit `fix(db): a store's own transaction inside a read transaction is a savepoint`.

## Task 3: 4.41 escalation maintenance over the tick interval
**Files:** `internal/alertroutes/*`, `internal/db/queries/alert_escalations.sql` (+ sqlc), tests.
- [ ] Commit `fix(alertroutes): maintenance between two ticks delays the next rung`.

## Task 4: Final
Whole-branch review, `make test`, lint, `docs/upgrades.md` only if operator-visible (4.41: escalations wait for short maintenance windows), changelog line in the docs clone if user-visible, backlog rows done, merge to local main, redeploy.
