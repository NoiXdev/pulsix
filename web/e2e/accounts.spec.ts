import { test, expect, type Page } from '@playwright/test';
import { readFileSync } from 'node:fs';
import {
  ADMIN_STORAGE_STATE,
  OPERATOR,
  OPERATOR_TOTP_STATE_PATH,
  OPERATOR_TOTP_STEP_STATE_PATH,
  ORG,
  TOTP_STATE_PATH,
  TOTP_STEP_STATE_PATH,
} from './env.ts';
import { nextTotpCode } from './otp.ts';

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
async function proveSecondFactorIfAsked(page: Page, done: ReturnType<Page['getByRole']>): Promise<void> {
  const stepUp = page.getByRole('dialog', { name: 'Confirm it is you', exact: true });
  await expect(stepUp.or(done)).toBeVisible({ timeout: 30_000 });
  if (await stepUp.isVisible()) {
    await stepUp
      .getByLabel('Authenticator or recovery code')
      .fill(await nextTotpCode(secretOf(TOTP_STATE_PATH), TOTP_STEP_STATE_PATH));
    await stepUp.getByRole('button', { name: 'Confirm' }).click();
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
