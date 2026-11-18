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

// Backlog 1.52: a save that finishes while a Back's question is open
// withdraws the question and leaves for the list. Its navigation must wait
// until the router has taken that Back, or the new entry is numbered one
// higher than its place in the browser's history, and a later blocked pop
// across it is undone by one entry too many: a Cancel would then still
// leave the next form and lose its edits.
test('a save that finishes during a Back keeps the next form guarded', async ({ page }) => {
  await openDirtyForm(page);
  await page.locator('#repo-def-name').fill(`e2e-save-during-back-${Date.now()}`);
  await page.locator('#repo-def-url').fill('https://git.example/e2e/save-during-back.git');

  let release!: () => void;
  const held = new Promise<void>((resolve) => (release = resolve));
  await page.route('**/api/v1/admin/repos/definitions', async (route) => {
    if (route.request().method() !== 'POST') return route.fallback();
    await held;
    await route.fallback();
  });

  const saved = page.waitForResponse(
    (r) => r.url().endsWith('/api/v1/admin/repos/definitions') && r.request().method() === 'POST',
  );
  await page.getByRole('button', { name: 'Create definition' }).click();
  await backOpensQuestion(page);
  release();
  expect((await saved).ok()).toBe(true);
  await expect(leaveDialog(page)).toHaveCount(0);
  await expect(page).toHaveURL(LIST);
  await page.unroute('**/api/v1/admin/repos/definitions');

  // A second form, dirty, and two Backs while its question is open.
  await page.getByRole('link', { name: 'Add repository definition' }).click();
  await expect(page).toHaveURL(FORM);
  await page.locator('#repo-def-name').fill('e2e-unsaved');
  await backOpensQuestion(page);
  await page.evaluate(() => history.back());
  await expect(page).toHaveURL(FORM);
  await expect(leaveDialog(page)).toHaveCount(1);

  await cancelLeave(page);
  await expectFormKept(page);
  await expectNextBackAsks(page);
});

// A settings card page guards its unsaved card the same way: a click in
// the settings navigation asks once, and after the card's Discard it does
// not ask at all.
test('a dirty card on Organization › General asks before Members, and not after Discard', async ({ page }) => {
  await page.goto(`/o/${ORG.slug}/settings/general`);
  const name = page.locator('#org-general-name');
  await expect(name).toHaveValue(ORG.name);
  await name.fill(`${ORG.name} e2e-unsaved`);

  const nav = page.getByRole('navigation', { name: 'Settings' });
  await nav.getByRole('link', { name: 'Members', exact: true }).click();
  await expect(leaveDialog(page)).toHaveCount(1);
  await cancelLeave(page);
  await expect(page).toHaveURL(new RegExp(`/o/${ORG.slug}/settings/general$`));
  await expect(name).toHaveValue(`${ORG.name} e2e-unsaved`);

  await page.getByRole('button', { name: enUi.settings.discard, exact: true }).click();
  await expect(name).toHaveValue(ORG.name);
  await nav.getByRole('link', { name: 'Members', exact: true }).click();
  await expect(page).toHaveURL(new RegExp(`/o/${ORG.slug}/settings/members$`));
  await expect(leaveDialog(page)).toHaveCount(0);
});
