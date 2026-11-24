# Small loose ends: withdrawn jobs in the run view, the badge fallback, blank token names (backlog 2.127 (2)(4), 2.129) — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`) syntax.

**Goal:** a job refused at claim because its approval was withdrawn reads as "not run" rather than as a failure wherever jobs are listed; the pending badge stops asking a missing count route on every poll; creating an organization token refuses a blank name like the edit does.

**Architecture:** `internal/jobs` exposes its existing `withdrawnAtClaim` predicate on the job read model; the job DTO carries it; the SPA renders a neutral state for it. The badge remembers a 404 for a while. The token create path validates the name in the service.

**Tech Stack:** Go (huma), React + TypeScript (Vitest).

**Spec:** backlog rows 2.127 and 2.129; the owner asked to continue with the backlog on 2026-11-24. 2.127 (1), (3) and (5) stay records. Rulings below are the controller's calls.

## Global Constraints
- Go layers per `CLAUDE.md`; targeted `go test -race -timeout 900s ./internal/<pkg>/...`, `./internal/api/...` with `-timeout 1800s`, never whole-module `go test ./...`; `golangci-lint run` clean; OpenAPI + `web/src/api/schema.d.ts` regenerated (`cd web && npm run api:gen`). No migration.
- SPA: `cd web && npx vitest run <files>`, then full `npx vitest run`, `npx tsc -b`, `npx eslint .`; single quotes in TS, double in JSX attributes; en + de (German "du"); `src/ui` presentational; files under ~400 lines.
- TDD. No Playwright, no Docker beyond the testdb harness; no push/tag/stash/reset/rebase/amend/branch switch; `git add <paths>` only (never `internal/web/dist`, never `.superpowers`); no subagents; no background processes left running.
- Conventional Commits, footer exactly `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.

## Rulings (controller's calls)
- **Withdrawn jobs (2.127 (2)).** Keep the job and its `error` status (no data change, the row stays for the record). Add a read-only boolean `withdrawn_at_claim` to the job DTO wherever a job is returned (detail, lists, run group jobs), computed by `internal/jobs`' existing predicate (`withdrawnAtClaim`: status error, attempts 1, the exact `WithdrawnAtClaimError` text) — export a method or field on `Job`, do not duplicate the rule in `internal/api`. The SPA shows such a job with a neutral status label instead of the error style: en "Not run — approval withdrawn", de "Nicht gelaufen – Freigabe zurückgenommen", in the run view's group section, the run detail header and the runs list; the error text stays visible in the detail. Filters by status are unchanged (it still matches `error`).
- **Badge fallback (2.127 (4)).** After the count route answers 404, the hook goes straight to the pending list for 10 minutes before asking the count route again (state kept per org inside the query layer, e.g. a module-level map keyed by org or a separate cached query with `staleTime` 10 min — no global singletons that leak across tests; reset in tests). Other errors keep today's behaviour.
- **Blank token names on create (2.129).** The organization token create path refuses an empty or whitespace-only name in the service with the same sentinel the edit uses (`ErrEmptyName`, 422), and the personal token create path too if it has the same gap. Existing tokens are untouched.

---

## Task 1: Server (2.127 (2) server side, 2.129)
**Files:** `internal/jobs/claim_withdrawn.go` (+ test), `internal/api/job_handlers.go` and other job DTO builders (+ tests), `internal/auth/token*.go` (+ tests), OpenAPI/schema regen.
- [ ] Tests first: the DTO carries `withdrawn_at_claim` true for a withdrawn job and false for an ordinary failed job; creating a token with a blank name answers 422 (service unit test and API test).
- [ ] Commit `feat(jobs): mark jobs whose approval was withdrawn at claim` and `fix(auth): refuse a blank token name on create`.

## Task 2: SPA (2.127 (2) client side, (4))
**Files:** job status rendering in `web/src/features/runs/*`, `web/src/api/hooks/useRunRequests.ts` (+ tests), i18n en/de.
- [ ] Tests first: a withdrawn job shows the neutral label (en and de) in the group section, the detail header and the runs list; after a 404 the next poll within 10 minutes asks only the list, after 10 minutes the count route again.
- [ ] Commit `fix(web): show withdrawn jobs as not run, remember a missing count route`.

## Task 3: Final
Whole-branch review, `make test`, lint, full vitest/tsc/eslint, `docs/upgrades.md` (new optional DTO field, token create 422), docs clone changelog line, backlog 2.127 (2)(4) and 2.129 done (2.127 keeps (1)(3)(5) open), merge to local main, redeploy.
