# Small items: a flaky fairness test, optional-factor session details, dialog Escape, code editor minors (backlog 3.65, 3.113, 3.121, 3.122) — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`) syntax.

**Goal:** close backlog rows 3.65, 3.113, 3.121 and 3.122 (`grep -nE "^\| (3\.65|3\.113|3\.121|3\.122) " docs/BACKLOG.md`).

**Architecture:** independent fixes: `internal/probes` (fair-share test or hand-off), `internal/auth`/accounts (session count, sign-in stamp), and the SPA (fields dialog, `CodeEditor`).

**Tech Stack:** Go (sqlc), React + TypeScript (Vitest).

**Spec:** the backlog rows themselves; the owner asked to continue with the backlog on 2026-11-24. Rulings below are the controller's calls.

## Global Constraints
- Go layers per `CLAUDE.md`; targeted `go test -race -timeout 900s ./internal/<pkg>/...`, `./internal/api/...` with `-timeout 1800s`, never whole-module `go test ./...`; `golangci-lint run` clean; sqlc via `PATH=$PATH:~/go/bin make sqlc`. Migrations additive only.
- SPA: `cd web && npx vitest run <files>`, then full `npx vitest run`, `npx tsc -b`, `npx eslint .`; single quotes in TS, double in JSX attributes; en + de (German "du"); `src/ui` presentational; files under ~400 lines.
- TDD. No Playwright, no Docker beyond the testdb harness; no push/tag/stash/reset/rebase/amend/branch switch; `git add <paths>` only (never `internal/web/dist`, never `.superpowers`); no subagents; no background processes left running.
- Conventional Commits, footer exactly `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.

## Rulings (controller's calls)
- **3.65 flaky fairness test.** Find the race first (the failure reads "A started 2 more probes into the freed slot while B waited"; `internal/probes/fairshare_internal_test.go` `TestOrgCannotStarveAnother`). Decide from the evidence whether the scheduler's hand-off itself can let A take a freed slot while B is already waiting (a real fairness bug: fix the hand-off) or whether only the test observes too early (fix the test with a deterministic sync point — never a longer sleep). Reproduce under load before and after (e.g. `-race -count=50` with `-cpu=1,2` or under `stress`-style parallel runs of the package) and report the numbers.
- **3.113 (1) session count.** The admin user list's `active_sessions` must not count a password-only session that a factor has superseded. Find what "superseded" leaves in the `sessions` table under `PULSIX_MFA_OPTIONAL` (a re-verify may mint a new row or flip the old one) and count exactly the sessions a person could still use; test with the mode on and off.
- **3.113 (2) sign-in stamp.** Completing a second factor on such a session (TOTP verify, and any other factor verify path with the same shape) stamps `users.last_sign_in_at`, like a sign-in; test it.
- **3.121 fields dialog Escape.** As the row says: ask on the Escape keydown (`preventDefault`, then the unsaved-changes question), keep `onCancel` as the fallback, follow `HostSshForm`'s pattern; test that repeated Escapes fire no `cancel` event and never close the dialog with edits.
- **3.122 (2) oversize paste.** `CodeEditor`'s `maxLength` truncates an oversize paste or insert to the room left, like a textarea, instead of refusing it; update the doc comment; test.
- **3.122 (3) label focus.** Clicking the visible label of the ad-hoc command (`AdhocDialog.tsx`) and of the schedule's extra vars (`ScheduleEditor.tsx`) focuses the editor: `CodeEditor` exposes `focus()` through a ref (or an equivalent prop), the label gets an `onClick` that calls it; test both.

---

## Task 1: 3.65 flaky fairness test
**Files:** `internal/probes/*` .
- [ ] Root cause with evidence; fix; before/after repetition numbers in the report; commit `fix(probes): …` or `test(probes): …` per what the cause was.

## Task 2: 3.113 optional-factor session details
**Files:** `internal/db/queries/accounts.sql` (+ sqlc), `internal/auth/*` (+ tests), API tests if the admin list is asserted there.
- [ ] Tests first; commit `fix(auth): count usable sessions, stamp a sign-in completed by a second factor`.

## Task 3: SPA (3.121, 3.122 (2)(3))
**Files:** `web/src/features/inventory/fields/FieldsDialog.tsx`, `web/src/ui/code-editor.tsx`, `AdhocDialog.tsx`, `ScheduleEditor.tsx` (+ tests).
- [ ] Tests first; commit `fix(web): ask before Escape closes the fields dialog, code editor paste and label focus`.

## Task 4: Final
Whole-branch review, `make test`, lint, full vitest/tsc/eslint, backlog rows done, user docs only if visible behaviour changed (likely none beyond the changelog), merge to local main, redeploy.
