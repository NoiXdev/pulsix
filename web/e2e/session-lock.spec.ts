import { test, expect } from '@playwright/test';
import { LOCK_PIN, signInToApp } from './session.ts';

// Own browser context, signed in from scratch: locking and signing out end
// sessions, and ADMIN_STORAGE_STATE must stay usable for the specs after.
test.use({ storageState: { cookies: [], origins: [] } });
test.setTimeout(120_000);

test('lock with a PIN: survives reload and a second tab, counts wrong PINs, unlocks', async ({ page, context }) => {
  await signInToApp(page);

  // No PIN yet (setup.spec skipped the offer): the lock icon asks for one
  // first, then locks.
  await page.getByRole('button', { name: 'Lock session' }).click();
  const setPin = page.getByRole('dialog', { name: 'Set a lock PIN' });
  await setPin.getByLabel('PIN', { exact: true }).fill(LOCK_PIN);
  // Two rows of four boxes; no submit button: the matching second row sets
  // the PIN and locks.
  await expect(setPin.getByRole('button', { name: 'Set PIN and lock' })).toHaveCount(0);
  await setPin.getByLabel('Repeat the PIN').fill(LOCK_PIN);
  await expect(page.getByRole('heading', { name: 'Session locked' })).toBeVisible();
  // The page is replaced, not covered: its content is not in the DOM.
  await expect(page.getByRole('heading', { name: 'Hosts' })).toHaveCount(0);

  // The lock is the server's, so a reload stays locked.
  await page.reload();
  await expect(page.getByRole('heading', { name: 'Session locked' })).toBeVisible();

  // So does a second page of the same browser.
  const second = await context.newPage();
  await second.goto('/');
  await expect(second.getByRole('heading', { name: 'Session locked' })).toBeVisible();
  await second.close();

  // A wrong PIN says how many attempts are left.
  // The fourth digit posts it; there is no Unlock button.
  await expect(page.getByRole('button', { name: 'Unlock' })).toHaveCount(0);
  await expect(page.locator('[data-pin-box]')).toHaveCount(4);
  await page.getByLabel('PIN', { exact: true }).fill('0000');
  await expect(page.getByRole('alert')).toContainText('Wrong PIN.');
  await expect(page.getByRole('alert')).toContainText('4 attempts left');
  // The boxes are emptied for the next try.
  await expect(page.getByLabel('PIN', { exact: true })).toHaveValue('');
  await expect(page.locator('[data-pin-box][data-filled="true"]')).toHaveCount(0);
  await expect(page.getByRole('heading', { name: 'Session locked' })).toBeVisible();

  // The right one returns to the page.
  await page.getByLabel('PIN', { exact: true }).fill(LOCK_PIN);
  await expect(page.getByRole('heading', { name: 'Hosts' })).toBeVisible();
  await expect(page.getByRole('heading', { name: 'Session locked' })).toHaveCount(0);
});

test('five wrong PINs end the session', async ({ page }) => {
  await signInToApp(page);
  await page.getByRole('button', { name: 'Lock session' }).click();
  await expect(page.getByRole('heading', { name: 'Session locked' })).toBeVisible();

  for (let i = 0; i < 5; i++) {
    await page.getByLabel('PIN', { exact: true }).fill('0000');
    if (i < 4) await expect(page.getByRole('alert')).toContainText('Wrong PIN.');
  }

  await expect(page).toHaveURL(/\/login\?.*notice=lock_ended/);
  await expect(page.getByText('Too many wrong PINs: the session was ended. Sign in again.')).toBeVisible();
});
