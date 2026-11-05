import { test, expect, type Locator, type Page } from '@playwright/test';
import { readFileSync } from 'node:fs';
import {
  ADMIN_STORAGE_STATE,
  FACTOR_PERSON,
  FACTOR_PERSON_TOTP_STATE_PATH,
  FACTOR_PERSON_TOTP_STEP_STATE_PATH,
  OPERATOR,
  OPERATOR_TOTP_STATE_PATH,
  OPERATOR_TOTP_STEP_STATE_PATH,
  ORG,
  TOTP_STATE_PATH,
  TOTP_STEP_STATE_PATH,
} from './env.ts';
import { nextTotpCode } from './otp.ts';
import { extractInviteLink, waitForMessageMatching, waitForMessageTo } from './mailpit.ts';
import { enrolTotp } from './enrol.ts';

// Account administration end to end: the instance administrator sets a
// temporary password for the operator members.spec created, and that
// person is held on the change-password screen after signing in with it
// until they choose a password of their own. Signing in with a temporary
// password still asks for the second factor: the temporary password
// replaces the password, never the factor.
test.use({ storageState: ADMIN_STORAGE_STATE });

function secretOf(path: string): string {
  return (JSON.parse(readFileSync(path, 'utf8')) as { secret: string }).secret;
}

/** proveSecondFactorIfAsked answers the step-up dialog when it appears.
 * Account actions need a second factor proven within the last few minutes;
 * whether the administrator's session still counts as fresh depends on how
 * long the specs before this one took, so the dialog may or may not come.
 * `done` is what shows when the action went through without it. */
async function proveSecondFactorIfAsked(page: Page, done: Locator): Promise<void> {
  const stepUp = page.getByRole('dialog', { name: 'Confirm it is you', exact: true });
  await expect(stepUp.or(done)).toBeVisible({ timeout: 30_000 });
  if (await stepUp.isVisible()) {
    await stepUp
      .getByLabel('Authenticator or recovery code')
      .fill(await nextTotpCode(secretOf(TOTP_STATE_PATH), TOTP_STEP_STATE_PATH));
    await stepUp.getByRole('button', { name: 'Confirm' }).click();
    // Proving the factor replaces the session's secret, so the cookie the
    // storage state file holds no longer authenticates: re-save it for the
    // specs that load it after this one.
    await expect(stepUp).toBeHidden();
    await page.context().storageState({ path: ADMIN_STORAGE_STATE });
  }
}

