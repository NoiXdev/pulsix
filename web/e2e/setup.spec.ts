import { test, expect } from '@playwright/test';
import { mkdirSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { ADMIN, ADMIN_STORAGE_STATE, ORG, SMTP, TOTP_STATE_PATH, TOTP_STEP_STATE_PATH } from './env.ts';
import { nextTotpCode } from './otp.ts';

/**
 * setup.spec is the one journey every other spec depends on: it is what
 * actually creates the instance (project "setup" has no `dependencies` in
 * playwright.config.ts, so it always runs first). It would fail if the
 * setup wizard did not exist, if it did not actually create a working
 * administrator account, or if TOTP enrolment did not really work end to
 * end against the real backend — none of that is faked or stubbed.
 *
 * At the end it saves the authenticated + TOTP-enrolled browser storage
 * state to ADMIN_STORAGE_STATE and the raw TOTP secret to
 * TOTP_STATE_PATH, so login.spec (which needs to compute a fresh code) and
 * every later spec (which just needs to already be signed in) do not each
 * have to re-run the wizard.
 */
test('a fresh instance redirects to /setup, the wizard creates the instance, and TOTP enrolment reaches an empty host list', async ({
  page,
  context,
}) => {
  await page.goto('/');
  await expect(page).toHaveURL(/\/setup$/);

  // Step 1: instance administrator.
  await page.getByLabel('Name', { exact: true }).fill(ADMIN.name);
  await page.getByLabel('Email address', { exact: true }).fill(ADMIN.email);
  await page.getByLabel('Password', { exact: true }).fill(ADMIN.password);
  await page.getByRole('button', { name: 'Continue' }).click();

  // Step 2: first organization. The slug is asserted against directly
  // (rather than left to whatever the form defaults to) because every
  // later spec's URLs (/o/acme/hosts, .../settings/members, ...) are
  // hardcoded against ORG.slug.
  await expect(page.getByRole('heading', { name: 'Create the first organization' })).toBeVisible();
  await page.getByLabel('Organization name').fill(ORG.name);
  await page.getByLabel('URL slug').fill(ORG.slug);
  await page.getByRole('button', { name: 'Continue' }).click();

  // Step 3: SMTP, pointed at the mail catcher — members.spec's invite flow
  // depends on this actually being configured, not skipped, since without
  // it the manager has nowhere to deliver the invitation e-mail at all.
  await expect(page.getByRole('heading', { name: 'Outgoing mail' })).toBeVisible();
  await page.getByLabel('Configure SMTP now').check();
  await page.getByLabel('Host').fill(SMTP.host);
  await page.getByLabel('Port').fill(String(SMTP.port));
  await page.getByLabel('From address').fill(SMTP.from);
  // mailpit's SMTP listener offers no STARTTLS extension, and the manager
  // refuses to send at all if StartTLS is requested but not advertised
  // (internal/mail.deliver) — leaving this unchecked is required, not
  // just a convenience default.
  await page.getByLabel('Use STARTTLS').uncheck();
  await page.getByRole('button', { name: 'Continue' }).click();

  // Step 4: confirmation, then account creation.
  await expect(page.getByRole('heading', { name: 'Enrol a second factor now' })).toBeVisible();
  await page.getByRole('button', { name: 'Create administrator & continue' }).click();

  await expect(page).toHaveURL(/\/account\/security$/);
  // Scoped to the status Pill specifically (span.rounded-full): the same
  // "second/Second factor required/active" text also appears, differently
  // capitalized, in the shell's own user-footer subtitle — a plain
  // getByText would match both and fail Playwright's strict-mode check.
  const mfaPill = page.locator('span.rounded-full', { hasText: 'second factor' });
  await expect(mfaPill).toHaveText(/second factor required/i);

  // Enrol TOTP with a code computed from the secret the page itself
  // exposes — this is the load-bearing assertion of the whole spec: if
  // enrolment were broken (wrong endpoint, wrong request shape, a code
  // the backend never actually validates), the "Confirm" step below would
  // never get past its own error toast.
  await page.getByRole('button', { name: 'Enrol authenticator app' }).click();
  const secret = await page.locator('code').textContent();
  if (!secret) throw new Error('TOTP secret was not rendered on the enrolment screen');

  mkdirSync(path.dirname(TOTP_STATE_PATH), { recursive: true });
  writeFileSync(TOTP_STATE_PATH, JSON.stringify({ secret: secret.trim() }));

  await page.getByPlaceholder('482913').fill(await nextTotpCode(secret.trim(), TOTP_STEP_STATE_PATH));
  await page.getByRole('button', { name: 'Confirm' }).click();

  await expect(page.getByText('Recovery codes — shown once.')).toBeVisible();
  // Eight one-time recovery codes, matching auth.TOTP's own enrolment
  // contract (see AccountSecurityPage: `recoveryCodes.map((c) => <span
  // key={c}>{c}</span>)` inside the "font-mono text-xs" grid) — a wrong
  // count would mean confirm returned something other than a real
  // recovery-code set.
  await expect(page.locator('div.font-mono.text-xs > span')).toHaveCount(8);
  await page.getByRole('button', { name: 'I have saved these' }).click();
  await expect(mfaPill).toHaveText(/second factor active/i);

  // Reach the (empty) host list — the brief's own success criterion for
  // this spec.
  await page.getByRole('link', { name: 'Hosts' }).click();
  await expect(page).toHaveURL(new RegExp(`/o/${ORG.slug}/hosts$`));
  await expect(page.getByText('No hosts yet. Add one to get started.')).toBeVisible();

  mkdirSync(path.dirname(ADMIN_STORAGE_STATE), { recursive: true });
  await context.storageState({ path: ADMIN_STORAGE_STATE });
});
