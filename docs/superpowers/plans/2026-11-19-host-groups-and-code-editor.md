# Host groups (3.119) and code editor (3.120) — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`) syntax.

**Goal:** manage a host's group memberships from its Inventory tab; replace the playbook textarea (and other code fields) with a real code editor.

**Spec:** `docs/superpowers/specs/2026-11-19-host-groups-and-code-editor-design.md` (binding).

## Global Constraints
- SPA only (`web/`); `src/ui` presentational (no value import from `src/api`); files under ~400 lines.
- `cd web && npx vitest run && npx tsc -b && npx eslint .` pass; single quotes in TS, double in JSX attributes; en + de copy (German "du").
- New npm packages: exact versions, each published at least 14 days before 2026-11-19 (check with `npm view <pkg> time`), no ranges; `npm install --save-exact`; commit `package.json` and `package-lock.json`.
- Never run Playwright except in Task 3; never touch Docker; no push/tag/stash/reset/rebase/amend/branch switch; `git add <paths>` only; no subagents; no background processes left running.
- Commits: Conventional Commits, footer `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. Add backlog rows 3.119 and 3.120 (status done, "Done 2026-11-19: …") in the task that finishes each.

## Task 1: Host Inventory tab manages groups (spec §1)
Reuse `web/src/features/inventory/sections/AssignHostDialog.tsx` with the host or the group as the fixed side (new group picker modelled on `ManagedHostPicker`), `useAssignHost`/`useUnassignHost` (`web/src/api/hooks/useInventory.ts`) and the group page's permission rule. Tests: writer sees Add/Edit/Remove and the dialog offers only groups the host is not in (no `all`); assign posts to the chosen group and invalidates the host's groups; remove confirms then unassigns; reader sees no actions; membership problems shown; inherited rows (if built) muted without actions.

## Task 2: CodeEditor and the playbook editor (spec §2)
`web/src/ui/code-editor.tsx` (lazy CodeMirror 6), playbook dialog switched (taller, full-screen toggle, Mod-S, unsaved-changes confirm, lint marker if line available), then the other code fields per spec. Tests per spec; adapt existing tests.

## Task 3: Final
Whole-branch review, full vitest/tsc/eslint, e2e (`npx playwright test` in web), bundle size check (`npm run build`, report main chunk before/after), public docs (host page Inventory tab, playbook editor) on the docs scratch branch.