test('an instance admin sets a temporary password and the person must change it at the next sign-in', async ({
  page,
  browser,
}) => {
  // --- Settings › Users › the operator's account page --------------------
  await page.goto(`/o/${ORG.slug}/settings/general`);
  await page.getByRole('navigation', { name: 'Settings' }).getByRole('link', { name: 'Users', exact: true }).click();
  await expect(page).toHaveURL(new RegExp(`/o/${ORG.slug}/settings/users$`));
  await page.getByLabel('Search users').fill(OPERATOR.email);
  await page.getByRole('link', { name: OPERATOR.name }).click();
  await expect(page).toHaveURL(new RegExp(`/o/${ORG.slug}/settings/users/[^/]+$`));
  await expect(page.getByText(OPERATOR.email).first()).toBeVisible();

  // --- Set the temporary password -----------------------------------------
  await page.getByRole('button', { name: 'Set temporary password' }).click();
  const confirm = page.getByRole('dialog', { name: 'Set temporary password', exact: true });
  await confirm.getByRole('button', { name: 'Set temporary password' }).click();

  // exact: a role name matches by substring otherwise, and the confirm
  // dialog's own name ("Set temporary password") contains this one.
  const reveal = page.getByRole('dialog', { name: 'Temporary password', exact: true });
  await proveSecondFactorIfAsked(page, reveal);
  await expect(reveal).toBeVisible({ timeout: 60_000 });
  await expect(reveal.getByText(`This is the temporary password of ${OPERATOR.email}.`)).toBeVisible();
  // The operator's account is active, so the notice mail went out.
  await expect(reveal.getByText('The person was notified by e-mail.')).toBeVisible();
  const temporary = (await reveal.locator('code').textContent())?.trim();
  if (!temporary) throw new Error('the temporary password was not shown');
  await reveal.getByRole('button', { name: 'Done' }).click();
  await expect(reveal).toHaveCount(0);

  // --- The person signs in with it and must choose their own --------------
  const context = await browser.newContext({ storageState: { cookies: [], origins: [] } });
  const operatorPage = await context.newPage();
  try {
    await operatorPage.goto('/login');
    await operatorPage.getByLabel('Email address').fill(OPERATOR.email);
    await operatorPage.getByLabel('Password').fill(temporary);
    await operatorPage.getByRole('button', { name: 'Sign in' }).click();

    await expect(operatorPage.getByRole('heading', { name: 'Enter your second-factor code' })).toBeVisible();
    await operatorPage
      .getByLabel('6-digit code')
      .fill(await nextTotpCode(secretOf(OPERATOR_TOTP_STATE_PATH), OPERATOR_TOTP_STEP_STATE_PATH));
    await operatorPage.getByRole('button', { name: 'Verify' }).click();

    await expect(operatorPage).toHaveURL(/\/account\/password$/);
    await expect(operatorPage.getByRole('heading', { name: 'Choose a new password' })).toBeVisible();

    // The app stays closed until the password is changed: an organization
    // page sends the session straight back here.
    await operatorPage.goto(`/o/${ORG.slug}/hosts`);
    await expect(operatorPage).toHaveURL(/\/account\/password$/);

    await operatorPage.getByLabel('Temporary password').fill(temporary);
    await operatorPage.getByLabel('New password', { exact: true }).fill(OPERATOR.changedPassword);
    await operatorPage.getByLabel('Repeat the new password').fill(OPERATOR.changedPassword);
    await operatorPage.getByRole('button', { name: 'Change password' }).click();

    await expect(operatorPage).toHaveURL(/\/o\/[^/]+\/hosts$/);
    await expect(operatorPage.getByRole('heading', { name: 'Hosts' })).toBeVisible();
  } finally {
    await context.close();
  }
});

// The reset link a forgot-password mail carries, token in the fragment.
const RESET_LINK = /\/reset-password#token=([A-Za-z0-9_-]+)/;

// Forgot password end to end: the operator asks for a link on the sign-in
// page, follows the one the mail carries, and chooses a new password. The
// page takes the token out of the address bar as soon as it has read it,
// and the reset signs nobody in: the next sign-in takes the new password
// and still asks for the second factor. Runs after the temporary-password
// journey above, which leaves the operator with no pending change (while
// one is pending, forgot sends no link).
test('a person resets a forgotten password through the mailed link and still signs in with the second factor', async ({
  browser,
}) => {
  const context = await browser.newContext({ storageState: { cookies: [], origins: [] } });
  const page = await context.newPage();
  try {
    // --- Ask for a link ----------------------------------------------------
    await page.goto('/login');
    await page.getByRole('link', { name: 'Forgot password?' }).click();
    await expect(page).toHaveURL(/\/forgot-password$/);
    await page.getByLabel('Email address').fill(OPERATOR.email);
    await page.getByRole('button', { name: 'Send link' }).click();
    await expect(page.getByText('If an account exists for this address, we sent a link to it.', { exact: false })).toBeVisible();

    const mail = await waitForMessageMatching(OPERATOR.email, RESET_LINK);
    const token = mail.match(RESET_LINK)?.[1];
    if (!token) throw new Error(`the reset mail carries no link:\n${mail}`);

    // --- Follow it: the token leaves the address bar -----------------------
    await page.goto(`/reset-password#token=${token}`);
    await expect(page.getByRole('heading', { name: 'Choose a new password' })).toBeVisible();
    await expect.poll(() => page.url()).not.toContain(token);
    expect(new URL(page.url()).hash).toBe('');
    await expect(page).toHaveURL(/\/reset-password$/);

    await page.getByLabel('New password', { exact: true }).fill(OPERATOR.resetPassword);
    await page.getByLabel('Repeat the new password').fill(OPERATOR.resetPassword);
    await page.getByRole('button', { name: 'Set password' }).click();
    await expect(page.getByRole('heading', { name: 'Your password is set' })).toBeVisible();

    // --- Sign in: the new password, and the second factor all the same ------
    await page.getByRole('link', { name: 'Go to sign in' }).click();
    await expect(page).toHaveURL(/\/login/);
    await page.getByLabel('Email address').fill(OPERATOR.email);
    await page.getByLabel('Password').fill(OPERATOR.resetPassword);
    await page.getByRole('button', { name: 'Sign in' }).click();

    await expect(page.getByRole('heading', { name: 'Enter your second-factor code' })).toBeVisible();
    await page
      .getByLabel('6-digit code')
      .fill(await nextTotpCode(secretOf(OPERATOR_TOTP_STATE_PATH), OPERATOR_TOTP_STEP_STATE_PATH));
    await page.getByRole('button', { name: 'Verify' }).click();

    await expect(page).toHaveURL(/\/o\/[^/]+\/hosts$/);
    await expect(page.getByRole('heading', { name: 'Hosts' })).toBeVisible();
  } finally {
    await context.close();
  }
});

