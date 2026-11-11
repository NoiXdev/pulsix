/**
 * The unsaved-changes guard (ui/useUnsavedGuard.ts) against REAL browser
 * history. Its unit tests run on memory history, which cannot show how
 * @tanstack/history's blocked-popstate handling (`history.go(-delta)` plus
 * "ignore the next pop") behaves when a second Back or a Forward arrives
 * while the leave question is still open — the cases that could step one
 * entry too far, or reload the page, when the first question is answered.
 *
 * A Back is done when the question opens: the browser shows A until the
 * question is answered, and a Cancel then restores B. So every step waits
 * for the URL the browser must show next instead of for a fixed time.
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

/** backOpensQuestion presses Back on the form: the browser moves to A at
 * once and the question opens over the form. */
async function backOpensQuestion(page: Page) {
  await page.goBack();
  await expect(page).toHaveURL(LIST);
  await expect(leaveDialog(page)).toHaveCount(1);
}

/** expectFormKept checks that the form, and the edit in it, are still there:
 * no navigation and no reload. */
async function expectFormKept(page: Page) {
  await expect(page).toHaveURL(FORM);
  await expect(page.locator('#repo-def-name')).toHaveValue('e2e-unsaved');
}

/** The guard still works: a later Back asks again instead of being
 * swallowed, and its Cancel restores the form. */
async function expectNextBackAsks(page: Page) {
  await backOpensQuestion(page);
  await cancelLeave(page);
  await expectFormKept(page);
}

test('Back asks, Cancel keeps the page and its edits, and the next Back asks again', async ({ page }) => {
  await openDirtyForm(page);

  await backOpensQuestion(page);
  await cancelLeave(page);
  await expectFormKept(page);

  await expectNextBackAsks(page);
});

test('a second Back while the question is open asks once, and Cancel stays on the page', async ({ page }) => {
  await openDirtyForm(page);

  await backOpensQuestion(page);
  // The second Back goes from A to the entry before it; being blocked, it
  // is undone at once, back to the form (A, then BEFORE, then B).
  await page.evaluate(() => history.back());
  await expect(page).toHaveURL(FORM);
  await expect(leaveDialog(page)).toHaveCount(1);

  await cancelLeave(page);
  await expectFormKept(page);
  await expectNextBackAsks(page);
});

test('a second Back while the question is open, then Confirm, lands on the previous page', async ({ page }) => {
  await openDirtyForm(page);

  await backOpensQuestion(page);
  await page.evaluate(() => history.back());
  await expect(page).toHaveURL(FORM);
  await expect(leaveDialog(page)).toHaveCount(1);

  await acceptConfirm(page, enUi.confirm.confirm);
  await expect(page).toHaveURL(LIST);
});

test('Back, then Forward while the question is open, then Cancel keeps the page without a reload', async ({ page }) => {
  await openDirtyForm(page);

  await backOpensQuestion(page);
  // Forward returns the browser to the form itself: nothing to undo there,
  // so it must neither reload the page nor leave history ignoring a pop.
  await page.evaluate(() => history.forward());
  await expect(page).toHaveURL(FORM);
  await expect(leaveDialog(page)).toHaveCount(1);

  await cancelLeave(page);
  await expectFormKept(page);
  await expectNextBackAsks(page);
});

test('Back, then Forward while the question is open, then Confirm, lands on the previous page', async ({ page }) => {
  await openDirtyForm(page);

  await backOpensQuestion(page);
  await page.evaluate(() => history.forward());
  await expect(page).toHaveURL(FORM);
  await expect(leaveDialog(page)).toHaveCount(1);

  await acceptConfirm(page, enUi.confirm.confirm);
  await expect(page).toHaveURL(LIST);
});
