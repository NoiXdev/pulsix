import { expect, type Locator, type Page } from '@playwright/test';

const pad = (n: number) => String(n).padStart(2, '0');

const MONTHS = Array.from({ length: 12 }, (_, i) =>
  new Intl.DateTimeFormat('en-US', { month: 'long' }).format(new Date(2000, i, 1)),
);

function ordinal(n: number): string {
  if (n % 100 >= 11 && n % 100 <= 13) return `${n}th`;
  return `${n}${['th', 'st', 'nd', 'rd'][n % 10] ?? 'th'}`;
}

/** The calendar's own name for a day button, as react-day-picker writes it
 * in English ("Wednesday, November 11th, 2026"), with the "Today, " and
 * ", selected" it adds for those days allowed around it. */
function dayName(d: Date): RegExp {
  const weekday = new Intl.DateTimeFormat('en-US', { weekday: 'long' }).format(d);
  const core = `${weekday}, ${MONTHS[d.getMonth()]} ${ordinal(d.getDate())}, ${d.getFullYear()}`;
  return new RegExp(`^(Today, )?${core}(, selected)?$`);
}

/** "November 2026", the month grid's accessible name, as a month count. */
function monthIndex(gridName: string): number {
  const [month, year] = gridName.trim().split(/\s+/);
  const m = MONTHS.indexOf(month);
  if (m === -1 || !/^\d{4}$/.test(year ?? '')) throw new Error(`pickDateTime: unreadable month "${gridName}"`);
  return Number(year) * 12 + m;
}

/**
 * The Playwright half of src/test/pickDateTime.ts: set a ui/DateTimePicker
 * to `at` (local time, minute precision) the way a person does. Replaces
 * `getByLabel(label).fill('YYYY-MM-DDTHH:mm')`, which only drove the native
 * datetime-local input.
 *
 * It opens the calendar behind the field labelled `label`, steps through
 * the months with the calendar's own Previous/Next buttons until `at`'s
 * month is shown (whichever month it opened on: the field's day, or
 * today), picks the day by its accessible name, then types the time into
 * the time box, which is named after the field ("From Time").
 *
 * `scope` is a page or a dialog locator, like pickOption's. The calendar
 * is a dialog of its own (inside the surrounding one), so it is looked up
 * on the page by its name rather than inside `scope`.
 */
export async function pickDateTime(scope: Page | Locator, label: string, at: Date): Promise<void> {
  const page = 'goto' in scope ? scope : scope.page();
  await scope.getByRole('button', { name: label, exact: true }).click();
  const calendar = page.getByRole('dialog', { name: 'Choose a date' });
  const grid = calendar.getByRole('grid');
  await expect(grid).toBeVisible();

  const target = at.getFullYear() * 12 + at.getMonth();
  // A bound, so a calendar that refuses to move fails here, not at the
  // test's timeout.
  for (let step = 0; step < 240; step++) {
    const shown = (await grid.getAttribute('aria-label')) ?? '';
    const diff = target - monthIndex(shown);
    if (diff === 0) break;
    await calendar.getByRole('button', { name: diff > 0 ? 'Go to the Next Month' : 'Go to the Previous Month' }).click();
    await expect(grid).not.toHaveAttribute('aria-label', shown);
  }

  await calendar.getByRole('button', { name: dayName(at) }).click();
  await expect(calendar).toHaveCount(0);

  const time = scope.getByRole('textbox', { name: `${label} Time`, exact: true });
  await time.fill(`${pad(at.getHours())}:${pad(at.getMinutes())}`);
  // Leaving the field commits it; Enter would also submit the form.
  await time.blur();
}
