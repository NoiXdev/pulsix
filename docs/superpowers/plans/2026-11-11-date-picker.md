# Date and time pickers Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the browser's native date, datetime and time inputs across the SPA with three shared Pulsix components: a calendar popover for dates, the same with a time field, and a 24-hour time field.

**Architecture:** Three presentational components in `web/src/ui` (no data fetching). The calendar grid is `react-day-picker` 10.0.2, styled with the app's Tailwind tokens. Each component reads and writes exactly the string format of the native input it replaces, so each call site only swaps the element.

**Tech Stack:** React 19, TypeScript, Tailwind, Vitest + Testing Library, `react-day-picker@10.0.2` (brings `date-fns`).

**Spec:** Owner-approved in chat on 2026-11-11.
- Three components:
  - `DatePicker`: a field with a calendar popover (month view, arrow-key navigation, a "Today" button, Pulsix styling).
  - `DateTimePicker`: the same calendar plus a time field.
  - `TimeField`: always 24-hour `HH:MM`.
- Month and weekday names follow the user's language (en/de). The week starts on Monday in German.
- Values keep today's formats (`YYYY-MM-DD`, `YYYY-MM-DDTHH:mm`, `HH:mm`) in browser local time.
- All 11 call sites are switched.

## Global Constraints

- SPA layering (CLAUDE.md): `web/src/ui` holds presentational components and never imports a value from `src/api`. ESLint enforces this.
- All user-visible strings come from the i18n catalogue, in en and de (`web/src/i18n/{en,de}`). de must satisfy the en-derived `Messages` type.
- Exact dependency: `react-day-picker@10.0.2`, pinned without a caret, the same way `web/package.json` pins its others. Check how it pins and follow that. `package-lock.json` is updated.
- Value contracts:

  | Component | Value | Empty |
  |---|---|---|
  | `DatePicker` | `'YYYY-MM-DD'` | `''` |
  | `DateTimePicker` | `'YYYY-MM-DDTHH:mm'` | `''` |
  | `TimeField` | `'HH:mm'` (00–23, 00–59) | `''` |

  The empty value means nothing is chosen. Local time, no time zone conversion.
- Week start: Monday for `de`, Sunday for `en`. Use the locale from `useLocale()` (`web/src/i18n/useT.ts`).
- Checks from `web/`, all clean before every commit:
  - `npx tsc --noEmit -p .`
  - `npx eslint src`
  - `npx vitest run`
