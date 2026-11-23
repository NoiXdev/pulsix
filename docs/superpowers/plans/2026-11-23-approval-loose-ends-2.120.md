# Run request approval loose ends (backlog 2.120) — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`) syntax.

**Goal:** close backlog row 2.120 (`grep -nE "^\| 2\.120 " docs/BACKLOG.md`): an approval covers what the run really uses, a request is refused when it is made rather than when it is approved, and the approval page never shows something other than what the person reviewed.

**Architecture:** the run picture (`internal/runrequests/picture.go`, built by `Resolver.Picture` in `resolve.go`) gains ids next to names and a vars digest over the real values; the service validates more at creation, caps pending requests per token, filters the list by status in SQL and exposes a pending count; the retry path joins the run path's idempotency key; the SPA reads the count, names the token and holds the reviewed fingerprint.

**Tech Stack:** Go (huma, sqlc), React + TypeScript (Vitest).

**Spec:** `docs/superpowers/specs/2026-11-16-assistant-access-design.md` ("Run requests") and the backlog row; the owner chose 2.120 on 2026-11-23. Rulings below are the controller's calls.

## Global Constraints
- Go layers per `CLAUDE.md`; targeted `go test -race -timeout 900s ./internal/<pkg>/...`, `./internal/api/...` with `-timeout 1800s`, never whole-module `go test ./...`; `golangci-lint run` clean; OpenAPI + `web/src/api/schema.d.ts` regenerated (`cd web && npm run api:gen`); sqlc via `PATH=$PATH:~/go/bin make sqlc`. Migrations additive only (next number 00119); a new table needs a backup round-trip fixture row (run `internal/backup` tests).
- Timestamps compared in tests are truncated to microseconds (Postgres precision).
- SPA: `cd web && npx vitest run <files>`, then full `npx vitest run`, `npx tsc -b`, `npx eslint .`; single quotes in TS, double in JSX attributes; en + de (German "du"); `src/ui` presentational; files under ~400 lines.
- TDD. No Playwright, no Docker beyond the testdb harness; no push/tag/stash/reset/rebase/amend/branch switch; `git add <paths>` only (never `internal/web/dist`); no subagents; no background processes left running.
- Conventional Commits, footer `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.

## Rulings (controller's calls)
- **Picture ids (item 3):** `Picture` gets `ssh_credential_id`; `PictureVault` `credential_id`; `PictureHost` `credential_id` and `jump_host_id`; `PictureRepo` `git_credential_id` and `git_credential` (name). Names stay for display. All new fields are part of the fingerprint. The picture still never carries a secret.
- **Vars digest (item 4):** the digest covers the real values of group vars, membership vars and template extra vars, secrets included, but a secret must never be recoverable from the fingerprint (it reaches the browser): secret values enter the digest only through a keyed HMAC whose key is server-side (derive it from the manager's existing secret-encryption key material, or another existing server secret — no new configuration), never as a plain hash. If the raw values are only reachable through a path that the resolver does not have, add a narrow port next to the resolver (interface in `internal/runrequests`, implementation in `internal/app`).
- **Fingerprint change across an upgrade:** a request pending across the upgrade answers "changed, review again" once. Acceptable; say so in `docs/upgrades.md`.
- **Audit revision (item 1):** `Approved` carries the approved picture's `TemplateRevision`; `approvalSummary` uses it.
- **Creation checks (item 10):** `ValidateRun` resolves the targets with the template's limit applied (as approval does with `effectiveLimit`); an empty target list → `ErrInvalidInput` ("the template's scope matches no host"); an `only_host` outside the limited set → `ErrInvalidInput`. Approval keeps its own checks (things can change in between).
- **Per-token cap (item 5):** at most 20 pending, unexpired requests per token (`MaxPendingPerToken = 20`); the 21st → new sentinel `ErrTooManyPending` mapped to 429 with a message saying to wait for decisions. Requests without a token (if any path exists) are not capped.
- **List and badge (item 5):** the status filter moves into SQL (pending = status pending and not expired, using the service's clock as a parameter), so `?status=pending` is never hidden by decided rows; the 500 cap stays per query. New route `GET /orgs/{org}/run-requests/pending-count` → `{ "count": n }` with the list's permission; the SPA badge reads it.
- **Revoked token (item 6):** hint, not a block — the approver is a person deciding the run themselves. The DTO gains `requested_by_token_name` (string, empty if the token row is gone) and `token_state` (`active` | `revoked` | `expired` | `deleted`); the detail page shows a warning for anything but `active` ("The token that asked for this run was revoked — it can no longer ask for runs. Approving still starts the run."). Read through one join or a token lookup port; no FK, no migration.
- **Approval page (item 7):** the page remembers the fingerprint the person first saw; when a refetch brings a different one (the query has already replaced the picture), it shows the existing `changed` notice, disable Approve, and offer a "Review the new version" button that accepts the current fingerprint as reviewed. Follow-up deselection resets as today.
- **Retry idempotency (item 8):** `RetryFailedInto` (and the `Enqueuer` port) take an idempotency key; the retry approval passes `"run-request:"+id` like the run approval, reserving through the same `ReserveRunGroup`/`job_idempotency_keys` path. A second approve of the same request enqueues nothing new.
- **MCP tests (items 2, 9):** the sweep seeds an SSH key credential (PEM marker), attaches a git credential to the repo, assigns the vault passphrase credential, checks the host-scoped membership secret really reaches a swept tool, and also checks the outputs of the setup calls (`request_run`, etc.) for markers; tools it skips stay skipped only with a one-line reason each. A log-capture test (slog handler into a buffer, restored after) runs a good, a revoked and a malformed-token call and asserts the token plaintext never appears; `TestEveryCallReauthenticates` gains an expired-token case.
- **extra_vars_schema (item 11):** `update_job_template`'s field description lists the field types and scopes in the same words as `set_group_schema` (share one constant).
- **List / detail (item 12):** the list shows the reason once (find the repetition — row link label plus name column — and drop one); the detail's "Requested by" shows the token's name and, under it, the token's owner.

---

## Task 1: Picture ids, real-value digest, audit revision (items 1, 3, 4)
**Files:** `internal/runrequests/{picture.go,resolve.go,service.go}` (+ tests), a raw-vars port if needed (`internal/app`), `internal/api/run_request_handlers.go` (`approvalSummary`) + test, OpenAPI/schema regen.
- [ ] Tests first: two credentials of the same name swapped on the template, a host's credential, a jump host of the same name, and the repo's git credential each change the fingerprint; changing a secret group var, a secret membership var and a secret extra var each change it; the fingerprint/picture JSON contains no secret value (assert the plaintext is absent); the audit entry of an approval after a template edit names the approved revision.
- [ ] Implement, regen, lint, commit `fix(runrequests): fingerprint ids and real values, audit the approved revision`.

## Task 2: Creation checks, per-token cap, list and count, retry idempotency, token on the DTO (items 5, 6, 8, 10, 12-server)
**Files:** `internal/runrequests/{service.go,resolve.go,store_sql.go}`, `internal/db/queries/run_requests.sql` (+ sqlc), `internal/jobs` (`RetryFailedInto`, `RunTargets` or a limited variant), `internal/api/run_request_handlers.go` (+ tests), OpenAPI/schema regen.
- [ ] Tests first: create with an only_host outside the limit → 422; template scope empty → 422; the 21st pending request of one token → 429, a decided or expired one frees a slot; 501 decided rows do not hide a pending one under `?status=pending`; pending-count route counts unexpired pending only and is org-scoped; approving a retry request twice (after a revert to pending) enqueues once; the DTO carries the token name and `token_state` for active, revoked, expired and deleted tokens.
- [ ] Implement, regen, lint, commit `fix(runrequests): refuse at request time, cap pending per token, count pending`.

## Task 3: MCP sweep, log capture, expired token, schema types (items 2, 9, 11)
**Files:** `internal/mcp/{sweep_test.go,server_test.go,fixture_test.go,tools_write.go}`.
- [ ] Extend the tests as ruled; each new marker must be shown to reach at least one swept output path (or the test says why none can) — a marker no tool could ever return proves nothing. Description change for `extra_vars_schema` with a test that the tool schema lists `secret` and `repeater`.
- [ ] Commit `test(mcp): sweep more secrets, keep tokens out of logs`.

## Task 4: SPA (items 5, 6, 7, 12)
**Files:** `web/src/api/hooks/useRunRequests.ts`, `web/src/routes/OrgLayout.tsx`, `web/src/features/runs/{RunRequestDetailPage.tsx,RunRequestsPage.tsx,runRequestColumns.tsx}` (+ tests, de test), `web/src/i18n/{en,de}/runs.ts`.
- [ ] Tests first: the badge shows the count route's number; a revoked/expired/deleted token shows its warning, an active one none; a refetch with a new fingerprint shows the notice and disables Approve until "Review the new version"; the reason appears once per list row; the detail names the token and its owner.
- [ ] Commit `fix(web): hold the reviewed run request, name its token`.

## Task 5: Final
Whole-branch review, `make test`, lint, full vitest/tsc/eslint, user docs on a docs branch (approval page: token warning, changed notice, per-token cap; API reference: pending-count, 429), `docs/upgrades.md` note (fingerprint change, new route, 429), backlog 2.120 done, merge to local main, redeploy the local stacks.
