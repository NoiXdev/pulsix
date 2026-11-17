import { test, expect, type Locator, type Page } from '@playwright/test';
import { ADMIN_STORAGE_STATE, ORG } from './env.ts';

/**
 * The shared list component on the hosts page (backlog 3.97/3.98): sorting
 * by a column header, the page size and the column choice surviving a
 * reload, the density, the right-click menu and the Active runs column.
 *
 * The person's list settings are stored on the account, so the specs after
 * this one would inherit a compact density or a hidden column: the last
 * hook puts the defaults back.
 */
test.use({ storageState: ADMIN_STORAGE_STATE });
test.describe.configure({ mode: 'serial' });

const PREFIX = 'zz-list-';
const HOST_COUNT = 26;
const hostName = (n: number): string => `${PREFIX}${String(n).padStart(2, '0')}`;
const RUNNING_HOST = hostName(1);

/** The footer's "1–10 of 41 hosts" total. */
async function footerTotal(page: Page): Promise<number> {
  const text = (await page.getByText(/^\d+–\d+ of \d+ hosts?$/).textContent()) ?? '';
  return Number(/of (\d+)/.exec(text)![1]);
}

function hostRow(page: Page, name: string): Locator {
  return page.locator('[data-list-row]', { has: page.getByRole('link', { name, exact: true }) });
}

test.beforeAll(async ({ browser }) => {
  const context = await browser.newContext({ storageState: ADMIN_STORAGE_STATE });
  try {
    const api = context.request;
    const zones = await api.get(`/api/v1/orgs/${ORG.slug}/zones`);
    expect(zones.ok()).toBeTruthy();
    const zoneId = ((await zones.json()) as { zones: { id: string }[] }).zones[0]!.id;
    for (let n = 1; n <= HOST_COUNT; n++) {
      const res = await api.post(`/api/v1/orgs/${ORG.slug}/hosts`, {
        data: { name: hostName(n), zone_id: zoneId, managed: n === 1 },
      });
      expect(res.status(), `create ${hostName(n)}`).toBe(201);
      if (n === 1) {
        // A command that has not finished: the run stays queued or running,
        // either way it is live and counts in the Active runs column.
        const hostId = ((await res.json()) as { host: { id: string } }).host.id;
        const run = await api.post(`/api/v1/orgs/${ORG.slug}/hosts/command`, {
          data: { host_ids: [hostId], module: 'shell', argv: 'sleep 600' },
        });
        expect(run.status(), 'enqueue the ad-hoc command').toBe(202);
      }
    }
  } finally {
    await context.close();
  }
});

test.afterAll(async ({ browser }) => {
  const context = await browser.newContext({ storageState: ADMIN_STORAGE_STATE });
  try {
    const res = await context.request.put('/api/v1/auth/me/preferences', {
      data: { density: 'standard', page_size: 25, lists: {} },
    });
    expect(res.ok()).toBeTruthy();
  } finally {
    await context.close();
  }
});

test('clicking the Host header twice sorts the hosts descending and puts that into the URL', async ({ page }) => {
  await page.goto(`/o/${ORG.slug}/hosts`);
  const header = page.getByRole('button', { name: 'Host', exact: true });
  await header.click();
  await expect(page).toHaveURL(/sort=name&order=asc/);
  await header.click();
  await expect(page).toHaveURL(/sort=name&order=desc/);
  // Every host's name starts with "zz-list-" or sorts before it, so the
  // last one created is the last name alphabetically.
  await expect(page.locator('[data-list-row]').first()).toContainText(hostName(HOST_COUNT));
});

