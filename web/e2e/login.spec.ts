import { test, expect } from '@playwright/test';
import { readFileSync } from 'node:fs';
import { ADMIN, ADMIN_STORAGE_STATE, ORG, TOTP_STATE_PATH } from './env.ts';
import { generateTotpCode, waitForFreshTotpWindow } from './otp.ts';

// Starts already signed in as the administrator setup.spec created (cookie
// + confirmed TOTP), matching this spec's own first step: "logout".
//
// Logging out is a real server-side session revocation (auth.Service.Logout
// -> Sessions.Revoke), not a client-only action — so the very first test
// below permanently kills the session ADMIN_STORAGE_STATE was captured
// with. Every test in this file therefore re-saves the browser's current
// storage state back to ADMIN_STORAGE_STATE once it has signed back in,
// so: (a) the next test in *this* file, which reloads the same path,
// gets a session that is actually still valid, and (b) every later spec
// (passkey, hosts, members, activity, tenancy — each declared to depend on
// this project finishing, see playwright.config.ts) inherits a live
// session too, not one this file itself already revoked.
test.use({ storageState: ADMIN_STORAGE_STATE });

// Read inside beforeAll, not at module scope: Playwright collects every
// spec file across every project (including this one) before any project
// actually runs, and the "setup" project — which writes this file — has
// not run yet at collection time.
let totpSecret: string;
test.beforeAll(() => {
  totpSecret = (JSON.parse(readFileSync(TOTP_STATE_PATH, 'utf8')) as { secret: string }).secret;
});

test('logout, then sign back in with password and a correct TOTP code', async ({ page }) => {
  await page.goto(`/o/${ORG.slug}/hosts`);
  await page.getByRole('button', { name: 'Account menu' }).click();
  await page.getByRole('button', { name: 'Log out' }).click();
  await expect(page).toHaveURL(/\/login$/);

  await page.getByLabel('Email address').fill(ADMIN.email);
  await page.getByLabel('Password').fill(ADMIN.password);
  await page.getByRole('button', { name: 'Sign in' }).click();

  // A real second factor is enrolled, so the API must answer
  // mfa_required: true and the page must show the TOTP step — this is the
  // assertion that would fail if login regressed to skipping MFA
  // entirely.
  await expect(page.getByRole('heading', { name: 'Enter your second-factor code' })).toBeVisible();

  // Wait for a time step later than whatever setup.spec's own TOTP confirm
  // last consumed — see waitForFreshTotpWindow's own comment for why a
  // correct, honestly-computed code can otherwise still be rejected as a
  // replay by the same account's own anti-replay check.
  await waitForFreshTotpWindow();
  await page.getByLabel('6-digit code').fill(generateTotpCode(totpSecret));
  await page.getByRole('button', { name: 'Verify' }).click();

  await expect(page).toHaveURL(new RegExp(`/o/${ORG.slug}/hosts`));
  await expect(page.getByRole('heading', { name: 'Hosts' })).toBeVisible();

  await page.context().storageState({ path: ADMIN_STORAGE_STATE });
});

test('a wrong TOTP code shows an inline error and stays on the TOTP step', async ({ page }) => {
  await page.goto(`/o/${ORG.slug}/hosts`);
  await page.getByRole('button', { name: 'Account menu' }).click();
  await page.getByRole('button', { name: 'Log out' }).click();
  await expect(page).toHaveURL(/\/login$/);

  await page.getByLabel('Email address').fill(ADMIN.email);
  await page.getByLabel('Password').fill(ADMIN.password);
  await page.getByRole('button', { name: 'Sign in' }).click();
  await expect(page.getByRole('heading', { name: 'Enter your second-factor code' })).toBeVisible();

  // A code that is syntactically valid (6 digits) but never the one the
  // real secret would produce right now — off by construction from
  // whatever a correct code would be, so this can never accidentally pass.
  await waitForFreshTotpWindow();
  const correct = generateTotpCode(totpSecret);
  const wrong = correct === '000000' ? '111111' : '000000';
  await page.getByLabel('6-digit code').fill(wrong);
  await page.getByRole('button', { name: 'Verify' }).click();

  await expect(page.getByText('invalid code')).toBeVisible();
  // Stayed on the TOTP step, not bounced anywhere else — the LoginPage's
  // step machine is local component state (see loginMachine.ts), so the
  // URL itself stays "/login" throughout this whole flow; the step is
  // asserted through the heading that only the TOTP step renders.
  await expect(page.getByRole('heading', { name: 'Enter your second-factor code' })).toBeVisible();
  await expect(page).toHaveURL(/\/login$/);

  // Finish signing in for real. Recomputed rather than reusing `correct`
  // from above: the wrong attempt's own round trip may have eaten into
  // the 30s window that code was valid for. It never consumed a TOTP step
  // either way — a mismatched code fails the comparison before
  // verifyAndConsume ever records one — so no fresh-window wait is needed
  // before this one.
  await page.getByLabel('6-digit code').fill(generateTotpCode(totpSecret));
  await page.getByRole('button', { name: 'Verify' }).click();
  await expect(page).toHaveURL(new RegExp(`/o/${ORG.slug}/hosts`));

  await page.context().storageState({ path: ADMIN_STORAGE_STATE });
});
