import { test, expect, request as pwRequest, type Page } from '@playwright/test';
import { ADMIN_STORAGE_STATE, BASE_URL, ORG } from './env.ts';
import { acceptConfirm } from './confirm.ts';
import { chooseRowAction } from './rowMenu.ts';
import { pickOption } from './pickOption.ts';

/**
 * Personal tokens (backlog 3.100). The instance administrator owns the
 * seeded organization A and creates organization B here. One personal API
 * token is granted operator in A and viewer in B; an administrator's
 * revocation in B blocks the person there, and the owner cannot add B
 * again.
 */
test.use({ storageState: ADMIN_STORAGE_STATE });
test.describe.configure({ mode: 'serial' });

const ORG_B = { slug: 'ptok-beta', name: 'Personal Beta' };
const TOKEN_NAME = 'e2e personal token';

let secret = '';

async function asToken(): Promise<ReturnType<typeof pwRequest.newContext>> {
  return pwRequest.newContext({ baseURL: BASE_URL, extraHTTPHeaders: { Authorization: `Bearer ${secret}` } });
}

async function openCreateDialog(page: Page) {
  await page.goto('/account/tokens');
  await page.getByRole('button', { name: 'Create token', exact: true }).click();
  return page.getByRole('dialog');
}

test('create a personal token with operator in one organization and viewer in the other', async ({ page }) => {
  const created = await page.request.post('/api/v1/orgs', { data: ORG_B });
  expect(created.ok()).toBeTruthy();

  const dialog = await openCreateDialog(page);
  await dialog.getByLabel('Name', { exact: true }).fill(TOKEN_NAME);
  await dialog.getByLabel(`Use in ${ORG.name}`).check();
  await pickOption(dialog, `Role in ${ORG.name}`, 'Operator');
  await dialog.getByLabel(`Use in ${ORG_B.name}`).check();
  await pickOption(dialog, `Role in ${ORG_B.name}`, 'Viewer');
  await dialog.getByRole('button', { name: 'Create token', exact: true }).click();

  await expect(dialog.getByText(/you will not see it again/)).toBeVisible();
  secret = ((await dialog.locator('code').textContent()) ?? '').trim();
  expect(secret).toMatch(/^pxp_/);
  await dialog.getByRole('button', { name: 'Done', exact: true }).click();

  const row = page.locator('[data-list-row]', { hasText: TOKEN_NAME });
  await expect(row).toContainText(`${ORG.slug}: Operator`);
  await expect(row).toContainText(`${ORG_B.slug}: Viewer`);
});

test('a new assistant token shows the connect guide under the secret', async ({ page }) => {
  const dialog = await openCreateDialog(page);
  await dialog.getByLabel('Name', { exact: true }).fill('e2e personal token guide');
  await dialog.getByLabel(`Use in ${ORG.name}`).check();
  await dialog.getByRole('radio', { name: 'Assistant token' }).check();
  await dialog.getByRole('checkbox', { name: 'Inventory: read' }).check();
  await dialog.getByRole('button', { name: 'Create token', exact: true }).click();

  await expect(dialog.getByText(/you will not see it again/)).toBeVisible();
  await expect(dialog.getByRole('heading', { name: 'Connect an assistant' })).toBeVisible();
  await expect(dialog.getByText(`assistant-mcp.sh add `).first()).toBeVisible();
  await expect(dialog.getByText(`--org ${ORG.slug}`)).toBeVisible();
  await expect(dialog.getByRole('button', { name: 'Copy command' }).first()).toBeVisible();
  await dialog.getByRole('button', { name: 'Done', exact: true }).click();
});

test('the token reads in both organizations and writes only where its cap allows', async () => {
  const api = await asToken();
  try {
    expect((await api.get(`/api/v1/orgs/${ORG.slug}/hosts`)).status()).toBe(200);
    expect((await api.get(`/api/v1/orgs/${ORG_B.slug}/hosts`)).status()).toBe(200);

    const zoneA = ((await (await api.get(`/api/v1/orgs/${ORG.slug}/zones`)).json()) as { zones: { id: string }[] }).zones[0]!.id;
    const inA = await api.post(`/api/v1/orgs/${ORG.slug}/hosts`, { data: { name: 'ptok-host-a', zone_id: zoneA } });
    expect(inA.status()).toBe(201);

    // Viewer in B: a write is refused (the zone id is irrelevant, the role check comes first).
    const inB = await api.post(`/api/v1/orgs/${ORG_B.slug}/hosts`, { data: { name: 'ptok-host-b', zone_id: zoneA } });
    expect(inB.status()).toBe(403);
  } finally {
    await api.dispose();
  }
});

test('an administrator revokes the grant in B: B is refused, A keeps working, and the owner cannot add B again', async ({ page }) => {
  await page.goto(`/o/${ORG_B.slug}/settings/tokens`);
  const entry = page.locator('[data-list-row]', { hasText: TOKEN_NAME });
  await expect(entry).toBeVisible();
  await chooseRowAction(page, `${TOKEN_NAME} (Ada Admin)`, 'Revoke for this organization…');
  await acceptConfirm(page, 'Confirm');
  await expect(entry).toContainText('Blocked');

  const api = await asToken();
  try {
    expect((await api.get(`/api/v1/orgs/${ORG_B.slug}/hosts`)).status()).toBe(403);
    expect((await api.get(`/api/v1/orgs/${ORG.slug}/hosts`)).status()).toBe(200);
  } finally {
    await api.dispose();
  }

  // The owner's edit offers B as blocked, and the server refuses it when it is sent anyway.
  await page.goto('/account/tokens');
  await chooseRowAction(page, TOKEN_NAME, 'Edit');
  const dialog = page.getByRole('dialog');
  await expect(dialog.getByText('Blocked by an administrator').first()).toBeVisible();
  await expect(dialog.getByLabel(`Use in ${ORG_B.name}`)).toBeDisabled();
  await page.keyboard.press('Escape');
  await expect(dialog).toHaveCount(0);

  const list = (await (await page.request.get('/api/v1/auth/me/tokens')).json()) as { tokens: { id: string; name: string }[] };
  const id = list.tokens.find((tok) => tok.name === TOKEN_NAME)!.id;
  const readd = await page.request.patch(`/api/v1/auth/me/tokens/${id}`, {
    data: { grants: [{ org: ORG.slug, role: 'operator' }, { org: ORG_B.slug, role: 'viewer' }] },
  });
  expect(readd.status()).toBe(409);
  expect(JSON.stringify(await readd.json())).toContain('grant_blocked');
});

test.afterAll(async ({ browser }) => {
  // Leave nothing behind that another spec could trip over: the token is revoked.
  const context = await browser.newContext({ storageState: ADMIN_STORAGE_STATE });
  try {
    const list = (await (await context.request.get('/api/v1/auth/me/tokens')).json()) as { tokens: { id: string; name: string }[] };
    for (const tok of list.tokens.filter((x) => x.name === TOKEN_NAME || x.name === 'e2e personal token guide')) {
      await context.request.delete(`/api/v1/auth/me/tokens/${tok.id}`);
    }
  } finally {
    await context.close();
  }
});
