# Job template detail page with tabs (backlog 1.53) — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`) syntax.

**Goal:** backlog 1.53 (`grep -n "^| 1.53 " docs/BACKLOG.md` on the other branch; the row text is quoted below). A page per job template, like the host page, replacing the template dialog as the place to see and manage a template.

**Spec:** the owner-approved mock https://claude.ai/artifact/Jwrod7fCDoomq7Keh3AFrE (2026-11-26) and this row: "a page per job template instead of the dialog — header with name, description, badges (enrols agents, follow-ups), playbook, repository/branch, last synced commit, target hosts and the actions Run, Dry run, Edit, more; a notice when an assistant's change paused its schedules/triggers (link to the review); tabs Overview (recent runs, what it starts, open requests), Variables (the existing template variables page; inline edit; assistant-written values marked with *Accept as live*), Schedules (this template's schedules: status incl. paused for review, edit, pause, create), Triggers (one card per webhook: URL, sender, timestamp, allowed sources, on-active; edit, new secret, deliveries, create), Runs (history with trigger and commit) and Settings (the template form inline with edit/cancel/save). The templates list links rows to it."

**Architecture:** SPA only, `web/src/features/templates` (+ a route in `web/src/routes`), mirroring the host page (`web/src/features/hosts/HostDetailPage.tsx`, `HostTabs.tsx`, `hostTabs.ts`: tab in `?tab=`, one tab panel). Reuse what exists: `TemplateVariablesPage` (variables tab), the template form inside `TemplateDialog` (extract the form so the dialog and the Settings tab share it), the schedule and trigger dialogs/lists (filtered with the existing `template_id` query on `/schedules`, `/triggers`, `/jobs`), `useRunTemplate` (Run with the enrolment confirmation), the playbook dry-run entry, run requests list (filter by template if the API has it; else filter client-side on the pending list and say so).

**Tech Stack:** React/TypeScript, TanStack Router/Query, Vitest; Go only if a small read is missing (then: `internal/api` handler + test, `npm run api:gen`).

## Global Constraints
- SPA layering per `CLAUDE.md` (`src/api` → `src/features` → `src/ui`; ESLint enforces it). `cd web && npm run lint && npx tsc -b && npx vitest run <files>`; en and de texts for every string (i18n catalogue test).
- The page must keep every behaviour the dialog has today (validation, conflicts/If-Match, enrolment confirmation, follow-ups, vault identities, credential creation inline).
- Go changes (only if unavoidable): targeted `go test -race -timeout 900s` / api `-timeout 1800s`, never `go test ./...`; `golangci-lint run`.
- TDD for new logic (tab parsing, data derivations); component tests for each tab. No Playwright.
- `node_modules` in this worktree is a symlink to the main worktree's — do not run `npm install`.
- No push/tag/stash/reset/rebase/amend/branch switch, no `git checkout -p`; `git add <paths>` only (never `internal/web/dist`, never `.superpowers`, never untracked plan files); no subagents; no background processes left running; do NOT run `make test` (another branch runs it on this machine).
- Conventional Commits, footer exactly `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.

## Rulings
- Route `/o/$org/templates/$id` with `?tab=` (`overview` default, `variables`, `schedules`, `triggers`, `runs`, `settings`); the existing template variables route redirects to `?tab=variables`. Templates list: the row name links to the page; the row menu keeps Run and adds Open.
- Header and Overview read only existing APIs; "paused for review" notice: when any of this template's schedules/triggers has the assistant-change reason (field present on the other branch, not on main — render the notice only when the field exists in the response; keep it behind a type-safe optional check so it lights up after the merge).
- Settings tab: the same form component as the dialog, read-only until Edit, then Cancel/Save; Save uses the same mutation and If-Match handling. The dialog remains for creating a template.
- Assistant-written variable marks / Accept as live come from the other branch (2.136) — not in this task; the variables tab embeds the existing page unchanged.

---

## Task 1: Route, header, tabs, Overview and Settings
- [ ] Commit `feat(web): a job template has its own page with tabs`.

## Task 2: Variables, Schedules, Triggers and Runs tabs
- [ ] Commit `feat(web): manage a template's schedules, triggers and runs on its page`.

## Task 3: Final
Whole-branch review, SPA lint/tsc/full vitest, user docs in the docs clone (playbooks-and-jobs page: the template page), backlog 1.53 done (on merge), merge to local main after 2.136, redeploy.
