# Small items: online window per agent, S3 backup test on Linux CI, fair JSON gate (backlog 3.58, 3.67, 3.71) — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`) syntax.

**Goal:** close backlog rows 3.58, 3.67 and 3.71 (`grep -nE "^\| (3\.58|3\.67|3\.71) " docs/BACKLOG.md`).

**Architecture:** 3.67 swaps a silent skip for `containertest.RequireLinuxDocker`; 3.71 gives `internal/probe/http`'s JSON gate per-organization fairness; 3.58 has the agent report its heartbeat interval, stores it per agent, and derives every online-window decision from one function of it.

**Tech Stack:** Go (sqlc, protobuf), React only if the agents list shows the window.

**Spec:** the backlog rows; 3.57's online window and 3.118's replay hold as background (`docs/BACKLOG.md`). The owner asked to continue with the backlog on 2026-11-24. Rulings below are the controller's calls.

## Global Constraints
- Go layers per `CLAUDE.md`; targeted `go test -race -timeout 900s ./internal/<pkg>/...`, `./internal/api/...` with `-timeout 1800s`, `internal/e2e` on its own invocation, never whole-module `go test ./...`; `golangci-lint run` clean; sqlc via `PATH=$PATH:~/go/bin make sqlc`; proto via the repo's make target (find it: `grep -n proto Makefile`). Migrations additive only (next number: check `ls internal/db/migrations | tail -1`); a new column needs no backup fixture unless the backup tests say otherwise — run `./internal/backup/...`. Proto changes additive only; version skew per `docs/upgrades.md`.
- Timestamps compared in tests are truncated to microseconds (Postgres precision).
- TDD. No Playwright, no Docker beyond the testdb harness; no push/tag/stash/reset/rebase/amend/branch switch; `git add <paths>` only (never `internal/web/dist`, never `.superpowers`); no subagents; no background processes left running.
- Conventional Commits, footer exactly `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.

## Rulings (controller's calls)
- **3.67.** `internal/backup/storage_s3_test.go` calls `containertest.RequireLinuxDocker(t)` instead of its own silent skip, so `PULSIX_TEST_REQUIRE_DOCKER=1` on Linux CI turns a broken Docker into a failure; on macOS without Docker it still skips as the helper decides.
- **3.71.** The JSON-stage gate in `internal/probe/http/gate.go` (capacity `min(2, GOMAXPROCS)`) becomes fair between organizations: when it is full, the next free slot goes to the waiting organization with the fewest slots in use (ties: the one waiting longest), never first come first served across orgs; within an org, FIFO. Keep the existing "the manager was too busy to read the JSON response" outcome for a wait that times out. Reuse the probe runner's `fairShare` idea, not its code, if the shapes differ. Tests: one org with many slow targets cannot keep another org's single check waiting past one slot turnover; deterministic (no sleeps — use channels/hooks).
- **3.58.** The agent reports its effective heartbeat interval (an additive proto field on the message it already sends at connect or on each heartbeat — pick the one that is sent first after a reconnect); the manager stores it per agent (`host_agents.heartbeat_interval_s`, additive, NULL for agents that never sent it). One function `agents.OnlineWindow(interval *time.Duration) time.Duration` = `max(3 × interval, 90 s)` (90 s = today's `StaleAfter`, the floor; NULL → 90 s) replaces every use of the fixed window: the agents list's online verdict, `OnlineWindowSeconds` in the SQL touch/heartbeat (online period start, which 3.118's replay hold depends on), the rule evaluation's staleness/unknown grace, the ingest budget, and anything else `grep -rn StaleAfter internal` finds — each call site either uses the per-agent window or says in a comment why the floor is right there. In SQL, compute the per-row window from the column. An older agent keeps today's behaviour exactly.

---

## Task 1: 3.67
**Files:** `internal/backup/storage_s3_test.go`.
- [ ] Commit `test(backup): require Docker for the S3 test on Linux CI`.

## Task 2: 3.71
**Files:** `internal/probe/http/gate.go` (+ tests).
- [ ] Commit `fix(http): share the JSON stage fairly between organizations`.

## Task 3: 3.58
**Files:** proto, `internal/agent/*` (send the interval), `internal/agentapi/*`/ingest (receive), `internal/db/migrations` + queries (+ sqlc), `internal/agents/*`, `internal/rules/*`, `internal/app/rules_ports.go`, every `StaleAfter` call site.
- [ ] Tests first: an agent with a 120 s interval stays online 300 s after its last contact and goes offline after 360 s; an agent without the field behaves exactly as today; the online period start (3.118) uses the per-agent window; rules staleness uses it.
- [ ] Commit `feat(agents): derive the online window from each agent's heartbeat interval`.

## Task 4: Final
Whole-branch review, `make test`, lint, `docs/upgrades.md` (migration, proto field, skew), user docs if the agent docs describe the 90 s window (docs clone), backlog rows done, merge to local main, redeploy.
