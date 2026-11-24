# Run request approval: the 2.120 review's deferred loose ends (backlog 2.126) — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`) syntax.

**Goal:** close backlog row 2.126 (`grep -nE "^\| 2\.126 " docs/BACKLOG.md`): a re-approved request runs every zone, the per-token cap is exact, the approval page shows the hosts the run reaches, gathered facts no longer fail an approved run, inventory secrets are opened only where a run carries them, the schema/membership race has a deterministic test, and the pending badge works against an older manager.

**Architecture:** small, independent changes in `internal/jobs` (claim refusal marker, limited targets), `internal/runrequests` (targets, cap, digest), `internal/inventory` (open sealed values after the scope is rendered), `internal/mcp` (race tests) and the SPA badge.

**Tech Stack:** Go (huma, sqlc), React + TypeScript (Vitest).

**Spec:** `docs/superpowers/specs/2026-11-16-assistant-access-design.md` ("Run requests") and the backlog row; the owner chose to continue with the backlog on 2026-11-24. Rulings below are the controller's calls.

## Global Constraints
- Go layers per `CLAUDE.md`; targeted `go test -race -timeout 900s ./internal/<pkg>/...`, `./internal/api/...` with `-timeout 1800s`, never whole-module `go test ./...`; `golangci-lint run` clean; regenerate OpenAPI + `web/src/api/schema.d.ts` (`cd web && npm run api:gen`) only if the API changes; sqlc via `PATH=$PATH:~/go/bin make sqlc`. Migrations additive only; a new table needs a backup round-trip fixture row.
- Timestamps compared in tests are truncated to microseconds (Postgres precision).
- SPA: `cd web && npx vitest run <files>`, then full `npx vitest run`, `npx tsc -b`, `npx eslint .`; single quotes in TS, double in JSX attributes; en + de (German "du"); files under ~400 lines.
- TDD. No Playwright, no Docker beyond the testdb harness; no push/tag/stash/reset/rebase/amend/branch switch; `git add <paths>` only (never `internal/web/dist`); no subagents; no background processes left running.
- Conventional Commits, footer `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.

## Rulings (controller's calls)
- **(1) Re-approval after a partial enqueue.** Today a failed enqueue reverts the request to pending; a job the first attempt did create is then claimed, the claim guard refuses it (`errRequestNotApproved`) and the queue fails it; a re-approval replays that failed job through the idempotency key instead of enqueuing its zone again. Fix in `internal/jobs`: the claim guard's refusal fails the job with an error text that starts with a fixed, unexported prefix (`claimRefusedPrefix = "refused at claim: "`, written only by the queue's guard path), and `reserveRunGroup`'s replay set leaves out jobs with status `error` whose `Error` starts with it — such a job ran nothing on any host, so its zone gets a new job. Every other replayed job (queued, running, finished, failed after it started) still covers its zone, so nothing runs twice. Applies to `EnqueueRunWith` and `RetryFailedInto` alike (both use `reserveRunGroup`). Cost if wrong: a guard-refused zone of a keyed schedule run could be enqueued once more on a write-back retry — impossible in practice, since only run-request groups are refused.
- **(2) Exact per-token cap.** Make `MaxPendingPerToken` exact: count and insert in one transaction under a transaction-scoped advisory lock keyed on the token id (`pg_advisory_xact_lock` with a key derived from the token id, in the store; follow how other advisory keys are chosen in the repo and do not collide with existing fixed keys). Requests without a token stay uncapped and take no lock.
- **(4) Picture targets under the template's limit.** New `jobs.Service.LimitedRunTargets(ctx, orgID, templateID) ([]uuid.UUID, error)`: the scope resolved under the template's own limit (`effectiveLimit(tmpl.Limit, nil)`), an empty result an empty list, not an error (the picture shows no targets; approval fails as today). `RunTargets` stays unlimited: the maintenance guard relies on that on purpose (`internal/schedules/maintenance_guard.go`). The resolver's default target case uses the new method (add it to the `JobRunner` port and its fakes).
- **(5) Facts out of the vars digest.** Gathered host facts (`pulsix_facts` in hostvars) are not something a person sets or reviews; they leave the vars digest, so a facts refresh between approval and claim no longer fails the run. Everything else in the snapshot stays in. Every fingerprint changes once more; this ships in the same release as 2.120, so the existing 2.120 upgrade note covers it — Task 5 extends that note's wording, no new section.
- **(3) Open only what the run carries.** `Renderer.renderIn` with reveal opens every sealed group and membership value of the organization before `render.Render` narrows to the scope. Change it to open lazily: with reveal, `secrets()` checks strays as today (org-wide, so backlog 2.22's fail-closed behaviour is unchanged) but leaves each sealed value as an internal placeholder value (an unexported Go type carrying the ciphertext and its AAD — never a string a user could have typed); after `render.Render`, a walk over the rendered document opens each placeholder it finds and replaces it with the plaintext, collecting the revealed values for `out.Secrets`. First verify that `render.Render` carries var values through as-is (no JSON round trip); if it does not, stop and report instead of working around it. A placeholder left anywhere after the walk is a bug: fail closed. Preview (masked) is unchanged. Tests: a snapshot for one host opens only the sealed values in its document (count `OpenAAD` calls through a counting box), still fails on an out-of-scope stray sealed value, and produces the same document and `Secrets` as before for an in-scope case.
- **(6) Deterministic schema/membership race tests.** In `internal/mcp`, drive the two re-checks in `tools_write.go` (`set_group_schema`'s membership re-check after the schema PUT, and the member write's schema re-check after the write, ~line 276) with a hook in the test's request dispatch that performs the competing write exactly between the tool's check and its own write. Assert that the plain value under the newly secret field is cleared (schema case) and the equivalent outcome for the member-write case. If the dispatch has no seam for such a hook, add the narrowest test seam (an unexported field or option on the server's dispatcher), not a production behaviour change.
- **(7) Badge on an older manager.** When `GET /orgs/{org}/run-requests/pending-count` answers 404, the SPA badge falls back to the length of the pending list (`?status=pending`) it can already read. Only 404 falls back; other errors show no badge as today. Do not retry the count route on every render after a 404 within the session's query lifetime (cache the fallback decision in the query).

---

## Task 1: Re-approval refills refused zones; picture targets under the limit (items 1, 4)
**Files:** `internal/jobs/{queue_sql.go,run_request.go,run_scope.go}` (+ tests), `internal/runrequests/resolve.go` (+ fakes/tests).
- [ ] Tests first: a run-request group with two zones where the first attempt created zone A's job and failed before zone B; zone A's job is refused at claim (request pending); after re-approval both zones have a job that can be claimed and zone A's refused job is not counted (jobs-level test of `reserveRunGroup`/`enqueueRunScope` plus a runrequests or API-level test through approve → revert → claim → approve); a replayed job that started still covers its zone. `LimitedRunTargets` returns only the hosts the template's limit selects; the picture's targets match it.
- [ ] Implement, lint, commit `fix(runrequests): re-approval enqueues refused zones, picture targets under the limit`.

## Task 2: Exact per-token cap; facts out of the digest (items 2, 5)
**Files:** `internal/runrequests/{store_sql.go,service.go,vars_digest.go}` (+ tests), `internal/db/queries/run_requests.sql` (+ sqlc) if needed.
- [ ] Tests first: 30 concurrent creates for one token against the real store → exactly 20 succeed, the rest `ErrTooManyPending`; creates without a token are not capped. A facts change on a target host leaves the vars digest unchanged; a var change still changes it.
- [ ] Implement, lint, commit `fix(runrequests): exact pending cap per token, facts out of the vars digest`.

## Task 3: Open only the sealed values the rendered document carries (item 3)
**Files:** `internal/inventory/renderer.go` (+ tests).
- [ ] Tests as ruled. Run `./internal/inventory/...`, `./internal/jobs/...`, `./internal/runrequests/...`, `./internal/app/...` and `./internal/api/...` (claims and previews go through the renderer).
- [ ] Commit `perf(inventory): open only the secrets a rendered run carries`.

## Task 4: Deterministic schema/membership race tests (item 6)
**Files:** `internal/mcp/*_test.go` (and the narrowest seam, if one is needed).
- [ ] Commit `test(mcp): drive the schema and membership re-checks deterministically`.

## Task 5: SPA badge fallback (item 7)
**Files:** `web/src/api/hooks/useRunRequests.ts` (+ test), wherever the badge reads it.
- [ ] Tests first: a 404 from the count route shows the pending list's length; a 500 shows no badge; a 200 uses the count.
- [ ] Commit `fix(web): fall back to the pending list when the manager has no count route`.

## Task 6: Final
Whole-branch review, `make test`, lint, full vitest/tsc/eslint, `docs/upgrades.md` (extend the 2.120 note: facts no longer part of the fingerprint; the badge falls back on an older manager instead of disappearing), user docs only if a user-visible behaviour changed (the per-token cap wording "at most 20" is now exact; check the docs clone branch `docs/pulsix-approval-loose-ends`), backlog 2.126 done, merge to local main, redeploy the local stacks.
