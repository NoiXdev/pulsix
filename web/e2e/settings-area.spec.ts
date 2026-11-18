import { test, expect } from '@playwright/test';
import { ADMIN_STORAGE_STATE, ORG } from './env.ts';

/**
 * The personal settings area (backlog 3.99): the account menu's My account
 * entry, the sidebar with its own entries and the way back to the
 * organization the person came from.
 */
test.use({ storageState: ADMIN_STORAGE_STATE });

test('the account menu opens the settings area, whose sidebar leads back to the organization', async ({ page }) => {
  await page.goto(`/o/${ORG.slug}/hosts`);
  await expect(page.getByRole('heading', { name: 'Hosts' })).toBeVisible();

  await page.getByRole('button', { name: 'Account menu' }).click();
  // The organization's own navigation has a "Settings" link too; this one is in the account menu.
  await page
    .locator('details', { has: page.getByRole('button', { name: 'Account menu' }) })
    .getByRole('link', { name: 'My account', exact: true })
    .click();
  await expect(page).toHaveURL(/\/account\/security$/);

  const nav = page.getByRole('navigation', { name: 'My account' });
  await expect(nav.getByRole('link', { name: `Back to ${ORG.name}` })).toBeVisible();
  for (const entry of ['Security', 'Appearance', 'Profile', 'API tokens', 'My activity']) {
    await expect(nav.getByRole('link', { name: entry, exact: true })).toBeVisible();
  }
  // Only the account pages: no organization, alerting or instance links.
  await expect(nav.getByRole('link')).toHaveCount(6);
  await expect(nav.getByRole('link', { name: 'Members', exact: true })).toHaveCount(0);
  // The organization's own navigation is not there.
  await expect(page.getByRole('link', { name: 'Hosts', exact: true })).toHaveCount(0);

  await nav.getByRole('link', { name: 'Appearance', exact: true }).click();
  await expect(page).toHaveURL(/\/account\/appearance$/);
  await expect(page.getByRole('heading', { name: 'Appearance' })).toBeVisible();
  // The language select moved here from the security page.
  await expect(page.getByLabel('Language', { exact: true })).toBeVisible();

  await nav.getByRole('link', { name: 'Security', exact: true }).click();
  await expect(page.getByLabel('Language', { exact: true })).toHaveCount(0);

  await nav.getByRole('link', { name: `Back to ${ORG.name}` }).click();
  await expect(page).toHaveURL(new RegExp(`/o/${ORG.slug}(/|$)`));
  await expect(page.getByRole('link', { name: 'Hosts', exact: true })).toBeVisible();
});

test('the settings sidebar carries the organization, alerting and instance groups, not the account pages', async ({ page }) => {
  await page.goto(`/o/${ORG.slug}/settings/general`);
  const nav = page.getByRole('navigation', { name: 'Settings' });
  await expect(nav.getByRole('link', { name: `Back to ${ORG.name}` })).toBeVisible();
  for (const group of [`Organization · ${ORG.name}`, 'Alerting', 'Instance']) {
    await expect(nav.getByRole('group', { name: group })).toBeVisible();
  }
  await expect(nav.getByRole('group', { name: 'My account' })).toHaveCount(0);
  await expect(nav.getByRole('link', { name: 'Security', exact: true })).toHaveCount(0);
  // No organization switcher.
  await expect(page.getByRole('combobox', { name: 'Organization' })).toHaveCount(0);

  await nav.getByRole('link', { name: 'Members', exact: true }).click();
  await expect(page).toHaveURL(new RegExp(`/o/${ORG.slug}/settings/members$`));
  await expect(nav.getByRole('link', { name: 'Members', exact: true })).toHaveAttribute('aria-current', 'page');
});

test('the app frame is pinned to the window: the document cannot scroll', async ({ page }) => {
  await page.setViewportSize({ width: 900, height: 740 });
  await page.goto(`/o/${ORG.slug}/settings/backups`);
  await expect(page.getByRole('navigation', { name: 'Settings' })).toBeVisible();

  const fits = await page.evaluate(() => (document.scrollingElement?.scrollHeight ?? 0) <= window.innerHeight);
  expect(fits).toBe(true);

  await page.locator('main').hover();
  await page.mouse.wheel(0, 3000);
  await page.waitForTimeout(200);
  const top = await page.evaluate(() => document.scrollingElement?.scrollTop ?? 0);
  expect(top).toBe(0);
});
