import { test, expect } from '@playwright/test';
import { ADMIN_STORAGE_STATE, ORG } from './env.ts';

// See login.spec.ts's top-of-file note: logging out is a real server-side
// session revocation, so this spec — which also logs out — re-saves
// ADMIN_STORAGE_STATE at the end so hosts/members/activity/tenancy (each
// declared to depend on this project) inherit a session that is still
// alive, not one this file itself just revoked.
test.use({ storageState: ADMIN_STORAGE_STATE });

const PASSKEY_NAME = 'Playwright Virtual Authenticator';

test('add a passkey, log out, and sign back in with the passkey alone', async ({ page }) => {
  // A CDP virtual authenticator stands in for a real security key/platform
  // authenticator: resident-key and user-verification capable, so both
  // registration (attestation) and discoverable login (assertion, no
  // e-mail typed) work exactly as they would against a real device — see
  // the brief's own instruction to use WebAuthn.enable/addVirtualAuthenticator
  // with hasResidentKey/hasUserVerification/isUserVerified.
  const client = await page.context().newCDPSession(page);
  await client.send('WebAuthn.enable', { enableUI: false });
  await client.send('WebAuthn.addVirtualAuthenticator', {
    options: {
      protocol: 'ctap2',
      transport: 'internal',
      hasResidentKey: true,
      hasUserVerification: true,
      isUserVerified: true,
      automaticPresenceSimulation: true,
    },
  });

  await page.goto('/account/security');
  await expect(page.getByRole('heading', { name: 'Security' })).toBeVisible();
  await expect(page.getByText('No passkeys registered yet.')).toBeVisible();

  await page.getByPlaceholder('e.g. MacBook Pro').fill(PASSKEY_NAME);
  await page.getByRole('button', { name: 'Add passkey' }).click();

  // The real assertion for "registration actually worked": the API's own
  // list of the account's passkeys (usePasskeys, refetched after the
  // mutation) shows the credential, named — not just a client-side toast.
  await expect(page.getByText(PASSKEY_NAME)).toBeVisible();
  await expect(page.getByText('No passkeys registered yet.')).toHaveCount(0);

  await page.getByRole('button', { name: 'Account menu' }).click();
  await page.getByRole('button', { name: 'Log out' }).click();
  await expect(page).toHaveURL(/\/login$/);

  // No email, no password, no TOTP code typed anywhere below — this is
  // the whole point: a discoverable credential logs in on its own.
  await page.getByRole('button', { name: 'Use a passkey' }).click();

  await expect(page).toHaveURL(new RegExp(`/o/${ORG.slug}/hosts`));
  await expect(page.getByRole('heading', { name: 'Hosts' })).toBeVisible();

  await page.goto('/account/security');
  await expect(page.getByText(PASSKEY_NAME)).toBeVisible();

  await page.context().storageState({ path: ADMIN_STORAGE_STATE });
});
