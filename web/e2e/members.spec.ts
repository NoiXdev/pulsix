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
  // logging in as the operator must not touch the administrator's own
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

    // PRODUCT BUG (reported, not papered over — see task-15b-report.md):
    // POST /api/v1/invites/accept (internal/api/member_handlers.go's
    // inviteAcceptOutput) never sets a session cookie — unlike
    // /setup/complete, whose Result.Cookie comes from a real
    // sessions.Create call, AcceptInvite has no auth.Sessions dependency
    // at all. InviteAcceptPage.tsx's own code (`await acceptInvite(...);
    // await refetchMe(); await navigate({to:'/'})`) clearly expects
    // acceptance to also sign the user in, and refetchMe()'s resulting
    // 401 surfaces to the invitee as this confusing "login required"
    // error — even though their account and membership were, in fact,
    // just created successfully server-side. Asserted here so a fix that
    // silently stops producing this error is caught, and documented
    // rather than hidden by skipping straight to the workaround below.
    await expect(operatorPage.getByText('login required')).toBeVisible();

    // Work around the gap above the way a real invitee would have to
    // today: sign in explicitly with the credentials just set. The
    // invited account has no second factor enrolled, and the backend
    // treats "no factor at all" as MFA-complete (see auth.Service.Login's
    // own reasoning), so this lands directly on the hosts page.
    await operatorPage.goto('/login');
    await operatorPage.getByLabel('Email address').fill(OPERATOR.email);
    await operatorPage.getByLabel('Password').fill(OPERATOR.password);
    await operatorPage.getByRole('button', { name: 'Sign in' }).click();
    await expect(operatorPage).toHaveURL(new RegExp(`/o/${ORG.slug}/hosts`));

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
