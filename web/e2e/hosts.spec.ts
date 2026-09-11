import { test, expect, type Page, type Locator } from '@playwright/test';
import { ADMIN_STORAGE_STATE, ORG } from './env.ts';

test.use({ storageState: ADMIN_STORAGE_STATE });

const ZONE = { name: 'Primary', slug: 'primary' };

async function addZone(page: Page): Promise<void> {
  await page.getByRole('button', { name: 'Add zone' }).click();
  const dialog = page.getByRole('dialog');
  await dialog.getByLabel('Name').fill(ZONE.name);
  await dialog.getByLabel('Slug').fill(ZONE.slug);
  await dialog.getByRole('button', { name: 'Create zone' }).click();
  await expect(page.getByText(`Zone ${ZONE.name} created`)).toBeVisible();
}

async function addHost(
  page: Page,
  opts: { name: string; managed?: boolean; monitored?: boolean },
): Promise<void> {
  await page.getByRole('button', { name: 'Add host' }).click();
  const dialog = page.getByRole('dialog');
  await dialog.getByLabel('Name').fill(opts.name);
  await dialog.getByLabel('Zone').selectOption({ label: ZONE.name });
  if (opts.managed) await dialog.getByLabel('Managed (Ansible via SSH)').check();
  if (opts.monitored) await dialog.getByLabel('Monitored', { exact: true }).check();
  await dialog.getByRole('button', { name: 'Create host' }).click();
  await expect(page.getByText(`Host ${opts.name} created`)).toBeVisible();
}

/** The row for one host, scoped so capability/tag assertions never
 * accidentally match another host's cells — HostRow renders the host name
 * as a Link that is a direct child of its GridRow container. */
function hostRow(page: Page, name: string): Locator {
  return page.getByRole('link', { name, exact: true }).locator('xpath=..');
}

test('create a zone and hosts, toggle capabilities independently, tag one, then filter and query for it', async ({
  page,
}) => {
  await page.goto(`/o/${ORG.slug}/hosts`);

  await addZone(page);

  // Three hosts with deliberately different capability combinations so the
  // filter chips below have something real to distinguish:
  //   A "web-04.fra"   — neither at creation; both toggled on via the row
  //                      below, which is what actually exercises the
  //                      "independent" contract.
  //   B "db-02.fra"    — monitored only.
  //   C "cache-01.fra" — neither.
  await addHost(page, { name: 'web-04.fra' });
  await addHost(page, { name: 'db-02.fra', monitored: true });
  await addHost(page, { name: 'cache-01.fra' });

  const rowA = hostRow(page, 'web-04.fra');
  await expect(rowA.getByRole('button', { name: /enable/ })).toHaveCount(2); // both capabilities off

  // Enable Managed first. This is PATCH …/hosts/{id}/capabilities with
  // {managed: true} alone — the assertion that matters is not just that
  // Managed turned on, but that Monitored is still untouched.
  await rowA.getByRole('button', { name: /enable/ }).first().click();
  await expect(rowA.getByText('Ansible', { exact: true })).toBeVisible();
  await expect(rowA.getByRole('button', { name: /enable/ })).toHaveCount(1); // only Monitored's is left

  // Now enable Monitored on the same host. If the capabilities patch were
  // not truly independent-fields (e.g. it silently defaulted the field it
  // was not given back to false), this would reset Managed to off —
  // asserting both are on afterward is what actually catches that.
  await rowA.getByRole('button', { name: /enable/ }).first().click();
  await expect(rowA.getByText('Agent', { exact: true })).toBeVisible();
  await expect(rowA.getByText('Ansible', { exact: true })).toBeVisible();
  await expect(rowA.getByRole('button', { name: /enable/ })).toHaveCount(0);

  // Tag host A through its detail page.
  await page.getByRole('link', { name: 'web-04.fra', exact: true }).click();
  await expect(page).toHaveURL(/\/hosts\/[0-9a-f-]+$/);
  await page.getByPlaceholder('role=db').fill('role=db');
  await page.getByRole('button', { name: 'Add tag' }).click();
  // Not exact: the tag chip's text node ("role=db") shares its <span>
  // with a "remove" button ("×"), so the element's full text content is
  // "role=db×", not "role=db" alone.
  await expect(page.getByText('role=db')).toBeVisible();
  await page.getByPlaceholder('role=db').fill('env=prod');
  await page.getByRole('button', { name: 'Add tag' }).click();
  await expect(page.getByText('env=prod')).toBeVisible();

  await page.getByRole('link', { name: 'Hosts' }).click();
  await expect(page).toHaveURL(new RegExp(`/o/${ORG.slug}/hosts$`));

  // Filter chips: "Managed" narrows the three hosts down to just the one
  // that is actually managed.
  await page.getByRole('button', { name: 'Managed', exact: true }).click();
  await expect(page.getByRole('link', { name: 'web-04.fra', exact: true })).toBeVisible();
  await expect(page.getByRole('link', { name: 'db-02.fra', exact: true })).toHaveCount(0);
  await expect(page.getByRole('link', { name: 'cache-01.fra', exact: true })).toHaveCount(0);

  // Switch to "Monitored": now both A and B qualify, C (neither
  // capability) is excluded — proves the chip is a real server-side
  // filter, not a coincidence of which chip happens to be on.
  await page.getByRole('button', { name: 'Managed', exact: true }).click(); // toggle off
  await page.getByRole('button', { name: 'Monitored', exact: true }).click();
  await expect(page.getByRole('link', { name: 'web-04.fra', exact: true })).toBeVisible();
  await expect(page.getByRole('link', { name: 'db-02.fra', exact: true })).toBeVisible();
  await expect(page.getByRole('link', { name: 'cache-01.fra', exact: true })).toHaveCount(0);
  await page.getByRole('button', { name: 'Monitored', exact: true }).click(); // toggle off

  // Selector query box: only host A carries role=db.
  await page.getByPlaceholder('selector, e.g. role = db').fill('role = db');
  await page.getByRole('button', { name: 'Run' }).click();
  await expect(page.getByRole('link', { name: 'web-04.fra', exact: true })).toBeVisible();
  await expect(page.getByRole('link', { name: 'db-02.fra', exact: true })).toHaveCount(0);
  await expect(page.getByRole('link', { name: 'cache-01.fra', exact: true })).toHaveCount(0);
});
