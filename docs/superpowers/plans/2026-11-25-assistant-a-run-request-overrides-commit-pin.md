# Assistant gaps part A: run requests with overrides, a pinned commit and sync_first — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`) syntax.

**Goal:** part A of the design (`docs/superpowers/specs/2026-11-25-assistant-api-gaps-design.md`, section "A. Run requests carry overrides and a pinned commit", owner decisions D3, D4) plus G's `sync_repo` dedupe, which `sync_first` needs.

**Architecture:** `internal/runrequests` (service, picture, resolve, approve/claim guard, store), `internal/api/run_request_handlers.go`, `internal/mcp/tools_jobs.go` (`request_run`), `internal/jobs` (enqueue with `RunOverrides`, claim SQL and flags, sync results), the worker's git checkout (`internal/worker/runner/gitrepo.go`), the worker protocol (`proto/…/worker.proto`, additive), the SPA's run-request page (`web/src/features` — grep `run-requests`/`RunRequest`).

**Tech Stack:** Go, sqlc, Postgres, protobuf, React/TypeScript.

**Spec:** the design document above (approved by the owner 2026-11-25). Read section A, "Constraints", and the research notes it cites. The design is binding; the rulings below only add what it leaves to the builder.

## Global Constraints
- Go layers per `CLAUDE.md`; targeted `go test -race -timeout 900s ./internal/<pkg>/...`, `./internal/api/...` with `-timeout 1800s`, `internal/e2e` only on its own invocation, never whole-module `go test ./...`; `golangci-lint run` clean; sqlc via `PATH=$PATH:~/go/bin make sqlc`; proto via the repo's make target (`grep -n proto Makefile`); SPA `cd web && npm run lint && npx vitest run <files> && npx tsc -b`; API client regenerated the repo's way (`npm run api:gen`) when the OpenAPI changes.
- Migrations additive only, next free number (`ls internal/db/migrations | tail -2`); a new column or table: run `./internal/backup/...` (a new table needs a backup round-trip fixture row). Proto changes additive; version skew per `docs/upgrades.md`.
- Secrets never reach a picture, an MCP response, a log or the activity log in clear.
- Timestamps compared in tests are truncated to microseconds. TDD: the failing test first. Deterministic tests.
- No Playwright, no Docker beyond the testdb harness; no push/tag/stash/reset/rebase/amend/branch switch, no `git checkout -p`; `git add <paths>` only (never `internal/web/dist`, never `.superpowers`, never untracked plan files); no subagents; no background processes left running.
- Conventional Commits, footer exactly `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.

## Rulings (controller's calls, in addition to the design)
- **Overrides are stored on the request** in one nullable sealed column (or a JSON column plus the sealed parts) carrying `check`, `diff`, `extra_vars`, `host_ids`, `limit`; a `retry_failed` request refuses overrides (422). Extra vars: `templates.ValidateOverrides` against the template's `extra_vars_schema`; any key whose schema type is `secret` → 422 naming the key. `host_ids` (max 500) each must be in the template's resolved targets (422 otherwise); `host_id` stays accepted as a one-element `host_ids`. `limit` narrows like `RunOverrides.Limit`. `expires_in` minutes 5…1440, default 60.
- **Picture**: new fields `check_mode` (bool), `diff` (bool), `extra_vars_override` (shown by value), `hosts` (the narrowed target list), `commit` (see Task 2). Pictures without these fields must keep their canonical JSON unchanged (omitempty), so pending requests from before the upgrade keep their fingerprint.
- **Approval** passes the overrides as `jobs.RunOverrides` to the enqueue the approval already does (`resolve.go` Run → `EnqueueRunWith`); a dry run is a `KindRun` job with `Check` set; follow-ups are not started for dry runs (the code already refuses them — keep it and show "no follow-ups for a dry run" in the picture).
- **Commit pin storage**: `run_requests.pinned_commit` and `jobs.pinned_commit` (nullable text). While pending, the picture's commit is live; approval stores it on both. The claim guard compares the stored commit with the job's pin rather than recomputing it. The worker claim flag `supports_pinned_commit` (proto field on the claim request, like `supports_check_mode`); the claim SQL never hands a job with `pinned_commit` to a claim without it. The worker checks out exactly that SHA after its fetch and fails the job with "commit <sha> is no longer in the repository" if it cannot. `get_job` and the job DTO expose `pinned_commit`.
- **"Repository changed after the sync"**: compare the repository's current URL, branch and subpath with what the last successful sync used; if `repo_var_sources` does not record them, add that (additive) and treat a sync recorded before the upgrade as "unknown → sync first".
- **sync_first states**: `run_requests.status` gains `waiting_for_sync` and `failed` (extend the CHECK in an additive migration: drop and re-add the constraint with the wider set is allowed — it only widens). `expires_at` is set when the request becomes `pending`; while waiting it is NULL (make the column nullable in the same migration, with a CHECK that only `waiting_for_sync` may have it NULL). The sync's terminal result (`jobs.RecordSyncResult` or where sync results land) advances every request waiting on that repository through a port: success → compute the picture, `pending` with its expiry; failure → `failed` with the sync's error. Waiting requests count against `MaxPendingPerToken`.
- **sync_repo dedupe** (design G): `POST /repos/{id}/sync` with a sync of that repository queued or running returns 200 with that job and `already_running: true`; MCP `sync_repo` passes it through. `sync_first` uses the same path.
- **SPA**: the request page shows a "Dry run (check mode, diff)" banner, the narrowed hosts, override extra vars, the commit (short SHA with the full one on hover/copy, and the "sync first" / "a sync is running" reasons that block approval), the waiting/failed states; the jobs view shows the pinned commit. en and de texts.

---

## Task 1: Overrides on run requests
**Files:** `internal/runrequests/*`, `internal/api/run_request_handlers.go` (+ DTO), `internal/mcp/tools_jobs.go`, migration, `internal/db/queries/*` (+ sqlc), SPA run-request page, tests.
- [ ] `request_run`/REST accept `check_mode`, `diff`, `extra_vars`, `host_ids`, `limit`, `expires_in`; stored, pictured, fingerprinted, enqueued as `RunOverrides`. Tests: validation errors (secret key, foreign host, retry with overrides, expires_in bounds), picture/fingerprint (old pictures unchanged), approval enqueues a check-mode job with the narrowed hosts and vars, MCP args pass through.
- [ ] Commit `feat(runrequests): run requests carry a dry run, extra vars, hosts and an expiry`.

## Task 2: The pinned commit
**Files:** `internal/runrequests/*`, `internal/repos/*` (last sync commit + what it synced), `internal/jobs/*` (pinned_commit, claim SQL, claim guard), proto + `internal/grpcsrv/claim.go`, `internal/worker/*` (flag, checkout), API job DTO, MCP `get_job` passthrough, migration, SPA, `docs/upgrades.md`.
- [ ] Picture `commit` live while pending (with the blocking reasons); approval refused while a sync of the repository is queued/running or the commit is unknown/outdated; stored at approval; claim guard compares stored values; worker checks out the SHA; old workers never claim pinned jobs. Tests per path, incl. a worker runner test with a local git repo (two commits; the pinned older one is checked out).
- [ ] Commit `feat(runs): an approved run runs the commit its approver saw`.

## Task 3: sync_first and sync dedupe
**Files:** `internal/runrequests/*`, `internal/jobs/*` (sync result hook, dedupe), `internal/repos/*`, `internal/api/repo_handlers.go`, `internal/mcp/tools_write.go`/`tools_jobs.go`, migration, SPA, tests.
- [ ] `sync_first` → `waiting_for_sync` → `pending` (picture with the new commit, expiry starts) or `failed`; dedupe of `sync_repo`. Tests: success and failure paths through the real store, dedupe returns the running job, waiting requests count against the cap, expiry starts at pending.
- [ ] Commit `feat(runrequests): sync first, then ask`.

## Task 4: Final
Whole-branch review, `make test`, lint, SPA checks, `docs/upgrades.md` (migrations, proto field, worker skew: pinned jobs wait for updated workers), user docs in the docs clone (assistant page: request_run parameters, dry run, commit, sync_first; approvals page; changelog), backlog row for the briefing's part A, merge to local main, redeploy.
