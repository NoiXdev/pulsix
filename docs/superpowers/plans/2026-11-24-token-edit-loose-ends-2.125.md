# Token edit and assistant areas: the 2.124 review's deferred loose ends (backlog 2.125) — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`) syntax.

**Goal:** close backlog row 2.125 (`grep -nE "^\| 2\.125 " docs/BACKLOG.md`): the token edit service validates and answers like the spec says, the MCP and end-to-end tests assert what they claim, the token pages read well in both languages, and spec and code agree.

**Architecture:** small changes in `internal/auth` (token edit), `internal/api` (token handlers, audit), `internal/mcp` and the assistant end-to-end suite (tests), and the SPA token pages.

**Tech Stack:** Go (huma, sqlc), React + TypeScript (Vitest).

**Spec:** `docs/superpowers/specs/2026-11-17-assistant-token-edit-design.md` and the backlog row; the owner asked to continue with the backlog on 2026-11-24. Rulings below are the controller's calls.

## Global Constraints
- Go layers per `CLAUDE.md`; targeted `go test -race -timeout 900s ./internal/<pkg>/...`, `./internal/api/...` and any e2e package with `-timeout 1800s` (the e2e package on its own invocation), never whole-module `go test ./...`; `golangci-lint run` clean; OpenAPI + `web/src/api/schema.d.ts` regenerated if the API changes (`cd web && npm run api:gen`); sqlc via `PATH=$PATH:~/go/bin make sqlc`. Migrations additive only.
- SPA: `cd web && npx vitest run <files>`, then full `npx vitest run`, `npx tsc -b`, `npx eslint .`; single quotes in TS, double in JSX attributes; en + de (German "du"); `src/ui` presentational; files under ~400 lines.
- TDD. No Playwright, no Docker beyond the testdb harness; no push/tag/stash/reset/rebase/amend/branch switch; `git add <paths>` only (never `internal/web/dist`); no subagents; no background processes left running.
- Conventional Commits, footer exactly `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.

## Rulings (controller's calls)
- **Auth (a1) unchanged-value test:** the unit test that an edit with unchanged values changes nothing compares every editable field (name, areas, expiry) and the audit outcome, not only the name.
- **Auth (a2) store scope test:** uses a real second organization (created through the test harness) rather than a random org id, so the scope check is shown against a row that exists in another org.
- **Auth (a3) empty name:** the service rejects an empty or whitespace-only name with its invalid-input sentinel (the handler's own check may stay as the first line); unit test in the service.
- **Auth (a4) expiry race:** a token that expires between the read and the conditional update answers 409 like an edit of an already expired token, not 404. The service tells "gone" from "expired" after a zero-row update by re-reading the row; test with an injected clock or a fake store that expires the token between the two calls.
- **Spec (s1) handler order:** the code's order stays (body validation, including the exclude-each-other 422, before the token lookup; so an expired token with invalid areas answers 422). Update the spec to say so.
- **Spec (s2) audit on rename:** the audit summary of an edit that renames the token names both the old and the new name (e.g. `renamed token "old" to "new"`, joined with the other changes as the code already joins them); an edit without rename keeps today's wording. Update the spec example to the code's final wording.
- **Spec (s3) per-area descriptions:** add the per-area descriptions the spec asks for: one short line per area under its label in the area grid, en and de (German "du"), from the i18n files; the write hint stays. Keep `AreaGrid` presentational.
- **MCP (m1–m6):** `get_host_metrics` / `get_host_agent` unit tests assert concrete fields of the result (values the fixture seeded), not just success; `TestAlertHandlingTools` asserts the exact activity entries (action and object) the calls produce; the secret sweep seeds the template secret before the schedule so the schedule carries it; `TestSetHostTags` shows collector-set tags survive a tool update; `allAreas` lists each area once; the sweep gains a successful `request_retry` (a failed run to retry seeded in the fixture) and checks its output for markers.
- **E2E (e1–e5):** a refused `set_group_vars` is followed by a REST read showing the vars unchanged; the local `clone` helper is replaced by `slices.Clone`; the `12.5` substring check becomes an exact assertion on the parsed value; the used-key copy assertion reads the key from the run under test, not the seed run; the MCP client in the suite has no 60 s `http.Client.Timeout` — calls get per-call context deadlines instead, so a long stream is not cut. **(e6)** the `wireScenario` flake (a `sync_repo` job queued for 60 s once) stays a record: there is no reproduction; note it in the backlog row as still open.
- **Web (w1–w4):** rewrap `AreaGrid.tsx`'s doc comment; fix `TokensPage.tsx` JSX indentation; the actions column no longer has a fixed 120px width — size it to its content (no wrap) so German labels fit; a disabled button that explains itself through `title` gets the title on a wrapper element (Firefox shows no tooltip on a disabled button) — reuse an existing wrapper pattern if the repo has one, and test that the wrapper carries the title.

---

## Task 1: Token edit service, handler audit, spec (a1–a4, s1, s2)
**Files:** `internal/auth/*` (token edit service/store + tests), `internal/api/*token*` (+ tests), `docs/superpowers/specs/2026-11-17-assistant-token-edit-design.md`.
- [ ] Tests first as ruled; implement; commit `fix(auth): validate token edits in the service, answer an expiry race with 409` (+ `docs:` commit for the spec).

## Task 2: MCP and end-to-end tests (m1–m6, e1–e5)
**Files:** `internal/mcp/*_test.go`, the assistant end-to-end suite (find it: `grep -rln "wireScenario" internal`).
- [ ] Each strengthened assertion is shown to fail against a deliberately broken value once (say how in the report); commit `test(mcp): assert what the assistant tools return`.

## Task 3: SPA token pages (s3, w1–w4)
**Files:** `web/src/features/**/AreaGrid.tsx`, `TokensPage.tsx`, i18n en/de, tests.
- [ ] Tests first for the descriptions (en and de) and the title wrapper; commit `fix(web): describe each assistant area, fit the token actions`.

## Task 4: Final
Whole-branch review, `make test`, lint, full vitest/tsc/eslint, user docs if a visible behaviour changed (the 409 on the race, the area descriptions — check the assistant access and token pages in the docs clone), `docs/upgrades.md` only if an API answer changed for clients (the 409), backlog 2.125 done (e6 kept open in a new row or noted), merge to local main, redeploy the local stacks.
