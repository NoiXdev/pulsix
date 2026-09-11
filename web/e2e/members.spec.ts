import { test, expect } from '@playwright/test';
import { ADMIN_STORAGE_STATE, ORG } from './env.ts';
import { extractInviteLink, waitForMessageTo } from './mailpit.ts';

test.use({ storageState: ADMIN_STORAGE_STATE });

const OPERATOR = { email: 'ops@example.test', name: 'Otto Operator', password: 'operator-password' };

test('invite an operator by e-mail, accept the invite, and confirm the operator is refused the members page', async ({
  page,
  browser,
}) => {
  await page.goto(`/o/${ORG.slug}/settings/members`);
  await expect(page.getByRole('heading', { name: 'Members' })).toBeVisible();

  await page.getByRole('button', { name: 'Invite member' }).click();
  const dialog = page.getByRole('dialog');
  await dialog.getByLabel('E-mail address').fill(OPERATOR.email);
  await dialog.getByLabel('Role').selectOption('operator');
  await dialog.getByRole('button', { name: 'Send invitation' }).click();
  await expect(page.getByText(`Invitation sent to ${OPERATOR.email}`)).toBeVisible();

  // Read the real invitation e-mail the manager sent through SMTP to
  // mailpit — see internal/tenancy.Service.Invite for the exact body this
  // parses ("Accept: <url>/invite/<token>").
  const body = await waitForMessageTo(OPERATOR.email);
  const { url } = extractInviteLink(body);

  // A separate, unauthenticated browser context: accepting the invite and
  // signing in as the operator must not touch the administrator's own
  // session in `page`.
  // storageState: { cookies: [], origins: [] } — not the default
  // browser.newContext() with no options. This file's own `test.use({
  // storageState: ADMIN_STORAGE_STATE })` (above) turns out to apply to
  // *every* context Playwright creates for this test, manual
  // browser.newContext() calls included, not only the `page` fixture —
  // without overriding it explicitly here, this "separate, unauthenticated
  // context" silently starts out carrying the administrator's own session
  // cookie, and the assertions below were actually exercising the admin's
  // access, not the operator's.
  const operatorContext = await browser.newContext({ storageState: { cookies: [], origins: [] } });
  const operatorPage = await operatorContext.newPage();
  try {
    await operatorPage.goto(url);
    await expect(operatorPage.getByRole('heading', { name: 'Join the organization' })).toBeVisible();
    await operatorPage.getByLabel('Your name').fill(OPERATOR.name);
    await operatorPage.getByLabel('Choose a password').fill(OPERATOR.password);
    await operatorPage.getByRole('button', { name: 'Accept invitation' }).click();

    // Accepting an invite that creates a brand-new account now opens a
    // session right away — MFA-complete, exactly like Login treats an
    // account with no second factor enrolled (see
    // tenancy.Service.AcceptInvite / api.registerInviteAcceptRoute) — so
    // the operator lands signed in, continuing straight past the
    // enrolment screen (this account never enrols one in this spec)
    // rather than hitting a login-required dead end that used to require
    // a separate, explicit sign-in step here.
    await expect(operatorPage).toHaveURL(/\/account\/security$/);

    // The actual assertion this spec exists for: an operator (role
    // "operator", which tenancy.PermMembersManage does not grant) hitting
    // the members page gets a real, friendly 403 — not a blank page, not
    // a silent redirect, and not the member list itself.
    await operatorPage.goto(`/o/${ORG.slug}/settings/members`);
    await expect(operatorPage.getByRole('heading', { name: 'You do not have access' })).toBeVisible();
    await expect(operatorPage.getByText(OPERATOR.email)).toHaveCount(0);
  } finally {
    await operatorContext.close();
  }

  // Back on the administrator's own session: the new member is really
  // there, not just accepted client-side.
  await page.reload();
  await expect(page.getByText(OPERATOR.email)).toBeVisible();
});

// This is AcceptInvite's other branch, exercised end to end: OPERATOR
// already has a password-bearing account (created by the test above). An
// invite token only proves control of a mailbox at the moment it is
// accepted, not knowledge of that account's real password, so accepting a
// second, intercepted invitation for the same address must gain its
// holder nothing — no session, and the account's *own* password (not the
// one just submitted at acceptance) is what actually signs it in.
test("re-inviting an address that already has an account adds the membership but signs in with that account's own password", async ({
  page,
  browser,
}) => {
  const secondOrg = { slug: 'members-second-org', name: 'Members Second Org' };
  const createOrg = await page.request.post('/api/v1/orgs', { data: secondOrg });
  expect(createOrg.ok()).toBeTruthy();

  const invite = await page.request.post(`/api/v1/orgs/${secondOrg.slug}/invites`, {
    data: { email: OPERATOR.email, role: 'viewer' },
  });
  expect(invite.ok()).toBeTruthy();

  const body = await waitForMessageTo(OPERATOR.email);
  const { url } = extractInviteLink(body);

  const context = await browser.newContext({ storageState: { cookies: [], origins: [] } });
  const acceptPage = await context.newPage();
  try {
    await acceptPage.goto(url);
    await acceptPage.getByLabel('Your name').fill('Attacker-Chosen Name');
    await acceptPage.getByLabel('Choose a password').fill('attacker-chosen-password');
    await acceptPage.getByRole('button', { name: 'Accept invitation' }).click();

    // No session opened: the page tells this holder to sign in with the
    // password the account already has, instead of leaving them signed in
    // as OPERATOR.
    await expect(acceptPage.getByRole('heading', { name: 'You already have an account' })).toBeVisible();
    await acceptPage.getByRole('button', { name: 'Go to sign in' }).click();
    await expect(acceptPage).toHaveURL(/\/login$/);

    // The account's real password — set when OPERATOR first accepted,
    // never the one just submitted above — is what actually works.
    await acceptPage.getByLabel('Email address').fill(OPERATOR.email);
    await acceptPage.getByLabel('Password').fill(OPERATOR.password);
    await acceptPage.getByRole('button', { name: 'Sign in' }).click();
    await expect(acceptPage).toHaveURL(/\/o\/[^/]+\/hosts$/);
  } finally {
    await context.close();
  }

  // And the membership this second invite granted is real, not just
  // accepted client-side: the operator now also belongs to secondOrg.
  await page.goto(`/o/${secondOrg.slug}/settings/members`);
  await expect(page.getByText(OPERATOR.email)).toBeVisible();
});
