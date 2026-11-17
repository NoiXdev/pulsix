import { test, expect } from '@playwright/test';
import { signInToApp } from './session.ts';

test.use({ storageState: { cookies: [], origins: [] } });
test.setTimeout(120_000);

test('a new server version offers a reload, and "Later" leaves a banner', async ({ page }) => {
  let bumped = false;
  // The status is public and always carries a version here, so the baseline
  // exists even for a build without one.
  await page.route('**/api/v1/setup/status', async (route) => {
    const response = await route.fetch();
    const body = await response.json();
    body.version = bumped ? '99.0.0' : body.version || '1.0.0';
    await route.fulfill({ response, json: body });
  });

  await signInToApp(page);
  await expect(page.getByRole('dialog', { name: /Pulsix was updated/ })).toHaveCount(0);

  // The server "was upgraded"; the tab comes back to the foreground.
  bumped = true;
  // The query client only refetches when the tab's visibility actually
  // changes, so hide it and show it again the way switching tabs would.
  await page.evaluate(() => {
    let state: DocumentVisibilityState = 'hidden';
    Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => state });
    document.dispatchEvent(new Event('visibilitychange', { bubbles: true }));
    state = 'visible';
    document.dispatchEvent(new Event('visibilitychange', { bubbles: true }));
  });

  const dialog = page.getByRole('dialog', { name: 'Pulsix was updated to v99.0.0' });
  await expect(dialog).toBeVisible();
  await expect(dialog.getByRole('link', { name: /Release notes/ })).toHaveAttribute(
    'href',
    'https://github.com/NoiXdev/pulsix/releases/tag/v99.0.0',
  );

  await dialog.getByRole('button', { name: 'Later' }).click();
  await expect(dialog).toHaveCount(0);
  const banner = page.getByRole('status').filter({ hasText: 'Pulsix was updated to v99.0.0.' });
  await expect(banner).toBeVisible();
  await expect(banner.getByRole('button', { name: 'Reload' })).toBeVisible();
});
