import { test, expect } from '@playwright/test';
import { BASE_URL, TOTP_STEP_STATE_PATH } from './env.ts';
import { nextTotpCode } from './otp.ts';
import { adminTotpSecret, signInToApp } from './session.ts';
import { ADMIN } from './env.ts';

test.use({ storageState: { cookies: [], origins: [] } });
// Renewing needs a TOTP step newer than the one the sign-in used, which can
// mean waiting out part of a 30 s window.
test.setTimeout(150_000);

test('the expiry warning renews the session in place and replaces its cookie', async ({ page, context, playwright }) => {
  await signInToApp(page);

  const before = (await context.cookies()).filter((c) => c.httpOnly);
  expect(before.length).toBeGreaterThan(0);
  const oldCookieHeader = before.map((c) => `${c.name}=${c.value}`).join('; ');

  // Make the session look like it ends in 4 minutes, but only for requests
  // that carry the old cookie: the renewed session reports its real expiry.
  await page.route('**/api/v1/auth/me', async (route) => {
    const response = await route.fetch();
    const sent = (await route.request().headerValue('cookie')) ?? '';
    if (!before.every((c) => sent.includes(`${c.name}=${c.value}`))) {
      await route.fulfill({ response });
      return;
    }
    const body = await response.json();
    body.session_expires_at = new Date(Date.now() + 4 * 60 * 1000).toISOString();
    await route.fulfill({ response, json: body });
  });
  await page.reload();

  const dialog = page.getByRole('dialog', { name: 'Your session is about to end' });
  await expect(dialog).toBeVisible();

  // "Later" collapses it into a banner with a countdown and "Renew".
  await dialog.getByRole('button', { name: 'Later' }).click();
  await expect(dialog).toHaveCount(0);
  const banner = page.getByRole('status').filter({ hasText: 'Your session ends in' });
  await expect(banner).toBeVisible();
  await banner.getByRole('button', { name: 'Renew' }).click();
  await expect(dialog).toBeVisible();

  await dialog.getByLabel('Password').fill(ADMIN.password);
  await dialog.getByLabel('Authenticator or recovery code').fill(await nextTotpCode(adminTotpSecret(), TOTP_STEP_STATE_PATH));
  await dialog.getByRole('button', { name: 'Renew session' }).click();

  await expect(dialog).toHaveCount(0);
  await expect(page.getByRole('status').filter({ hasText: 'Your session ends in' })).toHaveCount(0);
  await expect(page.getByRole('heading', { name: 'Hosts' })).toBeVisible();

  // Renewal replaces the session: the cookie changed, and the old value is
  // dead on the server.
  const after = (await context.cookies()).filter((c) => c.httpOnly);
  expect(after.map((c) => c.value).sort()).not.toEqual(before.map((c) => c.value).sort());
  const old = await playwright.request.newContext({ baseURL: BASE_URL, extraHTTPHeaders: { cookie: oldCookieHeader } });
  expect((await old.get('/api/v1/auth/me')).status()).toBe(401);
  await old.dispose();
});
