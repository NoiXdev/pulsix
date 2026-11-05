import { test, expect } from '@playwright/test';
import { ADMIN_STORAGE_STATE, ORG, TENANT_TOTP_STATE_PATH, TENANT_TOTP_STEP_STATE_PATH } from './env.ts';
import { extractInviteLink, waitForMessageTo } from './mailpit.ts';
import { enrolTotp } from './enrol.ts';
import { pickOption } from './pickOption.ts';

test.use({ storageState: ADMIN_STORAGE_STATE });

const SECOND_ORG = { slug: 'beta', name: 'Beta Corp' };
const SECOND_USER = { email: 'tenant2@example.test', name: 'Tina Tenant', password: 'tenant-password' };

test('a second organization\'s user cannot see the first organization\'s hosts, in the UI or over the API', async ({
  page,
  browser,
}) => {
  // The administrator (owner of ORG "acme", and an instance administrator —
  // the only kind of account that may create an organization) creates a
  // second, unrelated organization through the API directly. The org
  // switcher's "New organization" does the same; this spec is about
  // isolation, not about that dialog.
  const createOrg = await page.request.post('/api/v1/orgs', { data: SECOND_ORG });
  expect(createOrg.ok()).toBeTruthy();

  // Invite a brand-new user into "beta" only. They will never be a member
  // of "acme" at all — that is what makes the isolation check below a
  // genuine test of tenancy rather than a legitimate multi-org member
  // correctly seeing an org they do belong to.
  const invite = await page.request.post(`/api/v1/orgs/${SECOND_ORG.slug}/invites`, {
    data: { email: SECOND_USER.email, role: 'admin' },
  });
  expect(invite.ok()).toBeTruthy();

  const body = await waitForMessageTo(SECOND_USER.email);
  const { url } = extractInviteLink(body);
  expect(url).toContain(`/invite#token=`);

  // storageState must be overridden explicitly (see members.spec.ts's own
  // note on this): this file's own `test.use({storageState:
  // ADMIN_STORAGE_STATE})` otherwise applies to every context Playwright
  // creates for this test, including a manual browser.newContext() call —
  // without this, "tenantContext" would silently start out signed in as
  // the administrator, defeating the entire point of this spec.
  const tenantContext = await browser.newContext({ storageState: { cookies: [], origins: [] } });
  const tenantPage = await tenantContext.newPage();
  try {
    await tenantPage.goto(url);
    await tenantPage.getByLabel('Your name').fill(SECOND_USER.name);
    await tenantPage.getByLabel('Choose a password').fill(SECOND_USER.password);
    await tenantPage.getByRole('button', { name: 'Accept invitation' }).click();

    // Accepting an invite that creates a brand-new account opens a
    // session right away (see tenancy.Service.AcceptInvite /
    // api.registerInviteAcceptRoute); a second factor is mandatory, so the
    // invitee enrols one first and then continues into "beta".
    await enrolTotp(tenantPage, TENANT_TOTP_STATE_PATH, TENANT_TOTP_STEP_STATE_PATH);
    await expect(tenantPage).toHaveURL(new RegExp(`/o/${SECOND_ORG.slug}/hosts$`));
    await expect(tenantPage.getByRole('heading', { name: 'Hosts' })).toBeVisible();

    // The org switcher (and GET /orgs behind it) must only ever list the
    // organizations this account actually belongs to. ui/Select (v0.4g)
    // only renders its options while open, unlike the native select this
    // replaced, so the picker has to be opened first. Exact: the toggle
    // button beside it carries aria-label="Open Organization", a substring
    // match on plain "Organization".
    await tenantPage.getByLabel('Organization', { exact: true }).click();
    await expect(tenantPage.getByRole('option', { name: SECOND_ORG.name })).toHaveCount(1);
    await expect(tenantPage.getByRole('option', { name: ORG.name })).toHaveCount(0);

    // UI: navigating straight to the first organization's hosts page must
    // not leak anything — the friendly 403 page, not the host list, not a
    // blank screen.
    await tenantPage.goto(`/o/${ORG.slug}/hosts`);
    await expect(tenantPage.getByRole('heading', { name: 'You do not have access' })).toBeVisible();
    await expect(tenantPage.getByText('web-04.fra')).toHaveCount(0);

    // API: the same isolation must hold for a direct call made from this
    // session's own cookies, not only for whatever the SPA happens to
    // render.
    const status = await tenantPage.evaluate(async (orgSlug) => {
      const res = await fetch(`/api/v1/orgs/${orgSlug}/hosts`, { credentials: 'include' });
      return res.status;
    }, ORG.slug);
    expect(status).toBe(403);

    // The second user's own organization works normally and is entirely
    // separate data — a host created here must never surface in "acme".
    await tenantPage.goto(`/o/${SECOND_ORG.slug}/hosts`);
    await tenantPage.getByRole('button', { name: 'Add host' }).click();
    const dialog = tenantPage.getByRole('dialog');
    await dialog.getByLabel('Name').fill('beta-01.example');
    await pickOption(dialog, 'Zone', 'Default');
    await dialog.getByRole('button', { name: 'Create host' }).click();
    await expect(tenantPage.getByText('Host beta-01.example created')).toBeVisible();
    await expect(tenantPage.getByRole('link', { name: 'beta-01.example', exact: true })).toBeVisible();
  } finally {
    await tenantContext.close();
  }

  // And from the administrator's own session (a legitimate member of
  // both organizations), "acme"'s host list still never shows "beta"'s
  // host.
  await page.goto(`/o/${ORG.slug}/hosts`);
  await expect(page.getByRole('link', { name: 'beta-01.example', exact: true })).toHaveCount(0);
});
