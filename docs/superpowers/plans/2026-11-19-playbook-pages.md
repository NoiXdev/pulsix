# Playbook pages, clickable rows, resend invitation — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`) syntax.

**Goal:** playbooks get create/edit pages with tabs; every list row opens its item; pending invitations can be resent.

**Spec:** `docs/superpowers/specs/2026-11-19-playbook-pages-design.md` (binding). Mockup https://claude.ai/artifact/T9Mr66pttSSspSkqgyF2Bv.

## Global Constraints
- Go: layers per `CLAUDE.md`; sqlc via `PATH=$PATH:~/go/bin make sqlc`; migrations additive only; targeted `go test -race -timeout 900s ./internal/<pkg>/...`, `./internal/api/...` with `-timeout 1800s`, never whole-module `go test ./...`; `golangci-lint run` clean; OpenAPI + `web/src/api/schema.d.ts` regenerated (`cd web && npm run api:gen`).
- SPA: `cd web && npx vitest run && npx tsc -b && npx eslint .`; single quotes in TS, double in JSX attributes; en + de (German "du"); `src/ui` presentational; files under ~400 lines.
- TDD. No Playwright except Task 5; no Docker; no push/tag/stash/reset/rebase/amend/branch switch; `git add <paths>` only (never `internal/web/dist`); no subagents; no background processes left running.
- Conventional Commits, footer `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. Backlog rows 3.123–3.125 marked done in the finishing task; 3.126 added as planned in Task 4.

## Task 1: Server — runs by playbook, resend invitation (spec §2, §4)
`playbook_id` filter on the runs list; `POST /orgs/{org}/invites/{inviteId}/resend`. Tests: filter returns only runs of templates using the playbook, cross-org id returns none, invalid id 400 like template_id; resend rotates the token (old token no longer accepted, new one is), resets expiry, sends mail (fake mailer), audits, works for expired, 404 for accepted/revoked, permission like create. Mark 3.125 server part in the commit message; the row is marked done in Task 2.

## Task 2: SPA — clickable rows everywhere and resend (spec §3, §4 SPA)
`onRowActivate` on `DataList`, then every list per spec §3; Resend invitation in the pending invitations list. Tests: DataList activation (click, Enter, cell links still work, context menu unchanged); one test per converted list that the row opens its page/dialog; resend calls the endpoint and toasts. Mark 3.124 and 3.125 done.

## Task 3: SPA — playbook pages (spec §1)
Routes, header, tabs, editor tab with side column (check, variables helper, latest runs), runs tab (server filter), used-by tab, new page with source choice and starters, read-only repository playbooks, unsaved-changes blocker, Mod-S; remove the dialog; list rows → page. Adapt e2e specs that open the playbook dialog (`web/e2e/codeEditor.ts` helper, ansible/chain/variables specs) — do not run them. Tests per spec. Mark 3.123 done.

## Task 4: Backlog row 3.126 (dry run + outline, planned) — folded into Task 3's commit.

## Task 5: Final
Whole-branch review, `make test`, lint, full vitest, full e2e, docs (playbooks page, lists, members/invitations) on the docs scratch branch, merge to local main and redeploy stacks.
