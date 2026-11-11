# Confirm dialog Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace every native `window.confirm()` in the SPA with a Pulsix confirmation dialog.

**Architecture:**
- A `ConfirmProvider` near the app root renders one `Dialog` (`web/src/ui/dialog.tsx`).
- A `useConfirm()` hook returns `confirm(options): Promise<boolean>`. Each call site turns `if (!confirm(msg)) return;` into `if (!(await confirm({...}))) return;`.
- The two router blockers use the same promise, because TanStack Router's `shouldBlockFn` may return a `Promise<boolean>`.

**Tech Stack:** React 19, TanStack Router 1.170.46, Vitest + Testing Library.

**Spec:** Owner request in chat on 2026-11-11: "alle Lösch- oder Bestätigungsdialoge mit einem eigenen Dialog versehen, keine nativen JS-Alerts".

## Global Constraints

- `web/src/ui` stays presentational and never imports a value from `src/api`.
- Every string comes from the i18n catalogue, in both en and de.
- Afterwards no `confirm(`, `alert(` or `prompt(` call on `window` (or bare) is left in `web/src` outside tests. Verify with:
  `grep -rnE '\b(window\.)?(confirm|alert|prompt)\(' web/src | grep -v '\.test\.'`
  The hook's own `confirm` variable is named so it does not match; use `ask` or `confirmAction`.
- Checks from `web/`, all clean before each commit:
  - `npx tsc --noEmit -p .`
  - `npx eslint src`
  - `npx vitest run`
- Conventional Commits with the footer `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. Never push.

## Review Focus

1. Keyboard:
   - The dialog opens with focus on **Cancel** (the safe choice). For a destructive confirm, Enter must not delete by accident.
   - Escape and the close button resolve `false`.
   - Focus returns to the element that opened it.
2. Destructive confirms (delete, revoke, remove) show the confirm button in the destructive variant. A neutral one (switch a rule's kind, pause all, run a test) uses primary.
3. Only one dialog at a time. A second `confirm()` while one is open waits for the first, or replaces it with the first resolving `false`; it never leaks a promise that is never resolved.
4. The unsaved-changes guard still blocks a navigation, and leaving after a confirm goes to the intended target. A tab close or reload keeps the browser's own beforeunload prompt; nothing else can do that.
5. A confirm opened from inside another dialog (RepoDialog, TemplateDialog, CredentialDialog, TriggersDialog, RuleDialog, ItemDecisionDialog) stacks above it. Escape closes only the confirm (see `dialog.nesting.test.tsx`).

---

### Task 1: ConfirmProvider and useConfirm

**Files:**
- Create: `web/src/ui/confirm.tsx` (provider, hook and context; split into `confirmContext.ts` if react-refresh lint requires it, as `toast.tsx` and `toastContext.ts` do)
- Create: `web/src/ui/confirm.test.tsx`
- Modify: the app root that mounts `ToastProvider` (see `web/src/main.tsx`) so it also mounts `ConfirmProvider`, and any test render helper that wraps providers, so feature tests get it
- Modify: `web/src/ui/useUnsavedGuard.ts`, `web/src/features/users/useAccountActionRunner.ts` (async `shouldBlockFn` via the hook)
- Modify: i18n en/de `ui` catalogue: default title, Cancel, Confirm

**Interface:**

```ts
interface ConfirmOptions {
  message: string;
  title?: string;          // default: t.ui.confirm.title ("Are you sure?" / "Bist du sicher?")
  confirmLabel?: string;   // default: t.ui.confirm.confirm
  destructive?: boolean;   // confirm button variant
}
function useConfirm(): (options: ConfirmOptions) => Promise<boolean>;
```

- [ ] **Step 1: Write failing tests.**
  - `true` on Confirm, `false` on Cancel, Escape and the close button.
  - Focus is on Cancel when it opens and returns to the opener afterwards.
  - With `destructive`, the confirm button has the destructive variant.
  - A second call while one is open does not leave a promise unresolved.
  - A confirm opened inside an open `Dialog` closes on Escape without closing the outer dialog.
  - `useUnsavedGuard`: a navigation away from a dirty form asks in the dialog. Cancel stays on the page; Confirm leaves to the target.
- [ ] **Step 2:** Run them. Expected: FAIL.
- [ ] **Step 3:** Implement on top of the existing `Dialog`.
- [ ] **Step 4:** Run the full checks. Expected: PASS.
- [ ] **Step 5:** Commit `feat(web): a confirmation dialog instead of the browser's confirm`.

### Task 2: Every call site

**Files:** every file the grep in Global Constraints lists. There are 36 call sites in 30 files, among them:
- `RepoDialog`, `TriggersDialog` (3), `ItemDecisionDialog`, `ItemsPage`, `RulesPage`, `ChecksPage`, `MonitoringGroupsPage`, `RuleDialog` (2)
- `AgentsPage`, `PlaybookEditor`, `ScheduleEditor`, `SchedulesPage`, `GroupSidebar`, `MemberRow`
- `BackupList`, `instance/shared/useDeleteWithConfirm.ts`, `TemplateDialog`, `TemplateVariablesPage`
- `WorkersPage` (2), `MemberPage`, `PendingInvites`, `MembersPage`, `MaintenancePage`
- `useHostActions`, `HostAgentCard`, `TokensPage`, `AlertRoutesPage`, `RotationsPage`, `ChannelsPage`
- `CredentialDialog` (3), `JumpHostsPage`

Rules for each site:
- Keep the existing message text as `message`.
- Set `destructive` for delete, revoke and remove.
- Give a specific `confirmLabel` where the catalogue already has the action's word, such as the site's own Delete or Revoke label. Otherwise use the default.
- A title is optional.
- Keep the behaviour after a confirm unchanged.
- Update each feature test that stubs `window.confirm` (`vi.stubGlobal('confirm', …)`) to click the dialog's button instead.

- [ ] **Step 1:** Convert all sites and their tests.
- [ ] **Step 2:** Run the full checks and the grep. Expected: PASS and no matches.
- [ ] **Step 3:** Commit `refactor(web): use the confirmation dialog everywhere`.
