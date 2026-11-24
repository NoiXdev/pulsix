# Small items: installer re-run without a token, Ansible pin in the go CI job, end-to-end synthetic checks and resolve notification (backlog 3.60, 3.68, 3.4b) — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`) syntax.

**Goal:** close backlog rows 3.60, 3.68 and 3.4b (`grep -nE "^\| (3\.60|3\.68|3\.4b) " docs/BACKLOG.md`).

**Architecture:** 3.60 changes the two installers (`deploy/install.sh`, `deploy/agent/windows/install.ps1`) and their CI smoke scripts; 3.68 pins ansible-core in the `go` CI job; 3.4b adds `internal/e2e` scenarios.

**Tech Stack:** POSIX sh, PowerShell, GitHub Actions YAML, Go (`internal/e2e` harness).

**Spec:** the backlog rows themselves. The owner asked to continue with the backlog on 2026-11-24. Rulings below are the controller's calls.

## Global Constraints
- Go layers per `CLAUDE.md`; targeted `go test -race -timeout 900s ./internal/<pkg>/...`, `./internal/api/...` with `-timeout 1800s`, `internal/e2e` on its own invocation (`go test -race -timeout 900s ./internal/e2e/...` alone, nothing else in parallel), never whole-module `go test ./...`; `golangci-lint run` clean.
- Timestamps compared in tests are truncated to microseconds (Postgres precision).
- Never install or run the agent as a service on this Mac, never `sudo`, never run the installers for real here; installer changes are checked with `sh -n`/`shellcheck` (if installed) and whatever Go tests render or exercise the installer templates (`grep -rln install.sh internal`).
- TDD where a test harness exists. No Playwright, no Docker beyond the testdb/containertest harness; no push/tag/stash/reset/rebase/amend/branch switch; `git add <paths>` only (never `internal/web/dist`, never `.superpowers`); no subagents; no background processes left running.
- Conventional Commits, footer exactly `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.

## Rulings (controller's calls)
- **3.60.** Both installers skip the token prompt and the "no enrollment token given" stop when the data folder already holds the agent's identity — the files the agent itself loads as its identity (`internal/agent/config.go`: `agent.crt`, `agent.key`, `agent.ca.pem`; check which the agent requires to start without enrolling and require exactly those, each a non-empty regular file). In that case, and only when neither `--token`/`--token-file` (`-Token`/`-TokenFile`) was given, the env file is written without the `PULSIX_ENROLL_TOKEN` line (the agent forgets the token once its identity is valid anyway — confirm that from the agent code and say so in a comment); every other line is written as today. An explicitly given token is still read, validated and written as today. A missing or partial identity behaves exactly as today (prompt, stop on empty). The installer prints one line saying it reuses the existing identity. Extend the CI install smoke scripts (`scripts/ci/install-smoke-*.sh` and the Windows equivalent the `agent-windows` job runs) so their upgrade step runs without a token and asserts the agent still runs; header comments and usage text of both installers say the token is optional on a re-run.
- **3.68.** Pin it: the `go` job installs `ansible-core==2.21.4` (the pin `deploy/worker/Dockerfile` and the `e2e-ansible` job use) with `actions/setup-python` at the SHA the pin table already lists and `pip install`, before the coverage gate, with a comment naming backlog 3.68 and why (the runner image's ansible-core 2.21.6 galaxy refuses role names that need escaping). Find which tests in the go job call ansible (`grep -rn "ansible-galaxy\|ansible-playbook\|LookPath(\"ansible" internal`) and name them in the comment. Correct the stale version mentions the backlog row and the pin table drift left (the row says the local and worker images pin 2.21.4 and 2.21.5 — find where 2.21.5 is actually pinned, e.g. `uidboundary`'s evidence comment, and leave a comment true). The `uidboundary` fixture keeps escaping (nothing changes there).
- **3.4b.** Two new `internal/e2e` scenarios using the existing harness and its patterns (read `harness_test.go`, `checks_test.go`, `rules_test.go`, `notifications_test.go` first): (1) a synthetic (server-run) check defined through the API against a target the test controls (an `httptest` server the manager's probe can reach under the harness's network rules; check what the probe runner allows for loopback in tests) goes from definition to a recorded state, and flipping the target to failing makes the state follow; (2) an alert fired by a rule, routed to a webhook channel the test controls, sends a firing notification and, when the condition clears, a resolve notification — assert the resolve payload's kind/state field. Deterministic: drive clocks/tickers through the harness's hooks, poll with bounded deadlines, no fixed sleeps. If a production gap blocks either scenario (e.g. synthetic probes cannot target loopback at all in the harness), stop and report it instead of changing production code.

---

## Task 1: 3.60 installer re-run without a token
**Files:** `deploy/install.sh`, `deploy/agent/windows/install.ps1`, `scripts/ci/*` install smokes, Go tests that render the installers if any.
- [ ] Commit `feat(install): reuse an existing identity without asking for a token`.

## Task 2: 3.68 pin ansible-core in the go job
**Files:** `.github/workflows/ci.yml`.
- [ ] Commit `ci: pin ansible-core in the go job`.

## Task 3: 3.4b end-to-end synthetic check and resolve notification
**Files:** `internal/e2e/*_test.go`.
- [ ] Commit `test(e2e): synthetic check and resolve notification end to end`.

## Task 4: Final
Whole-branch review, `make test`, lint, `docs/upgrades.md` note for 3.60 (operators may re-run without a token), user docs install page in the docs clone (token optional on a re-run), backlog rows done, merge to local main, redeploy.
