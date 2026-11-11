/**
 * The scheduler's calendar (spec 2026-09-18 §8, mockup "Week · 8–14 Sep"):
 * the week, month and list views, moving the range, and a maintenance
 * window showing up in all three. Backlog 2.78 — ScheduleCalendar.test.tsx
 * covers the grid maths against fixed data; this is the browser against a
 * real range request.
 *
 * The thing on the calendar is a maintenance window, not a schedule: a
 * schedule needs a job template, and this suite's organization has none
 * of its own — a window needs nothing but itself, and it is drawn by all
 * three views, so it exercises the same range request each of them
 * makes. The window is created through the Maintenance page rather than
 * seeded, so this also pins that the two pages agree on what a window
 * covers.
 *
 * Why a one-off window over "now": the grid buckets by the BROWSER's zone
 * (the header says so — backlog 2.88), so a fixed wall-clock time would
 * land in a different cell for every runner. A window that starts an hour
 * ago and ends in three is inside today's column wherever this runs, and
 * today is inside the week the calendar opens on.
 */
import { test, expect } from '@playwright/test';
import { ADMIN_STORAGE_STATE, ORG } from './env.ts';
import { pickDateTime } from './pickDateTime.ts';

test.use({ storageState: ADMIN_STORAGE_STATE });

const WINDOW_NAME = 'e2e-calendar-window';

test('the calendar shows a maintenance window across its three views and moves its range', async ({ page }) => {
  const now = new Date();
  const from = new Date(now.getTime() - 60 * 60 * 1000);
  const until = new Date(now.getTime() + 3 * 60 * 60 * 1000);

  // --- A window the calendar can draw ----------------------------------
  await page.goto(`/o/${ORG.slug}/maintenance`);
  await page.getByRole('button', { name: 'New window' }).click();
  const dialog = page.getByRole('dialog');
  await dialog.getByLabel('Name').fill(WINDOW_NAME);
  // Through the calendar, which also handles "an hour ago" being yesterday
  // (or last month) for a run just after midnight.
  await pickDateTime(dialog, 'From', from);
  await pickDateTime(dialog, 'Until', until);
  await dialog.getByRole('button', { name: 'Save' }).click();
  await expect(dialog).toHaveCount(0);

  // --- The calendar, on the schedules page ------------------------------
  await page.goto(`/o/${ORG.slug}/schedules`);
  const calendar = page.getByRole('region', { name: 'Calendar' });
  await expect(calendar).toBeVisible();
  await expect(calendar.getByRole('tab', { name: 'week' })).toHaveAttribute('aria-selected', 'true');
  // The grid's own zone is named on screen rather than left implicit.
  await expect(calendar.getByText(/^Times in /)).toBeVisible();

  // Week: the window is a bar in today's column.
  await expect(calendar.getByText(`Maintenance · ${WINDOW_NAME}`).first()).toBeVisible();

  // --- Month ------------------------------------------------------------
  await calendar.getByRole('tab', { name: 'month' }).click();
  await expect(calendar.getByRole('tab', { name: 'month' })).toHaveAttribute('aria-selected', 'true');
  const pad = (n: number) => String(n).padStart(2, '0');
  const todayKey = `${now.getFullYear()}-${pad(now.getMonth() + 1)}-${pad(now.getDate())}`;
  // The month cell marks a covered day with a dot, not with the name.
  await expect(calendar.locator(`[data-day="${todayKey}"] [aria-label="maintenance"]`)).toBeVisible();

  // --- List -------------------------------------------------------------
  await calendar.getByRole('tab', { name: 'list' }).click();
  await expect(calendar.locator('[data-list-row]').filter({ hasText: WINDOW_NAME })).toHaveCount(1);

  // --- Moving the range leaves it behind --------------------------------
  // Back to the week view, whose title names the range, so "Next" can be
  // read as a changed range rather than inferred from what disappeared.
  await calendar.getByRole('tab', { name: 'week' }).click();
  const title = calendar.getByRole('heading', { level: 2 });
  const thisWeek = await title.textContent();
  await calendar.getByRole('button', { name: 'Next' }).click();
  await expect(title).not.toHaveText(thisWeek ?? '');
  await expect(calendar.getByText(`Maintenance · ${WINDOW_NAME}`)).toHaveCount(0);
  await calendar.getByRole('button', { name: 'Previous' }).click();
  await expect(title).toHaveText(thisWeek ?? '');
  await expect(calendar.getByText(`Maintenance · ${WINDOW_NAME}`).first()).toBeVisible();

  // --- Clean up ----------------------------------------------------------
  await page.goto(`/o/${ORG.slug}/maintenance`);
  page.once('dialog', (d) => void d.accept());
  await page.getByRole('button', { name: `Delete ${WINDOW_NAME}` }).click();
  await expect(page.getByRole('button', { name: `Delete ${WINDOW_NAME}` })).toHaveCount(0);
});