// The notice mails name the sign-in page and carry no secret.
const SIGN_IN_LINK = /Sign in: http:\/\/localhost:\d+\/?\s/;

// Factor reset end to end, with a fresh account (the operator's password and
// factor stay as later specs expect them): an invited person enrols an
// authenticator, an administrator resets the second factor and sets a
// temporary password, and the person signs in with it, is held on the
// enrolment page until a new factor is set up, and then on the password page
// until the temporary password is replaced. Both administrator actions mail
// a notice that says what happened, links to the sign-in page and carries no
// password.
test('a person whose second factor was reset enrols a new one, then replaces the temporary password', async ({
  page,
  browser,
}) => {
  // --- The person is invited, accepts and enrols a first factor -----------
  const invite = await page.request.post(`/api/v1/orgs/${ORG.slug}/invites`, {
    data: { email: FACTOR_PERSON.email, role: 'viewer' },
  });
  expect(invite.ok(), await invite.text()).toBeTruthy();
  const { url } = extractInviteLink(await waitForMessageTo(FACTOR_PERSON.email));

  const context = await browser.newContext({ storageState: { cookies: [], origins: [] } });
  const personPage = await context.newPage();
  try {
    await personPage.goto(url);
    await personPage.getByLabel('Your name').fill(FACTOR_PERSON.name);
    await personPage.getByLabel('Choose a password').fill(FACTOR_PERSON.password);
    await personPage.getByRole('button', { name: 'Accept invitation' }).click();
    await enrolTotp(personPage, FACTOR_PERSON_TOTP_STATE_PATH, FACTOR_PERSON_TOTP_STEP_STATE_PATH);
    await expect(personPage).toHaveURL(new RegExp(`/o/${ORG.slug}/hosts$`));

    // --- The administrator resets the second factor ------------------------
    await page.goto(`/o/${ORG.slug}/settings/users`);
    await page.getByLabel('Search users').fill(FACTOR_PERSON.email);
    await page.getByRole('link', { name: FACTOR_PERSON.name }).click();
    await expect(page).toHaveURL(new RegExp(`/o/${ORG.slug}/settings/users/[^/]+$`));
    await expect(page.getByText(FACTOR_PERSON.email).first()).toBeVisible();

    await page.getByRole('button', { name: 'Reset second factor' }).click();
    const resetDialog = page.getByRole('dialog', { name: 'Reset second factor', exact: true });
    await resetDialog.getByRole('button', { name: 'Reset second factor' }).click();
    const resetDone = page.getByText('Second factor reset.', { exact: false }).first();
    await proveSecondFactorIfAsked(page, resetDone);
    await expect(resetDone).toBeVisible({ timeout: 60_000 });

    // The notice names the sign-in page; there is no secret to carry.
    const resetMail = await waitForMessageMatching(FACTOR_PERSON.email, /second factor was reset/);
    expect(resetMail).toMatch(SIGN_IN_LINK);
    expect(resetMail).not.toContain(FACTOR_PERSON.password);

    // --- ... and sets a temporary password ---------------------------------
    await page.getByRole('button', { name: 'Set temporary password' }).click();
    await page
      .getByRole('dialog', { name: 'Set temporary password', exact: true })
      .getByRole('button', { name: 'Set temporary password' })
      .click();
    const reveal = page.getByRole('dialog', { name: 'Temporary password', exact: true });
    await proveSecondFactorIfAsked(page, reveal);
    await expect(reveal).toBeVisible({ timeout: 60_000 });
    const temporary = (await reveal.locator('code').textContent())?.trim();
    if (!temporary) throw new Error('the temporary password was not shown');
    await reveal.getByRole('button', { name: 'Done' }).click();

    const passwordMail = await waitForMessageMatching(FACTOR_PERSON.email, /password was reset/);
    expect(passwordMail).toMatch(SIGN_IN_LINK);
    expect(passwordMail).not.toContain(temporary);
    expect(passwordMail).not.toContain(FACTOR_PERSON.password);

    // --- The person signs in: a new factor first, then a new password -------
    await personPage.goto('/login');
    await personPage.getByLabel('Email address').fill(FACTOR_PERSON.email);
    await personPage.getByLabel('Password').fill(temporary);
    await personPage.getByRole('button', { name: 'Sign in' }).click();

    // No factor is left to ask for: the person is held on the enrolment page.
    await enrolTotp(personPage, FACTOR_PERSON_TOTP_STATE_PATH, FACTOR_PERSON_TOTP_STEP_STATE_PATH);
    await expect(personPage).toHaveURL(/\/account\/password$/);
    await expect(personPage.getByRole('heading', { name: 'Choose a new password' })).toBeVisible();

    await personPage.getByLabel('Temporary password').fill(temporary);
    await personPage.getByLabel('New password', { exact: true }).fill(FACTOR_PERSON.changedPassword);
    await personPage.getByLabel('Repeat the new password').fill(FACTOR_PERSON.changedPassword);
    await personPage.getByRole('button', { name: 'Change password' }).click();

    await expect(personPage).toHaveURL(/\/o\/[^/]+\/hosts$/);
    await expect(personPage.getByRole('heading', { name: 'Hosts' })).toBeVisible();
  } finally {
    await context.close();
  }
});

