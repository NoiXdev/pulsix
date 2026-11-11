/**
 * Settings → Notifications (spec 2026-09-18 §5 and §7): an admin adds an
 * e-mail channel, sends a test through it, finds the message in the real
 * mailbox and the attempt in the channel's own delivery log, points the
 * weekly digest at it, and removes it again. Backlog 2.78 — Vitest covers
 * the components and the Go side covers the sender; this is the one
 * account that runs all of it end to end.
 *
 * Mail is real, not stubbed: the compose stack this suite brings up runs
 * mailpit, and members.spec.ts already reads an invitation out of it the
 * same way. That is what makes "Send test" worth a browser test at all —
 * a toast saying the test was sent proves nothing on its own, and the
 * assertion that matters is a message actually arriving at the address
 * typed into the dialog.
 *
 * The delivery log is the second half, and is deliberately read AFTER
 * mailpit has the message: the row's status is written when the send
 * returns, so checking the log first would race the sender rather than
 * the renderer.
 */
import { test, expect } from '@playwright/test';
import { ADMIN_STORAGE_STATE, ORG } from './env.ts';
import { waitForMessageTo } from './mailpit.ts';
import { pickOption } from './pickOption.ts';
import { acceptConfirm } from './confirm.ts';
import { enCommon } from '../src/i18n/en/common.ts';

test.use({ storageState: ADMIN_STORAGE_STATE });

const CHANNEL = 'e2e-notify-channel';
const RECIPIENT = 'notify-e2e@example.test';

test('an admin adds an e-mail channel, sends a test through it, and points the digest at it', async ({ page }) => {
  await page.goto(`/o/${ORG.slug}/settings/notifications`);
  await expect(page.getByRole('heading', { name: 'Notifications' })).toBeVisible();

  // --- Add the channel -------------------------------------------------
  await page.getByRole('button', { name: 'Add channel' }).click();
  const dialog = page.getByRole('dialog');
  await pickOption(dialog, 'Kind', { value: 'email' });
  await dialog.getByLabel('Name').fill(CHANNEL);
  await dialog.getByLabel('Recipients (one per line)').fill(RECIPIENT);
  await dialog.getByRole('button', { name: 'Create channel' }).click();
  await expect(dialog).toHaveCount(0);

  const channelRow = page.locator('[data-list-row]', { hasText: CHANNEL });
  await expect(channelRow.getByText('E-mail', { exact: true })).toBeVisible();
  await expect(channelRow.getByText(RECIPIENT)).toBeVisible();
  await expect(channelRow.getByText('enabled', { exact: true })).toBeVisible();

  // --- Send a test, and find the mail -----------------------------------
  await channelRow.getByRole('button', { name: `Actions for ${CHANNEL}` }).click();
  await page.getByRole('menuitem', { name: 'Send test' }).click();
  await expect(page.getByText(`Test message sent through ${CHANNEL}`)).toBeVisible();
  const body = await waitForMessageTo(RECIPIENT);
  expect(body.length).toBeGreaterThan(0);

  // --- The delivery log records that attempt ----------------------------
  await channelRow.getByRole('button', { name: `Actions for ${CHANNEL}` }).click();
  await page.getByRole('menuitem', { name: 'Deliveries' }).click();
  const log = page.getByRole('dialog');
  await expect(log).toContainText(`Deliveries · ${CHANNEL}`);
  const delivery = log.locator('[data-delivery]').first();
  await expect(delivery.getByText('sent', { exact: true })).toBeVisible();
  // The payload is fetched on demand rather than shipped with the list —
  // a channel's log can hold a lot of them.
  await delivery.getByRole('button', { name: 'Show message' }).click();
  await expect(delivery.getByRole('button', { name: 'Hide message' })).toBeVisible();
  // ui/dialog.tsx is a native <dialog> with no close control of its own:
  // the ones that have a Cancel button put it in their own content, and
  // the delivery log — a read-only list — has none. Escape is how it
  // closes, through the element's own cancel event.
  await page.keyboard.press('Escape');
  await expect(log).toHaveCount(0);

  // --- Point the weekly digest at it ------------------------------------
  const digest = page.getByRole('region', { name: 'Weekly digest' });
  await digest.getByLabel('Send the weekly digest').check();
  await pickOption(digest, 'Day', 'Wednesday');
  // Exact: the card's other field is "Timezone", which a substring
  // match would pick up as well.
  await digest.getByLabel('Time', { exact: true }).fill('07:30');
  await digest.getByLabel(CHANNEL).check();
  await digest.getByRole('button', { name: 'Save digest' }).click();
  await expect(page.getByText('Weekly digest saved')).toBeVisible();

  // Reload rather than trust the card's own state: the settings must have
  // been stored, not merely accepted by the form.
  await page.reload();
  const saved = page.getByRole('region', { name: 'Weekly digest' });
  await expect(saved.getByLabel('Send the weekly digest')).toBeChecked();
  await expect(saved.getByLabel('Day', { exact: true })).toHaveValue('Wednesday');
  await expect(saved.getByLabel('Time', { exact: true })).toHaveValue('07:30');
  await expect(saved.getByLabel(CHANNEL)).toBeChecked();

  // --- Remove the channel ------------------------------------------------
  // Leaves the digest pointing at nothing, which is the state this
  // organization started in — the other specs share this manager.
  await channelRow.getByRole('button', { name: `Actions for ${CHANNEL}` }).click();
  await page.getByRole('menuitem', { name: 'Delete…' }).click();
  await acceptConfirm(page, enCommon.delete);
  await expect(page.getByRole('button', { name: `Actions for ${CHANNEL}` })).toHaveCount(0);
});
