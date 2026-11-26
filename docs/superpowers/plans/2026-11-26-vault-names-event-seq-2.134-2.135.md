# Vault-encrypted variable names, event sequence bound (backlog 2.134, 2.135) — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`) syntax.

**Goal:** close backlog rows 2.134 and 2.135 (`grep -nE "^\| (2\.134|2\.135) " docs/BACKLOG.md` — read both rows first).

**Spec:** the rows. 2.134 builds on the design `docs/superpowers/specs/2026-11-25-assistant-api-gaps-design.md` (principle: secrets never set by the assistant; part A's secret-shadow check `internal/runrequests/resolve_secrets.go` checkSecretShadows).

## Global Constraints
- Go layers per `CLAUDE.md`; targeted `go test -race -timeout 900s ./internal/<pkg>/...`, `./internal/api/...` with `-timeout 1800s`, `internal/e2e` only on its own invocation, never `go test ./...`; `golangci-lint run` clean; sqlc via `PATH=$PATH:~/go/bin make sqlc`; proto via the repo's make target (`grep -n proto Makefile`); migrations additive (next free number; backup fixture for new columns).
- Version skew per `docs/upgrades.md`: proto additive; an older worker sends no vault names — the manager then treats the names as unknown (see ruling).
- TDD: failing test first. Deterministic tests. No push/tag/stash/reset/rebase/amend/branch switch, no `git checkout -p`; `git add <paths>` only (never `internal/web/dist`, `.superpowers`, untracked plan files); no subagents; no background processes left running; do NOT run `make test`.
- Conventional Commits, footer exactly `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.

## Rulings
- **2.135.** The append path's `max(seq)` read uses the same lower bound as the read side since 2.133 (the job's `created_at` minus 1 hour), so an event a lagging worker stamped before `created_at` is still seen and its sequence number is never reused. Test with an event stamped before `created_at`.
- **2.134.** The worker's variable-source reader reports, next to the values it reads, the NAMES (never values) of variables whose value is vault-encrypted (`!vault` tags and `$ANSIBLE_VAULT` strings) in `group_vars`, `host_vars`, role defaults/vars and playbook vars it reads, with the file they come from; an additive proto field on the sync's sources report; the manager stores them with the repository's variable sources (additive column, e.g. `repo_var_sources.vault_names jsonb`), shows them in the sources view/`list_repo_files` as names only, and `checkSecretShadows` refuses an override key that matches one. A repository whose last sync came from a worker that did not report vault names (older worker, or a sync before the upgrade): the check cannot know — refuse overrides whose key matches a variable name the sources DO list as present but unreadable, and otherwise pass; document in `docs/upgrades.md` that full protection needs an updated worker and a fresh sync. Tests: worker reader unit test with a vault-encrypted var in each location; manager store round trip; run request refused for a vault name; older-worker path.

---

## Task 1: 2.135 append bound
**Files:** `internal/jobs` (append path, events.go / queries), tests.
- [ ] Commit `fix(jobs): an event stamped before its job is created keeps its sequence number unique`.

## Task 2: 2.134 vault names
**Files:** `internal/worker/sync_sources.go` (+ reader), proto, `internal/grpcsrv`, `internal/repos` (sources store), migration, `internal/runrequests/resolve_secrets.go`, MCP/REST sources view, `docs/upgrades.md`, tests.
- [ ] Commit `feat(repos): the manager knows the names of vault-encrypted variables`.

## Task 3: Final
Whole-branch review, `make test`, lint, backlog rows done, docs clone note if user-visible (assistant page: vault names protected with an updated worker), merge, redeploy.
