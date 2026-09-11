import { test, expect, type Page } from '@playwright/test';
import { readFileSync } from 'node:fs';
import { ADMIN, ADMIN_STORAGE_STATE, ORG, TOTP_STATE_PATH, TOTP_STEP_STATE_PATH } from './env.ts';
import { nextTotpCode } from './otp.ts';

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

// lastAcceptedCode is the code the previous test in this file last signed
// in with — the replay test below reuses it verbatim instead of minting a
// throwaway code of its own, so the suite spends its (rare, real) TOTP
// window waits only on codes a test actually needs, never on an extra one
// invented purely to be thrown away.
let lastAcceptedCode: string;

async function logout(page: Page) {
  await page.goto(`/o/${ORG.slug}/hosts`);
  await page.getByRole('button', { name: 'Account menu' }).click();
  await page.getByRole('button', { name: 'Log out' }).click();
  await expect(page).toHaveURL(/\/login$/);
}

async function signInToTotpStep(page: Page) {
  await page.getByLabel('Email address').fill(ADMIN.email);
  await page.getByLabel('Password').fill(ADMIN.password);
  await page.getByRole('button', { name: 'Sign in' }).click();
  // A real second factor is enrolled, so the API must answer
  // mfa_required: true and the page must show the TOTP step — this is the
  // assertion that would fail if login regressed to skipping MFA
  // entirely.
  await expect(page.getByRole('heading', { name: 'Enter your second-factor code' })).toBeVisible();
}

test('logout, then sign back in with password and a correct TOTP code', async ({ page }) => {
  await logout(page);
  await signInToTotpStep(page);

  // nextTotpCode computes a code for a step that validates immediately
  // (internal/auth/totp.go's totpSkewSteps = 1 accepts the current step
  // and one step either side) while still recording a step newer than
  // whatever setup.spec's own TOTP confirm last consumed — see its own
  // comment for why a fixed sleep into a fresh window is not needed to
  // get that guarantee.
  lastAcceptedCode = await nextTotpCode(totpSecret, TOTP_STEP_STATE_PATH);
  await page.getByLabel('6-digit code').fill(lastAcceptedCode);
  await page.getByRole('button', { name: 'Verify' }).click();

  await expect(page).toHaveURL(new RegExp(`/o/${ORG.slug}/hosts`));
  await expect(page.getByRole('heading', { name: 'Hosts' })).toBeVisible();

  await page.context().storageState({ path: ADMIN_STORAGE_STATE });
});

test('a wrong TOTP code shows an inline error and stays on the TOTP step', async ({ page }) => {
  await logout(page);
  await signInToTotpStep(page);

  // A code that is syntactically valid (6 digits) but never the one the
  // real secret would produce right now — off by construction from
  // whatever a correct code would be, so this can never accidentally pass.
  const correct = await nextTotpCode(totpSecret, TOTP_STEP_STATE_PATH);
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

  // Finish signing in for real, with the code already reserved above — a
  // mismatched code never consumes a TOTP step (verifyAndConsume only
  // records one on a match), so this is still the next valid step.
  await page.getByLabel('6-digit code').fill(correct);
  await page.getByRole('button', { name: 'Verify' }).click();
  await expect(page).toHaveURL(new RegExp(`/o/${ORG.slug}/hosts`));

  lastAcceptedCode = correct;
  await page.context().storageState({ path: ADMIN_STORAGE_STATE });
});

// This is the one test in the suite that genuinely proves anti-replay,
// rather than merely relying on it not firing by accident: it resubmits
// the exact code the previous test just signed in with, expecting the
// server to reject it. internal/auth/totp.go's verifyAndConsume rejects
// any code whose time step is not strictly newer than the last one
// already accepted for that account — not "older than 30 seconds", but
// "the same or an earlier window" — so replaying a code that already
// signed the admin in must fail even though the code is, by construction,
// syntactically identical to the one that just worked.
test('a previously used TOTP code is rejected on a second login attempt', async ({ page }) => {
  await logout(page);
  await signInToTotpStep(page);

  // Replay: the exact code the test above just signed in with, submitted
  // again on this fresh login attempt.
  await page.getByLabel('6-digit code').fill(lastAcceptedCode);
  await page.getByRole('button', { name: 'Verify' }).click();
  await expect(page.getByText('invalid code')).toBeVisible();
  await expect(page.getByRole('heading', { name: 'Enter your second-factor code' })).toBeVisible();

  // Finish signing in for real, so later specs still inherit a live
  // session.
  await page.getByLabel('6-digit code').fill(await nextTotpCode(totpSecret, TOTP_STEP_STATE_PATH));
  await page.getByRole('button', { name: 'Verify' }).click();
  await expect(page).toHaveURL(new RegExp(`/o/${ORG.slug}/hosts`));

  await page.context().storageState({ path: ADMIN_STORAGE_STATE });
});