// Creating an organization is an instance administrator's act. The org
// switcher offers no "New organization" to anyone else, and the API refuses
// the call all the same. Signs in as the operator with the password the
// reset-link journey left behind, and creates nothing.
test('someone who is not an instance administrator cannot create an organization', async ({ browser }) => {
  const context = await browser.newContext({ storageState: { cookies: [], origins: [] } });
  const page = await context.newPage();
  try {
    await page.goto('/login');
    await page.getByLabel('Email address').fill(OPERATOR.email);
    await page.getByLabel('Password').fill(OPERATOR.resetPassword);
    await page.getByRole('button', { name: 'Sign in' }).click();
    await expect(page.getByRole('heading', { name: 'Enter your second-factor code' })).toBeVisible();
    await page
      .getByLabel('6-digit code')
      .fill(await nextTotpCode(secretOf(OPERATOR_TOTP_STATE_PATH), OPERATOR_TOTP_STEP_STATE_PATH));
    await page.getByRole('button', { name: 'Verify' }).click();
    await expect(page).toHaveURL(/\/o\/[^/]+\/hosts$/);

    // The picker is open and lists the operator's organizations, so the
    // missing entry is not an empty list. Exact: the toggle button beside it
    // carries aria-label="Open Organization".
    await page.getByLabel('Organization', { exact: true }).click();
    await expect(page.getByRole('option', { name: ORG.name })).toHaveCount(1);
    await expect(page.getByRole('option', { name: 'New organization' })).toHaveCount(0);

    const created = await page.request.post('/api/v1/orgs', { data: { slug: 'operator-org', name: 'Operator Org' } });
    expect(created.status()).toBe(403);
  } finally {
    await context.close();
  }
});