- Conventional Commits, footer `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. Never push.

## Review Focus

1. Keyboard-only use. Tab into the field, open the calendar with Enter or Space, move with the arrows, pick with Enter, and close with Escape back on the field. Escape must not close the surrounding dialog (see `ui/dialog.escape.test.tsx`).
2. A popover inside a scrolling dialog stays usable. It is positioned below the field (or above when there is no room) and not clipped by the dialog's overflow.
3. Typing a time: `9:5`, `0930`, `25:00` and an empty field. A partial or invalid entry never emits a malformed value. It emits `''` or keeps the last valid one, and shows the field invalid.
4. `required` still blocks a form submit while the value is empty, as the native inputs did (WindowDialog, RotationDialog).
5. An existing value round-trips unchanged. Opening and closing the picker without choosing anything does not alter or re-format it.

---

### Task 1: The three components

**Files:**
- Create: `web/src/ui/DatePicker.tsx`, `web/src/ui/DateTimePicker.tsx`, `web/src/ui/TimeField.tsx` (a shared popover or calendar helper file in `web/src/ui` if useful)
- Test: `web/src/ui/DatePicker.test.tsx`, `web/src/ui/DateTimePicker.test.tsx`, `web/src/ui/TimeField.test.tsx`
- Modify: `web/package.json`, `web/package-lock.json`, `web/src/i18n/en/ui.ts`, `web/src/i18n/de/ui.ts` (or the catalogue file the other `ui` strings live in)

**Interfaces (produced, used by Task 2):**

```ts
interface DatePickerProps {
  value: string;
  onChange: (value: string) => void;
  id?: string;
  'aria-label'?: string;
  required?: boolean;
  disabled?: boolean;
  min?: string;
  max?: string;
  placeholder?: string;
}
// DateTimePicker: the same props, with values in 'YYYY-MM-DDTHH:mm'.
interface TimeFieldProps {
  value: string;
  onChange: (value: string) => void;
  id?: string;
  'aria-label'?: string;
  required?: boolean;
  disabled?: boolean;
}
```

`id` lands on the focusable trigger or input, so an existing `<Label htmlFor>` keeps working. `required` must take part in native form validation; a hidden or readonly input carrying the value and `required` is acceptable.

- [ ] **Step 1: Write failing tests**
  - `DatePicker`:
    - shows `value` formatted for the locale (de `11.11.2026`, en `11/11/2026` or the locale's short date) and `placeholder` when empty;
    - opens on click, Enter and Space;
    - picking a day calls `onChange('2026-11-14')`;
    - "Today" picks today;
    - Escape closes and returns focus to the trigger;
    - the de week row starts with Monday ("Mo"), en with Sunday ("Su");
    - days outside `min`/`max` are disabled;
    - a form with `required` and an empty value does not submit.
  - `DateTimePicker`:
    - picking a day keeps the time part;
    - changing the time keeps the date part;
    - with an empty value, picking a day yields that day at `00:00`;
    - the value round-trips unchanged.
  - `TimeField`:
    - typing `0930` or `9:30` gives `'09:30'`;
    - `25:00` gives no change and marks the field invalid;
    - clearing gives `''`;
    - `value` `'14:05'` displays `14:05` regardless of locale.
- [ ] **Step 2: Run** `npx vitest run src/ui/DatePicker.test.tsx src/ui/DateTimePicker.test.tsx src/ui/TimeField.test.tsx`. Expected: FAIL.
- [ ] **Step 3: Install and implement.**
  - Install: `npm install --save-exact react-day-picker@10.0.2`.
  - Build the components with the `Input` and `Button` styles. The calendar uses the app's tokens: background `bg-card`, border `border-border`, selected day `bg-primary text-primary-foreground`, today ringed.
  - Pass the locale (`date-fns/locale` `de` or `enUS`) and `weekStartsOn`.
  - Close the popover on an outside click and on Escape, stopping Escape from reaching the dialog.
- [ ] **Step 4: Run** the three test files, then the full checks. Expected: PASS.
- [ ] **Step 5: Commit** `feat(web): date, date-time and time picker components`.

### Task 2: Use them everywhere

**Files (each swaps its native input for the matching component and keeps its state handling):**

| File | Native input | Replaced by |
|---|---|---|
| `web/src/features/alerts/SnoozeDialog.tsx:73` | datetime-local | `DateTimePicker` |
| `web/src/features/maintenance/WindowDialog.tsx:150,154` | datetime-local | `DateTimePicker`, required |
| `web/src/features/notifications/RotationsPage.tsx:293,297` | datetime-local | `DateTimePicker` |
| `web/src/features/notifications/RotationDialog.tsx:133` | datetime-local | `DateTimePicker`, required |
| `web/src/features/credentials/AddCredentialDialog.tsx:173` | date | `DatePicker` |
| `web/src/features/schedules/ScheduleEditor.tsx:254` | time | `TimeField` |
| `web/src/features/notifications/DigestCard.tsx:85` | time | `TimeField` |
| `web/src/features/notifications/RouteDialog.tsx:291,301` | time | `TimeField` |

- Keep `min` where the native input had one (SnoozeDialog's must stay in the future, if it set `min`).
- Comments in `features/maintenance/time.ts` and `features/notifications/alertRoute.ts` that name the native input are updated to name the component; behaviour stays.

- [ ] **Step 1:** Swap each site. Update that feature's existing tests where they type into a native input (`user.type` on a datetime-local); drive the component instead.
- [ ] **Step 2:** Run `npx vitest run` and the full checks. Expected: PASS, with no `type="date"`, `type="datetime-local"` or `type="time"` left outside `src/ui`. Verify with `grep -rn 'type="date\|datetime-local\|type="time"' src --include=*.tsx`.
- [ ] **Step 3:** Commit `feat(web): use the shared pickers for every date and time field`.