test('the page size and the hidden column survive a reload', async ({ page }) => {
  await page.goto(`/o/${ORG.slug}/hosts`);
  const total = await footerTotal(page);
  expect(total).toBeGreaterThanOrEqual(HOST_COUNT);

  // Chosen with the keyboard: the footer's listbox opens below the list's card, whose
  // clipping cuts it off, so a mouse click on the option cannot land (reported as a
  // concern of the task that wrote this spec).
  await page.getByLabel('Rows per page', { exact: true }).click();
  await page.keyboard.press('ArrowUp');
  await page.keyboard.press('Enter');
  await expect(page.getByText(`1–10 of ${total} hosts`)).toBeVisible();
  await expect(page.locator('[data-list-row]')).toHaveCount(10);

  await expect(page.getByRole('columnheader', { name: 'Zone' })).toBeVisible();
  await page.getByRole('button', { name: 'Columns' }).click();
  await page.getByRole('checkbox', { name: 'Zone', exact: true }).uncheck();
  await page.keyboard.press('Escape');
  await expect(page.getByRole('columnheader', { name: 'Zone' })).toHaveCount(0);

  // The choice is saved on the account; wait for the save before reloading.
  await expect
    .poll(async () => {
      const res = await page.request.get('/api/v1/auth/me/preferences');
      const prefs = (await res.json()) as { page_size: number; lists: Record<string, { columns: string[] }> };
      return prefs.page_size === 10 && prefs.lists.hosts !== undefined && !prefs.lists.hosts.columns.includes('zone');
    })
    .toBe(true);

  await page.reload();
  await expect(page.getByText(`1–10 of ${total} hosts`)).toBeVisible();
  await expect(page.locator('[data-list-row]')).toHaveCount(10);
  await expect(page.getByRole('columnheader', { name: 'Host' })).toBeVisible();
  await expect(page.getByRole('columnheader', { name: 'Zone' })).toHaveCount(0);
});

test('Appearance → Compact sets the density on the page', async ({ page }) => {
  await page.goto('/account/appearance');
  await page.getByRole('radio', { name: /Compact/ }).click();
  await expect(page.locator('html')).toHaveAttribute('data-density', 'compact');
  await page.reload();
  await expect(page.locator('html')).toHaveAttribute('data-density', 'compact');
  await page.getByRole('radio', { name: /Standard/ }).click();
  await expect(page.locator('html')).toHaveAttribute('data-density', 'standard');
});

test('a right-click on a host row opens the row menu with "Open in new tab"', async ({ page }) => {
  await page.goto(`/o/${ORG.slug}/hosts?q=${hostName(2)}`);
  const row = hostRow(page, hostName(2));
  await expect(row).toBeVisible();

  // The entries of the "…" menu ...
  await page.getByRole('button', { name: `Actions for ${hostName(2)}`, exact: true }).click();
  const fromButton = await page.getByRole('menuitem').allInnerTexts();
  await page.keyboard.press('Escape');
  expect(fromButton.length).toBeGreaterThan(0);

  // ... plus the two link entries when the row is right-clicked.
  await row.click({ button: 'right', position: { x: 500, y: 12 } });
  const items = page.getByRole('menuitem');
  await expect(items.first()).toBeVisible();
  const fromRightClick = await items.allInnerTexts();
  expect(fromRightClick.map((s) => s.trim())).toContain('Open in new tab');
  for (const entry of fromButton) expect(fromRightClick.map((s) => s.trim())).toContain(entry.trim());
});

test('a host with a running command shows it in Active runs, and the link opens the filtered runs', async ({ page }) => {
  await page.goto(`/o/${ORG.slug}/hosts`);
  await page.getByPlaceholder('Filter hosts…').fill(RUNNING_HOST);
  const row = hostRow(page, RUNNING_HOST);
  await expect(row.getByRole('link', { name: '1 running' })).toBeVisible();

  await row.getByRole('link', { name: '1 running' }).click();
  await expect(page).toHaveURL(/\/runs\?.*status=active/);
  await expect(page).toHaveURL(/host=/);
  await expect(page.getByRole('heading', { name: 'Runs' })).toBeVisible();
  await expect(page.locator('[data-list-row]')).toHaveCount(1);
});
