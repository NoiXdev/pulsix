# Backlog hardening batch (3.56, 3.61–3.64, 3.66, 3.69, 3.114) — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`) syntax.

**Goal:** close eight small, already-specified backlog items: the optional second factor's three review follow-ups, the agent heartbeat's hygiene items, dropped local checks, secret audit detail and the effective-rules filter.

**Spec:** the backlog rows themselves (`docs/BACKLOG.md`, rows 3.56, 3.61, 3.62, 3.63, 3.64, 3.66, 3.69, 3.114). Each row states the defect and the intended fix; it is the binding text for its task.

## Global Constraints
- Layers per `CLAUDE.md`: handlers decode/call/encode, services hold rules behind interfaces, SQL via sqlc (`internal/db/queries` → `make generate` or the repo's sqlc target; never hand-edit `internal/db/gen`).
- Migrations additive only (`docs/upgrades.md`). No new tables expected; if one appears, add a backup round-trip fixture row (`internal/backup`).
- TDD: failing test first. Go timestamps compared against Postgres must be truncated to microseconds.
- Run only the touched packages with `go test -race -timeout 900s ./internal/<pkg>/...` (`internal/api` with `-timeout 1800s`), never one whole-module `go test ./...`; `golangci-lint run` clean. SPA changes: `cd web && npx vitest run <files> && npx tsc -b && npx eslint .`.
- Never touch Docker containers, never push, tag, stash, reset, rebase, amend or switch branches.
- Commits: Conventional Commits, footer `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. Mark each backlog row `done` with a one-line "Done 2026-11-18: …" note in the same commit as its fix.
- User-visible wording changes go to the public docs only through the controller (report them; do not edit the docs repo).

## Task 1: Optional second factor follow-ups (3.114)
All three points of the row:
1. Clear (or never set) the password-only sign-in marker when the account has a factor by the time the session is inserted, so a later first factor does not revoke tokens created after a factor existed. Test: factor added between the factor check and the session insert → marker not set / later first enrolment keeps those tokens.
2. Org-token creation (`internal/api/token_handlers.go` around 160–171): take the account lock and re-check the session inside `WithWriteTx(orgID)` instead of an instance transaction through `org_isolation_bypass`, so RLS checks the insert. Keep the existing behaviour tests green; add one proving the insert goes through the org-scoped transaction (or that a cross-org insert is refused).
3. First enrolment from a pending (not password-only) session: refuse when the account is already enrolled and the session is not complete. Test the refusal and that a normal first enrolment still works.

## Task 2: Agent heartbeat hygiene (3.61, 3.64, 3.66)
- 3.61: `LookupAgentSerial` and `LockHostAgentsByName` select only identity columns (no `sqlc.embed`/`*`). Callers keep compiling; existing auth tests green.
- 3.64: the heartbeat writes `host_agents.local_checks` and `local_check_problems` only when they differ (`CASE WHEN … IS DISTINCT FROM …`). The saving is not observable through values, so the tests are: a store integration test that a changed report is stored and an identical one still reads back correctly (no regression), plus whatever cheap guard keeps the `IS DISTINCT FROM` from being dropped later (e.g. a test over the query text in `internal/db/queries`). Do not add triggers or schema just to observe it.
- 3.66: bound `Heartbeat.version` before storing: drop control characters (NUL included), cut to a sane maximum (reuse the existing `boundLine` helper or its rules; 64 runes unless the code already has a version bound) on a rune boundary. Table test incl. NUL, oversize, multibyte cut.

## Task 3: Dropped local checks (3.62, 3.63)
- 3.62: the manager logs the "dropped local check" warning once per change of the dropped set per agent (in-memory per-agent last-set, bounded; injected/owned by the service, no package state). Test: same dropped set over several heartbeats → one warning; changed set → one more.
- 3.63: append one bounded problem per dropped check to the stored local check problems, within the existing 32 cap, carrying the check key and the manager's fixed reason, never params. Test the cap and that params never appear. Report the user-docs wording change for "Problems with local checks" to the controller.

## Task 4: Secret audit and effective rules (3.69, 3.56)
- 3.69: the audit entry of `PUT /orgs/{org}/secrets/{name}` records whether the call created or replaced the secret (e.g. action `secret.create` vs `secret.replace`, or a `created: true|false` detail — follow how other audit entries express this). API test for both.
- 3.56: `internal/api/rule_handlers.go` `toTuples` filters with `checkplugins.AsksForItem` (item `""` always asked) so the effective-rules view leaves out items a check no longer asks for; keep the host page's `stillAsked` consistent (SPA test if it changes). API test with a mount the check no longer lists.

## Task 5: Final
Whole-branch review, `make test` (via `scripts/test-groups.sh`), `golangci-lint run`, SPA checks if touched; docs on the docs scratch branch for 3.63's wording.
