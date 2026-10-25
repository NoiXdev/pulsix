/**
 * The Maintenance page (spec 2026-09-18 §6, v0.4a decision 3): an admin
 * creates a one-off maintenance window, sees it listed with the reason
 * that distinguishes it from a silence, edits it into a silence, and
 * deletes it. Backlog 2.78 — Vitest and the Go side cover these paths
 * today; this is the browser's own account of them.
 *
 * The reason is what this spec is really about. The two look alike in a
 * list and do entirely different things: a maintenance window lets
 * schedules skip the hosts it covers, a silence only mutes their alerts.
 * The v0.4a final review found the dialog dropping `reason` on save, so
 * every edit silently turned a silence back into a maintenance window —
 * un-muting alerts and newly pausing scheduled runs, with nothing on
 * screen to say so. The round trip below is that bug's shape: save, read
 * the row's own pill, reopen, change the reason, read it back.
 *
 * Org-scoped on purpose: a window covering the whole organization needs
 * no host and no agent, so this stays about the page rather than about
 * what a window suppresses — which the Go side already pins.
 */
import { test, expect } from '@playwright/test';
import { ADMIN_STORAGE_STATE, ORG } from './env.ts';
import { pickOption } from './pickOption.ts';

test.use({ storageState: ADMIN_STORAGE_STATE });

const WINDOW_NAME = 'e2e-maintenance-window';

/** row is the window's own line, found through the Edit button that
 * names it — the list has no per-row test id and the name alone also
 * appears inside the dialog's title while it is open. */
function row(page: import('@playwright/test').Page) {
  return page.getByRole('button', { name: `Edit ${WINDOW_NAME}` }).locator('xpath=../..');
}

test('an admin creates a maintenance window, turns it into a silence, and deletes it', async ({ page }) => {
  await page.goto(`/o/${ORG.slug}/maintenance`);
  await expect(page.getByRole('heading', { name: 'Maintenance' })).toBeVisible();

  // --- Create a one-off maintenance window -----------------------------
  await page.getByRole('button', { name: 'New window' }).click();
  const dialog = page.getByRole('dialog');
  await dialog.getByLabel('Name').fill(WINDOW_NAME);
  await expect(dialog.getByLabel('Applies to')).toHaveValue('The whole organization');
  // "Once" is the default; both instants are required, and they are far
  // enough in the future that the row is listed but never "active now" —
  // which keeps the status assertion below stable whenever this runs.
  await expect(dialog.getByRole('radio', { name: 'Once' })).toHaveAttribute('aria-checked', 'true');
  await dialog.getByLabel('From').fill('2099-01-01T02:00');
  await dialog.getByLabel('Until').fill('2099-01-01T04:00');
  await dialog.getByLabel('Note').fill('created by maintenance.spec.ts');
  await dialog.getByRole('button', { name: 'Save' }).click();
  await expect(dialog).toHaveCount(0);

  // --- See it listed, as a maintenance window --------------------------
  await expect(row(page).getByText('maintenance', { exact: true })).toBeVisible();
  await expect(row(page).getByText('created by maintenance.spec.ts')).toBeVisible();
  await expect(row(page).getByText('active now')).toHaveCount(0);

  // --- Turn it into a silence ------------------------------------------
  await page.getByRole('button', { name: `Edit ${WINDOW_NAME}` }).click();
  const editDialog = page.getByRole('dialog');
  await expect(editDialog.getByLabel('Name')).toHaveValue(WINDOW_NAME);
  await pickOption(editDialog, 'Why', 'Silence — do not raise alerts; scheduled runs are unaffected');
  await editDialog.getByRole('button', { name: 'Save' }).click();
  await expect(editDialog).toHaveCount(0);

  // The row's own pill is the assertion the dropped-reason bug would
  // have failed: an edit that saved everything else correctly still put
  // "maintenance" back here.
  await expect(row(page).getByText('silence', { exact: true })).toBeVisible();
  await expect(row(page).getByText('maintenance', { exact: true })).toHaveCount(0);

  // And it survives a reload, so this is the stored reason rather than
  // the dialog's own state still on screen.
  await page.reload();
  await expect(row(page).getByText('silence', { exact: true })).toBeVisible();

  // --- Delete ----------------------------------------------------------
  page.once('dialog', (d) => void d.accept());
  await page.getByRole('button', { name: `Delete ${WINDOW_NAME}` }).click();
  await expect(page.getByRole('button', { name: `Edit ${WINDOW_NAME}` })).toHaveCount(0);
});
