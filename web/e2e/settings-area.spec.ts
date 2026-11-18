import { test, expect } from '@playwright/test';
import { ADMIN_STORAGE_STATE, ORG } from './env.ts';

/**
 * The personal settings area (backlog 3.99): the account menu's Settings
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
    .getByRole('link', { name: 'Settings', exact: true })
    .click();
  await expect(page).toHaveURL(/\/account\/security$/);

  const nav = page.getByRole('navigation', { name: 'Settings' });
  await expect(nav.getByRole('link', { name: `Back to ${ORG.name}` })).toBeVisible();
  // The organization group has an "API tokens" entry too, so look in My account.
  const account = nav.getByRole('group', { name: 'My account' });
  for (const entry of ['Security', 'Appearance', 'Profile', 'API tokens', 'My activity']) {
    await expect(account.getByRole('link', { name: entry, exact: true })).toBeVisible();
  }
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

test('one sidebar carries account, organization and instance settings', async ({ page }) => {
  await page.goto(`/o/${ORG.slug}/settings/general`);
  const nav = page.getByRole('navigation', { name: 'Settings' });
  await expect(nav.getByRole('link', { name: `Back to ${ORG.name}` })).toBeVisible();
  for (const group of ['My account', `Organization · ${ORG.name}`, 'Alerting', 'Instance']) {
    await expect(nav.getByRole('group', { name: group })).toBeVisible();
  }
  // No organization switcher.
  await expect(page.getByRole('combobox', { name: 'Organization' })).toHaveCount(0);

  await nav.getByRole('link', { name: 'Members', exact: true }).click();
  await expect(page).toHaveURL(new RegExp(`/o/${ORG.slug}/settings/members$`));
  await expect(nav.getByRole('link', { name: 'Members', exact: true })).toHaveAttribute('aria-current', 'page');

  await nav.getByRole('link', { name: 'Security', exact: true }).click();
  await expect(page).toHaveURL(/\/account\/security$/);
  await expect(nav.getByRole('link', { name: 'Members', exact: true })).toHaveAttribute('href', `/o/${ORG.slug}/settings/members`);
});
