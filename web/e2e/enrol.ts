/**
 * enrolTotp drives the second-factor enrolment page (/account/enrol) to
 * completion with an authenticator app: a second factor is mandatory, so
 * every account the suite creates — the administrator in setup.spec, every
 * invitee — lands there first and can do nothing else until this is done.
 * It confirms the secret the page shows with a real code, dismisses the
 * recovery codes, and returns the secret so a later sign-in can compute
 * codes for it. The page then continues into the app on its own.
 */
import { expect, type Page } from '@playwright/test';
import { mkdirSync, writeFileSync } from 'node:fs';
import path from 'node:path';
import { nextTotpCode } from './otp.ts';

export async function enrolTotp(page: Page, secretPath: string, stepStatePath: string): Promise<string> {
  await expect(page).toHaveURL(/\/account\/enrol$/);
  await expect(page.getByRole('heading', { name: 'Set up a second factor' })).toBeVisible();
  await page.getByRole('button', { name: 'Enrol authenticator app' }).click();
  const secret = (await page.locator('code').textContent())?.trim();
  if (!secret) throw new Error('TOTP secret was not rendered on the enrolment screen');
  mkdirSync(path.dirname(secretPath), { recursive: true });
  writeFileSync(secretPath, JSON.stringify({ secret }));

  await page.getByPlaceholder('482913').fill(await nextTotpCode(secret, stepStatePath));
  await page.getByRole('button', { name: 'Confirm' }).click();
  await expect(page.getByText('Recovery codes — shown once.')).toBeVisible();
  await page.getByRole('button', { name: 'I have saved these' }).click();
  return secret;
}
