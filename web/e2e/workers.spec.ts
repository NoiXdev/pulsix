/**
 * The Workers page (spec 2026-09-18 §4): an admin creates an enrollment
 * token for a new worker, sees it waiting under Pending enrollment with
 * the one-time token and the ready-made run command on screen, and
 * withdraws it again. Backlog 2.68 — Vitest covers WorkersPage against
 * fixtures; this is the browser against the real enrollment endpoint.
 *
 * It stops where a real worker would begin, and that is the honest
 * boundary rather than a shortcut: enrolling for real needs the worker
 * IMAGE in the compose test stack, which backlog 2.96 is waiting on, and
 * ansible.spec.ts already self-skips for exactly that reason. Everything
 * up to redeeming the token is the page's own responsibility and is
 * covered here; what the token then does is the Go side's
 * (internal/workers, internal/grpcsrv).
 *
 * The token must be shown once and then be gone: it is the credential a
 * worker enrolls with, so the dialog showing it and the pending list NOT
 * showing it are two halves of the same rule.
 */
import { test, expect } from '@playwright/test';
import { ADMIN_STORAGE_STATE, ORG } from './env.ts';

test.use({ storageState: ADMIN_STORAGE_STATE });

const WORKER_NAME = 'e2e-worker-1';

test('an admin creates a worker enrollment token and withdraws it again', async ({ page }) => {
  await page.goto(`/o/${ORG.slug}/workers`);
  await expect(page.getByRole('heading', { name: 'Workers' })).toBeVisible();

  // --- Create the token -------------------------------------------------
  await page.getByRole('button', { name: 'Add worker' }).click();
  const dialog = page.getByRole('dialog', { name: 'Add worker' });
  await dialog.getByLabel('Name').fill(WORKER_NAME);
  // The zone defaults to the organization's only one; naming it here
  // would pin setup.spec's fixture rather than this page's behaviour.
  await dialog.getByRole('button', { name: 'Create token' }).click();

  const token = (await dialog.getByLabel('Enrollment token').textContent())?.trim() ?? '';
  expect(token).toMatch(/^pxw1\./);
  // The snippet is the point of the dialog: an operator copies one line
  // rather than assembling the manager address and the token by hand.
  const snippet = (await dialog.getByLabel('Worker snippet').textContent()) ?? '';
  expect(snippet).toContain(token);
  expect(snippet).toContain('PULSIX_MANAGER_ADDR');
  // Both forms are offered; the compose one carries the same token.
  await dialog.getByRole('tab', { name: 'Compose service' }).click();
  expect(await dialog.getByLabel('Worker snippet').textContent()).toContain('PULSIX_ENROLL_TOKEN');
  await dialog.getByRole('button', { name: 'Done' }).click();
  await expect(dialog).toHaveCount(0);

  // --- It waits, and the token is not in the list -----------------------
  const rowActions = page.getByRole('button', { name: `Actions for enrollment ${WORKER_NAME}` });
  await expect(rowActions).toBeVisible();
  // Shown once: the pending list names the worker, never its secret.
  await expect(page.getByText(token)).toHaveCount(0);
  // No worker exists yet — the token has not been redeemed.
  // The pending row's menu is labelled as an enrollment, so it never collides
  // with a worker of the same name, and it offers no Revoke.
  await rowActions.click();
  await expect(page.getByRole('menuitem', { name: 'Withdraw' })).toBeVisible();
  await expect(page.getByRole('menuitem', { name: /^Revoke/ })).toHaveCount(0);
  await page.keyboard.press('Escape');

  // --- Withdraw ---------------------------------------------------------
  // No confirmation here, unlike Revoke and Delete on a real worker: an
  // unredeemed token has no running jobs and no history to lose.
  await rowActions.click();
  await page.getByRole('menuitem', { name: 'Withdraw' }).click();
  await expect(page.getByRole('button', { name: `Actions for enrollment ${WORKER_NAME}` })).toHaveCount(0);
});
