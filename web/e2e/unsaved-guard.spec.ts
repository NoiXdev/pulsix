/**
 * The unsaved-changes guard (ui/useUnsavedGuard.ts) against REAL browser
 * history. Its unit tests run on memory history, which cannot show how
 * @tanstack/history's blocked-popstate handling (`history.go(-delta)` plus
 * "ignore the next pop") behaves when a second Back arrives while the leave
 * question is still open — the case that could step one entry too far when
 * the first question is then cancelled.
 *
 * Every scenario starts from the same known history: the shared
 * templates list, the shared repositories list (A), then the "new
 * definition" form (B), both reached by links, with an edit made so the
 * form is dirty.
 */
import { test, expect, type Page } from '@playwright/test';
import { ADMIN_STORAGE_STATE, ORG } from './env.ts';
import { acceptConfirm } from './confirm.ts';
import { enUi } from '../src/i18n/en/ui.ts';

test.use({ storageState: ADMIN_STORAGE_STATE });

const LIST = `/o/${ORG.slug}/settings/shared/repos`;
const FORM = `${LIST}/new`;
// One entry before A, in the same document: a Back from A must land in the
// app, as it does for a person who has been using it, not on about:blank.
const BEFORE = `/o/${ORG.slug}/settings/shared/templates`;

async function openDirtyForm(page: Page) {
  await page.goto(BEFORE);
  await page.getByRole('link', { name: 'Shared repositories' }).click();
  await expect(page).toHaveURL(LIST);
  await page.getByRole('link', { name: 'Add repository definition' }).click();
  await expect(page).toHaveURL(FORM);
  await page.locator('#repo-def-name').fill('e2e-unsaved');
}

const leaveDialog = (page: Page) => page.getByRole('dialog', { name: enUi.confirm.title, exact: true });

async function cancelLeave(page: Page) {
  await leaveDialog(page).getByRole('button', { name: enUi.confirm.cancel, exact: true }).click();
  await expect(leaveDialog(page)).toHaveCount(0);
}

/** settle lets a blocked pop's history.go(-delta) restore finish. */
async function settle(page: Page) {
  await page.waitForTimeout(500);
}

test('Back asks, Cancel keeps the page and its edits, and the next Back asks again', async ({ page }) => {
  await openDirtyForm(page);

  await page.goBack();
  await expect(leaveDialog(page)).toHaveCount(1);
  await cancelLeave(page);
  await settle(page);
  await expect(page).toHaveURL(FORM);
  await expect(page.locator('#repo-def-name')).toHaveValue('e2e-unsaved');

  await page.goBack();
  await expect(leaveDialog(page)).toHaveCount(1);
  await cancelLeave(page);
  await settle(page);
  await expect(page).toHaveURL(FORM);
});

test('a second Back while the question is open asks once, and Cancel stays on the page', async ({ page }) => {
  await openDirtyForm(page);

  await page.goBack();
  await expect(leaveDialog(page)).toHaveCount(1);
  // Let the first blocked pop's restore finish before the second Back, as a
  // person's second key press would: otherwise it leaves from A itself.
  await settle(page);
  await page.evaluate(() => history.back());
  await settle(page);
  await expect(leaveDialog(page)).toHaveCount(1);

  await cancelLeave(page);
  await settle(page);
  await expect(leaveDialog(page)).toHaveCount(0);
  await expect(page).toHaveURL(FORM);
  await expect(page.locator('#repo-def-name')).toHaveValue('e2e-unsaved');

  // The guard still works: a later Back asks again instead of being swallowed.
  await page.goBack();
  await expect(leaveDialog(page)).toHaveCount(1);
  await cancelLeave(page);
  await settle(page);
  await expect(page).toHaveURL(FORM);
});

test('a second Back while the question is open, then Confirm, lands on the previous page', async ({ page }) => {
  await openDirtyForm(page);

  await page.goBack();
  await expect(leaveDialog(page)).toHaveCount(1);
  // Let the first blocked pop's restore finish before the second Back, as a
  // person's second key press would: otherwise it leaves from A itself.
  await settle(page);
  await page.evaluate(() => history.back());
  await settle(page);
  await expect(leaveDialog(page)).toHaveCount(1);

  await acceptConfirm(page, enUi.confirm.confirm);
  await expect(page).toHaveURL(LIST);
});
