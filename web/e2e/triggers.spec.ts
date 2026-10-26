/**
 * Webhook triggers (spec 2026-09-18 §9): an operator adds a repository,
 * opens its Triggers dialog, creates a repo-sync trigger, sees the secret
 * exactly once and the hook URL afterwards, and deletes it. Backlog 2.68
 * — Vitest covers TriggersDialog against fixtures and the Go side covers
 * the receive pipeline; this is the browser against the real trigger API.
 *
 * A REPOSITORY is the target, not a job template, and that is what makes
 * this runnable at all: a template needs a playbook, a playbook needs a
 * synced repository, and syncing is a job — which needs the worker image
 * backlog 2.96 is waiting on. A repository row needs nothing but a name
 * and a URL, and it carries the same trigger surface.
 *
 * The secret is the assertion that matters. It is shown once, on
 * creation, and never again — so the test reads it there and then checks
 * that reopening the dialog shows the hook URL and no secret. A page that
 * quietly kept showing it would pass every test that only looked at
 * creation.
 */
import { test, expect } from '@playwright/test';
import { ADMIN_STORAGE_STATE, ORG } from './env.ts';
import { pickOption } from './pickOption.ts';

test.use({ storageState: ADMIN_STORAGE_STATE });

const REPO = 'e2e-trigger-repo';
const TRIGGER = 'e2e-on-push';

test('an operator creates a repo-sync webhook trigger, sees its secret once, and deletes it', async ({ page }) => {
  // --- A repository to hang the trigger on ------------------------------
  await page.goto(`/o/${ORG.slug}/repos`);
  await page.getByRole('button', { name: 'Add repository' }).click();
  const repoDialog = page.getByRole('dialog', { name: 'New repository' });
  await repoDialog.getByLabel('Name').fill(REPO);
  await repoDialog.getByLabel('URL').fill('https://example.invalid/e2e/playbooks.git');
  await repoDialog.getByRole('button', { name: 'Create repository' }).click();
  await expect(repoDialog).toHaveCount(0);

  // --- Create the trigger ------------------------------------------------
  await page.getByRole('button', { name: `Triggers of ${REPO}` }).click();
  const dialog = page.getByRole('dialog', { name: `Triggers · ${REPO}` });
  await expect(dialog.getByText('No incoming webhooks yet.')).toBeVisible();
  await dialog.getByRole('button', { name: 'New trigger' }).click();
  await dialog.getByLabel('Name').fill(TRIGGER);
  await pickOption(dialog, 'Sender', { value: 'github' });
  await dialog.getByRole('button', { name: 'Create trigger' }).click();

  // --- Shown once --------------------------------------------------------
  await expect(dialog.getByText('Shown once — store it in the sender now')).toBeVisible();
  const secret = (await dialog.locator('code').first().textContent())?.trim() ?? '';
  expect(secret.length).toBeGreaterThan(16);
  await dialog.getByRole('button', { name: 'I stored the secret' }).click();
  await expect(dialog.getByText('Shown once — store it in the sender now')).toHaveCount(0);

  // The trigger is listed with the URL a sender posts to, and the secret
  // is gone from the page — reopening must not bring it back.
  await expect(dialog.getByText(TRIGGER)).toBeVisible();
  await expect(dialog.getByRole('button', { name: 'Copy URL' })).toBeVisible();
  await expect(dialog.getByText(secret)).toHaveCount(0);
  await page.keyboard.press('Escape');
  await expect(dialog).toHaveCount(0);

  await page.getByRole('button', { name: `Triggers of ${REPO}` }).click();
  const reopened = page.getByRole('dialog', { name: `Triggers · ${REPO}` });
  await expect(reopened.getByText(TRIGGER)).toBeVisible();
  await expect(reopened.getByText(secret)).toHaveCount(0);

  // --- Delete -------------------------------------------------------------
  page.once('dialog', (d) => void d.accept());
  await reopened.getByRole('button', { name: 'Delete' }).click();
  await expect(reopened.getByText('No incoming webhooks yet.')).toBeVisible();
  await page.keyboard.press('Escape');

  // Leave the organization as it was found: the repository exists only to
  // carry the trigger, and later specs share this manager. Deleting one
  // lives inside its own dialog, which the row's name opens.
  await page.getByRole('button', { name: REPO, exact: true }).click();
  page.once('dialog', (d) => void d.accept());
  await page.getByRole('dialog', { name: REPO }).getByRole('button', { name: 'Delete' }).click();
  await expect(page.getByRole('button', { name: `Triggers of ${REPO}` })).toHaveCount(0);
});
