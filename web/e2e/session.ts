/**
 * Helpers for the session specs (lock, expiry, version notice). Each of
 * them signs in on a fresh browser context of its own instead of loading
 * ADMIN_STORAGE_STATE: the lock and the renewal both change the session
 * they run in (renewal replaces it), and the shared state must stay alive
 * for the specs that load it.
 */
import { expect, type Page } from '@playwright/test';
import { readFileSync } from 'node:fs';
import { ADMIN, TOTP_STATE_PATH, TOTP_STEP_STATE_PATH } from './env.ts';
import { nextTotpCode } from './otp.ts';

export const LOCK_PIN = '4821';

export function adminTotpSecret(): string {
  return (JSON.parse(readFileSync(TOTP_STATE_PATH, 'utf8')) as { secret: string }).secret;
}

/** Signs the administrator in with password and a fresh TOTP code and
 * returns once the browser has left the sign-in page (it lands on the
 * hosts page, or on the one-time lock PIN offer). */
export async function signInAdmin(page: Page): Promise<void> {
  await page.goto('/login');
  await page.getByLabel('Email address').fill(ADMIN.email);
  await page.getByLabel('Password').fill(ADMIN.password);
  await page.getByRole('button', { name: 'Sign in' }).click();
  await expect(page.getByRole('heading', { name: 'Enter your second-factor code' })).toBeVisible();
  await page.getByLabel('6-digit code').fill(await nextTotpCode(adminTotpSecret(), TOTP_STEP_STATE_PATH));
  await page.getByRole('button', { name: 'Verify' }).click();
  await expect(page).not.toHaveURL(/\/login/);
}

/** Signs in and gets past the lock PIN offer (skipping it when it comes). */
export async function signInToApp(page: Page): Promise<void> {
  await signInAdmin(page);
  if (/\/account\/lock-pin$/.test(page.url())) {
    await page.getByRole('button', { name: 'Skip' }).click();
  }
  await expect(page.getByRole('heading', { name: 'Hosts' })).toBeVisible();
}
