# Approved run follow-ups: the 2.122 review's deferred loose ends (backlog 2.123) — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`) syntax.

**Goal:** close backlog row 2.123 (`grep -nE "^\| 2\.123 " docs/BACKLOG.md`): the assistant settings update is one atomic write that changes only what the request names, the approval path has no silent skip and no panic path, and the open test gaps are filled.

**Architecture:** the settings update moves from the handler into `internal/assistant` as one transactional service call; `assistant_enabled` becomes optional on PUT and the SPA sends only the field it changes; the run request follow-up helpers parse ids once and fail closed.

**Tech Stack:** Go (huma, sqlc), React + TypeScript (Vitest).

**Spec:** `docs/superpowers/specs/2026-11-16-approved-run-follow-ups-design.md` and the backlog row; the owner asked to continue with the backlog on 2026-11-24. Rulings below are the controller's calls.

## Global Constraints
- Go layers per `CLAUDE.md` (handlers decode, call a service, encode; no business logic, no store access in handlers); targeted `go test -race -timeout 900s ./internal/<pkg>/...`, `./internal/api/...` with `-timeout 1800s`, never whole-module `go test ./...`; `golangci-lint run` clean; OpenAPI + `web/src/api/schema.d.ts` regenerated (`cd web && npm run api:gen`); sqlc via `PATH=$PATH:~/go/bin make sqlc`. Migrations additive only.
- SPA: `cd web && npx vitest run <files>`, then full `npx vitest run`, `npx tsc -b`, `npx eslint .`; single quotes in TS, double in JSX attributes; en + de (German "du"); files under ~400 lines.
- TDD. No Playwright, no Docker beyond the testdb harness; no push/tag/stash/reset/rebase/amend/branch switch; `git add <paths>` only (never `internal/web/dist`); no subagents; no background processes left running.
- Conventional Commits, footer exactly `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.

## Rulings (controller's calls)
- **(1, 2) One atomic, partial settings update.** `PUT /orgs/{org}/settings/assistant` takes `assistant_enabled` and `approved_runs_start_follow_ups` both optional (pointers); omitted keeps the stored value; both omitted → 422 ("nothing to change"). A new `internal/assistant` service method (e.g. `Update(ctx, orgID, Change{Enabled, StartsFollowUps *bool}) (before, after Settings, error)`) reads the current row locked and writes both fields in one transaction; the handler only maps the request, calls it, builds the audit summary from before/after (the existing wording and the `"; "` join unchanged; with nothing actually changed the summary names the requested state as today) and encodes. An older SPA that sends both fields keeps working.
- **(3) One `RetryTargets` per retry picture.** The resolver calls `Jobs.RetryTargets` once per `Picture` of a retry request and passes the result to whatever needed the second call (find both call sites in `internal/runrequests/resolve*.go`).
- **(4, 8) Ids parsed once, fail closed.** A follow-up's template id in the picture is always written from a `uuid.UUID`; an unparsable one is a bug. `Startable` and `selectFollowUps` return an error for it (wrapped, not `ErrInvalidInput` — it is an internal fault) instead of skipping it, and `selectFollowUps` returns the parsed ids alongside the follow-ups so `Approve` drops its `uuid.MustParse`.
- **(5) Error paths of the stores.** Tests for the store-backed `StartsFollowUps` (`internal/assistant`) and `ListForGroup` (`internal/chain`) when the query fails (closed pool or cancelled context against testdb, whichever the package's existing tests use) — the error is returned wrapped, never swallowed into a default.
- **(6) Audit breadth.** API tests of the settings PUT cover: follow-ups only turned on; only turned off ("approved run requests start no follow-ups"); both fields changed (the `"; "` join, order as the code writes it); nothing changed; access only (follow-ups omitted).
- **(7) SPA test gaps.** The nav item's negative assertion (no Run requests item when the user lacks the permission or access is off — whichever the current code gates on); the de strings for `too_deep`, `deleted` and the follow-ups setting appear in a de-rendered test; the follow-ups toggle while access is off (shown/disabled per current behaviour, and its PUT sends only `approved_runs_start_follow_ups`).
- **(9) Docs cross-links.** Done: both docs branches reached the docs repo's main (`c0e853e` and the assistant access pages), and the docs build reports every internal link valid. Closed in the backlog row, no code.

---

## Task 1: Server (items 1–6, 8)
**Files:** `internal/assistant/*` (+ tests, sqlc if a query is needed), `internal/api/assistant_settings_handlers.go` (+ tests), `internal/runrequests/{follow_ups.go,approve.go,resolve*.go}` (+ tests), `internal/chain` tests, OpenAPI/schema regen.
- [ ] Tests first as ruled; a concurrent pair of PUTs (one changing only access, one only follow-ups) ends with both changes stored.
- [ ] Implement, regen, lint, commit `fix(assistant): one atomic settings update that keeps what it is not given` and `fix(runrequests): parse follow-up ids once, resolve a retry's targets once` (two commits fine).

## Task 2: SPA (items 2-client, 7)
**Files:** `web/src/api/hooks/useAssistantSettings.ts`, `web/src/features/settings/AssistantCard.tsx` (+ tests), nav test, de tests.
- [ ] Tests first: the access switch PUT sends only `assistant_enabled`, the follow-ups toggle only `approved_runs_start_follow_ups`; the gaps as ruled.
- [ ] Commit `fix(web): send only the assistant setting that changes`.

## Task 3: Final
Whole-branch review, `make test`, lint, full vitest/tsc/eslint, `docs/upgrades.md` note (PUT fields optional, 422 on an empty body; older clients unaffected), API reference in the docs clone if it lists the PUT body, backlog 2.123 done, merge to local main, redeploy the local stacks.
